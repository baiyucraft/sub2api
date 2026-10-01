package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAPIAuthSessionTestConfig(siteURL string) *UpstreamConfig {
	return &UpstreamConfig{
		ID:       901,
		Provider: UpstreamProviderNewAPI,
		SiteURL:  siteURL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.test",
			AccountCredentialNewAPILoginPassword: "test-password",
		},
	}
}

func newAPIAuthSessionTestLoginBody(code string, expiresAt int64) string {
	return fmt.Sprintf(`{"success":true,"code":%s,"data":{"user":{"id":42,"username":"owner"},"access_token":"login-token","access_expires_at":%d,"session":{"sid":"sid-1"}}}`, code, expiresAt)
}

func newAPIAuthSessionTestRefreshBody(code string, userID int64, token, sid string, expiresAt int64) string {
	return fmt.Sprintf(`{"success":true,"code":%s,"data":{"user":{"id":%d},"access_token":"%s","access_expires_at":%d,"session":{"sid":"%s"}}}`, code, userID, token, expiresAt, sid)
}

func newAPIAuthSessionTestSetRefreshCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     newAPIRefreshCookieName,
		Value:    value,
		Path:     "/api/user/auth",
		HttpOnly: true,
		Secure:   false,
	})
}

func newAPIAuthSessionTestCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func newAPIAuthSessionTestRestoreSecret(t *testing.T, secret *UpstreamAuthSessionSecret) *UpstreamAuthSessionSecret {
	t.Helper()
	raw, err := json.Marshal(secret)
	require.NoError(t, err)
	var restored UpstreamAuthSessionSecret
	require.NoError(t, json.Unmarshal(raw, &restored))
	return &restored
}

func TestNewAPIAuthStrategyLoginSerializeAndRestoreScopedCookies(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, newAPILoginPath, r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/", HttpOnly: true})
		newAPIAuthSessionTestSetRefreshCookie(w, "refresh-1")
		_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody("0", expiresAt)))
	}))
	t.Cleanup(server.Close)

	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	cfg := newAPIAuthSessionTestConfig(server.URL)
	handle, err := strategy.Login(context.Background(), cfg, "")
	require.NoError(t, err)
	require.True(t, handle.Authenticated)
	require.NotNil(t, handle.ExpiresAt)
	require.Equal(t, expiresAt, handle.ExpiresAt.Unix())

	secret, err := strategy.Serialize(handle)
	require.NoError(t, err)
	require.Equal(t, UpstreamProviderNewAPI, secret.Provider)
	require.Equal(t, "session=session-1", secret.Data["cookie"])
	require.Equal(t, "Bearer login-token", secret.Data["access_token"])
	require.Equal(t, int64(42), int64FromAny(secret.Data["user_id"]))
	require.Equal(t, "sid-1", secret.Data["session_id"])

	cookies, ok := secret.Data["cookies"].([]newAPISavedCookie)
	require.True(t, ok)
	require.Len(t, cookies, 2)
	var refresh newAPISavedCookie
	for _, cookie := range cookies {
		if cookie.Name == newAPIRefreshCookieName {
			refresh = cookie
		}
	}
	require.Equal(t, "refresh-1", refresh.Value)
	require.Equal(t, "/api/user/auth", refresh.Path)
	require.True(t, refresh.HTTPOnly)

	restoredSecret := newAPIAuthSessionTestRestoreSecret(t, secret)
	restoredHandle, err := strategy.Restore(context.Background(), cfg, "", restoredSecret)
	require.NoError(t, err)
	value, ok := restoredHandle.Value.(newAPIAuthValue)
	require.True(t, ok)
	require.Equal(t, int64(42), value.UserID)
	require.Equal(t, "sid-1", value.Session.sessionID)
	require.Equal(t, expiresAt, restoredHandle.ExpiresAt.Unix())
	require.Equal(t, "Bearer login-token", value.Session.client.Transport.(newAPIAuthTransport).accessToken)

	rootURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	refreshURL, err := url.Parse(server.URL + newAPIRefreshPath)
	require.NoError(t, err)
	require.Nil(t, newAPIAuthSessionTestCookie(value.Session.client.Jar.Cookies(rootURL), newAPIRefreshCookieName))
	restoredCookie := newAPIAuthSessionTestCookie(value.Session.client.Jar.Cookies(refreshURL), newAPIRefreshCookieName)
	require.NotNil(t, restoredCookie)
	require.Equal(t, "refresh-1", restoredCookie.Value)
}

