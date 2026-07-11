package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type upstreamConfigServiceRepo struct {
	UpstreamConfigRepository

	configs []UpstreamConfig
	keys    []UpstreamKey

	upserts        []UpstreamKey
	checks         []upstreamConfigCheck
	savedTokens    []upstreamConfigSavedToken
	extraUpdates   []upstreamConfigExtraUpdate
	updateExtraErr error
	mu             sync.Mutex
}

type upstreamConfigCheck struct {
	id      int64
	success bool
	err     string
}

type upstreamConfigSavedToken struct {
	id           int64
	accessToken  string
	refreshToken string
	expiresAt    *time.Time
}

type upstreamConfigExtraUpdate struct {
	id      int64
	updates map[string]any
}

func (r *upstreamConfigServiceRepo) List(ctx context.Context, params pagination.PaginationParams, provider, status, search string) ([]UpstreamConfig, *pagination.PaginationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]UpstreamConfig, 0, len(r.configs))
	for _, cfg := range r.configs {
		if provider != "" && cfg.Provider != provider {
			continue
		}
		if status != "" && cfg.Status != status {
			continue
		}
		out = append(out, cloneUpstreamConfig(cfg))
	}
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: len(out), Pages: 1}, nil
}

func (r *upstreamConfigServiceRepo) GetByID(ctx context.Context, id int64) (*UpstreamConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, cfg := range r.configs {
		if cfg.ID == id {
			out := cloneUpstreamConfig(cfg)
			return &out, nil
		}
	}
	return nil, ErrUpstreamConfigNotFound
}

func (r *upstreamConfigServiceRepo) GetKeyByID(ctx context.Context, id int64) (*UpstreamKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, key := range r.keys {
		if key.ID == id {
			out := cloneUpstreamKey(key)
			return &out, nil
		}
	}
	return nil, ErrUpstreamKeyNotFound
}

func (r *upstreamConfigServiceRepo) ListKeys(ctx context.Context, upstreamConfigID int64) ([]UpstreamKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]UpstreamKey, 0, len(r.keys))
	for _, key := range r.keys {
		if key.UpstreamConfigID == upstreamConfigID {
			out = append(out, cloneUpstreamKey(key))
		}
	}
	return out, nil
}

func (r *upstreamConfigServiceRepo) UpsertKey(ctx context.Context, key *UpstreamKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range r.keys {
		if r.keys[i].UpstreamConfigID == key.UpstreamConfigID && r.keys[i].KeyHash == key.KeyHash {
			key.ID = r.keys[i].ID
			key.CreatedAt = r.keys[i].CreatedAt
			r.keys[i] = cloneUpstreamKey(*key)
			r.upserts = append(r.upserts, cloneUpstreamKey(*key))
			return nil
		}
	}
	key.ID = int64(1000 + len(r.keys) + 1)
	r.keys = append(r.keys, cloneUpstreamKey(*key))
	r.upserts = append(r.upserts, cloneUpstreamKey(*key))
	return nil
}

func (r *upstreamConfigServiceRepo) RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.checks = append(r.checks, upstreamConfigCheck{id: id, success: success, err: safeErr})
	return nil
}

func (r *upstreamConfigServiceRepo) SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.savedTokens = append(r.savedTokens, upstreamConfigSavedToken{id: id, accessToken: accessToken, refreshToken: refreshToken, expiresAt: expiresAt})
	for i := range r.configs {
		if r.configs[i].ID != id {
			continue
		}
		if r.configs[i].Credentials == nil {
			r.configs[i].Credentials = map[string]any{}
		}
		r.configs[i].Credentials[AccountCredentialSub2APIAccessToken] = accessToken
		r.configs[i].Credentials[AccountCredentialSub2APIRefreshToken] = refreshToken
		if expiresAt != nil {
			r.configs[i].Credentials[AccountCredentialSub2APITokenExpiresAt] = expiresAt.UTC().Format(time.RFC3339)
		} else {
			delete(r.configs[i].Credentials, AccountCredentialSub2APITokenExpiresAt)
		}
	}
	return nil
}

func (r *upstreamConfigServiceRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.updateExtraErr != nil {
		return r.updateExtraErr
	}
	copied := cloneAnyMap(updates)
	r.extraUpdates = append(r.extraUpdates, upstreamConfigExtraUpdate{id: id, updates: copied})
	for i := range r.configs {
		if r.configs[i].ID != id {
			continue
		}
		if r.configs[i].Extra == nil {
			r.configs[i].Extra = map[string]any{}
		}
		for k, v := range updates {
			r.configs[i].Extra[k] = v
		}
	}
	return nil
}

func TestNormalizeUpstreamAccountInputClearsAccountProxy(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	proxyID := int64(30)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
			Key:              "sk-upstream",
			KeyHash:          HashUpstreamKey("sk-upstream"),
			Status:           StatusActive,
		}},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}
	input := &CreateAccountInput{
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		ProxyID:          &proxyID,
	}

	require.NoError(t, svc.normalizeUpstreamAccountInput(context.Background(), input))
	require.Equal(t, AccountTypeAPIKey, input.Type)
	require.Nil(t, input.ProxyID)
	require.Equal(t, UpstreamProviderSub2API, input.Extra[AccountUpstreamProviderKey])
}

