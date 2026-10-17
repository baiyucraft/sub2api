package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type expiryETagSettings struct{ service.SettingRepository }

func (expiryETagSettings) GetValue(_ context.Context, key string) (string, error) {
	if key != service.SettingKeyUpstreamCacheGuard {
		return "", service.ErrSettingNotFound
	}
	s := service.DefaultUpstreamCacheGuardSettings()
	s.Mode = upstreamcacheguard.Protect
	raw, err := json.Marshal(s)
	return string(raw), err
}

// A frozen successful snapshot followed by failed refreshes exercises the real
// cache engine lifetime in the HTTP handler. It is not a cached response stub.
type expiryETagStore struct {
	upstreamcacheguard.Store
	mu    sync.Mutex
	down  bool
	state upstreamcacheguard.AccountState
}

type expiryETagTx struct {
	upstreamcacheguard.Tx
	store *expiryETagStore
}

func (s *expiryETagStore) TransactAccount(ctx context.Context, _, _, _ int64, fn func(upstreamcacheguard.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(&expiryETagTx{store: s})
}
func (tx *expiryETagTx) ReadAccount(_, _ int64, out any) (int64, error) {
	raw, _ := json.Marshal(tx.store.state)
	return 1, json.Unmarshal(raw, out)
}
func (tx *expiryETagTx) WriteAccount(_, _ int64, value any) (int64, error) {
	raw, _ := json.Marshal(value)
	return 1, json.Unmarshal(raw, &tx.store.state)
}
func (tx *expiryETagTx) AccountFacts(_, _ int64, _ time.Time) ([]upstreamcacheguard.Evidence, error) {
	return nil, nil
}
func (tx *expiryETagTx) LegacyAccounts(_, _ int64) ([]upstreamcacheguard.LegacyAccountState, error) {
	return nil, nil
}
func (s *expiryETagStore) AccountRows(context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return nil, errors.New("refresh unavailable")
	}
	raw, _ := json.Marshal(s.state)
	return []upstreamcacheguard.AccountRuntimeRow{{AccountID: 1, UpstreamKeyID: 1, Version: 1, Payload: raw}}, nil
}
func (s *expiryETagStore) AccountWindows(context.Context, time.Time, time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]upstreamcacheguard.AccountWindow{"1:1": {AccountID: 1, UpstreamKeyID: 1, Version: 1, State: s.state, ValidRequests: 21, TotalInputTokens: 2100, CacheReadTokens: 1890, GroupIDs: []int64{1}}}, nil
}
func (*expiryETagStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return nil, nil
}
func (*expiryETagStore) HasPendingBefore(context.Context, time.Time) (bool, error) { return false, nil }
func (*expiryETagStore) ClaimPending(context.Context, string, int, time.Duration) ([]upstreamcacheguard.Evidence, error) {
	return nil, nil
}
func (*expiryETagStore) HasPending(context.Context) (bool, error) { return false, nil }
func (*expiryETagStore) Cleanup(context.Context, time.Time) error { return nil }