func TestNewAPIAuthStrategyRefreshUsesOriginSIDWithoutBearerAndRotatesCookie(t *testing.T) {
	var refreshCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case newAPILoginPath:
			newAPIAuthSessionTestSetRefreshCookie(w, "refresh-1")
			_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody("null", time.Now().Add(time.Hour).Unix())))
		case newAPIRefreshPath:
			call := refreshCalls.Add(1)
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, server.URL, r.Header.Get("Origin"))
			assert.Equal(t, "sid-1", r.Header.Get("X-Auth-Session"))
			assert.Equal(t, "42", r.Header.Get("New-Api-User"))
			assert.Empty(t, r.Header.Get("Authorization"))
			cookie := r.Header.Get("Cookie")
			if call == 1 {
				assert.Contains(t, cookie, "new_api_refresh=refresh-1")
				newAPIAuthSessionTestSetRefreshCookie(w, "refresh-2")
				_, _ = w.Write([]byte(newAPIAuthSessionTestRefreshBody("0", 42, "access-2", "sid-1", time.Now().Add(time.Hour).Unix())))
				return
			}
			assert.Contains(t, cookie, "new_api_refresh=refresh-2")
			assert.NotContains(t, cookie, "new_api_refresh=refresh-1")
			newAPIAuthSessionTestSetRefreshCookie(w, "refresh-3")
			_, _ = w.Write([]byte(newAPIAuthSessionTestRefreshBody("null", 42, "access-3", "sid-1", time.Now().Add(time.Hour).Unix())))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	cfg := newAPIAuthSessionTestConfig(server.URL)
	handle, err := strategy.Login(context.Background(), cfg, "")
	require.NoError(t, err)

	refreshed, err := strategy.Refresh(context.Background(), cfg, "", handle)
	require.NoError(t, err)
	require.True(t, refreshed.Refreshed)
	value := refreshed.Value.(newAPIAuthValue)
	transport := value.Session.client.Transport.(newAPIAuthTransport)
	require.Equal(t, "Bearer access-2", transport.accessToken)

	secret, err := strategy.Serialize(refreshed)
	require.NoError(t, err)
	restored, err := strategy.Restore(context.Background(), cfg, "", newAPIAuthSessionTestRestoreSecret(t, secret))
	require.NoError(t, err)

	refreshed, err = strategy.Refresh(context.Background(), cfg, "", restored)
	require.NoError(t, err)
	require.EqualValues(t, 2, refreshCalls.Load())
	value = refreshed.Value.(newAPIAuthValue)
	require.Equal(t, "refresh-3", newAPIAuthSessionTestCookie(value.Session.client.Jar.Cookies(mustParseURL(t, server.URL+newAPIRefreshPath)), newAPIRefreshCookieName).Value)
	require.Equal(t, "Bearer access-3", value.Session.client.Transport.(newAPIAuthTransport).accessToken)
}

func TestNewAPIAuthStrategyRefreshRejectsIdentityAndSessionMismatch(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	tests := []struct {
		name   string
		userID int64
		sid    string
		token  string
		want   string
	}{
		{name: "identity", userID: 99, sid: "sid-1", token: "new-access", want: "different user"},
		{name: "negative identity", userID: -1, sid: "sid-1", token: "new-access", want: "different user"},
		{name: "zero identity", userID: 0, sid: "sid-1", token: "new-access", want: "different user"},
		{name: "session", userID: 42, sid: "sid-2", token: "new-access", want: "different session"},
		{name: "empty session", userID: 42, token: "new-access", want: "invalid session"},
		{name: "oversized session", userID: 42, sid: strings.Repeat("s", 129), token: "new-access", want: "invalid session"},
		{name: "jwt session", userID: 42, token: newAPIAuthSessionTestJWT(t, future, "sid-2"), want: "different session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == newAPIRefreshPath {
					body := newAPIAuthSessionTestRefreshBody("0", test.userID, test.token, test.sid, future)
					if test.name == "jwt session" {
						body = fmt.Sprintf(`{"success":true,"data":{"user":{"id":42},"access_token":%q}}`, test.token)
					}
					_, _ = w.Write([]byte(body))
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)

			cfg := newAPIAuthSessionTestConfig(server.URL)
			session, err := newAPIAuthSession(context.Background(), cfg, "", 42, "new_api_refresh=refresh-1", "old-access")
			require.NoError(t, err)
			session.sessionID = "sid-1"
			expires := time.Unix(future, 0).UTC()
			session.expiresAt = &expires
			err = (newAPIUpstreamProviderAdapter{}).refreshSession(context.Background(), session)
			require.Error(t, err)
			require.Contains(t, err.Error(), test.want)
			require.False(t, (newAPIAuthStrategy{}).CanLoginAfterRefreshError(err))
			require.Equal(t, "Bearer old-access", session.client.Transport.(newAPIAuthTransport).accessToken)
			require.Equal(t, "sid-1", session.sessionID)
			require.Equal(t, future, session.expiresAt.Unix())
		})
	}
}

