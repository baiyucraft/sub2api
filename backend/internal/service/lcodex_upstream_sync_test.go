package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLCodexAdapterDiscoversAPIHostAndBuildsIndependentSnapshot(t *testing.T) {
	var forbidden atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case lcodexLoginPath:
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "login-user", body["email"])
			_, _ = w.Write([]byte(`{"access_token":"access-one","refresh_token":"refresh-one"}`))
		case lcodexGroupsPath:
			require.Equal(t, "Bearer access-one", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`[{"id":7,"name":"Image","platform":"openai","rate_multiplier":2,"allow_image_generation":true}]`))
		case lcodexGroupRatesPath:
			_, _ = w.Write([]byte(`{"7":0.5}`))
		case lcodexKeysPath:
			require.Equal(t, "100", r.URL.Query().Get("page_size"))
			_, _ = w.Write([]byte(`{"items":[{"id":11,"key":"sk-complete-visible-key","name":"primary","group_id":7,"status":"active","group":{"id":7,"name":"Image","platform":"openai","rate_multiplier":3}}],"total":1,"page":1,"page_size":100,"pages":1}`))
		case lcodexProfilePath:
			_, _ = w.Write([]byte(`{"id":9,"email":"profile@example.com","credit_balance":12.5,"max_concurrency":4,"disabled":false}`))
		case "/api/v1/auth/login", "/api/v1/api-keys", "/api/v1/auth/me", "/v1/sub2api/billing":
			forbidden.Store(true)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, lcodexPublicSettingsPath, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"api_base_url": api.URL})
	}))
	defer site.Close()

	cfg := &UpstreamConfig{ID: 3, Provider: UpstreamProviderLCodex, SiteURL: site.URL + "/base", AuthMode: UpstreamAuthModeUserLogin, RechargeRate: 1, Credentials: map[string]any{
		AccountCredentialLCodexLoginIdentifier: "login-user", AccountCredentialLCodexLoginPassword: "login-password",
	}}
	snapshot, err := (lcodexUpstreamProviderAdapter{}).SyncSnapshot(context.Background(), cfg, "", true)
	require.NoError(t, err)
	require.False(t, forbidden.Load())
	require.NotNil(t, snapshot.DiscoveredAPIURL)
	require.Equal(t, api.URL, *snapshot.DiscoveredAPIURL)
	require.Len(t, snapshot.Keys, 1)
	require.InDelta(t, 0.5, *snapshot.Keys[0].SourceRateMultiplier, 1e-12)
	capability, ok := parseLCodexImageCapabilitySnapshot(snapshot.Keys[0].Extra)
	require.True(t, ok)
	require.True(t, capability.AllowImageGeneration)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, capability.Status)
	providerSnapshot := snapshot.ExtraUpdates["upstream_provider_snapshot"].(map[string]any)
	require.Equal(t, UpstreamProviderLCodex, providerSnapshot["provider"])
	require.Equal(t, "USD", providerSnapshot["currency"])
	require.InDelta(t, 12.5, providerSnapshot["balance_amount"], 1e-12)
}

func TestLCodexRatePriorityAndFallbacks(t *testing.T) {
	groupID := int64(7)
	groupDefault := 2.0
	row := lcodexKeyRow{
		ID:      11,
		Key:     "sk-complete-rate-priority",
		GroupID: &groupID,
		Group: &lcodexKeyGroupRow{
			ID:             groupID,
			RateMultiplier: json.RawMessage(`3`),
		},
	}
	tests := []struct {
		name           string
		dedicatedRates map[int64]float64
		groupRate      *float64
		want           float64
	}{
		{name: "dedicated rate wins", dedicatedRates: map[int64]float64{groupID: 0.5}, groupRate: &groupDefault, want: 0.5},
		{name: "zero dedicated rate is valid", dedicatedRates: map[int64]float64{groupID: 0}, groupRate: &groupDefault, want: 0},
		{name: "group default is the first fallback", groupRate: &groupDefault, want: 2},
		{name: "embedded key group is the final fallback", want: 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			groups := map[int64]lcodexGroupInfo{
				groupID: {ID: groupID, DefaultRate: tc.groupRate},
			}
			key, _, err := lcodexUpstreamKey(3, row, groups, tc.dedicatedRates, time.Now().UTC())
			require.NoError(t, err)
			require.NotNil(t, key.SourceRateMultiplier)
			require.InDelta(t, tc.want, *key.SourceRateMultiplier, 1e-12)
		})
	}
}

