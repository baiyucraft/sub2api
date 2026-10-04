//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type systemOneForkConcurrencyCache struct {
	fakeConcurrencyCache
	acquired, released, waiting, unwaiting []service.ConcurrencyTarget
	busyFirst, alwaysBusy, queueFull       bool
}

func (s *systemOneForkConcurrencyCache) AcquireConcurrencyTargetSlot(_ context.Context, target service.ConcurrencyTarget, _ string) (bool, error) {
	s.acquired = append(s.acquired, target)
	return !s.alwaysBusy && !(s.busyFirst && len(s.acquired) == 1), nil
}

func (s *systemOneForkConcurrencyCache) ReleaseConcurrencyTargetSlot(_ context.Context, target service.ConcurrencyTarget, _ string) error {
	s.released = append(s.released, target)
	return nil
}

func (s *systemOneForkConcurrencyCache) IncrementConcurrencyTargetWaitCount(_ context.Context, target service.ConcurrencyTarget, _ int) (bool, error) {
	s.waiting = append(s.waiting, target)
	return !s.queueFull, nil
}

func (s *systemOneForkConcurrencyCache) DecrementConcurrencyTargetWaitCount(_ context.Context, target service.ConcurrencyTarget) error {
	s.unwaiting = append(s.unwaiting, target)
	return nil
}

func (s *systemOneForkConcurrencyCache) GetConcurrencyTargetWaitingCount(context.Context, service.ConcurrencyTarget) (int, error) {
	return 0, nil
}

type systemOneForkRPMCache struct {
	service.RPMCache
	denied  map[int64]bool
	err     error
	checked []int64
}

func (s *systemOneForkRPMCache) GetRPM(context.Context, int64) (int, error) {
	return 0, nil
}

func (s *systemOneForkRPMCache) GetRPMBatch(context.Context, []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}

func (s *systemOneForkRPMCache) TryAcquireRPM(_ context.Context, accountID int64, _ int) (bool, int, time.Duration, error) {
	s.checked = append(s.checked, accountID)
	return !s.denied[accountID], 1, 17 * time.Second, s.err
}

type systemOneForkUpstream struct {
	service.HTTPUpstream
	do func(*http.Request, int64) (*http.Response, error)
}

type systemOneForkAccountRepo struct {
	service.AccountRepository
}

func (*systemOneForkAccountRepo) ListUpstreamSchedulingEnabledAccountIDs(_ context.Context, ids []int64) ([]int64, error) {
	return append([]int64(nil), ids...), nil
}

func (s *systemOneForkUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	return s.do(req, accountID)
}

func systemOneForkAccount(id int64, upstreamBound bool) *service.Account {
	rate := 1.0
	account := &service.Account{
		ID: id, Platform: service.PlatformTypeSafe, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 50, RPMLimit: 2,
		Priority: int(id), RateMultiplier: &rate,
		Credentials:   map[string]any{"api_key": "test-typesafe-key"},
		AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: 9300}},
	}
	if upstreamBound {
		configID, keyID := int64(9301), id+10000
		account.UpstreamConfigID = &configID
		account.UpstreamKeyID = &keyID
		account.UpstreamConcurrencyLimit = 2
	}
	return account
}

func systemOneForkResponse(status int, retryAfter string) *http.Response {
	headers := http.Header{"Content-Type": []string{"application/json"}}
	if retryAfter != "" {
		headers.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: status, Header: headers,
		Body: io.NopCloser(strings.NewReader(`{"detail":"test upstream rejection"}`)),
	}
}

func newSystemOneForkHandler(t *testing.T, accounts []*service.Account, cache *systemOneForkConcurrencyCache, rpm service.RPMCache, upstream service.HTTPUpstream) (*GatewayHandler, *service.Group) {
	t.Helper()
	group := &service.Group{ID: 9300, Hydrated: true, Platform: service.PlatformTypeSafe, Status: service.StatusActive}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.Gateway.Scheduling.FallbackWaitTimeout = 10 * time.Millisecond
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	concurrency := service.NewConcurrencyService(cache)
	accountRepo := &systemOneForkAccountRepo{}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, accountRepo, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewGatewayService(
		accountRepo, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, cfg,
		snapshot, concurrency, nil, nil, billing, nil, upstream, nil, nil, nil,
		rpm, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return &GatewayHandler{
		gatewayService: gateway, billingCacheService: billing,
		settingService:     service.NewSettingService(nil, cfg),
		concurrencyHelper:  NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0),
		maxAccountSwitches: 10, cfg: cfg,
	}, group
}