func TestNewAPIAuthStrategyExpiryIsRestoredAndExpiredRefreshIsRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == newAPIRefreshPath {
			_, _ = w.Write([]byte(newAPIAuthSessionTestRefreshBody("0", 42, "expired-access", "sid-1", time.Now().Add(-time.Minute).Unix())))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	cfg := newAPIAuthSessionTestConfig(server.URL)
	future := time.Now().Add(time.Hour).Unix()
	secret := &UpstreamAuthSessionSecret{Provider: UpstreamProviderNewAPI, Data: map[string]any{
		"user_id":           int64(42),
		"access_token":      "old-access",
		"access_expires_at": future,
		"session_id":        "sid-1",
		"cookies": []newAPISavedCookie{{
			Name: newAPIRefreshCookieName, Value: "refresh-1", Path: "/api/user/auth",
		}},
	}}
	restored, err := strategy.Restore(context.Background(), cfg, "", newAPIAuthSessionTestRestoreSecret(t, secret))
	require.NoError(t, err)
	require.NotNil(t, restored.ExpiresAt)
	require.WithinDuration(t, time.Unix(future, 0), *restored.ExpiresAt, time.Second)
	require.False(t, expiredHandle(restored, time.Now().UTC()))

	_, err = strategy.Refresh(context.Background(), cfg, "", restored)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired access token")
}

func TestNewAPIAuthStrategyRefreshLoginFallbackPolicy(t *testing.T) {
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "transport", err: errors.New("dial tcp 127.0.0.1:1: connection refused"), want: false},
		{name: "forbidden", err: newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusForbidden, "AUTH_USER_DISABLED", "disabled"), want: false},
		{name: "auth conflict", err: newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusConflict, "AUTH_SESSION_LIMIT", "limit"), want: false},
		{name: "server error", err: newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusInternalServerError, "AUTH_INTERNAL_ERROR", "failed"), want: false},
		{name: "unauthorized", err: newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusUnauthorized, "AUTH_UNAUTHORIZED", "expired"), want: true},
		{name: "no refresh cookie", err: errNewAPIRefreshUnsupported, want: true},
		{name: "legacy endpoint", err: newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusNotFound, "", "missing"), want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, strategy.CanLoginAfterRefreshError(test.err))
		})
	}
}

func TestNewAPIAuthStrategyTypedAuthCodeAndBusinessConflict(t *testing.T) {
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	authErr := newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, http.StatusConflict, "AUTH_SESSION_LIMIT", "session limit")
	var typed *newAPIHTTPError
	require.ErrorAs(t, authErr, &typed)
	require.Equal(t, "AUTH_SESSION_LIMIT", typed.Code)
	require.Equal(t, UpstreamAuthErrorConflict, strategy.ClassifyAuthError(authErr))

	businessErr := newAPIStatusError("list tokens", http.MethodGet, newAPITokensPath, http.StatusConflict, "BUSINESS_CONFLICT", "business conflict")
	require.ErrorAs(t, businessErr, &typed)
	require.Empty(t, typed.Code)
	require.Equal(t, UpstreamAuthErrorUnknown, strategy.ClassifyAuthError(businessErr))
	require.False(t, strategy.CanLoginAfterRefreshError(businessErr))

	// Even an AUTH code on a business endpoint must not become an auth-session conflict.
	businessErr = newAPIStatusError("list tokens", http.MethodGet, newAPITokensPath, http.StatusConflict, "AUTH_SESSION_LIMIT", "409 unauthorized")
	require.Equal(t, UpstreamAuthErrorUnknown, strategy.ClassifyAuthError(fmt.Errorf("wrapped: %w", businessErr)))
}

func TestNewAPIAuthStrategyStructuredCookiesOverrideFlattenedCookie(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
			cfg := newAPIAuthSessionTestConfig("https://newapi.example.test")
			cookies := []newAPISavedCookie{}
			if !empty {
				cookies = append(cookies, newAPISavedCookie{Name: newAPIRefreshCookieName, Value: "fresh", Path: "/api/user/auth", Secure: true, HTTPOnly: true})
			}
			secret := &UpstreamAuthSessionSecret{Provider: UpstreamProviderNewAPI, Data: map[string]any{
				"user_id": int64(42), "access_token": "access", "cookie": "session=stale; new_api_refresh=stale", "cookies": cookies,
			}}
			handle, err := strategy.Restore(context.Background(), cfg, "", newAPIAuthSessionTestRestoreSecret(t, secret))
			require.NoError(t, err)
			session := handle.Value.(newAPIAuthValue).Session
			require.Empty(t, session.client.Jar.Cookies(mustParseURL(t, cfg.SiteURL)))
			refresh := newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, cfg.SiteURL+newAPIRefreshPath)), newAPIRefreshCookieName)
			if empty {
				require.Nil(t, refresh)
				require.ErrorIs(t, strategy.adapter.refreshSession(context.Background(), session), errNewAPIRefreshUnsupported)
			} else {
				require.NotNil(t, refresh)
				require.Equal(t, "fresh", refresh.Value)
			}
		})
	}
}