func TestAccountListETagExpiresCacheTTFTAndReflectsPolicySwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	synctest.Test(t, func(t *testing.T) {
		store := &expiryETagStore{state: upstreamcacheguard.AccountState{AccountID: 1, UpstreamKeyID: 1, Phase: upstreamcacheguard.Healthy}}
		settings := service.NewSettingService(expiryETagSettings{}, nil)
		runner := service.ProvideUpstreamCacheGuardRunner(store, settings, &config.Config{JWT: config.JWTConfig{Secret: "test-only-expiry-etag"}}, nil)
		defer runner.Close()
		synctest.Wait()
		store.mu.Lock()
		store.down = true
		store.mu.Unlock()
		adminSvc := newStubAdminService()
		id := int64(1)
		adminSvc.accounts = []service.Account{{ID: 1, Name: "expiry", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, UpstreamConfigID: &id, UpstreamKeyID: &id, GroupIDs: []int64{1}, CreatedAt: time.Now(), UpdatedAt: time.Now()}}
		policies := accountProtectionPolicyStub{1: {GroupID: 1, GroupPlatform: service.PlatformOpenAI, Priority: "cache_first", Cache: service.DefaultUpstreamCacheGuardSettings(), CacheMode: "enabled", TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: "custom"}}}
		policy := policies[1]
		policy.Cache.Mode = upstreamcacheguard.Protect
		policies[1] = policy
		ttft := &service.OpenAIGatewayService{}
		ttft.SetOpenAITTFTGuardConfigProvider(expiryETagTTFTConfig{})
		ms := 60000
		ttft.ReportOpenAIAccountScheduleResultForGroup(&id, &adminSvc.accounts[0], "gpt-test", true, &ms)
		handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
		handler.protectionPolicyReader = policies
		handler.SetOpenAITTFTGuardDegradationReader(ttft)
		router := gin.New()
		router.GET("/accounts", handler.List)
		read := func(etag string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/accounts?lite=1", nil)
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			return rec
		}
		first := read("")
		require.Equal(t, http.StatusOK, first.Code)
		firstTag := first.Header().Get("ETag")
		require.NotEmpty(t, firstTag)
		require.Contains(t, first.Body.String(), `"rate_percent":90`)
		require.Contains(t, first.Body.String(), `"gpt-test"`)
		time.Sleep(5 * time.Second)
		require.Equal(t, http.StatusNotModified, read(firstTag).Code, "freshness alone is not a business response change")
		time.Sleep(25 * time.Second)
		require.Equal(t, http.StatusNotModified, read(firstTag).Code, "last successful cache rate remains usable at exactly 30 seconds")
		time.Sleep(time.Nanosecond)
		expired := read(firstTag)
		require.Equal(t, http.StatusOK, expired.Code)
		require.NotEqual(t, firstTag, expired.Header().Get("ETag"))
		require.NotContains(t, expired.Body.String(), `"rate_percent":90`)
		cacheExpiredTag := expired.Header().Get("ETag")
		time.Sleep(15*time.Minute - 30*time.Second - 2*time.Nanosecond)
		require.Equal(t, http.StatusNotModified, read(cacheExpiredTag).Code, "TTFT remains active immediately before its strict TTL")
		time.Sleep(time.Nanosecond)
		ttftExpired := read(cacheExpiredTag)
		require.Equal(t, http.StatusOK, ttftExpired.Code, "TTFT strict TTL must invalidate an old ETag")
		require.NotEqual(t, cacheExpiredTag, ttftExpired.Header().Get("ETag"))
		require.Contains(t, ttftExpired.Body.String(), `"model":"gpt-test","state":"insufficient"`, "expired observations mean there are no current samples")
		require.NotContains(t, ttftExpired.Body.String(), `"state":"expired"`, "TTFT has no separate expired state")
		oldTag := ttftExpired.Header().Get("ETag")
		policy = policies[1]
		policy.Priority = "ttft_first"
		policy.TTFT.Threshold = 40 * time.Second
		policy.Cache.ThresholdPercent = 65
		policies[1] = policy
		changed := read(oldTag)
		require.Equal(t, http.StatusOK, changed.Code)
		require.NotEqual(t, oldTag, changed.Header().Get("ETag"))
		require.Contains(t, changed.Body.String(), `"priority":"ttft_first"`)
		require.Equal(t, http.StatusNotModified, read(changed.Header().Get("ETag")).Code)
	})
}

type expiryETagTTFTConfig struct{}

func (expiryETagTTFTConfig) OpenAITTFTGuardConfigSnapshot() service.OpenAITTFTGuardConfigSnapshot {
	return service.OpenAITTFTGuardConfigSnapshot{Enabled: true, Threshold: 20 * time.Second, MinSamples: 5, Source: "custom"}
}
