package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSub2APIAuthStrategyRestoreUsesPersistedRefreshTokenForUserLogin(t *testing.T) {
	const (
		oldAccess  = "old-access"
		refresh    = "persisted-refresh"
		newAccess  = "new-access"
		newRefresh = "rotated-refresh"
	)
	refreshSeen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sub2APIKeysPath:
			if r.Header.Get("Authorization") != "Bearer "+newAccess {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0,"pages":1}}`))
		case sub2APIRefreshPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			refreshSeen <- body["refresh_token"]
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"` + newAccess + `","refresh_token":"` + newRefresh + `","expires_in":3600}}`))
		case sub2APIAvailableGroups, sub2APIGroupRatesPath:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &UpstreamConfig{
		ID:       99,
		Provider: UpstreamProviderSub2API,
		SiteURL:  server.URL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialSub2APILoginEmail:    "user@example.test",
			AccountCredentialSub2APILoginPassword: "password-not-used",
		},
	}
	strategy := sub2APIAuthStrategy{service: &Sub2APIUpstreamRateSyncService{
		upstreamConfigRepo: &sub2APIRateSyncUpstreamRepo{},
	}}
	handle, err := strategy.Restore(context.Background(), cfg, "", &UpstreamAuthSessionSecret{
		Provider: UpstreamProviderSub2API,
		Data: map[string]any{
			"access_token":  oldAccess,
			"refresh_token": refresh,
		},
	})

	require.NoError(t, err)
	require.True(t, handle.Refreshed)
	require.Equal(t, refresh, <-refreshSeen)
	value, ok := handle.Value.(sub2APIAuthValue)
	require.True(t, ok)
	require.Equal(t, newAccess, value.AccessToken)
	require.Equal(t, newRefresh, value.RefreshToken)
}

func TestSub2APIAuthStrategyRefreshUsesHandleRefreshTokenForUserLogin(t *testing.T) {
	const (
		newAccess  = "refreshed-access"
		refresh    = "persisted-refresh"
		newRefresh = "rotated-refresh"
	)
	refreshSeen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sub2APIRefreshPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			refreshSeen <- body["refresh_token"]
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"` + newAccess + `","refresh_token":"` + newRefresh + `","expires_in":3600}}`))
		case sub2APIKeysPath:
			if r.Header.Get("Authorization") != "Bearer "+newAccess {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":0,"pages":1}}`))
		case sub2APIAvailableGroups, sub2APIGroupRatesPath:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &UpstreamConfig{
		ID: 100, Provider: UpstreamProviderSub2API, SiteURL: server.URL, AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialSub2APILoginEmail:    "user@example.test",
			AccountCredentialSub2APILoginPassword: "password-not-used",
		},
	}
	strategy := sub2APIAuthStrategy{service: &Sub2APIUpstreamRateSyncService{upstreamConfigRepo: &sub2APIRateSyncUpstreamRepo{}}}
	handle := &UpstreamAuthHandle{Value: sub2APIAuthValue{AccessToken: "old-access", RefreshToken: refresh}}
	refreshed, err := strategy.Refresh(context.Background(), cfg, "", handle)

	require.NoError(t, err)
	require.True(t, refreshed.Refreshed)
	require.Equal(t, refresh, <-refreshSeen)
	value, ok := refreshed.Value.(sub2APIAuthValue)
	require.True(t, ok)
	require.Equal(t, newAccess, value.AccessToken)
	require.Equal(t, newRefresh, value.RefreshToken)
}
