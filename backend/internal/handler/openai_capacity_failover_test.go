package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func newOpenAICapacityTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	return c, w
}

func newOpenAICapacityTestHandler(maxSwitches, exhaustedStatus int) *OpenAIGatewayHandler {
	return &OpenAIGatewayHandler{cfg: &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		CapacityFailoverEnabled:             true,
		CapacityFailoverMaxSwitches:         maxSwitches,
		CapacityFailoverExhaustedStatusCode: exhaustedStatus,
	}}}}
}

type capacityFullConcurrencyCache struct {
	concurrencyCacheMock
}

func (c *capacityFullConcurrencyCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return false, nil
}

func TestOpenAICapacityFailoverUsesIndependentSwitchBudget(t *testing.T) {
	h := newOpenAICapacityTestHandler(1, http.StatusTooManyRequests)
	c, w := newOpenAICapacityTestContext()
	failed := make(map[int64]struct{})
	accountA := &service.Account{ID: 101, Platform: service.PlatformOpenAI, Concurrency: 1}
	accountB := &service.Account{ID: 102, Platform: service.PlatformOpenAI, Concurrency: 1}

	require.True(t, h.handleOpenAICapacityFull(c, "gpt-test", accountA, failed, false, zap.NewNop()))
	require.Contains(t, failed, accountA.ID)
	require.Empty(t, w.Body.Bytes())

	require.False(t, h.handleOpenAICapacityFull(c, "gpt-test", accountB, failed, false, zap.NewNop()))
	require.Equal(t, http.StatusTooManyRequests, w.Code)
	require.Equal(t, "api_error", gjson.GetBytes(w.Body.Bytes(), "error.type").String())
	require.Equal(t, gatewayCapacityExhaustedCode, gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	require.Equal(t, gatewayCapacityExhaustedMessage, gjson.GetBytes(w.Body.Bytes(), "error.message").String())
}

func TestOpenAICapacityFailoverExcludesSharedConcurrencyTargetOnce(t *testing.T) {
	h := newOpenAICapacityTestHandler(3, http.StatusServiceUnavailable)
	c, w := newOpenAICapacityTestContext()
	failed := make(map[int64]struct{})
	upstreamID := int64(77)
	accountA := &service.Account{ID: 201, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstreamID, UpstreamConcurrencyLimit: 2}
	accountB := &service.Account{ID: 202, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstreamID, UpstreamConcurrencyLimit: 2}

	require.True(t, h.handleOpenAICapacityFull(c, "gpt-test", accountA, failed, false, zap.NewNop()))
	require.False(t, h.handleOpenAICapacityFull(c, "gpt-test", accountB, failed, false, zap.NewNop()))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)

	state := openAICapacityState(c)
	require.Equal(t, 1, state.switchCount)
	require.Len(t, state.excludedTargets, 1)
}

func TestOpenAICapacitySelectionExhaustedIgnoresMixedFailureCauses(t *testing.T) {
	h := newOpenAICapacityTestHandler(3, http.StatusServiceUnavailable)
	c, w := newOpenAICapacityTestContext()
	failed := make(map[int64]struct{})
	account := &service.Account{ID: 301, Platform: service.PlatformOpenAI, Concurrency: 1}

	require.True(t, h.handleOpenAICapacityFull(c, "gpt-test", account, failed, false, zap.NewNop()))
	failed[999] = struct{}{}
	require.False(t, h.handleOpenAICapacitySelectionExhausted(c, false, zap.NewNop(), failed, nil))
	require.Empty(t, w.Body.Bytes())
}

func TestOpenAICapacityFailoverEligibilityKeepsDisabledAndWebSocketBehavior(t *testing.T) {
	account := &service.Account{ID: 401, Platform: service.PlatformOpenAI}
	c, _ := newOpenAICapacityTestContext()
	disabled := &OpenAIGatewayHandler{cfg: &config.Config{}}
	require.False(t, disabled.openAICapacityFailoverEligible(c, account, false))

	enabled := newOpenAICapacityTestHandler(3, http.StatusServiceUnavailable)
	require.False(t, enabled.openAICapacityFailoverEligible(c, account, true))
	c.Request.Header.Set("Connection", "Upgrade")
	c.Request.Header.Set("Upgrade", "websocket")
	require.False(t, enabled.openAICapacityFailoverEligible(c, account, false))
	require.False(t, enabled.openAICapacityFailoverEligible(c, &service.Account{ID: 402, Platform: service.PlatformGrok}, false))

	writtenContext, _ := newOpenAICapacityTestContext()
	_, err := writtenContext.Writer.Write([]byte("x"))
	require.NoError(t, err)
	require.False(t, enabled.openAICapacityFailoverEligible(writtenContext, account, false))
}

func TestAcquireResponsesAccountSlotCapacityFullBehavior(t *testing.T) {
	account := &service.Account{
		ID:          501,
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
	}
	newSelection := func() *service.AccountSelectionResult {
		return &service.AccountSelectionResult{
			Account: account,
			WaitPlan: &service.AccountWaitPlan{
				AccountID:      account.ID,
				MaxConcurrency: 1,
				Timeout:        time.Second,
				MaxWaiting:     1,
			},
		}
	}
	newHandler := func(enabled bool) *OpenAIGatewayHandler {
		return &OpenAIGatewayHandler{
			cfg: &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
				CapacityFailoverEnabled:             enabled,
				CapacityFailoverMaxSwitches:         3,
				CapacityFailoverExhaustedStatusCode: http.StatusServiceUnavailable,
			}}},
			concurrencyHelper: NewConcurrencyHelper(
				service.NewConcurrencyService(&capacityFullConcurrencyCache{}),
				SSEPingFormatComment,
				0,
			),
		}
	}

	t.Run("enabled returns internal capacity result without writing response", func(t *testing.T) {
		c, w := newOpenAICapacityTestContext()
		streamStarted := false
		release, result := newHandler(true).acquireResponsesAccountSlot(c, nil, "", newSelection(), false, &streamStarted, zap.NewNop())
		require.Nil(t, release)
		require.Equal(t, openAISlotAcquireCapacityFull, result)
		require.Empty(t, w.Body.Bytes())
	})

	t.Run("disabled preserves gateway queue full 429", func(t *testing.T) {
		c, w := newOpenAICapacityTestContext()
		streamStarted := false
		release, result := newHandler(false).acquireResponsesAccountSlot(c, nil, "", newSelection(), false, &streamStarted, zap.NewNop())
		require.Nil(t, release)
		require.Equal(t, openAISlotAcquireFailed, result)
		require.Equal(t, http.StatusTooManyRequests, w.Code)
		require.Equal(t, gatewayQueueFullCode, gjson.GetBytes(w.Body.Bytes(), "error.code").String())
	})
}

func TestOpenAICapacityFailoverMetricsSnapshot(t *testing.T) {
	before := SnapshotOpenAICapacityFailoverMetrics()
	h := newOpenAICapacityTestHandler(0, http.StatusServiceUnavailable)
	c, _ := newOpenAICapacityTestContext()
	account := &service.Account{ID: 601, Platform: service.PlatformOpenAI, Concurrency: 1}

	require.False(t, h.handleOpenAICapacityFull(c, "gpt-test", account, map[int64]struct{}{}, false, zap.NewNop()))
	after := SnapshotOpenAICapacityFailoverMetrics()
	require.Equal(t, before.SwitchTotal, after.SwitchTotal)
	require.Equal(t, before.ExhaustedTotal+1, after.ExhaustedTotal)
}