func TestNormalizeUpstreamAccountUpdateClearsAccountProxy(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	oldProxyID := int64(30)
	newProxyID := int64(40)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
			Key:              "sk-upstream",
			KeyHash:          HashUpstreamKey("sk-upstream"),
			Status:           StatusActive,
		}},
	}
	svc := &adminServiceImpl{upstreamConfigRepo: repo}
	account := &Account{
		ID:               1,
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		ProxyID:          &oldProxyID,
		Proxy:            &Proxy{ID: oldProxyID, Name: "dirty-account-proxy"},
	}
	input := &UpdateAccountInput{ProxyID: &newProxyID}

	require.NoError(t, svc.normalizeUpstreamAccountUpdate(context.Background(), account, input))
	require.Nil(t, account.ProxyID)
	require.Nil(t, account.Proxy)
	require.Nil(t, input.ProxyID)
}

func TestAccountIsUpstreamBoundRequiresBothBindingIDs(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)

	require.False(t, (&Account{UpstreamConfigID: &cfgID}).IsUpstreamBound())
	require.False(t, (&Account{UpstreamKeyID: &keyID}).IsUpstreamBound())
	require.True(t, (&Account{UpstreamConfigID: &cfgID, UpstreamKeyID: &keyID}).IsUpstreamBound())
}

func TestAdminServiceUpdateUpstreamBoundAccountCanClearPoolOnlyCredentials(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	accountID := int64(101)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "NewAPI Main", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
		}},
	}
	accountRepo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:               accountID,
		Name:             "NewAPI Main-pro",
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		Credentials: map[string]any{
			"base_url":                     "https://stale.example.com",
			"api_key":                      "sk-stale",
			"pool_mode":                    true,
			"pool_mode_retry_count":        4,
			"pool_mode_retry_status_codes": []any{429.0},
		},
	})
	svc := &adminServiceImpl{accountRepo: accountRepo, upstreamConfigRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{},
	})

	require.NoError(t, err)
	require.Empty(t, updated.Credentials)
}

func TestNormalizeUpstreamAccountInputStripsLocalForwardingSecrets(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "NewAPI Main", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
		}},
	}
	accountRepo := newAdminSub2APIRateSyncAccountRepo()
	svc := &adminServiceImpl{accountRepo: accountRepo, upstreamConfigRepo: repo}

	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		Credentials: map[string]any{
			"base_url":  "https://stale.example.com",
			"api_key":   "sk-stale",
			"pool_mode": true,
		},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.NotContains(t, created.Credentials, "base_url")
	require.NotContains(t, created.Credentials, "api_key")
	require.Equal(t, true, created.Credentials["pool_mode"])
}

func TestAdminServiceCreateUpstreamBoundAccountAutoLoadFactor(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "NewAPI Main", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
			Key:              "sk-upstream",
			KeyHash:          HashUpstreamKey("sk-upstream"),
			Status:           StatusActive,
		}},
	}
	svc := &adminServiceImpl{
		accountRepo:        newAdminSub2APIRateSyncAccountRepo(),
		upstreamConfigRepo: repo,
	}
	loadFactor := 999999

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "upstream-bound",
		Type:                 AccountTypeAPIKey,
		Platform:             PlatformOpenAI,
		UpstreamConfigID:     &cfgID,
		UpstreamKeyID:        &keyID,
		Concurrency:          80,
		Priority:             7,
		LoadFactor:           &loadFactor,
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Equal(t, AccountTypeAPIKey, account.Type)
	require.Equal(t, 80, account.Concurrency)
	require.NotNil(t, account.LoadFactor)
	require.Equal(t, 120, *account.LoadFactor)
}

func TestAdminServiceUpdateUpstreamBoundAccountAutoLoadFactor(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(cfgID, "NewAPI Main", UpstreamProviderNewAPI, StatusActive, "https://upstream.example.com")},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: cfgID,
			Name:             "pro",
			Platform:         PlatformOpenAI,
			Key:              "sk-upstream",
			KeyHash:          HashUpstreamKey("sk-upstream"),
			Status:           StatusActive,
		}},
	}
	staleLoadFactor := 999
	accountID := int64(101)
	accountRepo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:               accountID,
		Name:             "upstream-bound",
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		Concurrency:      10,
		Priority:         50,
		LoadFactor:       &staleLoadFactor,
		Credentials: map[string]any{
			"base_url": "https://upstream.example.com",
			"api_key":  "sk-upstream",
		},
		Extra: map[string]any{AccountUpstreamProviderKey: UpstreamProviderNewAPI},
	})
	svc := &adminServiceImpl{accountRepo: accountRepo, upstreamConfigRepo: repo}
	priority := 21
	concurrency := 40
	incomingLoadFactor := 999999

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Priority:    &priority,
		Concurrency: &concurrency,
		LoadFactor:  &incomingLoadFactor,
	})

	require.NoError(t, err)
	require.Equal(t, 40, updated.Concurrency)
	require.Equal(t, 21, updated.Priority)
	require.NotNil(t, updated.LoadFactor)
	require.Equal(t, 30, *updated.LoadFactor)
}

