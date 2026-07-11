package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type sub2APIRateSyncAccountRepo struct {
	AccountRepository

	accounts     []Account
	bulkUpdates  []sub2APIRateSyncBulkUpdate
	extraUpdates []sub2APIRateSyncExtraUpdate
	mu           sync.Mutex
}

type sub2APIRateSyncBulkUpdate struct {
	ids     []int64
	updates AccountBulkUpdate
}

type sub2APIRateSyncExtraUpdate struct {
	id      int64
	updates map[string]any
}

type sub2APIRateSyncProxyRepo struct {
	ProxyRepository

	proxies   map[int64]Proxy
	err       error
	listCalls int
	getCalls  int
	mu        sync.Mutex
}

type sub2APIRateSyncUpstreamRepo struct {
	UpstreamConfigRepository

	settings *UpstreamSettings
	err      error
}

func (r *sub2APIRateSyncUpstreamRepo) GetUpstreamSettings(ctx context.Context) (*UpstreamSettings, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.settings == nil {
		return &UpstreamSettings{}, nil
	}
	settings := *r.settings
	return &settings, nil
}

func enableLegacySub2APICompliance(svc *Sub2APIUpstreamRateSyncService) {
	svc.SetUpstreamConfigRepository(&sub2APIRateSyncUpstreamRepo{settings: &UpstreamSettings{Sub2APINotInCNConfirmed: true}})
}

func (r *sub2APIRateSyncProxyRepo) ListByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.listCalls++
	if r.err != nil {
		return nil, r.err
	}
	out := make([]Proxy, 0, len(ids))
	for _, id := range ids {
		if proxy, ok := r.proxies[id]; ok {
			out = append(out, proxy)
		}
	}
	return out, nil
}

func (r *sub2APIRateSyncProxyRepo) GetByID(ctx context.Context, id int64) (*Proxy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.getCalls++
	if r.err != nil {
		return nil, r.err
	}
	proxy, ok := r.proxies[id]
	if !ok {
		return nil, ErrProxyNotFound
	}
	return &proxy, nil
}

func (r *sub2APIRateSyncAccountRepo) ListWithFilters(ctx context.Context, params pagination.PaginationParams, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, *pagination.PaginationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Account, len(r.accounts))
	copy(out, r.accounts)
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: len(out), Pages: 1}, nil
}

func (r *sub2APIRateSyncAccountRepo) BulkUpdate(ctx context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	copiedIDs := append([]int64(nil), ids...)
	credentials := make(map[string]any, len(updates.Credentials))
	for k, v := range updates.Credentials {
		credentials[k] = v
	}
	extra := make(map[string]any, len(updates.Extra))
	for k, v := range updates.Extra {
		extra[k] = v
	}
	updates.Credentials = credentials
	updates.Extra = extra
	r.bulkUpdates = append(r.bulkUpdates, sub2APIRateSyncBulkUpdate{ids: copiedIDs, updates: updates})
	return int64(len(ids)), nil
}

func (r *sub2APIRateSyncAccountRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	copied := make(map[string]any, len(updates))
	for k, v := range updates {
		copied[k] = v
	}
	r.extraUpdates = append(r.extraUpdates, sub2APIRateSyncExtraUpdate{id: id, updates: copied})
	return nil
}

func TestSub2APIUpstreamPriority(t *testing.T) {
	require.Equal(t, 10, Sub2APIUpstreamPriority(0.1))
	require.Equal(t, 6, Sub2APIUpstreamPriority(0.06))
	require.Equal(t, 7, Sub2APIUpstreamPriority(0.065))
}

func TestSub2APIUpstreamRateSync_RunOnceDoesNotUseDeprecatedAccountScanFallback(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{accounts: []Account{
		newSub2APIRateSyncAccount(1, server.URL, "sk-upstream"),
	}}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)

	svc.runOnce()

	require.Zero(t, requestCount)
	require.Empty(t, repo.bulkUpdates)
	require.Empty(t, repo.extraUpdates)
}

