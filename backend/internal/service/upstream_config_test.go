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

func (r *upstreamConfigServiceRepo) SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.savedTokens = append(r.savedTokens, upstreamConfigSavedToken{id: id, accessToken: accessToken, refreshToken: refreshToken})
	for i := range r.configs {
		if r.configs[i].ID != id {
			continue
		}
		if r.configs[i].Credentials == nil {
			r.configs[i].Credentials = map[string]any{}
		}
		r.configs[i].Credentials[AccountCredentialSub2APIAccessToken] = accessToken
		r.configs[i].Credentials[AccountCredentialSub2APIRefreshToken] = refreshToken
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
		Type:             AccountTypeUpstream,
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

	keys, _, err := svc.SyncKeys(context.Background(), configID)

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
	require.Len(t, repo.extraUpdates, 1)
	snapshot, ok := repo.extraUpdates[0].updates["upstream_provider_snapshot"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, UpstreamProviderNewAPI, snapshot["provider"])
	require.InDelta(t, 86995.0, snapshot["quota"], 1e-12)
	require.InDelta(t, 4913005.0, snapshot["used_quota"], 1e-12)
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

func TestUpstreamConfigService_NewAPISkipsMaskedKeys(t *testing.T) {
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
	require.Equal(t, 1, result.KeyCount)
	require.Len(t, keys, 1)
	require.Len(t, repo.upserts, 1)
	require.Equal(t, "sk-visible", repo.upserts[0].Key)
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