func TestLCodexImageCapabilityDoesNotAffectAccountScheduling(t *testing.T) {
	configID := int64(3)
	firstKeyID, secondKeyID := int64(11), int64(12)
	accountRepo := &sub2APIRateSyncAccountRepo{accounts: []Account{
		{ID: 21, Type: AccountTypeAPIKey, UpstreamConfigID: &configID, UpstreamKeyID: &firstKeyID, Concurrency: 8},
		{ID: 22, Type: AccountTypeAPIKey, UpstreamConfigID: &configID, UpstreamKeyID: &secondKeyID, Concurrency: 8},
	}}
	svc := NewUpstreamConfigService(nil, nil, accountRepo)
	rate := 0.75
	keys := []UpstreamKey{
		{ID: firstKeyID, RateMultiplier: &rate, Extra: map[string]any{
			LCodexImageCapabilitySnapshotExtraKey: lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{
				Version: lcodexImageCapabilitySnapshotVersion, Status: UpstreamKeyImagePricingStatusPartial, AllowImageGeneration: true,
			}),
		}},
		{ID: secondKeyID, RateMultiplier: &rate, Extra: map[string]any{
			LCodexImageCapabilitySnapshotExtraKey: lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{
				Version: lcodexImageCapabilitySnapshotVersion, Status: UpstreamKeyImagePricingStatusDisabled,
			}),
		}},
	}

	updated, err := svc.syncBoundAccountRates(context.Background(), &UpstreamConfig{ID: configID, Provider: UpstreamProviderLCodex}, keys)
	require.NoError(t, err)
	require.Equal(t, 2, updated)
	require.Len(t, accountRepo.bulkUpdates, 2)
	require.Equal(t, accountRepo.bulkUpdates[0].updates.RateMultiplier, accountRepo.bulkUpdates[1].updates.RateMultiplier)
	require.Equal(t, accountRepo.bulkUpdates[0].updates.Priority, accountRepo.bulkUpdates[1].updates.Priority)
	require.Equal(t, accountRepo.bulkUpdates[0].updates.LoadFactor, accountRepo.bulkUpdates[1].updates.LoadFactor)
}

func TestLCodexGroupsRejectsUnspecifiedEnvelope(t *testing.T) {
	for _, raw := range []string{`{"items":[]}`, `{"code":0,"data":[]}`, `null`, `{}`, ``} {
		_, err := decodeLCodexGroups(json.RawMessage(raw))
		require.Error(t, err, raw)
	}
}

func TestLCodexMissingRateRetainsPreviousValueOrStopsNewKey(t *testing.T) {
	remoteID := int64(11)
	previousRate := 0.75
	repo := &upstreamConfigServiceRepo{keys: []UpstreamKey{{UpstreamConfigID: 3, RemoteKeyID: &remoteID, SourceRateMultiplier: &previousRate}}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	cfg := &UpstreamConfig{ID: 3, Provider: UpstreamProviderLCodex}
	snapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &remoteID}}}

	require.NoError(t, svc.preserveMissingProviderRates(context.Background(), cfg, snapshot))
	require.True(t, snapshot.Partial)
	require.Equal(t, previousRate, *snapshot.Keys[0].SourceRateMultiplier)

	newRemoteID := int64(12)
	newSnapshot := &upstreamProviderSnapshot{Keys: []UpstreamKey{{RemoteKeyID: &newRemoteID}}}
	require.ErrorContains(t, svc.preserveMissingProviderRates(context.Background(), cfg, newSnapshot), "has no valid rate multiplier")
}

