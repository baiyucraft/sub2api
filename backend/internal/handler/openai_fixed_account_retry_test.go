//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOpenAIUpstreamAttemptBudgetCannotBeBypassedByReselection(t *testing.T) {
	b := make(openAIUpstreamAttemptBudget)
	failure := &service.UpstreamFailoverError{StatusCode: 403}
	for _, id := range []int64{1, 2, 3} {
		require.True(t, b.allows(id, 2))
		b.recordFailure(id, failure)
		require.True(t, b.allows(id, 2), "same-account attempts retain their independent retry budget")
	}
	require.False(t, b.allows(4, 2))
	b.recordFailure(5, &service.UpstreamFailoverError{PluginAdmissionRejected: true})
	require.Len(t, b, 3)
}

func TestOpenAIFixedRetryAbandonBudgets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		handlerMax  int
		capacityMax int
		wantSwitch  int
	}{
		{"handler exhausted", 0, 3, 0},
		{"capacity exhausted", 3, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newOpenAICapacityTestContext()
			h := &OpenAIGatewayHandler{
				gatewayService: &service.OpenAIGatewayService{},
				capacityFailoverProvider: &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
					Enabled: true, MaxSwitches: tc.capacityMax,
				}},
			}
			switches, reported := 0, 0
			c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { reported = count }))
			excluded := make(map[int64]struct{})
			var state service.OpenAIOAuth429FailoverState
			for id := int64(1); id <= 2; id++ {
				retry := &openAIFixedAccountRetry{account: upstream429TestAccount(id), failure: upstream429TestError("7")}
				require.Equal(t, int(id) <= tc.wantSwitch, h.abandonFixedAccountRetry(c, retry, excluded, &switches, tc.handlerMax, &state, zap.NewNop()))
			}
			require.Equal(t, tc.wantSwitch, switches)
			require.Equal(t, switches, reported)
			require.Equal(t, switches, getUpstream429CapacityState(c).switchCount)
			require.Len(t, excluded, switches)
		})
	}
}

func TestOpenAIFixedRetryAbandonOAuthStop(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			c, _ := newOpenAICapacityTestContext()
			h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
			var state service.OpenAIOAuth429FailoverState
			excluded := make(map[int64]struct{})
			switches, reported := 0, 0
			c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { reported = count }))
			attempts := 3
			if platform == service.PlatformGrok {
				attempts = 2
			}
			for i := 1; i <= attempts; i++ {
				account := &service.Account{ID: int64(i), Platform: platform, Type: service.AccountTypeOAuth}
				failure := &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}
				if platform == service.PlatformGrok && i == 2 {
					account.Platform, account.Type = service.PlatformOpenAI, service.AccountTypeAPIKey
					failure.StatusCode = http.StatusBadGateway
				}
				require.Equal(t, i < attempts, h.abandonFixedAccountRetry(c, &openAIFixedAccountRetry{account, failure}, excluded, &switches, 10, &state, zap.NewNop()))
				require.Equal(t, i, switches)
				require.Equal(t, i, reported)
			}
		})
	}
}

func TestOpenAIFixedRetryAbandonIgnoresNonUpstreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failure  *service.UpstreamFailoverError
		canceled bool
	}{
		{name: "no failure"},
		{name: "plugin rejection", failure: &service.UpstreamFailoverError{PluginAdmissionRejected: true}},
		{name: "canceled", failure: upstream429TestError(""), canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newOpenAICapacityTestContext()
			if tc.canceled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			h := &OpenAIGatewayHandler{}
			switches := 0
			excluded := make(map[int64]struct{})
			require.False(t, h.abandonFixedAccountRetry(c, &openAIFixedAccountRetry{upstream429TestAccount(1), tc.failure}, excluded, &switches, 10, nil, zap.NewNop()))
			require.Zero(t, switches)
			require.Empty(t, excluded)
		})
	}
}

type fixedRetryRPMCache struct {
	service.RPMCache
	calls []int64
	deny  func(int64, int) bool
}

func (*fixedRetryRPMCache) GetRPM(context.Context, int64) (int, error) { return 0, nil }
func (*fixedRetryRPMCache) GetRPMBatch(context.Context, []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}
func (r *fixedRetryRPMCache) TryAcquireRPM(_ context.Context, id int64, _ int) (bool, int, time.Duration, error) {
	r.calls = append(r.calls, id)
	count := 0
	for _, called := range r.calls {
		if called == id {
			count++
		}
	}
	return r.deny == nil || !r.deny(id, count), 0, time.Second, nil
}

type fixedRetryHTTPEndpoint struct {
	name, path, body string
	run              func(*OpenAIGatewayHandler, *gin.Context)
}

var fixedRetryHTTPEndpoints = []fixedRetryHTTPEndpoint{
	{"responses", "/v1/responses", `{"model":"gpt-5.1","input":"hello","stream":false}`, (*OpenAIGatewayHandler).Responses},
	{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
	{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).ChatCompletions},
	{"images", "/v1/images/generations", `{"model":"gpt-image-1","prompt":"draw a square"}`, (*OpenAIGatewayHandler).Images},
}