func TestNewAPIAuthStrategyConfiguredAuthenticationUsesJarAndNormalizedBearer(t *testing.T) {
	for _, mode := range []string{UpstreamAuthModeCookie, UpstreamAuthModeAccessToken} {
		for _, token := range []string{"configured-token", "Bearer configured-token"} {
			t.Run(mode+"/"+token, func(t *testing.T) {
				cfg := newAPIAuthSessionTestConfig("https://newapi.example.test")
				cfg.AuthMode = mode
				cfg.Credentials = map[string]any{
					AccountCredentialNewAPIUserID: "42", AccountCredentialNewAPICookie: "session=configured; new_api_refresh=configured-refresh",
					AccountCredentialNewAPIAccessToken: token,
				}
				strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
				handle, err := strategy.Seed(context.Background(), cfg, "")
				require.NoError(t, err)
				require.False(t, handle.Authenticated)
				require.False(t, strategy.CanLogin(cfg))
				session := handle.Value.(newAPIAuthValue).Session
				require.IsType(t, &newAPISessionCookieJar{}, session.client.Jar)
				transport := session.client.Transport.(newAPIAuthTransport)
				require.Empty(t, transport.cookie)
				require.Equal(t, "Bearer configured-token", transport.accessToken)
				rootCookies := session.client.Jar.Cookies(mustParseURL(t, cfg.SiteURL))
				require.Nil(t, newAPIAuthSessionTestCookie(rootCookies, newAPIRefreshCookieName))
				require.Equal(t, "configured", newAPIAuthSessionTestCookie(rootCookies, "session").Value)
				refresh := newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, cfg.SiteURL+newAPIRefreshPath)), newAPIRefreshCookieName)
				require.NotNil(t, refresh)
				require.Equal(t, "configured-refresh", refresh.Value)
			})
		}
	}
}

func TestNewAPIAuthSessionMetadataUsesJWTExpiryAndSID(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	token := newAPIAuthSessionTestJWT(t, future, "jwt-sid")
	session := &newAPISession{}
	setNewAPIAuthMetadata(session, newAPILoginData{AccessToken: token})
	require.NotNil(t, session.expiresAt)
	require.Equal(t, future, session.expiresAt.Unix())
	require.Equal(t, "jwt-sid", session.sessionID)
	setNewAPIAuthMetadata(session, newAPILoginData{AccessToken: token, AccessExpiresAt: future + 60, Session: &newAPILoginSessionInfo{SID: "explicit-sid"}})
	require.Equal(t, future+60, session.expiresAt.Unix())
	require.Equal(t, "explicit-sid", session.sessionID)
	require.Empty(t, newAPITokenSessionID("opaque-token"))
	require.Empty(t, newAPITokenSessionID("bad.bad.bad"))
	require.Empty(t, newAPITokenSessionID(newAPIAuthSessionTestJWT(t, future, strings.Repeat("s", 129))))

	session.expiresAt = nil
	setNewAPIAuthMetadata(session, newAPILoginData{AccessToken: "opaque-token"})
	require.Nil(t, session.expiresAt)
	for _, expiry := range []int64{future, time.Now().Add(-time.Minute).Unix()} {
		session.expiresAt = func() *time.Time { v := time.Unix(expiry, 0); return &v }()
		require.Equal(t, expiry < time.Now().Unix(), expiredHandle(newAPIHandle(session), time.Now().UTC()))
	}
}

