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

func protectionBudgetHandlerRejection() *service.UpstreamFailoverError {
	return &service.UpstreamFailoverError{PluginAdmissionRejected: true, ProtectionOrdinaryFallback: true, StatusCode: http.StatusServiceUnavailable, Scope: service.GatewayFailureScopeRequest, Reason: "scheduling_protection_budget_exhausted"}
}

func TestProtectionBudgetHTTPHandlerKeepsOnlyAccountForOrdinaryRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	endpoints := []struct {
		name, path, body string
		run              func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5.1","input":"hello","stream":false}`, (*OpenAIGatewayHandler).Responses},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).ChatCompletions},
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			attempts := 0
			upstream := &pluginAdmissionHandlerUpstream{transportError: func(pluginAdmissionHandlerAttempt) error {
				attempts++
				if attempts == 1 {
					return fmt.Errorf("pre-send protection budget: %w", protectionBudgetHandlerRejection())
				}
				return nil
			}}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts()[:1], 0)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
			switches := 0
			c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { switches = count }))
			c.Request.Header.Set("Content-Type", "application/json")
			setPluginAdmissionHandlerAuth(c)
			endpoint.run(h, c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			requirePluginAdmissionAttempts(t, upstream, 1, 1)
			require.Zero(t, switches)
			require.False(t, service.IsForceCacheBilling(c.Request.Context()))
			_, recorded := c.Get(service.OpsUpstreamErrorsKey)
			require.False(t, recorded, "a protection deadline is not upstream failure")
		})
	}
}

func TestProtectionBudgetExtendedHTTPHandlerKeepsOnlyAccountForOrdinaryRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range pluginAdmissionExtendedEndpoints() {
		t.Run(endpoint.name, func(t *testing.T) {
			attempts := 0
			upstream := &pluginAdmissionHandlerUpstream{respond: pluginAdmissionExtendedResponse, transportError: func(pluginAdmissionHandlerAttempt) error {
				attempts++
				if attempts == 1 {
					return fmt.Errorf("pre-send protection budget: %w", protectionBudgetHandlerRejection())
				}
				return nil
			}}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionExtendedAccounts(endpoint.accountType)[:1], 0)
			c, recorder := pluginAdmissionExtendedContext(endpoint)
			switches := 0
			c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { switches = count }))
			endpoint.run(h, c)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			requirePluginAdmissionAttempts(t, upstream, 1, 1)
			require.Zero(t, switches)
			require.False(t, service.IsForceCacheBilling(c.Request.Context()))
			_, recorded := c.Get(service.OpsUpstreamErrorsKey)
			require.False(t, recorded)
		})
	}
}

func TestProtectionBudgetGeneralFailoverKeepsSingleAccountAtExhaustedBudget(t *testing.T) {
	for _, budget := range []int{0, 2} {
		t.Run(fmt.Sprintf("budget=%d", budget), func(t *testing.T) {
			state := NewFailoverState(budget, true)
			state.SwitchCount = budget
			unscheduler := &mockTempUnscheduler{}
			switchReports := 0
			ctx := service.WithMonitorSwitchReporter(context.Background(), func(int) { switchReports++ })
			failure := protectionBudgetHandlerRejection()
			require.Equal(t, FailoverContinue, state.HandleFailoverError(ctx, unscheduler, 1, service.PlatformOpenAI, 3, failure))
			require.NotContains(t, state.FailedAccountIDs, int64(1), "the only account remains available to ordinary scheduling")
			require.Empty(t, state.pluginRejectedAccountIDs)
			require.Equal(t, budget, state.SwitchCount)
			require.Zero(t, switchReports)
			require.False(t, state.ForceCacheBilling)
			require.Empty(t, state.SameAccountRetryCount)
			require.Empty(t, unscheduler.calls)
		})
	}
}