func TestAdminServiceUpdateUnboundAccountUsesOrdinaryLoadFactor(t *testing.T) {
	cfgID := int64(10)
	keyID := int64(20)
	accountID := int64(101)
	staleLoadFactor := 150
	accountRepo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:               accountID,
		Name:             "upstream-bound",
		Type:             AccountTypeAPIKey,
		Platform:         PlatformOpenAI,
		UpstreamConfigID: &cfgID,
		UpstreamKeyID:    &keyID,
		Concurrency:      100,
		Priority:         7,
		LoadFactor:       &staleLoadFactor,
		Credentials: map[string]any{
			"base_url": "https://upstream.example.com",
			"api_key":  "sk-upstream",
		},
		Extra: map[string]any{AccountUpstreamProviderKey: UpstreamProviderNewAPI},
	})
	svc := &adminServiceImpl{accountRepo: accountRepo}
	zero := int64(0)
	ordinaryLoadFactor := 33

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		UpstreamConfigID: &zero,
		UpstreamKeyID:    &zero,
		LoadFactor:       &ordinaryLoadFactor,
	})

	require.NoError(t, err)
	require.Nil(t, updated.UpstreamConfigID)
	require.Nil(t, updated.UpstreamKeyID)
	require.NotNil(t, updated.LoadFactor)
	require.Equal(t, 33, *updated.LoadFactor)
	require.NotContains(t, updated.Credentials, "base_url")
	require.NotContains(t, updated.Credentials, "api_key")
}

func TestUpstreamConfigService_SyncKeysUpsertsKeysAndUpdatesBoundAccounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			require.Contains(t, r.Header.Get("User-Agent"), "Mozilla/5.0")
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/available":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}]}`))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"10":0.065}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	keyID := int64(1440)
	configID := int64(7)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{{
			ID:       configID,
			Name:     "Sub2API Main",
			Provider: UpstreamProviderSub2API,
			BaseURL:  server.URL,
			AuthMode: UpstreamAuthModeUserLogin,
			Credentials: map[string]any{
				AccountCredentialSub2APILoginEmail:    "admin@example.com",
				AccountCredentialSub2APILoginPassword: "secret",
			},
			Status: StatusActive,
		}},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: configID,
			Key:              "sk-bound",
			KeyHash:          HashUpstreamKey("sk-bound"),
			Platform:         PlatformOpenAI,
			Status:           StatusActive,
		}},
	}
	accountRepo := &sub2APIRateSyncAccountRepo{accounts: []Account{{
		ID:               101,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		Concurrency:      100,
	}}}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)

	keys, _, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Len(t, repo.upserts, 1)
	require.Equal(t, "plus", repo.upserts[0].Name)
	require.Equal(t, "Plus Group", repo.upserts[0].UpstreamGroupName)
	require.NotNil(t, repo.upserts[0].RateMultiplier)
	require.InDelta(t, 0.065, *repo.upserts[0].RateMultiplier, 1e-12)
	require.InDelta(t, 0.12, repo.upserts[0].Extra["default_rate_multiplier"], 1e-12)
	require.InDelta(t, 0.065, repo.upserts[0].Extra["dedicated_rate_multiplier"], 1e-12)
	require.Equal(t, true, repo.upserts[0].Extra["has_dedicated_rate_multiplier"])
	require.Len(t, accountRepo.bulkUpdates, 1)
	require.Equal(t, []int64{101}, accountRepo.bulkUpdates[0].ids)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.RateMultiplier)
	require.InDelta(t, 0.065, *accountRepo.bulkUpdates[0].updates.RateMultiplier, 1e-12)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.Priority)
	require.Equal(t, 7, *accountRepo.bulkUpdates[0].updates.Priority)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.LoadFactor)
	require.Equal(t, 150, *accountRepo.bulkUpdates[0].updates.LoadFactor)
	require.Equal(t, "Plus Group", accountRepo.bulkUpdates[0].updates.Extra["sub2api_upstream_group_name"])
	require.Len(t, repo.checks, 1)
	require.True(t, repo.checks[0].success)
}

func TestUpstreamConfigService_Sub2APIGroupRatesFallbacks(t *testing.T) {
	t.Run("rates unavailable uses available default", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/auth/login":
				_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
			case "/api/v1/keys":
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Key Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
			case "/api/v1/groups/available":
				_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Available Group","platform":"openai","rate_multiplier":0.06}]}`))
			case "/api/v1/groups/rates":
				http.NotFound(w, r)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		configID := int64(77)
		repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{testUpstreamConfig(configID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, server.URL)}}
		svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

		keys, result, err := svc.SyncKeys(context.Background(), configID)

		require.NoError(t, err)
		require.True(t, result.Success)
		require.Len(t, keys, 1)
		require.InDelta(t, 0.06, *keys[0].RateMultiplier, 1e-12)
		require.Equal(t, "Available Group", keys[0].UpstreamGroupName)
		require.Equal(t, false, keys[0].Extra["has_dedicated_rate_multiplier"])
	})

	t.Run("available unavailable still uses dedicated group rate", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/auth/login":
				_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
			case "/api/v1/keys":
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Key Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
			case "/api/v1/groups/available":
				http.Error(w, "unavailable", http.StatusInternalServerError)
			case "/api/v1/groups/rates":
				_, _ = w.Write([]byte(`{"code":0,"data":{"10":0.06}}`))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		configID := int64(78)
		repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{testUpstreamConfig(configID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, server.URL)}}
		svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

		keys, result, err := svc.SyncKeys(context.Background(), configID)

		require.NoError(t, err)
		require.True(t, result.Success)
		require.Len(t, keys, 1)
		require.InDelta(t, 0.06, *keys[0].RateMultiplier, 1e-12)
		require.Equal(t, "Key Group", keys[0].UpstreamGroupName)
		require.Equal(t, true, keys[0].Extra["has_dedicated_rate_multiplier"])
		require.InDelta(t, 0.12, keys[0].Extra["default_rate_multiplier"], 1e-12)
		require.InDelta(t, 0.06, keys[0].Extra["dedicated_rate_multiplier"], 1e-12)
	})
}