func TestLCodexAdapterExplicitAPIURLSkipsDiscoveryAndRefreshesOnlyOnce(t *testing.T) {
	var refreshes atomic.Int32
	var unauthorized atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case lcodexLoginPath:
			_, _ = w.Write([]byte(`{"access_token":"stale","refresh_token":"refresh"}`))
		case lcodexRefreshPath:
			refreshes.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"fresh","refresh_token":"rotated"}`))
		case lcodexGroupsPath:
			if r.Header.Get("Authorization") == "Bearer stale" {
				unauthorized.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			require.Equal(t, "Bearer fresh", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`[]`))
		case lcodexGroupRatesPath:
			_, _ = w.Write([]byte(`{}`))
		case lcodexKeysPath:
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"page_size":100,"pages":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("explicit api url must skip site discovery: %s", r.URL.Path)
	}))
	defer site.Close()

	explicitAPIURL := api.URL + "/api/v1"
	cfg := &UpstreamConfig{ID: 4, Provider: UpstreamProviderLCodex, SiteURL: site.URL, APIURL: &explicitAPIURL, AuthMode: UpstreamAuthModeUserLogin, Credentials: map[string]any{
		AccountCredentialLCodexLoginIdentifier: "user", AccountCredentialLCodexLoginPassword: "password",
	}}
	snapshot, err := (lcodexUpstreamProviderAdapter{}).SyncSnapshot(context.Background(), cfg, "", false)
	require.NoError(t, err)
	require.Nil(t, snapshot.DiscoveredAPIURL)
	require.EqualValues(t, 1, unauthorized.Load())
	require.EqualValues(t, 1, refreshes.Load())
}

func TestLCodexAdapterRejectsIncompleteAndDuplicatePagination(t *testing.T) {
	for _, tc := range []struct {
		name string
		page string
		want string
	}{
		{"incomplete", `{"items":[{"id":1,"key":"sk-complete-one"}],"total":2,"page":1,"page_size":100,"pages":1}`, "ended before total"},
		{"duplicate", `{"items":[{"id":1,"key":"sk-complete-one"},{"id":1,"key":"sk-complete-two"}],"total":2,"page":1,"page_size":100,"pages":1}`, "duplicate id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case lcodexLoginPath:
					_, _ = w.Write([]byte(`{"access_token":"access"}`))
				case lcodexGroupsPath:
					_, _ = w.Write([]byte(`[]`))
				case lcodexGroupRatesPath:
					_, _ = w.Write([]byte(`{}`))
				case lcodexKeysPath:
					_, _ = w.Write([]byte(tc.page))
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			cfg := &UpstreamConfig{ID: 5, Provider: UpstreamProviderLCodex, SiteURL: api.URL, APIURL: &api.URL, AuthMode: UpstreamAuthModeUserLogin, Credentials: map[string]any{
				AccountCredentialLCodexLoginIdentifier: "user", AccountCredentialLCodexLoginPassword: "password",
			}}
			_, err := (lcodexUpstreamProviderAdapter{}).SyncSnapshot(context.Background(), cfg, "", false)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLCodexConfigValidationPruningAndSanitization(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderLCodex, AuthMode: UpstreamAuthModeUserLogin, Credentials: map[string]any{
		AccountCredentialLCodexLoginIdentifier: "identifier-secret", AccountCredentialLCodexLoginPassword: "password-secret",
	}}
	require.NoError(t, (lcodexUpstreamProviderAdapter{}).ValidateConfig(cfg, true))
	got := (lcodexUpstreamProviderAdapter{}).SanitizeError(fmt.Errorf("identifier-secret password-secret sk-complete-secret-key"), cfg.Credentials)
	require.NotContains(t, got, "identifier-secret")
	require.NotContains(t, got, "password-secret")
	require.NotContains(t, strings.ToLower(got), "sk-complete-secret-key")

	credentials := map[string]any{
		AccountCredentialLCodexLoginIdentifier: "user", AccountCredentialLCodexLoginPassword: "password",
		AccountCredentialSub2APILoginPassword: "stale-sub2api", AccountCredentialNewAPICookie: "stale-newapi",
	}
	pruneUpstreamProviderCredentials(credentials, UpstreamProviderLCodex, UpstreamAuthModeUserLogin)
	require.Equal(t, "user", credentials[AccountCredentialLCodexLoginIdentifier])
	require.Equal(t, "password", credentials[AccountCredentialLCodexLoginPassword])
	require.NotContains(t, credentials, AccountCredentialSub2APILoginPassword)
	require.NotContains(t, credentials, AccountCredentialNewAPICookie)
}

func TestLCodexProfileFailureIsPartialAndPreservesGenericSnapshot(t *testing.T) {
	cfg := &UpstreamConfig{Provider: UpstreamProviderLCodex, Extra: map[string]any{
		"upstream_provider_snapshot": map[string]any{"provider": UpstreamProviderLCodex, "balance_amount": 9.5},
	}, Credentials: map[string]any{AccountCredentialLCodexLoginPassword: "secret"}}
	updates, _ := lcodexProfileExtraUpdates(cfg, nil, fmt.Errorf("temporary profile failure"))
	require.NotContains(t, updates, "upstream_provider_snapshot")
	require.Contains(t, updates, "upstream_provider_snapshot_last_error")
	require.NotContains(t, fmt.Sprint(updates), "secret")
}

func TestLCodexImageCapabilityDisabledAndOldSnapshotBecomesStale(t *testing.T) {
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	groupID := int64(7)
	disabled := false
	key, _, err := lcodexUpstreamKey(2, lcodexKeyRow{ID: 1, Key: "sk-complete-disabled", GroupID: &groupID}, map[int64]lcodexGroupInfo{
		groupID: {ID: groupID, ImageCapability: &disabled},
	}, nil, now)
	require.NoError(t, err)
	pricing := deriveUpstreamKeyImagePricing(&key, &UpstreamConfig{Provider: UpstreamProviderLCodex})
	require.False(t, pricing.Supported)
	require.Equal(t, UpstreamKeyImagePricingStatusDisabled, pricing.Status)
	require.Nil(t, pricing.FinalCost1K)

	supported := true
	previous, _, err := lcodexUpstreamKey(2, lcodexKeyRow{ID: 1, Key: "sk-complete-previous", GroupID: &groupID}, map[int64]lcodexGroupInfo{
		groupID: {ID: groupID, ImageCapability: &supported},
	}, nil, now)
	require.NoError(t, err)
	repo := &upstreamConfigServiceRepo{keys: []UpstreamKey{previous}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	incoming := &upstreamProviderSnapshot{Keys: []UpstreamKey{{UpstreamConfigID: 2, RemoteKeyID: previous.RemoteKeyID, Extra: map[string]any{}}}}
	svc.mergeLCodexImageCapabilitySnapshots(context.Background(), &UpstreamConfig{ID: 2}, incoming)
	retained, ok := parseLCodexImageCapabilitySnapshot(incoming.Keys[0].Extra)
	require.True(t, ok)
	require.True(t, retained.AllowImageGeneration)
	require.True(t, retained.Stale)
	require.Equal(t, UpstreamKeyImagePricingStatusPartial, retained.Status)
}

func TestLCodexSyncPersistsDiscoveredAPIURL(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case lcodexLoginPath:
			_, _ = w.Write([]byte(`{"access_token":"access"}`))
		case lcodexGroupsPath:
			_, _ = w.Write([]byte(`[]`))
		case lcodexGroupRatesPath:
			_, _ = w.Write([]byte(`{}`))
		case lcodexKeysPath:
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"page_size":100,"pages":0}`))
		case lcodexProfilePath:
			_, _ = w.Write([]byte(`{"id":1,"credit_balance":0,"max_concurrency":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"api_base_url": api.URL})
	}))
	defer site.Close()
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID: 22, Name: "LCodex", Provider: UpstreamProviderLCodex, SiteURL: site.URL,
		AuthMode: UpstreamAuthModeUserLogin, RechargeRate: 1, Status: StatusActive,
		Credentials: map[string]any{AccountCredentialLCodexLoginIdentifier: "user", AccountCredentialLCodexLoginPassword: "password"}, Extra: map[string]any{},
	}}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	_, result, err := svc.SyncKeys(context.Background(), 22)
	require.NoError(t, err)
	require.True(t, result.Success)
	stored, err := repo.GetByID(context.Background(), 22)
	require.NoError(t, err)
	require.NotNil(t, stored.APIURL)
	require.Equal(t, api.URL, *stored.APIURL)
}

func TestLCodexSyncPersistsSameOriginDiscoveredAPIURL(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case lcodexPublicSettingsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"api_base_url": server.URL})
		case lcodexLoginPath:
			_, _ = w.Write([]byte(`{"access_token":"access"}`))
		case lcodexGroupsPath:
			_, _ = w.Write([]byte(`[]`))
		case lcodexGroupRatesPath:
			_, _ = w.Write([]byte(`{}`))
		case lcodexKeysPath:
			_, _ = w.Write([]byte(`{"items":[],"total":0,"page":1,"page_size":100,"pages":0}`))
		case lcodexProfilePath:
			_, _ = w.Write([]byte(`{"id":1,"credit_balance":0,"max_concurrency":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID: 23, Name: "LCodex", Provider: UpstreamProviderLCodex, SiteURL: server.URL,
		AuthMode: UpstreamAuthModeUserLogin, RechargeRate: 1, Status: StatusActive,
		Credentials: map[string]any{AccountCredentialLCodexLoginIdentifier: "user", AccountCredentialLCodexLoginPassword: "password"}, Extra: map[string]any{},
	}}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	_, result, err := svc.SyncKeys(context.Background(), 23)
	require.NoError(t, err)
	require.True(t, result.Success)
	stored, err := repo.GetByID(context.Background(), 23)
	require.NoError(t, err)
	require.NotNil(t, stored.APIURL)
	require.Equal(t, server.URL, *stored.APIURL)
	require.NoError(t, normalizeAndValidateUpstreamConfig(stored, false))
	require.NotNil(t, stored.APIURL)
}