func TestSub2APIUpstreamRateSync_UsesAccountProxyForUserLoginAndFallback(t *testing.T) {
	var loginCount, keysCount, fallbackCount, ratesCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "proxy-a", r.Header.Get("X-Test-Proxy-ID"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			keysCount++
			http.NotFound(w, r)
		case "/api/v1/api-keys":
			fallbackCount++
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/available":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":[{"id":10,"name":"Plus","platform":"openai","rate_multiplier":0.12}]}`))
		case "/api/v1/groups/rates":
			ratesCount++
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	proxyServer := newSub2APITestHTTPProxy(t, "proxy-a")
	defer proxyServer.Close()
	proxyID := int64(7)
	account := newSub2APIRateSyncAccount(42, upstream.URL+"/v1", "sk-upstream")
	account.ProxyID = &proxyID
	repo := &sub2APIRateSyncAccountRepo{}
	proxyRepo := &sub2APIRateSyncProxyRepo{proxies: map[int64]Proxy{
		proxyID: proxyFromTestServer(t, proxyID, proxyServer),
	}}
	svc := NewSub2APIUpstreamRateSyncService(repo, proxyRepo, time.Minute)
	enableLegacySub2APICompliance(svc)

	err := svc.SyncAccountNow(context.Background(), &account)

	require.NoError(t, err)
	require.Equal(t, 1, loginCount)
	require.Equal(t, 1, keysCount)
	require.Equal(t, 1, fallbackCount)
	require.Equal(t, 1, ratesCount)
	require.Equal(t, 1, proxyRepo.getCalls)
	require.Len(t, repo.bulkUpdates, 1)
	require.Empty(t, repo.extraUpdates)
}

func TestSub2APIUpstreamRateSync_KeysFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			http.NotFound(w, r)
		case "/api/v1/api-keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"anthropic","rate_multiplier":0.1}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	enableLegacySub2APICompliance(svc)

	err := svc.SyncAccountNow(context.Background(), ptrAccount(newSub2APIRateSyncAccount(42, server.URL+"/v1", "sk-upstream")))

	require.NoError(t, err)
	require.Len(t, repo.bulkUpdates, 1)
	require.InDelta(t, 0.1, *repo.bulkUpdates[0].updates.RateMultiplier, 1e-12)
	require.Equal(t, 10, *repo.bulkUpdates[0].updates.Priority)
	require.Equal(t, 150, *repo.bulkUpdates[0].updates.LoadFactor)
}

func TestSub2APIUpstreamRateSync_ManualJWTSkipsLogin(t *testing.T) {
	loginCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCount++
			http.Error(w, "login should not be called", http.StatusInternalServerError)
		case "/api/v1/keys":
			require.Equal(t, "Bearer manual-jwt", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.12}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer manual-jwt", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)

	err := svc.SyncAccountNow(context.Background(), ptrAccount(newSub2APIManualJWTRateSyncAccount(42, server.URL+"/api/v1", "sk-upstream", "manual-jwt")))

	require.NoError(t, err)
	require.Equal(t, 0, loginCount)
	require.Len(t, repo.bulkUpdates, 1)
	require.InDelta(t, 0.12, *repo.bulkUpdates[0].updates.RateMultiplier, 1e-12)
	require.Equal(t, 12, *repo.bulkUpdates[0].updates.Priority)
	require.Equal(t, 100, *repo.bulkUpdates[0].updates.LoadFactor)
}

func TestSub2APIUpstreamRateSync_LegacyUserLoginFailsClosedWhenComplianceSettingsUnavailable(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	account := newSub2APIRateSyncAccount(42, server.URL, "sk-upstream")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.ErrorContains(t, err, "compliance settings")
	require.Zero(t, requestCount)
	require.Empty(t, repo.bulkUpdates)
	require.Len(t, repo.extraUpdates, 1)
}

func TestSub2APIUpstreamRateSync_ManualJWTRefreshesExpiredTokenAndRetries(t *testing.T) {
	refreshCount := 0
	keysCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			t.Fatal("manual jwt sync must not call login")
		case "/api/v1/auth/refresh":
			refreshCount++
			require.Equal(t, http.MethodPost, r.Method)
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "refresh-old", body["refresh_token"])
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new"}}`))
		case "/api/v1/keys":
			keysCount++
			switch r.Header.Get("Authorization") {
			case "Bearer jwt-old":
				http.Error(w, `{"code":401,"reason":"TOKEN_EXPIRED"}`, http.StatusUnauthorized)
			case "Bearer jwt-new":
				_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.07}}],"page":1,"page_size":100,"pages":1}}`))
			default:
				t.Fatalf("unexpected auth header %q", r.Header.Get("Authorization"))
			}
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	account := newSub2APIManualJWTRefreshRateSyncAccount(42, server.URL, "sk-upstream", "jwt-old", "refresh-old")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.NoError(t, err)
	require.Equal(t, 1, refreshCount)
	require.Equal(t, 2, keysCount)
	require.Len(t, repo.bulkUpdates, 2)
	tokenUpdate := repo.bulkUpdates[0]
	require.Equal(t, []int64{42}, tokenUpdate.ids)
	require.Equal(t, "jwt-new", tokenUpdate.updates.Credentials[AccountCredentialSub2APIAccessToken])
	require.Equal(t, "refresh-new", tokenUpdate.updates.Credentials[AccountCredentialSub2APIRefreshToken])
	rateUpdate := repo.bulkUpdates[1]
	require.InDelta(t, 0.07, *rateUpdate.updates.RateMultiplier, 1e-12)
	require.Equal(t, 7, *rateUpdate.updates.Priority)
	require.Equal(t, 150, *rateUpdate.updates.LoadFactor)
	require.Empty(t, repo.extraUpdates)
}

func TestSub2APIUpstreamRateSync_ManualJWTRefreshOnlyFetchesAccessTokenBeforeSync(t *testing.T) {
	refreshCount := 0
	keysCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			t.Fatal("manual jwt sync must not call login")
		case "/api/v1/auth/refresh":
			refreshCount++
			require.Equal(t, http.MethodPost, r.Method)
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "refresh-only", body["refresh_token"])
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-new","refresh_token":"refresh-new"}}`))
		case "/api/v1/keys":
			keysCount++
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.08}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-new", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	account := newSub2APIManualJWTRefreshRateSyncAccount(42, server.URL, "sk-upstream", "", "refresh-only")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.NoError(t, err)
	require.Equal(t, 1, refreshCount)
	require.Equal(t, 1, keysCount)
	require.Len(t, repo.bulkUpdates, 2)
	tokenUpdate := repo.bulkUpdates[0]
	require.Equal(t, "jwt-new", tokenUpdate.updates.Credentials[AccountCredentialSub2APIAccessToken])
	require.Equal(t, "refresh-new", tokenUpdate.updates.Credentials[AccountCredentialSub2APIRefreshToken])
	rateUpdate := repo.bulkUpdates[1]
	require.InDelta(t, 0.08, *rateUpdate.updates.RateMultiplier, 1e-12)
	require.Equal(t, 8, *rateUpdate.updates.Priority)
	require.Empty(t, repo.extraUpdates)
}