func TestNewAPIAuthSessionManagerRestartUsesLatestBusinessCookies(t *testing.T) {
	var logins, groups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case newAPILoginPath:
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "before-operation", Path: "/"})
			newAPIAuthSessionTestSetRefreshCookie(w, "refresh-before")
			_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody("0", time.Now().Add(time.Hour).Unix())))
		case newAPIUserGroupsPath:
			assert.Equal(t, "Bearer login-token", r.Header.Get("Authorization"))
			assert.NotContains(t, r.Header.Get("Cookie"), "new_api_refresh=")
			if groups.Add(1) == 1 {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "after-operation", Path: "/"})
				newAPIAuthSessionTestSetRefreshCookie(w, "refresh-after")
			} else {
				assert.Contains(t, r.Header.Get("Cookie"), "session=after-operation")
				assert.NotContains(t, r.Header.Get("Cookie"), "session=before-operation")
			}
			_, _ = w.Write([]byte(`{"success":true,"code":null,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := newAPIAuthSessionTestConfig(server.URL)
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	repo := &authSessionRepoFake{}
	operation := func(ctx context.Context, handle *UpstreamAuthHandle) error {
		_, err := strategy.adapter.fetchGroups(ctx, handle.Value.(newAPIAuthValue).Session)
		return err
	}
	manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	_, err := manager.Run(context.Background(), cfg, "", strategy, operation)
	require.NoError(t, err)

	restarted := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
	handle, err := restarted.Run(context.Background(), cfg, "", strategy, operation)
	require.NoError(t, err)
	require.EqualValues(t, 1, logins.Load())
	require.EqualValues(t, 2, groups.Load())
	refresh := newAPIAuthSessionTestCookie(handle.Value.(newAPIAuthValue).Session.client.Jar.Cookies(mustParseURL(t, server.URL+newAPIRefreshPath)), newAPIRefreshCookieName)
	require.NotNil(t, refresh)
	require.Equal(t, "refresh-after", refresh.Value)
}

func TestNewAPIAuthSessionManagerRefreshFailureControlsLogin(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		noCookie  bool
		transport bool
		wantLogin bool
	}{
		{name: "transport", transport: true},
		{name: "403", status: http.StatusForbidden},
		{name: "409", status: http.StatusConflict},
		{name: "500", status: http.StatusInternalServerError},
		{name: "401", status: http.StatusUnauthorized, wantLogin: true},
		{name: "no refresh", noCookie: true, wantLogin: true},
		{name: "404 legacy", status: http.StatusNotFound, wantLogin: true},
		{name: "405 legacy", status: http.StatusMethodNotAllowed, wantLogin: true},
		{name: "501 legacy", status: http.StatusNotImplemented, wantLogin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logins, refreshes, operations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case newAPILoginPath:
					logins.Add(1)
					_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody("0", time.Now().Add(time.Hour).Unix())))
				case newAPIRefreshPath:
					refreshes.Add(1)
					if test.transport {
						conn, _, err := w.(http.Hijacker).Hijack()
						if assert.NoError(t, err) {
							_ = conn.Close()
						}
						return
					}
					w.WriteHeader(test.status)
					_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_UNAUTHORIZED","message":"refresh refused"}`))
				case newAPIUserGroupsPath:
					if r.Header.Get("Authorization") == "Bearer login-token" {
						_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
						return
					}
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"success":false,"code":"AUTH_TOKEN_EXPIRED","message":"expired"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			cfg := newAPIAuthSessionTestConfig(server.URL)
			strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
			cookie := "new_api_refresh=refresh-1"
			if test.noCookie {
				cookie = ""
			}
			session, err := newAPIAuthSession(context.Background(), cfg, "", 42, cookie, "old-access")
			require.NoError(t, err)
			session.sessionID = "sid-1"
			secret, err := strategy.Serialize(newAPIHandle(session))
			require.NoError(t, err)
			raw, err := json.Marshal(secret)
			require.NoError(t, err)
			ciphertext, err := (authSessionEncryptorFake{}).Encrypt(string(raw))
			require.NoError(t, err)
			repo := &authSessionRepoFake{record: &UpstreamAuthSessionRecord{
				UpstreamConfigID: cfg.ID, Provider: cfg.Provider, AuthMode: cfg.AuthMode,
				CredentialFingerprint: strategy.Fingerprint(cfg), SecretCiphertext: ciphertext,
			}}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
			_, err = manager.Run(context.Background(), cfg, "", strategy, func(ctx context.Context, handle *UpstreamAuthHandle) error {
				operations.Add(1)
				_, operationErr := strategy.adapter.fetchGroups(ctx, handle.Value.(newAPIAuthValue).Session)
				return operationErr
			})
			if test.wantLogin {
				require.NoError(t, err)
				require.EqualValues(t, 1, logins.Load())
				require.EqualValues(t, 2, operations.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, logins.Load())
				require.EqualValues(t, 1, operations.Load())
			}
			if test.noCookie {
				require.Zero(t, refreshes.Load())
			} else {
				require.EqualValues(t, 1, refreshes.Load())
			}
		})
	}
}