func TestParseSub2APIGroupRateOverrides(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    map[int64]float64
	}{
		{
			name:    "wrapped map",
			payload: map[string]any{"data": map[string]any{"1": 0.8, "2": "1.2"}},
			want:    map[int64]float64{1: 0.8, 2: 1.2},
		},
		{
			name:    "wrapped array snake case",
			payload: map[string]any{"data": []any{map[string]any{"group_id": 1.0, "rate_multiplier": 0.8}}},
			want:    map[int64]float64{1: 0.8},
		},
		{
			name:    "unwrapped array camel case",
			payload: []any{map[string]any{"groupId": "1", "rateMultiplier": "0.8"}},
			want:    map[int64]float64{1: 0.8},
		},
		{
			name: "invalid entries ignored",
			payload: map[string]any{"data": []any{
				map[string]any{"group_id": 1.0, "rate_multiplier": 0.8},
				map[string]any{"rate_multiplier": 1.0},
				map[string]any{"group_id": 2.0, "rate_multiplier": -1.0},
				map[string]any{"group_id": 3.0, "rate_multiplier": "nan"},
			}},
			want: map[int64]float64{1: 0.8},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parseSub2APIGroupRateOverrides(tt.payload))
		})
	}
}

func TestUpstreamConfigService_Sub2APIManualJWTRefreshSavesCamelCaseTokensAndExpiry(t *testing.T) {
	refreshCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshCalled = true
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"jwt-new","refreshToken":"refresh-new","expiresIn":"3600"}}`))
		case "/api/v1/keys":
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/available":
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}]}`))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(79)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "Sub2API Main",
		Provider: UpstreamProviderSub2API,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken:    "jwt-old",
			AccountCredentialSub2APIRefreshToken:   "refresh-old",
			AccountCredentialSub2APITokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	keys, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, refreshCalled)
	require.Len(t, keys, 1)
	require.Len(t, repo.savedTokens, 1)
	require.Equal(t, "jwt-new", repo.savedTokens[0].accessToken)
	require.Equal(t, "refresh-new", repo.savedTokens[0].refreshToken)
	require.NotNil(t, repo.savedTokens[0].expiresAt)
	require.True(t, repo.savedTokens[0].expiresAt.After(time.Now().Add(30*time.Minute)))
	require.Equal(t, "jwt-new", repo.configs[0].Credentials[AccountCredentialSub2APIAccessToken])
	require.Equal(t, "refresh-new", repo.configs[0].Credentials[AccountCredentialSub2APIRefreshToken])
	require.NotEmpty(t, repo.configs[0].Credentials[AccountCredentialSub2APITokenExpiresAt])
}

func TestUpstreamConfigService_Sub2APIRefreshPersistsBeforeDownstreamFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new"}}`))
		case "/api/v1/keys":
			http.Error(w, "temporary failure", http.StatusBadGateway)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(80)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "Sub2API Main",
		Provider: UpstreamProviderSub2API,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken:    "jwt-old",
			AccountCredentialSub2APIRefreshToken:   "refresh-old",
			AccountCredentialSub2APITokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.False(t, result.Success)
	require.Len(t, repo.savedTokens, 1)
	require.Equal(t, "jwt-new", repo.savedTokens[0].accessToken)
	require.Equal(t, "refresh-new", repo.savedTokens[0].refreshToken)
	require.Nil(t, repo.savedTokens[0].expiresAt)
	_, hasStaleExpiry := repo.configs[0].Credentials[AccountCredentialSub2APITokenExpiresAt]
	require.False(t, hasStaleExpiry)
}

func TestUpstreamConfigService_Sub2APIRefreshPersistsWhenRateResolutionFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-bound","name":"plus","group_id":10}],"pages":1}}`))
		case "/api/v1/groups/available":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus","platform":"openai"}]}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(83)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "Sub2API Main",
		Provider: UpstreamProviderSub2API,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken:    "jwt-old",
			AccountCredentialSub2APIRefreshToken:   "refresh-old",
			AccountCredentialSub2APITokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.False(t, result.Success)
	require.Len(t, repo.savedTokens, 1)
	require.Equal(t, "jwt-new", repo.savedTokens[0].accessToken)
	require.Equal(t, "refresh-new", repo.savedTokens[0].refreshToken)
}