func TestSub2APIUpstreamRateSync_ManualJWTRefreshFailureDoesNotUpdateRateOrTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/keys":
			http.Error(w, `{"code":401,"reason":"TOKEN_EXPIRED"}`, http.StatusUnauthorized)
		case "/api/v1/auth/refresh":
			http.Error(w, `{"code":401,"reason":"invalid_refresh_token","data":{"refresh_token":"refresh-secret"}}`, http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	account := newSub2APIManualJWTRefreshRateSyncAccount(42, server.URL, "sk-secret", "jwt-secret", "refresh-secret")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.Error(t, err)
	require.Empty(t, repo.bulkUpdates)
	require.Len(t, repo.extraUpdates, 1)
	errText := fmt.Sprint(repo.extraUpdates[0].updates["sub2api_rate_sync_last_error"])
	require.Contains(t, errText, "refresh")
	require.NotContains(t, errText, "jwt-secret")
	require.NotContains(t, errText, "refresh-secret")
	require.NotContains(t, errText, "sk-secret")
}

func TestSub2APIUpstreamRateSync_ManualJWTExpiredHintDoesNotLeakToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/keys":
			http.Error(w, `{"code":401,"reason":"TOKEN_EXPIRED"}`, http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	account := newSub2APIManualJWTRateSyncAccount(44, server.URL, "sk-secret", "jwt-secret")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.Error(t, err)
	require.Empty(t, repo.bulkUpdates)
	require.Len(t, repo.extraUpdates, 1)
	errText := fmt.Sprint(repo.extraUpdates[0].updates["sub2api_rate_sync_last_error"])
	require.Contains(t, errText, "may be expired")
	require.NotContains(t, errText, "jwt-secret")
	require.NotContains(t, errText, "sk-secret")
}

