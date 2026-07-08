package service

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	upserts     []UpstreamKey
	checks      []upstreamConfigCheck
	savedTokens []upstreamConfigSavedToken
	mu          sync.Mutex
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

func (r *upstreamConfigServiceRepo) SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.savedTokens = append(r.savedTokens, upstreamConfigSavedToken{id: id, accessToken: accessToken, refreshToken: refreshToken})
	return nil
}

func TestUpstreamConfigService_SyncKeysUpsertsKeysAndUpdatesBoundAccounts(t *testing.T) {
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
	}}}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)

	keys, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Len(t, repo.upserts, 1)
	require.Equal(t, "plus", repo.upserts[0].Name)
	require.Equal(t, "Plus Group", repo.upserts[0].UpstreamGroupName)
	require.NotNil(t, repo.upserts[0].RateMultiplier)
	require.InDelta(t, 0.065, *repo.upserts[0].RateMultiplier, 1e-12)
	require.Len(t, accountRepo.bulkUpdates, 1)
	require.Equal(t, []int64{101}, accountRepo.bulkUpdates[0].ids)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.RateMultiplier)
	require.InDelta(t, 0.065, *accountRepo.bulkUpdates[0].updates.RateMultiplier, 1e-12)
	require.NotNil(t, accountRepo.bulkUpdates[0].updates.Priority)
	require.Equal(t, 7, *accountRepo.bulkUpdates[0].updates.Priority)
	require.Equal(t, "Plus Group", accountRepo.bulkUpdates[0].updates.Extra["sub2api_upstream_group_name"])
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

	_, err := svc.SyncKeys(context.Background(), configID)

	require.Error(t, err)
	require.Empty(t, accountRepo.bulkUpdates)
	require.Len(t, repo.checks, 1)
	require.False(t, repo.checks[0].success)
}

func upstreamConfigTestFloat64(v float64) *float64 {
	return &v
}

func upstreamConfigTestTime(v time.Time) *time.Time {
	return &v
}