func fixedRetryHTTPAccounts() []service.Account {
	accounts := pluginAdmissionHandlerAccounts()
	for i := range accounts {
		accounts[i].Type = service.AccountTypeAPIKey
		accounts[i].Credentials = map[string]any{"api_key": "fixture-token", "base_url": "https://fixture.invalid", "pool_mode": true, "pool_mode_retry_count": 1}
		accounts[i].Extra = map[string]any{"openai_responses_supported": true}
		accounts[i].RPMLimit = 10
		accounts[i].GroupIDs = []int64{7301}
		configID, keyID := accounts[i].ID+7000, accounts[i].ID+9000
		accounts[i].UpstreamConfigID, accounts[i].UpstreamKeyID = &configID, &keyID
	}
	return accounts
}

func fixedRetryHTTPContext(endpoint fixedRetryHTTPEndpoint) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
	c.Request.Header.Set("Content-Type", "application/json")
	setPluginAdmissionHandlerAuth(c)
	key, _ := c.Get(string(middleware2.ContextKeyAPIKey))
	key.(*service.APIKey).Group.AllowImageGeneration = true
	return c, recorder
}

func fixedRetryCapacityResponse(pluginAdmissionHandlerAttempt) *http.Response {
	return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewBufferString(`{"error":{"message":"Too many pending requests"}}`))}
}

func TestOpenAIFixedRetryFinalRPMConsumesUpstreamCapacityBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range fixedRetryHTTPEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			upstream := &pluginAdmissionHandlerUpstream{respond: fixedRetryCapacityResponse}
			h := newPluginAdmissionHandler(t, upstream, fixedRetryHTTPAccounts(), 2)
			h.capacityFailoverProvider = &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
				Enabled: true, MaxSwitches: 1, ExhaustedStatusCode: http.StatusServiceUnavailable,
			}}
			rpm := &fixedRetryRPMCache{deny: func(_ int64, count int) bool { return count > 1 }}
			h.gatewayService.SetRPMCache(rpm)
			c, recorder := fixedRetryHTTPContext(endpoint)
			reported := 0
			c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { reported = count }))

			endpoint.run(h, c)

			requirePluginAdmissionAttempts(t, upstream, 1, 2)
			require.Equal(t, []int64{1, 1, 2, 2}, rpm.calls, "fixed retry must reach final RPM admission before switching")
			require.Equal(t, 1, reported)
			require.Equal(t, 1, getUpstream429CapacityState(c).switchCount)
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
		})
	}
}

func TestOpenAIFixedRetryAttemptBudgetBeforeRPM(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range fixedRetryHTTPEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			calls := 0
			upstream := &pluginAdmissionHandlerUpstream{
				reject: func(pluginAdmissionHandlerAttempt) bool {
					calls++
					return calls > 1
				},
				respond: fixedRetryCapacityResponse,
			}
			h := newPluginAdmissionHandler(t, upstream, fixedRetryHTTPAccounts(), 0)
			rpm := &fixedRetryRPMCache{}
			h.gatewayService.SetRPMCache(rpm)
			c, _ := fixedRetryHTTPContext(endpoint)

			endpoint.run(h, c)

			requirePluginAdmissionAttempts(t, upstream, 1, 1)
			require.Equal(t, []int64{1, 1}, rpm.calls, "attempt budget exhaustion must not reserve RPM on a fresh account")
		})
	}
}

type fixedRetryProxyCache struct {
	tokenCountAdmissionCache
	service.ConcurrencyTargetCache
	claims map[int64]int
}

func (r *fixedRetryProxyCache) AcquireConcurrencyTargetSlot(ctx context.Context, target service.ConcurrencyTarget, requestID string) (bool, error) {
	return r.AcquireAccountSlot(ctx, target.ID, target.Limit, requestID)
}
func (r *fixedRetryProxyCache) ReleaseConcurrencyTargetSlot(ctx context.Context, target service.ConcurrencyTarget, requestID string) error {
	return r.ReleaseAccountSlot(ctx, target.ID, requestID)
}

type fixedRetryTargetCache struct {
	concurrencyCacheMock
	service.ConcurrencyTargetCache
}

func (r *fixedRetryTargetCache) AcquireConcurrencyTargetSlot(ctx context.Context, target service.ConcurrencyTarget, requestID string) (bool, error) {
	return r.AcquireAccountSlot(ctx, target.ID, target.Limit, requestID)
}
func (r *fixedRetryTargetCache) ReleaseConcurrencyTargetSlot(ctx context.Context, target service.ConcurrencyTarget, requestID string) error {
	return r.ReleaseAccountSlot(ctx, target.ID, requestID)
}

func (*fixedRetryProxyCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, service.ErrStickySessionNotFound
}
func (*fixedRetryProxyCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}
func (*fixedRetryProxyCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}
func (r *fixedRetryProxyCache) AcquireAccountProxySlot(_ context.Context, id, _ int64, _ int, _ string) (bool, error) {
	r.claims[id]++
	return r.claims[id] == 1, nil
}