func TestSub2APIUpstreamRateSync_HiddenKeyFailsWithoutSecretInError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-***","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.1}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)
	enableLegacySub2APICompliance(svc)
	account := newSub2APIRateSyncAccount(11, server.URL, "sk-upstream-secret")

	err := svc.SyncAccountNow(context.Background(), &account)

	require.Error(t, err)
	require.Empty(t, repo.bulkUpdates)
	require.Len(t, repo.extraUpdates, 1)
	errText := fmt.Sprint(repo.extraUpdates[0].updates["sub2api_rate_sync_last_error"])
	require.NotContains(t, errText, "sk-upstream-secret")
	require.NotContains(t, errText, "secret-password")
	require.Contains(t, errText, "complete keys")
}

func TestSub2APIUpstreamRateSync_DuplicateKeyFails(t *testing.T) {
	rate := 0.1
	session := &sub2APIUserLoginSession{keys: []sub2APIUpstreamKey{
		{Key: "sk-same", GroupID: ptrSub2APITestInt64(1), Group: &struct {
			ID             int64    `json:"id"`
			Name           string   `json:"name"`
			Platform       string   `json:"platform"`
			RateMultiplier *float64 `json:"rate_multiplier"`
		}{ID: 1, Platform: "openai", RateMultiplier: &rate}},
		{Key: "sk-same", GroupID: ptrSub2APITestInt64(1), Group: &struct {
			ID             int64    `json:"id"`
			Name           string   `json:"name"`
			Platform       string   `json:"platform"`
			RateMultiplier *float64 `json:"rate_multiplier"`
		}{ID: 1, Platform: "openai", RateMultiplier: &rate}},
	}}

	_, _, err := resolveSub2APIEffectiveRate("sk-same", session)

	require.ErrorContains(t, err, "multiple")
}

func TestSub2APIUpstreamRateSync_SyncAccountNowSkipsNonSub2API(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
	}))
	defer server.Close()

	repo := &sub2APIRateSyncAccountRepo{}
	svc := NewSub2APIUpstreamRateSyncService(repo, nil, time.Minute)

	err := svc.SyncAccountNow(context.Background(), &Account{
		ID:          43,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": server.URL, "api_key": "sk-upstream"},
		Extra:       map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderNewAPI},
	})

	require.NoError(t, err)
	require.Equal(t, 0, requestCount)
	require.Empty(t, repo.bulkUpdates)
	require.Empty(t, repo.extraUpdates)
}

type apiKeyServiceRateRepoStub struct {
	UserGroupRateRepository
	rate *float64
	err  error
}

func (r *apiKeyServiceRateRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	return r.rate, r.err
}