func TestUpstreamConfigService_TestPersistsRotatedSub2APITokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new","expires_in":3600}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus","platform":"openai","rate_multiplier":0.1}}],"pages":1}}`))
		case "/api/v1/groups/available":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus","platform":"openai","rate_multiplier":0.1}]}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(81)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "Sub2API Main",
		Provider: UpstreamProviderSub2API,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken:    "jwt-old",
			AccountCredentialSub2APIRefreshToken:   "refresh-old",
			AccountCredentialSub2APITokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	require.NoError(t, svc.Test(context.Background(), configID))
	require.Len(t, repo.savedTokens, 1)
	require.Equal(t, "jwt-new", repo.savedTokens[0].accessToken)
	require.Equal(t, "refresh-new", repo.savedTokens[0].refreshToken)
}

func TestUpstreamConfigService_GroupRateUnauthorizedRefreshesBeforeSync(t *testing.T) {
	refreshCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			refreshCount++
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new","expires_in":3600}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus","platform":"openai","rate_multiplier":0.1}}],"pages":1}}`))
		case "/api/v1/groups/available":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus","platform":"openai","rate_multiplier":0.1}]}`))
		case "/api/v1/groups/rates":
			if auth == "Bearer jwt-old" {
				http.Error(w, "expired", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"10":0.06}}`))
		case "/api/v1/auth/me":
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":1,"email":"u@example.com"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(82)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "Sub2API Main",
		Provider: UpstreamProviderSub2API,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken:  "jwt-old",
			AccountCredentialSub2APIRefreshToken: "refresh-old",
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	keys, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, refreshCount)
	require.Len(t, repo.savedTokens, 1)
	require.Len(t, keys, 1)
	require.InDelta(t, 0.06, *keys[0].RateMultiplier, 1e-12)
}

func TestUpstreamConfigService_SyncKeysRecordsProfileBalanceSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"10":0.065}}`))
		case "/api/v1/auth/me":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":27,"email":"owner@example.com","balance":12.34,"total_recharged":169.17}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(7)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(configID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, server.URL)},
	}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, repo.extraUpdates, 1)
	updates := repo.extraUpdates[0].updates
	require.Equal(t, configID, repo.extraUpdates[0].id)
	require.InDelta(t, 12.34, updates["sub2api_balance"], 1e-12)
	require.InDelta(t, 169.17, updates["sub2api_total_recharged"], 1e-12)
	require.Equal(t, "owner@example.com", updates["sub2api_user_email"])
	require.Equal(t, int64(27), updates["sub2api_user_id"])
	require.NotEmpty(t, updates["sub2api_balance_synced_at"])
	require.Equal(t, "", updates["sub2api_balance_last_error"])
}

func TestUpstreamConfigService_ProfileFailureDoesNotFailKeySyncOrOverwriteBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/v1/auth/me":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(7)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{{
			ID:       configID,
			Name:     "Sub2API Main",
			Provider: UpstreamProviderSub2API,
			BaseURL:  server.URL,
			AuthMode: UpstreamAuthModeUserLogin,
			Credentials: map[string]any{
				AccountCredentialSub2APILoginEmail:    "admin@example.com",
				AccountCredentialSub2APILoginPassword: "secret",
			},
			Extra:  map[string]any{"sub2api_balance": 88.8},
			Status: StatusActive,
		}},
	}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, repo.checks, 1)
	require.True(t, repo.checks[0].success)
	require.Len(t, repo.extraUpdates, 1)
	updates := repo.extraUpdates[0].updates
	require.NotContains(t, updates, "sub2api_balance")
	require.Contains(t, updates["sub2api_balance_last_error"], "status 404")
	require.NotEmpty(t, updates["sub2api_balance_last_error_at"])
	require.Equal(t, 88.8, repo.configs[0].Extra["sub2api_balance"])
}

func TestUpstreamConfigService_TestDoesNotRecordProfileBalance(t *testing.T) {
	profileCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1440,"key":"sk-bound","name":"plus","group_id":10,"group":{"id":10,"name":"Plus Group","platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/v1/auth/me":
			profileCalled = true
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(7)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{testUpstreamConfig(configID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, server.URL)}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	require.NoError(t, svc.Test(context.Background(), configID))
	require.False(t, profileCalled)
	require.Empty(t, repo.extraUpdates)
	require.Len(t, repo.checks, 1)
	require.True(t, repo.checks[0].success)
}

func TestUpstreamConfigService_SyncActiveSub2APIConfigsOnlySyncsActiveSub2API(t *testing.T) {
	loginCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-active","name":"pro","group_id":10,"group":{"id":10,"name":"Pro","platform":"openai","rate_multiplier":0.1}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{
		testUpstreamConfig(1, "active sub2api", UpstreamProviderSub2API, StatusActive, server.URL),
		testUpstreamConfig(2, "inactive sub2api", UpstreamProviderSub2API, StatusDisabled, server.URL),
		testUpstreamConfig(3, "active newapi", UpstreamProviderNewAPI, StatusActive, server.URL),
	}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	results := svc.SyncActiveSub2APIConfigs(context.Background())

	require.Len(t, results, 1)
	require.True(t, results[0].Success)
	require.Equal(t, int64(1), results[0].ConfigID)
	require.Equal(t, 1, results[0].KeyCount)
	require.Equal(t, 1, loginCount)
}