func newFixedRetryAdmissionHandler(t *testing.T, upstream service.HTTPUpstream, accounts []service.Account, cache service.ConcurrencyCache, binding service.GatewayCache, budget int) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	concurrency := service.NewConcurrencyService(cache)
	gw := service.NewOpenAIGatewayService(
		pluginAdmissionHandlerRepo{openAIImagesFailoverAccountRepo{accounts: accounts}}, nil, nil, nil, nil, nil,
		binding, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil,
	)
	h := NewOpenAIGatewayHandler(gw, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = budget
	return h
}

func TestOpenAIFixedRetryFinalCapacityConsumesUpstreamBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range fixedRetryHTTPEndpoints {
		for _, budget := range []int{0, 2} {
			t.Run(endpoint.name+"/"+strconv.Itoa(budget), func(t *testing.T) {
				accounts := fixedRetryHTTPAccounts()
				proxyGroupID := int64(71)
				for i := range accounts {
					accounts[i].Type = service.AccountTypeOAuth
					accounts[i].Credentials = map[string]any{"access_token": "fixture-token"}
					accounts[i].ProxyIPGroupID = &proxyGroupID
				}
				upstream := &pluginAdmissionHandlerUpstream{respond: fixedRetryCapacityResponse}
				cache := &fixedRetryProxyCache{claims: make(map[int64]int)}
				cache.acquireUserSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
				cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
				h := newFixedRetryAdmissionHandler(t, upstream, accounts, cache, cache, budget)
				h.capacityFailoverProvider = &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
					Enabled: true, MaxSwitches: 1, ExhaustedStatusCode: http.StatusServiceUnavailable,
				}}
				h.gatewayService.SetOpenAIProxyGroupRepositories(
					&tokenCountProxyGroupRepo{group: &service.ProxyIPGroup{ID: proxyGroupID, PerIPConcurrency: 1, ProxyIDs: []int64{9}}},
					&tokenCountProxyRepo{proxies: []service.Proxy{{ID: 9, Protocol: "http", Host: "fixture.invalid", Port: 8080, Status: service.StatusActive}}},
				)
				rpm := &fixedRetryRPMCache{}
				h.gatewayService.SetRPMCache(rpm)
				c, recorder := fixedRetryHTTPContext(endpoint)
				reported := 0
				c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { reported = count }))

				endpoint.run(h, c)

				want := []int64{1}
				if budget > 0 {
					want = append(want, 2)
				}
				requirePluginAdmissionAttempts(t, upstream, want...)
				require.Equal(t, want, rpm.calls, "full proxy admission must not reserve RPM")
				require.Equal(t, len(want)-1, reported)
				require.Equal(t, reported, getUpstream429CapacityState(c).switchCount)
				for _, id := range want {
					require.Equal(t, 2, cache.claims[id], "fixed selection succeeded, then final proxy capacity rejected it")
				}
				require.EqualValues(t, len(want), cache.proxyReleases.Load())
				require.EqualValues(t, len(want)*2, cache.releaseAccountCalled, "initial and proxy-rejected fixed account slots must both be released")
				require.Equal(t, http.StatusServiceUnavailable, recorder.Code, recorder.Body.String())
			})
		}
	}
}

func TestOpenAIFixedRetryFinalProfitConsumesUpstreamBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Independent Images requests intentionally suppress the profit gate.
	for _, endpoint := range fixedRetryHTTPEndpoints[:3] {
		for _, budget := range []int{0, 1} {
			t.Run(endpoint.name+"/"+strconv.Itoa(budget), func(t *testing.T) {
				accounts := fixedRetryHTTPAccounts()
				rates := make(map[int64]*float64)
				for i := range accounts {
					rate := 0.1
					accounts[i].RateMultiplier = &rate
					rates[accounts[i].SchedulingConcurrencyTarget().ID] = &rate
				}
				claims := make(map[int64]int)
				cache := &fixedRetryTargetCache{concurrencyCacheMock: concurrencyCacheMock{
					acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
					acquireAccountSlotFn: func(_ context.Context, id int64, _ int, _ string) (bool, error) {
						claims[id]++
						if claims[id] == 2 {
							*rates[id] = 0.9
						}
						return true, nil
					},
				}}
				upstream := &pluginAdmissionHandlerUpstream{respond: fixedRetryCapacityResponse}
				h := newFixedRetryAdmissionHandler(t, upstream, accounts, cache, nil, budget)
				rpm := &fixedRetryRPMCache{}
				h.gatewayService.SetRPMCache(rpm)
				c, recorder := fixedRetryHTTPContext(endpoint)
				c.Request = c.Request.WithContext(profitSlotTestContext(t, h.gatewayService, 7301, false))
				reported := 0
				c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { reported = count }))

				endpoint.run(h, c)

				want := []int64{1}
				if budget > 0 {
					want = append(want, 2)
				}
				requirePluginAdmissionAttempts(t, upstream, want...)
				require.Equal(t, want, rpm.calls, "terminal profit veto must not reserve RPM")
				require.Equal(t, budget, reported)
				require.EqualValues(t, len(want)*2, cache.releaseAccountCalled, "each initial and vetoed fixed slot must be released")
				require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
			})
		}
	}
}