func TestAPIKeyServiceResolveEffectiveRateMultiplier(t *testing.T) {
	groupID := int64(20)
	apiKey := &APIKey{
		UserID:  10,
		GroupID: &groupID,
		Group:   &Group{ID: groupID, RateMultiplier: 0.1},
	}

	t.Run("falls back to group default", func(t *testing.T) {
		svc := &APIKeyService{userGroupRateRepo: &apiKeyServiceRateRepoStub{}}
		got, err := svc.ResolveEffectiveRateMultiplier(context.Background(), apiKey)
		require.NoError(t, err)
		require.Equal(t, 0.1, got)
	})

	t.Run("user specific rate overrides group default", func(t *testing.T) {
		userRate := 0.06
		svc := &APIKeyService{userGroupRateRepo: &apiKeyServiceRateRepoStub{rate: &userRate}}
		got, err := svc.ResolveEffectiveRateMultiplier(context.Background(), apiKey)
		require.NoError(t, err)
		require.Equal(t, 0.06, got)
	})

	t.Run("repo error is returned", func(t *testing.T) {
		svc := &APIKeyService{userGroupRateRepo: &apiKeyServiceRateRepoStub{err: errors.New("db down")}}
		_, err := svc.ResolveEffectiveRateMultiplier(context.Background(), apiKey)
		require.ErrorContains(t, err, "db down")
	})
}

func newSub2APIRateSyncAccount(id int64, baseURL, apiKey string) Account {
	return Account{
		ID:          id,
		Type:        AccountTypeAPIKey,
		Concurrency: 100,
		Credentials: map[string]any{
			"base_url":                            baseURL,
			"api_key":                             apiKey,
			AccountCredentialSub2APILoginEmail:    "user@example.com",
			AccountCredentialSub2APILoginPassword: "secret-password",
		},
		Extra: map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
	}
}