func TestUpstreamConfigService_SyncKeysNewAPIUpsertsPagedKeysAndSnapshot(t *testing.T) {
	loginCount := 0
	tokenPages := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			loginCount++
			require.Equal(t, http.MethodPost, r.Method)
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"message":"","data":{"id":4798,"username":"owner"}}`))
		case "/api/user/self/groups":
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			require.NotEmpty(t, r.Header.Get("Cookie"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"gptplus":{"desc":"team","ratio":"0.06"},"gptproo":{"desc":"pro","ratio":0.15}}}`))
		case "/api/token/":
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			page := r.URL.Query().Get("p")
			tokenPages[page]++
			if page == "0" {
				items := make([]map[string]any, 0, 100)
				items = append(items, map[string]any{"id": 14287, "user_id": 4798, "key": "sk-plus", "status": 1, "name": "plus", "group": "gptplus", "used_quota": 0, "remain_quota": 0, "unlimited_quota": true})
				items = append(items, map[string]any{"id": 9128, "user_id": 4798, "key": "sk-pro", "status": 2, "name": "pro", "group": "gptproo", "used_quota": 4913005, "remain_quota": 0, "unlimited_quota": true})
				for i := 2; i < 100; i++ {
					items = append(items, map[string]any{"id": 20000 + i, "user_id": 4798, "key": "sk-fill-" + strconv.Itoa(i), "status": 1, "name": "fill", "group": "unknown"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"page": 1, "page_size": 100, "total": 101, "items": items}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"page": 2, "page_size": 100, "total": 101, "items": []map[string]any{{"id": 30101, "user_id": 4798, "key": "sk-last", "status": 1, "name": "last", "group": "gptplus"}}}})
		case "/api/user/self":
			require.Equal(t, "4798", r.Header.Get("New-Api-User"))
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"email":"owner@example.com","username":"owner","display_name":"Owner","group":"default","quota":86995,"used_quota":4913005,"request_count":701}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_display_type":"USD","quota_per_unit":500000,"usd_exchange_rate":7.3,"custom_currency_symbol":"¤","custom_currency_exchange_rate":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(70)
	keyID := int64(88)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{{
			ID:       configID,
			Name:     "NewAPI Main",
			Provider: UpstreamProviderNewAPI,
			BaseURL:  server.URL,
			AuthMode: UpstreamAuthModeUserLogin,
			Credentials: map[string]any{
				AccountCredentialNewAPILoginUsername: "owner@example.com",
				AccountCredentialNewAPILoginPassword: "secret",
			},
			Status: StatusActive,
		}},
		keys: []UpstreamKey{{
			ID:               keyID,
			UpstreamConfigID: configID,
			Key:              "sk-plus",
			KeyHash:          HashUpstreamKey("sk-plus"),
			Platform:         PlatformOpenAI,
			Status:           StatusActive,
		}},
	}
	accountRepo := &sub2APIRateSyncAccountRepo{accounts: []Account{{
		ID:               501,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		Concurrency:      100,
	}}}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)

	keys, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 101, result.KeyCount)
	require.Equal(t, 1, result.UpdatedAccountCount)
	require.Equal(t, 1, loginCount)
	require.Equal(t, 1, tokenPages["0"])
	require.Equal(t, 1, tokenPages["1"])
	require.Len(t, keys, 101)
	require.Len(t, repo.upserts, 101)
	require.Equal(t, "plus", repo.upserts[0].Name)
	require.Equal(t, "gptplus", repo.upserts[0].UpstreamGroupName)
	require.NotNil(t, repo.upserts[0].RateMultiplier)
	require.InDelta(t, 0.06, *repo.upserts[0].RateMultiplier, 1e-12)
	require.Equal(t, StatusDisabled, repo.upserts[1].Status)
	require.Len(t, accountRepo.bulkUpdates, 1)
	require.InDelta(t, 0.06, *accountRepo.bulkUpdates[0].updates.RateMultiplier, 1e-12)
	require.Equal(t, 6, *accountRepo.bulkUpdates[0].updates.Priority)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.LoadFactor)
	require.Equal(t, 150, *accountRepo.bulkUpdates[0].updates.LoadFactor)
	require.Len(t, repo.extraUpdates, 1)
	snapshot, ok := repo.extraUpdates[0].updates["upstream_provider_snapshot"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, UpstreamProviderNewAPI, snapshot["provider"])
	require.InDelta(t, 86995.0, snapshot["quota"], 1e-12)
	require.InDelta(t, 4913005.0, snapshot["used_quota"], 1e-12)
	require.InDelta(t, 86995.0, snapshot["remain_quota"], 1e-12)
	require.InDelta(t, 5000000.0, snapshot["total_quota"], 1e-12)
	require.InDelta(t, 0.17399, snapshot["balance_amount"], 1e-12)
	require.InDelta(t, 9.82601, snapshot["used_amount"], 1e-12)
	require.InDelta(t, 10.0, snapshot["total_amount"], 1e-12)
	require.Equal(t, "USD", snapshot["currency"])
	require.Equal(t, "$", snapshot["currency_symbol"])
	require.InDelta(t, 500000.0, snapshot["quota_per_unit"], 1e-12)
	require.Equal(t, "owner@example.com", snapshot["email"])
}

func TestUpstreamConfigService_NewAPIProfileFailureDoesNotFailKeySync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798}}`))
		case "/api/user/self/groups":
			_, _ = w.Write([]byte(`{"success":true,"data":{"gptplus":{"desc":"team","ratio":0.06}}}`))
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":{"page":1,"page_size":100,"total":1,"items":[{"id":14287,"user_id":4798,"key":"sk-plus","status":1,"name":"plus","group":"gptplus"}]}}`))
		case "/api/user/self":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(71)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "NewAPI Main",
		Provider: UpstreamProviderNewAPI,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret",
		},
		Extra:  map[string]any{"upstream_provider_snapshot": map[string]any{"quota": 123}},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, 1, result.KeyCount)
	require.Len(t, repo.extraUpdates, 1)
	require.NotContains(t, repo.extraUpdates[0].updates, "upstream_provider_snapshot")
	require.Contains(t, repo.extraUpdates[0].updates["upstream_provider_snapshot_last_error"], "status 404")
}