func systemOneForkContext(group *service.Group) (*gin.Context, *httptest.ResponseRecorder) {
	c, recorder := newSystemOneHandlerContext(validSystemOneHandlerBody)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
	key := &service.APIKey{
		ID: 9302, UserID: 9303, GroupID: &group.ID, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 9303, Concurrency: 10, Balance: 100},
	}
	c.Set(string(middleware.ContextKeyAPIKey), key)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})
	return c, recorder
}

func TestSystemOneForkSharedWaitTarget(t *testing.T) {
	account := systemOneForkAccount(1, true)
	cache := &systemOneForkConcurrencyCache{busyFirst: true}
	forwarded := 0
	upstream := &systemOneForkUpstream{do: func(req *http.Request, accountID int64) (*http.Response, error) {
		forwarded++
		require.Equal(t, account.ID, accountID)
		require.Equal(t, "/v1/systemone", req.URL.Path)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		require.Equal(t, validSystemOneHandlerBody, string(body))
		return systemOneForkResponse(http.StatusBadRequest, ""), nil
	}}
	h, group := newSystemOneForkHandler(t, []*service.Account{account}, cache, nil, upstream)
	c, recorder := systemOneForkContext(group)
	h.SystemOne(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Equal(t, 1, forwarded)
	target := account.SchedulingConcurrencyTarget()
	require.Equal(t, []service.ConcurrencyTarget{target, target}, cache.acquired)
	require.Equal(t, []service.ConcurrencyTarget{target}, cache.released)
	require.Equal(t, []service.ConcurrencyTarget{target}, cache.waiting)
	require.Equal(t, cache.waiting, cache.unwaiting)
}

func TestSystemOneForkWaitQueueCleanup(t *testing.T) {
	for _, queueFull := range []bool{false, true} {
		name := "timeout"
		if queueFull {
			name = "queue full"
		}
		t.Run(name, func(t *testing.T) {
			account := systemOneForkAccount(1, true)
			cache := &systemOneForkConcurrencyCache{alwaysBusy: true, queueFull: queueFull}
			h, group := newSystemOneForkHandler(t, []*service.Account{account}, cache, nil, &systemOneForkUpstream{do: func(*http.Request, int64) (*http.Response, error) {
				t.Fatal("a full shared pool must not forward")
				return nil, nil
			}})
			c, recorder := systemOneForkContext(group)
			h.SystemOne(c)
			require.Equal(t, http.StatusTooManyRequests, recorder.Code, recorder.Body.String())
			require.NotEmpty(t, cache.acquired)
			for _, target := range cache.acquired {
				require.Equal(t, account.SchedulingConcurrencyTarget(), target)
			}
			require.Equal(t, []service.ConcurrencyTarget{account.SchedulingConcurrencyTarget()}, cache.waiting)
			if queueFull {
				require.Empty(t, cache.unwaiting)
				require.Equal(t, gatewayQueueFullCode, gjson.Get(recorder.Body.String(), "error.code").String())
			} else {
				require.Equal(t, cache.waiting, cache.unwaiting)
			}
			require.Empty(t, cache.released)
		})
	}
}

func TestSystemOneForkAtomicRPM(t *testing.T) {
	for _, failOpen := range []bool{false, true} {
		name := "denied account is skipped"
		if failOpen {
			name = "redis failure is fail open"
		}
		t.Run(name, func(t *testing.T) {
			accounts := []*service.Account{systemOneForkAccount(1, true), systemOneForkAccount(2, true)}
			rpm := &systemOneForkRPMCache{denied: map[int64]bool{1: true}}
			wantForwarded := int64(2)
			if failOpen {
				rpm.err = errors.New("rpm cache unavailable")
				wantForwarded = 1
			}
			forwarded := []int64{}
			cache := &systemOneForkConcurrencyCache{}
			h, group := newSystemOneForkHandler(t, accounts, cache, rpm, &systemOneForkUpstream{do: func(_ *http.Request, id int64) (*http.Response, error) {
				forwarded = append(forwarded, id)
				return systemOneForkResponse(http.StatusBadRequest, ""), nil
			}})
			c, recorder := systemOneForkContext(group)
			h.SystemOne(c)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Equal(t, []int64{wantForwarded}, forwarded)
			if failOpen {
				require.Equal(t, []int64{1}, rpm.checked)
			} else {
				require.Equal(t, []int64{1, 2}, rpm.checked)
				require.Equal(t, 17, service.AccountRPMRetryAfter(c.Request.Context()))
			}
			require.Equal(t, cache.acquired, cache.released)
			require.Nil(t, getUpstream429CapacityState(c).excludedAccountIDs, "local RPM denial must not consume capacity failover budget")
		})
	}
}

func TestSystemOneForkUpstream429CapacityBudget(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabled       bool
		upstreamBound bool
		budget        int
		wantCalls     int
		wantStatus    int
	}{
		{name: "enabled bounded", enabled: true, upstreamBound: true, budget: 1, wantCalls: 2, wantStatus: 507},
		{name: "enabled unlimited", enabled: true, upstreamBound: true, budget: 0, wantCalls: 3, wantStatus: 507},
		{name: "disabled", upstreamBound: true, budget: 1, wantCalls: 3, wantStatus: http.StatusTooManyRequests},
		{name: "ordinary accounts", enabled: true, budget: 1, wantCalls: 3, wantStatus: http.StatusTooManyRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := []*service.Account{systemOneForkAccount(1, tc.upstreamBound), systemOneForkAccount(2, tc.upstreamBound), systemOneForkAccount(3, tc.upstreamBound)}
			cache := &systemOneForkConcurrencyCache{}
			forwarded := []int64{}
			h, group := newSystemOneForkHandler(t, accounts, cache, nil, &systemOneForkUpstream{do: func(_ *http.Request, id int64) (*http.Response, error) {
				forwarded = append(forwarded, id)
				return systemOneForkResponse(http.StatusTooManyRequests, "12"), nil
			}})
			h.cfg.Gateway.Scheduling.CapacityFailoverEnabled = tc.enabled
			h.cfg.Gateway.Scheduling.CapacityFailoverMaxSwitches = tc.budget
			h.cfg.Gateway.Scheduling.CapacityFailoverExhaustedStatusCode = tc.wantStatus
			c, recorder := systemOneForkContext(group)
			h.SystemOne(c)
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
			require.Len(t, forwarded, tc.wantCalls)
			for i, id := range forwarded {
				require.Equal(t, accounts[i].ID, id, "429 failover must advance by account even within one shared pool")
			}
			require.Equal(t, cache.acquired, cache.released)
			if tc.enabled && tc.upstreamBound {
				require.Equal(t, gatewayCapacityExhaustedCode, gjson.Get(recorder.Body.String(), "error.code").String())
				require.Equal(t, "12", recorder.Header().Get("Retry-After"))
				require.Equal(t, http.StatusTooManyRequests, c.GetInt(service.OpsUpstreamStatusCodeKey))
				require.NotZero(t, getUpstream429CapacityState(c).switchCount)
			} else {
				require.Empty(t, gjson.Get(recorder.Body.String(), "error.code").String())
			}
		})
	}
}