func TestSyncSub2APIUpstreamKeysPreservesNamesAndGroupMetadata(t *testing.T) {
	longGroupName := strings.Repeat("长", 120)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/keys":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":0,"message":"success","data":{"items":[
				{"id":11917,"key":"sk-free","name":" free ","group_id":44,"group":{"id":44,"name":" ChatGPT-Plus【高并发-特惠通道】 ","platform":"openai","rate_multiplier":0.03}},
				{"id":10046,"key":"sk-plus","name":"plus","group":{"id":33,"name":%q,"platform":"openai","rate_multiplier":0.05}},
				{"id":1440,"key":"sk-pro","name":"","group_id":37,"group":{"id":37,"name":"","platform":"openai","rate_multiplier":0.12}}
			],"page":1,"page_size":100,"pages":1}}`, longGroupName)))
		case "/api/v1/groups/rates":
			require.Equal(t, "Bearer jwt-upstream", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"44":0.03,"33":0.05,"37":0.12}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := &UpstreamConfig{
		ID:       9,
		Name:     "乔燃",
		Provider: UpstreamProviderSub2API,
		SiteURL:  server.URL,
		AuthMode: UpstreamAuthModeManualJWT,
		Credentials: map[string]any{
			AccountCredentialSub2APIAccessToken: "jwt-upstream",
		},
	}

	keys, refreshed, err := syncSub2APIUpstreamKeys(context.Background(), cfg, "")

	require.NoError(t, err)
	require.Nil(t, refreshed)
	require.Len(t, keys, 3)
	require.Equal(t, "free", keys[0].Name)
	require.Equal(t, "ChatGPT-Plus【高并发-特惠通道】", keys[0].UpstreamGroupName)
	require.NotNil(t, keys[0].UpstreamGroupID)
	require.Equal(t, int64(44), *keys[0].UpstreamGroupID)
	require.Equal(t, "openai", keys[0].Platform)
	require.NotNil(t, keys[0].RateMultiplier)
	require.Equal(t, 0.03, *keys[0].RateMultiplier)

	require.Equal(t, "plus", keys[1].Name)
	require.NotNil(t, keys[1].UpstreamGroupID)
	require.Equal(t, int64(33), *keys[1].UpstreamGroupID, "group.id should backfill missing group_id")
	require.Len(t, []rune(keys[1].UpstreamGroupName), 100)

	require.Empty(t, keys[2].Name)
	require.Empty(t, keys[2].UpstreamGroupName)
	require.NotNil(t, keys[2].UpstreamGroupID)
	require.Equal(t, int64(37), *keys[2].UpstreamGroupID)
}

func newSub2APIManualJWTRateSyncAccount(id int64, baseURL, apiKey, token string) Account {
	return Account{
		ID:          id,
		Type:        AccountTypeAPIKey,
		Concurrency: 100,
		Credentials: map[string]any{
			"base_url":                          baseURL,
			"api_key":                           apiKey,
			AccountCredentialSub2APIAccessToken: token,
		},
		Extra: map[string]any{
			AccountUpstreamProviderKey:       AccountUpstreamProviderSub2API,
			AccountSub2APIRateSyncAdapterKey: AccountSub2APIRateSyncAdapterManualJWT,
		},
	}
}

func newSub2APIManualJWTRefreshRateSyncAccount(id int64, baseURL, apiKey, token, refreshToken string) Account {
	account := newSub2APIManualJWTRateSyncAccount(id, baseURL, apiKey, token)
	account.Credentials[AccountCredentialSub2APIRefreshToken] = refreshToken
	return account
}

func newSub2APITestHTTPProxy(t *testing.T, proxyID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, r.URL.IsAbs(), "request should arrive in absolute-form through HTTP proxy")
		outbound := r.Clone(r.Context())
		outbound.RequestURI = ""
		outbound.URL = cloneURL(r.URL)
		outbound.Host = r.URL.Host
		outbound.Header = r.Header.Clone()
		outbound.Header.Set("X-Test-Proxy-ID", proxyID)
		resp, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

func cloneURL(in *url.URL) *url.URL {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func proxyFromTestServer(t *testing.T, id int64, server *httptest.Server) Proxy {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	host, rawPort, err := net.SplitHostPort(parsed.Host)
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	return Proxy{
		ID:       id,
		Name:     fmt.Sprintf("proxy-%d", id),
		Protocol: parsed.Scheme,
		Host:     host,
		Port:     port,
		Status:   StatusActive,
	}
}

func ptrAccount(account Account) *Account {
	return &account
}

func ptrSub2APITestInt64(v int64) *int64 {
	return &v
}

func TestSanitizeSub2APISyncError(t *testing.T) {
	account := newSub2APIRateSyncAccount(1, "https://upstream.example", "sk-secret")
	err := errors.New("failed for user@example.com with sk-secret and secret-password")

	got := sanitizeSub2APISyncError(&account, err)

	require.NotContains(t, got, "user@example.com")
	require.NotContains(t, got, "sk-secret")
	require.NotContains(t, got, "secret-password")
	require.True(t, strings.Contains(got, "u***@example.com") || strings.Contains(got, "[REDACTED]"))
}

func TestSanitizeSub2APISyncErrorRedactsManualJWT(t *testing.T) {
	account := newSub2APIManualJWTRefreshRateSyncAccount(1, "https://upstream.example", "sk-secret", "jwt-secret", "refresh-secret")
	err := errors.New("failed with sk-secret and jwt-secret and refresh-secret")

	got := sanitizeSub2APISyncError(&account, err)

	require.NotContains(t, got, "sk-secret")
	require.NotContains(t, got, "jwt-secret")
	require.NotContains(t, got, "refresh-secret")
}

func TestSub2APIRefreshDataNormalizeDerivesJWTExpiry(t *testing.T) {
	expected := time.Now().UTC().Add(45 * time.Minute).Truncate(time.Second)
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, expected.Unix())))
	data := sub2APIRefreshData{
		AccessToken:  "header." + payload + ".signature",
		RefreshToken: "refresh-new",
	}

	data.normalize("refresh-old")

	require.NotNil(t, data.ExpiresAt)
	require.Equal(t, expected, *data.ExpiresAt)
}

func TestNormalizeSub2APIBaseURLRejectsUserInfo(t *testing.T) {
	_, err := normalizeSub2APIBaseURL("https://user:secret@example.com/v1")

	require.ErrorContains(t, err, "must not contain user info")
}