func TestUpstreamConfigService_NewAPILoginFailureReturnsSanitizedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		require.Equal(t, "/api/user/login", r.URL.Path)
		_, _ = w.Write([]byte(`{"success":false,"message":"bad owner@example.com secret-password sk-0123456789abcdef cookie=session-secret","data":{}}`))
	}))
	defer server.Close()

	configID := int64(73)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "NewAPI Main",
		Provider: UpstreamProviderNewAPI,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret-password",
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.False(t, result.Success)
	require.Len(t, repo.checks, 1)
	for _, text := range []string{err.Error(), result.Error, repo.checks[0].err} {
		require.NotContains(t, text, "owner@example.com")
		require.NotContains(t, text, "secret-password")
		require.NotContains(t, text, "sk-0123456789abcdef")
		require.NotContains(t, text, "session-secret")
		require.Contains(t, text, "[REDACTED]")
		require.Contains(t, text, "sk-***")
	}
	require.False(t, repo.checks[0].success)
}

func TestUpstreamConfigService_NewAPIExtraUpdateFailureFailsSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798}}`))
		case "/api/user/self/groups":
			_, _ = w.Write([]byte(`{"success":true,"data":{"gptplus":{"ratio":0.06}}}`))
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":{"page":1,"page_size":100,"total":1,"items":[{"id":14287,"user_id":4798,"key":"sk-plus","status":1,"name":"plus","group":"gptplus"}]}}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"email":"owner@example.com","quota":10,"used_quota":4}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(74)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{{
			ID:       configID,
			Name:     "NewAPI Main",
			Provider: UpstreamProviderNewAPI,
			BaseURL:  server.URL,
			AuthMode: UpstreamAuthModeUserLogin,
			Credentials: map[string]any{
				AccountCredentialNewAPILoginUsername: "owner@example.com",
				AccountCredentialNewAPILoginPassword: "secret",
			},
			Status: StatusActive,
		}},
		updateExtraErr: errors.New("database write failed"),
	}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "database write failed")
	require.Len(t, repo.checks, 1)
	require.False(t, repo.checks[0].success)
}

func TestUpstreamConfigService_NewAPIResolvesMaskedKeys(t *testing.T) {
	batchCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798}}`))
		case "/api/user/self/groups":
			_, _ = w.Write([]byte(`{"success":true,"data":{"gptplus":{"ratio":0.06}}}`))
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":{"page":1,"page_size":100,"total":3,"items":[{"id":1,"user_id":4798,"key":"sk-visible","status":1,"name":"visible","group":"gptplus"},{"id":2,"user_id":4798,"key":"sk-********","status":1,"name":"masked","group":"gptplus"},{"id":3,"user_id":4798,"key":"","status":1,"name":"empty","group":"gptplus"}]}}`))
		case "/api/token/batch/keys":
			batchCalled = true
			_, _ = w.Write([]byte(`{"success":true,"data":{"keys":{"2":"sk-unmasked"}}}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"email":"owner@example.com","quota":10,"used_quota":4}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(75)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "NewAPI Main",
		Provider: UpstreamProviderNewAPI,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret",
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	keys, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.True(t, batchCalled)
	require.Equal(t, 2, result.KeyCount)
	require.Len(t, keys, 2)
	require.Len(t, repo.upserts, 2)
	require.Equal(t, "sk-visible", repo.upserts[0].Key)
	require.Equal(t, "sk-unmasked", repo.upserts[1].Key)
}

func TestUpstreamConfigService_NewAPILoginRequiresUserID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
		_, _ = w.Write([]byte(`{"success":true,"data":{"username":"owner"}}`))
	}))
	defer server.Close()

	configID := int64(72)
	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID:       configID,
		Name:     "NewAPI Main",
		Provider: UpstreamProviderNewAPI,
		BaseURL:  server.URL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret",
		},
		Status: StatusActive,
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "user id")
	require.Len(t, repo.checks, 1)
	require.False(t, repo.checks[0].success)
}

func TestUpstreamConfigService_SyncActiveUpstreamConfigsIncludesNewAPI(t *testing.T) {
	sub2LoginCount := 0
	newAPILoginCount := 0
	sub2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			sub2LoginCount++
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-active","name":"pro","group_id":10,"group":{"id":10,"name":"Pro","platform":"openai","rate_multiplier":0.1}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer sub2Server.Close()
	newAPIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			newAPILoginCount++
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798}}`))
		case "/api/user/self/groups":
			_, _ = w.Write([]byte(`{"success":true,"data":{"gptplus":{"ratio":0.06}}}`))
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":{"page":1,"page_size":100,"total":1,"items":[{"id":1,"user_id":4798,"key":"sk-newapi","status":1,"name":"plus","group":"gptplus"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer newAPIServer.Close()

	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{
		testUpstreamConfig(1, "active sub2api", UpstreamProviderSub2API, StatusActive, sub2Server.URL),
		testUpstreamConfig(2, "active newapi", UpstreamProviderNewAPI, StatusActive, newAPIServer.URL),
		testUpstreamConfig(3, "inactive newapi", UpstreamProviderNewAPI, StatusDisabled, newAPIServer.URL),
		testUpstreamConfig(4, "other", UpstreamProviderOther, StatusActive, newAPIServer.URL),
	}}
	repo.configs[1].Credentials = map[string]any{AccountCredentialNewAPILoginUsername: "owner@example.com", AccountCredentialNewAPILoginPassword: "secret"}
	repo.configs[2].Credentials = repo.configs[1].Credentials
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	results := svc.SyncActiveUpstreamConfigs(context.Background())

	require.Len(t, results, 2)
	require.Equal(t, 1, sub2LoginCount)
	require.Equal(t, 1, newAPILoginCount)
	require.ElementsMatch(t, []int64{1, 2}, []int64{results[0].ConfigID, results[1].ConfigID})
}