func TestNewAPISessionCookieJarAttributesEffectiveDomainAndLimits(t *testing.T) {
	root := mustParseURL(t, "https://auth.example.test/")
	jar, err := newAPICookieJar(root.String())
	require.NoError(t, err)
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	jar.SetCookies(root, []*http.Cookie{{Name: "session", Value: "host-only", Path: "/", Secure: true, HttpOnly: true, Expires: expires}})
	jar.SetCookies(root, []*http.Cookie{{Name: "session", Value: "domain", Domain: root.Hostname(), Path: "/", Secure: true, HttpOnly: true, Expires: expires}})
	snapshot := jar.snapshot()
	require.Len(t, snapshot, 1)
	require.Equal(t, "domain", snapshot[0].Value)
	require.True(t, snapshot[0].Secure)
	require.True(t, snapshot[0].HTTPOnly)
	require.Equal(t, expires, snapshot[0].Expires)
	jar.SetCookies(root, []*http.Cookie{{Name: "session", Path: "/", MaxAge: -1}})
	require.Empty(t, jar.snapshot())
	require.Empty(t, jar.Cookies(root))
	jar.SetCookies(root, []*http.Cookie{{Name: "too-large", Value: strings.Repeat("x", 4<<10), Path: "/"}})
	require.Empty(t, jar.snapshot())
	jar.SetCookies(root, []*http.Cookie{{Name: "foreign", Value: "no", Domain: "other.example.test", Path: "/"}, {Name: "public-suffix", Value: "no", Domain: "test", Path: "/"}})
	require.Empty(t, jar.snapshot())
	for i := 0; i < newAPIMaxSessionCookies; i++ {
		jar.SetCookies(root, []*http.Cookie{{Name: fmt.Sprintf("cookie-%d", i), Value: strings.Repeat("x", 3500), Path: "/"}})
	}
	snapshot = jar.snapshot()
	require.NotEmpty(t, snapshot)
	require.Less(t, len(snapshot), newAPIMaxSessionCookies)
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), newAPIMaxSessionCookieBytes)
	require.Len(t, jar.Cookies(root), len(snapshot))
}

func newAPIAuthSessionTestJWT(t *testing.T, expiry int64, sid string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"exp": expiry, "sid": sid})
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".test-signature"
}