func TestSystemOneForkInstallsInputEstimate(t *testing.T) {
	account := systemOneForkAccount(1, false)
	h, group := newSystemOneForkHandler(t, []*service.Account{account}, &systemOneForkConcurrencyCache{}, nil, &systemOneForkUpstream{do: func(req *http.Request, _ int64) (*http.Response, error) {
		estimate, ok := service.GatewayInputTokenEstimate(req.Context())
		require.True(t, ok)
		require.Positive(t, estimate)
		account.ProbeMinInputTokens = estimate + 1
		require.False(t, service.IsAccountInputLengthEligible(req.Context(), account))
		return systemOneForkResponse(http.StatusBadRequest, ""), nil
	}})
	c, recorder := systemOneForkContext(group)
	h.SystemOne(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestSystemOneForkInputFloorBeforeScheduling(t *testing.T) {
	body := `{"model":"jev-latest","state":{"record":[{"arbitrary":"abcdefghijklmnop"}]},"questions":{"q":{"type":"choice","instructions":null,"criteria":{"answer":{"reason":"qrstuvwxyzabcdef"}}}}}`
	estimate := service.EstimateGatewayInputTokens([]byte(body), "typesafe_systemone")
	tooShort, eligible := systemOneForkAccount(1, true), systemOneForkAccount(2, true)
	tooShort.ProbeMinInputTokens = estimate + 1
	eligible.ProbeMinInputTokens = estimate
	cache := &systemOneForkConcurrencyCache{}
	forwarded := []int64{}
	h, group := newSystemOneForkHandler(t, []*service.Account{tooShort, eligible}, cache, nil, &systemOneForkUpstream{do: func(req *http.Request, id int64) (*http.Response, error) {
		forwarded = append(forwarded, id)
		got, ok := service.GatewayInputTokenEstimate(req.Context())
		require.True(t, ok)
		require.Equal(t, estimate, got)
		return systemOneForkResponse(http.StatusBadRequest, ""), nil
	}})
	c, recorder := systemOneForkContext(group)
	c.Request.Body = io.NopCloser(strings.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	h.SystemOne(c)
	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Equal(t, []int64{eligible.ID}, forwarded)
	require.Equal(t, []service.ConcurrencyTarget{eligible.SchedulingConcurrencyTarget()}, cache.acquired)
	require.Equal(t, cache.acquired, cache.released)
}
