//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Obtain the error from the public physical-send boundary instead of copying
// its metadata into a fixture. Composite is always outside protection scope.
func schedulingProtectionScopeRejection(t *testing.T, accountID int64) *service.UpstreamFailoverError {
	t.Helper()
	ctx := service.WithSchedulingProtectionBeforeSend(context.Background(), accountID, func(context.Context) error {
		t.Fatal("a canceled scope must not commit the migration binding")
		return nil
	})
	err := service.BeginUpstreamCacheGuardSend(ctx, &service.Account{ID: accountID, Platform: service.PlatformComposite}, "gpt-5.1", "openai_ws")
	var failover *service.UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, service.GatewayFailureReason("scheduling_protection_scope_changed"), failover.Reason)
	require.False(t, failover.ShouldReportAccountScheduleFailure())
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(failover))
	return failover
}

func TestSchedulingProtectionScopeChangeHTTPHandlerReselectsWithoutFaultBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name, path, body string
		run              func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5.1","input":"hello","stream":false}`, (*OpenAIGatewayHandler).Responses},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).ChatCompletions},
	} {
		for _, scenario := range []struct {
			name         string
			budget       int
			rejectIDs    map[int64]bool
			failIDs      map[int64]bool
			wantIDs      []int64
			wantStatus   int
			wantSwitches int
		}{
			{name: "zero budget", rejectIDs: map[int64]bool{1: true}, wantIDs: []int64{1, 2}, wantStatus: 200},
			{name: "already exhausted budget", budget: 1, failIDs: map[int64]bool{1: true}, rejectIDs: map[int64]bool{2: true}, wantIDs: []int64{1, 2, 3}, wantStatus: 200, wantSwitches: 1},
			{name: "preserves budget for later failure", budget: 1, rejectIDs: map[int64]bool{1: true}, failIDs: map[int64]bool{2: true}, wantIDs: []int64{1, 2, 3}, wantStatus: 200, wantSwitches: 1},
			{name: "all rejected stops at selection exhaustion", rejectIDs: map[int64]bool{1: true, 2: true, 3: true}, wantIDs: []int64{1, 2, 3}, wantStatus: 503},
		} {
			t.Run(endpoint.name+"/"+scenario.name, func(t *testing.T) {
				upstream := &pluginAdmissionHandlerUpstream{
					failIDs: scenario.failIDs,
					transportError: func(attempt pluginAdmissionHandlerAttempt) error {
						if scenario.rejectIDs[attempt.accountID] {
							return fmt.Errorf("pre-send scope changed: %w", schedulingProtectionScopeRejection(t, attempt.accountID))
						}
						return nil
					},
				}
				h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts(), scenario.budget)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
				switches := 0
				c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { switches = count }))
				c.Request.Header.Set("Content-Type", "application/json")
				setPluginAdmissionHandlerAuth(c)

				endpoint.run(h, c)

				require.Equal(t, scenario.wantStatus, recorder.Code, recorder.Body.String())
				requirePluginAdmissionAttempts(t, upstream, scenario.wantIDs...)
				require.Equal(t, scenario.wantSwitches, switches)
				require.False(t, service.IsForceCacheBilling(c.Request.Context()))
				if len(scenario.failIDs) == 0 {
					_, recorded := c.Get(service.OpsUpstreamErrorsKey)
					require.False(t, recorded, "a scope change is not an upstream failure")
				}
			})
		}
	}
}

func TestSchedulingProtectionScopeChangeGeneralFailoverLoopReselectsAtExhaustedBudget(t *testing.T) {
	for _, budget := range []int{0, 2} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			state := NewFailoverState(budget, true)
			state.SwitchCount = budget
			unscheduler := &mockTempUnscheduler{}
			switchReports := 0
			ctx := service.WithMonitorSwitchReporter(context.Background(), func(int) { switchReports++ })
			var selected []int64
			for {
				var candidate int64
				for _, id := range []int64{1, 2} {
					if _, excluded := state.FailedAccountIDs[id]; !excluded {
						candidate = id
						break
					}
				}
				require.NotZero(t, candidate, "scope rejection must allow a healthy candidate")
				selected = append(selected, candidate)
				if candidate == 2 {
					break
				}
				failover := schedulingProtectionScopeRejection(t, candidate)
				require.False(t, failover.ShouldReportAccountScheduleFailure())
				require.Equal(t, FailoverContinue, state.HandleFailoverError(ctx, unscheduler, candidate, service.PlatformOpenAI, 3, failover))
			}
			require.Equal(t, []int64{1, 2}, selected)
			require.Equal(t, budget, state.SwitchCount)
			require.Zero(t, switchReports)
			require.False(t, state.ForceCacheBilling)
			require.Empty(t, state.SameAccountRetryCount)
			require.Empty(t, unscheduler.calls)
		})
	}
}
