//go:build unit

package handler

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type mergeSessionReleaseCache struct {
	service.SessionLimitCache
	registered map[int64]string
	released   map[int64]string
}

func (s *mergeSessionReleaseCache) RegisterSession(_ context.Context, id int64, session string, _ int, _ time.Duration) (bool, error) {
	s.registered[id] = session
	return true, nil
}

func (s *mergeSessionReleaseCache) UnregisterSession(ctx context.Context, id int64, session string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.released[id] = session
	return nil
}

func (s *mergeSessionReleaseCache) GetWindowCost(context.Context, int64) (float64, bool, error) {
	return 0, true, nil
}

type mergeBusyAccountCache struct {
	fakeConcurrencyCache
	waitAllowed bool
	waitFailed  bool
	acquires    int
}

func (s *mergeBusyAccountCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	s.acquires++
	if s.waitFailed && s.acquires > 1 {
		return false, context.Canceled
	}
	return false, nil
}

func (s *mergeBusyAccountCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return s.waitAllowed, nil
}

func newMergeSessionGateway(group *service.Group, accounts []*service.Account, sessions *mergeSessionReleaseCache, concurrency *service.ConcurrencyService) *service.GatewayService {
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	return service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil, nil,
		snapshot, concurrency, nil, nil, nil, nil, nil, nil, nil, sessions,
		nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func TestGatewayMessagesReleasesSessionBeforeAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name               string
		warmup, waitFailed bool
	}{
		{name: "warmup interception", warmup: true},
		{name: "wait queue rejected"},
		{name: "wait acquisition canceled", waitFailed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groupID := int64(9210)
			group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
			account := &service.Account{
				ID: 9211, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
				Status: service.StatusActive, Schedulable: true, Concurrency: 1,
				Credentials:   map[string]any{"access_token": "test-token", "intercept_warmup_requests": tc.warmup},
				Extra:         map[string]any{"max_sessions": 1},
				AccountGroups: []service.AccountGroup{{AccountID: 9211, GroupID: groupID}},
			}
			h, cleanup := newTestGatewayHandler(t, group, []*service.Account{account})
			t.Cleanup(cleanup)
			sessions := &mergeSessionReleaseCache{registered: map[int64]string{}, released: map[int64]string{}}
			var concurrency *service.ConcurrencyService
			if !tc.warmup {
				concurrency = service.NewConcurrencyService(&mergeBusyAccountCache{waitAllowed: tc.waitFailed, waitFailed: tc.waitFailed})
				h.concurrencyHelper = NewConcurrencyHelper(concurrency, SSEPingFormatClaude, 0)
			}
			h.gatewayService = newMergeSessionGateway(group, []*service.Account{account}, sessions, concurrency)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			body := `{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":[{"type":"text","text":"Warmup"}]}]}`
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, group))
			key := &service.APIKey{ID: 9212, UserID: 9213, GroupID: &groupID, Group: group, Status: service.StatusActive,
				User: &service.User{ID: 9213, Concurrency: 10, Balance: 100}}
			c.Set(string(middleware.ContextKeyAPIKey), key)
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: key.UserID, Concurrency: 10})

			h.Messages(c)

			require.NotEmpty(t, sessions.registered[account.ID], "must reach session registration before returning")
			require.Equal(t, sessions.registered, sessions.released)
			if tc.warmup {
				require.Equal(t, http.StatusOK, w.Code)
			} else {
				require.NotEqual(t, http.StatusOK, w.Code)
			}
		})
	}
}

func TestGatewaySessionFinalCleanupPreservesServedRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		served bool
	}{
		{name: "profit veto exhausted"},
		{name: "failover exhausted"},
		{name: "client canceled"},
		{name: "success", served: true},
		{name: "partial usage with error", served: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &mergeSessionReleaseCache{registered: map[int64]string{}, released: map[int64]string{}}
			h := &GatewayHandler{gatewayService: newMergeSessionGateway(&service.Group{}, nil, sessions, nil)}
			accounts := map[int64]*service.Account{}
			for _, id := range []int64{1, 2} {
				accounts[id] = &service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
					Extra: map[string]any{"max_sessions": 1}}
			}
			h.releaseUnservedAccountSessions(accounts, "session-key", tc.served)
			if tc.served {
				require.Empty(t, sessions.released)
			} else {
				require.Equal(t, map[int64]string{1: "session-key", 2: "session-key"}, sessions.released)
			}
		})
	}
}

type mergeWSTargetCache struct {
	fakeConcurrencyCache
	service.ConcurrencyTargetCache
	acquired, released []service.ConcurrencyTarget
}

func (s *mergeWSTargetCache) AcquireConcurrencyTargetSlot(_ context.Context, target service.ConcurrencyTarget, _ string) (bool, error) {
	s.acquired = append(s.acquired, target)
	return true, nil
}

func (s *mergeWSTargetCache) ReleaseConcurrencyTargetSlot(_ context.Context, target service.ConcurrencyTarget, _ string) error {
	s.released = append(s.released, target)
	return nil
}

func TestOpenAIWSRetryUsesSharedConcurrencyTarget(t *testing.T) {
	// Guard the actual WS handler call sites, not just the helper implementation.
	source, err := parser.ParseFile(token.NewFileSet(), "openai_gateway_handler.go", nil, 0)
	require.NoError(t, err)
	checked := false
	for _, decl := range source.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ResponsesWebSocket" {
			continue
		}
		checked = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok {
				require.NotEqual(t, "TryAcquireAccountSlot", selector.Sel.Name, "all WS attempts must use account-aware acquisition")
			}
			return true
		})
	}
	require.True(t, checked)

	cache := &mergeWSTargetCache{}
	helper := NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, 0)
	upstreamID := int64(94)
	for _, id := range []int64{1, 2} {
		account := &service.Account{ID: id, Concurrency: 999, UpstreamConfigID: &upstreamID, UpstreamConcurrencyLimit: 3}
		release, acquired, err := helper.TryAcquireAccountSlotForAccount(context.Background(), account)
		require.NoError(t, err)
		require.True(t, acquired)
		require.NotNil(t, release)
		release()
	}
	require.Len(t, cache.acquired, 2)
	require.Equal(t, cache.acquired[0], cache.acquired[1])
	require.Equal(t, service.ConcurrencyTarget{Kind: service.ConcurrencyTargetUpstream, ID: upstreamID, Limit: 3}, cache.acquired[0])
	require.Equal(t, cache.acquired, cache.released)
}
