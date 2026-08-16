package service

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestNewAPIBearerAuthorizationDoesNotDuplicatePrefix(t *testing.T) {
	require.Equal(t, "", newAPIBearerAuthorization(""))
	require.Equal(t, "Bearer login-token", newAPIBearerAuthorization("login-token"))
	require.Equal(t, "Bearer login-token", newAPIBearerAuthorization("Bearer login-token"))
}
