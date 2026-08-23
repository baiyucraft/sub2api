package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newAPIUserLoginTestConfig(siteURL string) *UpstreamConfig {
	return &UpstreamConfig{
		Provider: UpstreamProviderNewAPI,
		SiteURL:  siteURL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret",
		},
	}
}

func TestNewAPIUpstreamProviderAdapter_LoginLegacyIDAndCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case newAPILoginPath:
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "legacy-session", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"username":"owner"}}`))
		case newAPIUserGroupsPath:
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			require.Empty(t, r.Header.Get("Authorization"))
			require.Contains(t, r.Header.Get("Cookie"), "session=legacy-session")
			_, _ = w.Write([]byte(`{"success":true,"data":{"default":{"ratio":1}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")
	require.NoError(t, err)
	require.Equal(t, int64(4798), session.userID)
	_, err = (newAPIUpstreamProviderAdapter{}).fetchGroups(context.Background(), session)
	require.NoError(t, err)
}

func TestNewAPIUpstreamProviderAdapter_LoginNestedUserTokenAndCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case newAPILoginPath:
			http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: "refresh-cookie", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"user":{"id":4798,"username":"owner"},"access_token":"login-token"}}`))
		case newAPIUserGroupsPath:
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			require.Equal(t, "Bearer login-token", r.Header.Get("Authorization"))
			require.Contains(t, r.Header.Get("Cookie"), "refresh_token=refresh-cookie")
			_, _ = w.Write([]byte(`{"success":true,"data":{"default":{"ratio":1}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")
	require.NoError(t, err)
	require.Equal(t, int64(4798), session.userID)
	_, err = (newAPIUpstreamProviderAdapter{}).fetchGroups(context.Background(), session)
	require.NoError(t, err)
}

func TestNewAPIUpstreamProviderAdapter_LoginNestedUserTokenWithoutCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case newAPILoginPath:
			_, _ = w.Write([]byte(`{"success":true,"data":{"user":{"id":4798},"access_token":"login-token"}}`))
		case newAPIUserGroupsPath:
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			require.Equal(t, "Bearer login-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"default":{"ratio":1}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")
	require.NoError(t, err)
	_, err = (newAPIUpstreamProviderAdapter{}).fetchGroups(context.Background(), session)
	require.NoError(t, err)
}

func TestNewAPIUpstreamProviderAdapter_LoginRequiresUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "session", Path: "/"})
		_, _ = w.Write([]byte(`{"success":true,"data":{"username":"owner"}}`))
	}))
	defer server.Close()

	_, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")
	require.EqualError(t, err, "newapi login returned no user id")
}

func TestNewAPIUpstreamProviderAdapter_LoginRequiresCookieOrToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"username":"owner"}}`))
	}))
	defer server.Close()

	_, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")
	require.EqualError(t, err, "newapi login returned no session cookie")
}

func TestNewAPIUpstreamProviderAdapter_LoginConflictIncludesSafeMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"message":"session limit reached; password=secret"}`))
	}))
	defer server.Close()

	_, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "newapi login returned status 409")
	require.Contains(t, err.Error(), "session limit reached")
	require.NotContains(t, err.Error(), "password=secret")
	require.Contains(t, err.Error(), "password=***")
}

func TestNewAPIUpstreamProviderAdapter_LoginConflictDoesNotExposeUntrustedBody(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "html", body: `<html>private upstream detail</html>`},
		{name: "malformed json", body: `{"success":false,"message":"private upstream detail"`},
		{name: "oversized json", body: `{"success":false,"message":"` + strings.Repeat("x", newAPIErrorBodyMaxBytes) + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := (newAPIUpstreamProviderAdapter{}).login(context.Background(), newAPIUserLoginTestConfig(server.URL), "")

			require.EqualError(t, err, "newapi login returned status 409")
			require.NotContains(t, err.Error(), "private upstream detail")
		})
	}
}

func TestNewAPIBearerAuthorizationDoesNotDuplicatePrefix(t *testing.T) {
	require.Equal(t, "", newAPIBearerAuthorization(""))
	require.Equal(t, "Bearer login-token", newAPIBearerAuthorization("login-token"))
	require.Equal(t, "Bearer login-token", newAPIBearerAuthorization("Bearer login-token"))
}