func TestNewAPIAuthStrategyRefreshAllowsLegacyResponseWithoutIdentityAndSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, newAPIRefreshPath, r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"code":null,"data":{"access_token":"legacy-refreshed-token"}}`))
	}))
	t.Cleanup(server.Close)
	cfg := newAPIAuthSessionTestConfig(server.URL)
	session, err := newAPIAuthSession(context.Background(), cfg, "", 42, "new_api_refresh=legacy-refresh", "old-access")
	require.NoError(t, err)
	session.sessionID = "sid-1"
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	handle, err := strategy.Refresh(context.Background(), cfg, "", newAPIHandle(session))
	require.NoError(t, err)
	require.True(t, handle.Refreshed)
	require.Equal(t, int64(42), session.userID)
	require.Equal(t, "sid-1", session.sessionID)
	require.Equal(t, "Bearer legacy-refreshed-token", session.client.Transport.(newAPIAuthTransport).accessToken)
}

func TestNewAPIAuthStrategyConfiguredQuotedCookiesRoundTrip(t *testing.T) {
	observed := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}))
	t.Cleanup(server.Close)
	cfg := newAPIAuthSessionTestConfig(server.URL)
	cfg.AuthMode = UpstreamAuthModeCookie
	cfg.Credentials = map[string]any{
		AccountCredentialNewAPIUserID: "42",
		AccountCredentialNewAPICookie: `session="abc"; new_api_refresh="quoted-refresh"`,
	}
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	handle, err := strategy.Seed(context.Background(), cfg, "")
	require.NoError(t, err)
	secret, err := strategy.Serialize(handle)
	require.NoError(t, err)
	handle, err = strategy.Restore(context.Background(), cfg, "", newAPIAuthSessionTestRestoreSecret(t, secret))
	require.NoError(t, err)
	session := handle.Value.(newAPIAuthValue).Session
	rootCookie := newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, server.URL)), "session")
	require.NotNil(t, rootCookie)
	require.Equal(t, "abc", rootCookie.Value)
	refresh := newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, server.URL+newAPIRefreshPath)), newAPIRefreshCookieName)
	require.NotNil(t, refresh)
	require.Equal(t, "quoted-refresh", refresh.Value)
	_, err = strategy.adapter.fetchGroups(context.Background(), session)
	require.NoError(t, err)
	require.Equal(t, "session=abc", <-observed)
}

func TestNewAPIAuthStrategyConfiguredRefreshCookieUsesSitePrefix(t *testing.T) {
	const prefix = "/nested/site"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, prefix+newAPIRefreshPath, r.URL.Path)
		assert.Equal(t, server.URL, r.Header.Get("Origin"))
		assert.Equal(t, "sid-1", r.Header.Get("X-Auth-Session"))
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Contains(t, r.Header.Get("Cookie"), "new_api_refresh=prefixed-refresh")
		_, _ = w.Write([]byte(newAPIAuthSessionTestRefreshBody("0", 42, "prefixed-access", "sid-1", time.Now().Add(time.Hour).Unix())))
	}))
	t.Cleanup(server.Close)
	cfg := newAPIAuthSessionTestConfig(server.URL + prefix)
	cfg.AuthMode = UpstreamAuthModeAccessToken
	cfg.Credentials = map[string]any{
		AccountCredentialNewAPIUserID: "42", AccountCredentialNewAPIAccessToken: "Bearer old-access",
		AccountCredentialNewAPICookie: "new_api_refresh=prefixed-refresh",
	}
	strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	handle, err := strategy.Seed(context.Background(), cfg, "")
	require.NoError(t, err)
	session := handle.Value.(newAPIAuthValue).Session
	session.sessionID = "sid-1"
	require.Nil(t, newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, server.URL+newAPIRefreshPath)), newAPIRefreshCookieName))
	require.Nil(t, newAPIAuthSessionTestCookie(session.client.Jar.Cookies(mustParseURL(t, cfg.SiteURL+newAPIUserGroupsPath)), newAPIRefreshCookieName))
	secret, err := strategy.Serialize(handle)
	require.NoError(t, err)
	cookies := secret.Data["cookies"].([]newAPISavedCookie)
	require.Len(t, cookies, 1)
	require.Equal(t, prefix+"/api/user/auth", cookies[0].Path)
	handle, err = strategy.Restore(context.Background(), cfg, "", newAPIAuthSessionTestRestoreSecret(t, secret))
	require.NoError(t, err)
	handle, err = strategy.Refresh(context.Background(), cfg, "", handle)
	require.NoError(t, err)
	require.Equal(t, "Bearer prefixed-access", handle.Value.(newAPIAuthValue).Session.client.Transport.(newAPIAuthTransport).accessToken)
}

func TestNewAPIUpstreamProviderAdapterLoginAcceptsNumericAndNullCode(t *testing.T) {
	for _, code := range []string{"0", "null"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case newAPILoginPath:
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
					_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody(code, time.Now().Add(time.Hour).Unix())))
				case newAPIUserGroupsPath:
					_, _ = fmt.Fprintf(w, `{"success":true,"code":%s,"data":{}}`, code)
				case newAPITokensPath:
					_, _ = fmt.Fprintf(w, `{"success":true,"code":%s,"data":{"items":[],"total":0}}`, code)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			session, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIAuthSessionTestConfig(server.URL), "")
			require.NoError(t, err)
			require.Equal(t, int64(42), session.userID)
			require.Equal(t, "Bearer login-token", session.client.Transport.(newAPIAuthTransport).accessToken)
			_, err = (newAPIUpstreamProviderAdapter{}).fetchGroups(context.Background(), session)
			require.NoError(t, err)
			keys, err := (newAPIUpstreamProviderAdapter{}).fetchKeys(context.Background(), session)
			require.NoError(t, err)
			require.Empty(t, keys.Rows)
		})
	}
}

func TestNewAPISessionCookieJarDeletionAndSameOriginRedirect(t *testing.T) {
	jar, err := newAPICookieJar("https://newapi.example.test")
	require.NoError(t, err)
	root := mustParseURL(t, "https://newapi.example.test/")
	jar.SetCookies(root, []*http.Cookie{{Name: "session", Value: "one", Path: "/"}})
	require.NotNil(t, newAPIAuthSessionTestCookie(jar.Cookies(root), "session"))
	jar.SetCookies(root, []*http.Cookie{{Name: "session", MaxAge: -1, Path: "/"}})
	require.Nil(t, newAPIAuthSessionTestCookie(jar.Cookies(root), "session"))
	require.Empty(t, jar.snapshot())
	jar.SetCookies(mustParseURL(t, "https://other.example.test/"), []*http.Cookie{{Name: "session", Value: "cross-origin", Path: "/"}})
	require.Nil(t, newAPIAuthSessionTestCookie(jar.Cookies(root), "session"))

	observed := make(chan http.Header, 1)
	sameOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "redirect-cookie", Path: "/"})
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		observed <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(sameOrigin.Close)
	redirectJar, err := newAPICookieJar(sameOrigin.URL)
	require.NoError(t, err)
	client := &http.Client{Jar: redirectJar, Transport: newAPIAuthTransport{base: http.DefaultTransport, accessToken: "Bearer redirect-access"}}
	restrictNewAPIAuthRedirects(client, sameOrigin.URL)
	resp, err := client.Get(sameOrigin.URL + "/start")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	headers := <-observed
	require.Equal(t, "Bearer redirect-access", headers.Get("Authorization"))
	require.Contains(t, headers.Get("Cookie"), "session=redirect-cookie")

	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(foreign.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)
	client = &http.Client{Transport: newAPIAuthTransport{base: http.DefaultTransport, accessToken: "Bearer private-token"}}
	restrictNewAPIAuthRedirects(client, redirector.URL)
	_, err = client.Get(redirector.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cross-origin redirect refused")
	require.Zero(t, foreignCalls.Load())
}

func TestNewAPIAuthSessionManagerAuthenticationSurvivesBusinessFailureAndRestart(t *testing.T) {
	for _, mode := range []string{"login", "refresh"} {
		t.Run(mode, func(t *testing.T) {
			var logins, refreshes, business atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case newAPILoginPath:
					logins.Add(1)
					newAPIAuthSessionTestSetRefreshCookie(w, "current-refresh")
					_, _ = w.Write([]byte(newAPIAuthSessionTestLoginBody("0", time.Now().Add(time.Hour).Unix())))
				case newAPIRefreshPath:
					refreshes.Add(1)
					assert.Empty(t, r.Header.Get("Authorization"))
					assert.Equal(t, "sid-1", r.Header.Get("X-Auth-Session"))
					assert.Contains(t, r.Header.Get("Cookie"), "new_api_refresh=old-refresh")
					newAPIAuthSessionTestSetRefreshCookie(w, "current-refresh")
					_, _ = w.Write([]byte(newAPIAuthSessionTestRefreshBody("0", 42, "current-access", "sid-1", time.Now().Add(time.Hour).Unix())))
				case newAPIUserGroupsPath:
					if business.Add(1) == 1 {
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte(`{"success":false}`))
						return
					}
					_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			cfg := newAPIAuthSessionTestConfig(server.URL)
			strategy := newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
			repo := &authSessionRepoFake{}
			manager := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{}).(*upstreamAuthSessionManager)
			if mode == "refresh" {
				session, err := newAPIAuthSession(context.Background(), cfg, "", 42, "new_api_refresh=old-refresh", "old-access")
				require.NoError(t, err)
				expired := time.Now().Add(-time.Minute)
				session.expiresAt, session.sessionID = &expired, "sid-1"
				require.NoError(t, manager.saveSnapshot(context.Background(), cfg, strategy, newAPIHandle(session), &UpstreamAuthSessionRecord{UpstreamConfigID: cfg.ID}))
			}
			expectedToken := "Bearer login-token"
			if mode == "refresh" {
				expectedToken = "Bearer current-access"
			}
			operation := func(ctx context.Context, handle *UpstreamAuthHandle) error {
				// The checkpoint must exist before the first business request.
				require.NotNil(t, repo.record)
				secret, err := manager.decrypt(repo.record.SecretCiphertext)
				require.NoError(t, err)
				require.Equal(t, expectedToken, secret.Data["access_token"])
				_, err = strategy.adapter.fetchGroups(ctx, handle.Value.(newAPIAuthValue).Session)
				return err
			}
			_, err := manager.Run(context.Background(), cfg, "", strategy, operation)
			require.Error(t, err)
			var upstream *newAPIHTTPError
			require.ErrorAs(t, err, &upstream)
			require.Equal(t, http.StatusInternalServerError, upstream.Status)
			require.Nil(t, repo.record.CooldownUntil)
			secret, err := manager.decrypt(repo.record.SecretCiphertext)
			require.NoError(t, err)
			restored, err := strategy.Restore(context.Background(), cfg, "", secret)
			require.NoError(t, err)
			cookie := newAPIAuthSessionTestCookie(restored.Value.(newAPIAuthValue).Session.client.Jar.Cookies(mustParseURL(t, server.URL+newAPIRefreshPath)), newAPIRefreshCookieName)
			require.NotNil(t, cookie)
			require.Equal(t, "current-refresh", cookie.Value)
			restarted := NewUpstreamAuthSessionManager(repo, nil, authSessionEncryptorFake{})
			_, err = restarted.Run(context.Background(), cfg, "", strategy, operation)
			require.NoError(t, err)
			require.EqualValues(t, 2, business.Load())
			if mode == "refresh" {
				require.Zero(t, logins.Load())
				require.EqualValues(t, 1, refreshes.Load())
			} else {
				require.EqualValues(t, 1, logins.Load())
				require.Zero(t, refreshes.Load())
			}
		})
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}