func testUpstreamConfig(id int64, name, provider, status, baseURL string) UpstreamConfig {
	return UpstreamConfig{
		ID:       id,
		Name:     name,
		Provider: provider,
		BaseURL:  baseURL,
		AuthMode: UpstreamAuthModeUserLogin,
		Credentials: map[string]any{
			AccountCredentialSub2APILoginEmail:    "admin@example.com",
			AccountCredentialSub2APILoginPassword: "secret",
		},
		Status: status,
	}
}

func cloneUpstreamConfig(cfg UpstreamConfig) UpstreamConfig {
	cfg.Credentials = cloneAnyMap(cfg.Credentials)
	cfg.Extra = cloneAnyMap(cfg.Extra)
	if len(cfg.Keys) > 0 {
		cfg.Keys = append([]*UpstreamKey(nil), cfg.Keys...)
	}
	return cfg
}

func cloneUpstreamKey(key UpstreamKey) UpstreamKey {
	key.Extra = cloneAnyMap(key.Extra)
	return key
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestUpstreamConfigService_SyncFailureDoesNotOverwriteBoundAccountRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/auth/login") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
			return
		}
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	configID := int64(7)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{testUpstreamConfig(configID, "Sub2API Main", UpstreamProviderSub2API, StatusActive, server.URL)},
		keys: []UpstreamKey{{
			ID:               1440,
			UpstreamConfigID: configID,
			Key:              "sk-bound",
			KeyHash:          HashUpstreamKey("sk-bound"),
			Platform:         PlatformOpenAI,
			RateMultiplier:   upstreamConfigTestFloat64(0.12),
			Status:           StatusActive,
			LastSeenAt:       upstreamConfigTestTime(time.Now()),
		}},
	}
	accountRepo := &sub2APIRateSyncAccountRepo{}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)

	_, _, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.Empty(t, accountRepo.bulkUpdates)
	require.Len(t, repo.checks, 1)
	require.False(t, repo.checks[0].success)
}

func TestNormalizeAndValidateUpstreamConfig_ManualJWTAllowsAccessOrRefresh(t *testing.T) {
	base := UpstreamConfig{
		Name:     "JWT Upstream",
		Provider: UpstreamProviderSub2API,
		BaseURL:  "https://upstream.example.com",
		AuthMode: UpstreamAuthModeManualJWT,
		Status:   StatusActive,
	}

	t.Run("access token only", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{AccountCredentialSub2APIAccessToken: "jwt-token"}
		require.NoError(t, normalizeAndValidateUpstreamConfig(&cfg, true))
	})

	t.Run("refresh token only", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{AccountCredentialSub2APIRefreshToken: "refresh-token"}
		require.NoError(t, normalizeAndValidateUpstreamConfig(&cfg, true))
	})

	t.Run("missing both tokens", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{}
		require.ErrorContains(t, normalizeAndValidateUpstreamConfig(&cfg, true), "access token or refresh token")
	})
}

func TestNormalizeAndValidateUpstreamConfig_NewAPIRequiresUsernameAndPassword(t *testing.T) {
	base := UpstreamConfig{
		Name:     "NewAPI Upstream",
		Provider: UpstreamProviderNewAPI,
		BaseURL:  "https://newapi.example.com",
		AuthMode: UpstreamAuthModeUserLogin,
		Status:   StatusActive,
	}

	t.Run("missing username", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{AccountCredentialNewAPILoginPassword: "secret"}
		require.ErrorContains(t, normalizeAndValidateUpstreamConfig(&cfg, true), "login username")
	})

	t.Run("missing password", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{AccountCredentialNewAPILoginUsername: "owner@example.com"}
		require.ErrorContains(t, normalizeAndValidateUpstreamConfig(&cfg, true), "login password")
	})

	t.Run("complete credentials", func(t *testing.T) {
		cfg := base
		cfg.Credentials = map[string]any{
			AccountCredentialNewAPILoginUsername: "owner@example.com",
			AccountCredentialNewAPILoginPassword: "secret",
		}
		require.NoError(t, normalizeAndValidateUpstreamConfig(&cfg, true))
	})
}

func upstreamConfigTestFloat64(v float64) *float64 {
	return &v
}

func upstreamConfigTestTime(v time.Time) *time.Time {
	return &v
}
