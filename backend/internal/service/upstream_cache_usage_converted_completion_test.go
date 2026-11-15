package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardConvertedHTTPFreezesOnlyDeliveredCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses_passthrough", "responses_to_cc", "responses_to_messages", "cc_to_responses", "cc_to_messages"} {
		for _, scenario := range []string{"success", "cancel_before_terminal", "missing_terminal", "missing_cache", "invalid_cache", "writer_failure", "completed_incomplete_status"} {
			if scenario == "completed_incomplete_status" && !strings.HasPrefix(route, "responses_") {
				continue
			}
			t.Run(route+"/"+scenario, func(t *testing.T) {
				store := cacheGuardTestRunner(t)
				ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
				defer cancel()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				if scenario == "writer_failure" {
					c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
				}
				if scenario == "cancel_before_terminal" {
					cancel()
				}
				bodyScenario := scenario
				if scenario == "cancel_before_terminal" || scenario == "writer_failure" {
					bodyScenario = "success"
				}
				body := cacheGuardHTTPOutcomeSSE(strings.HasPrefix(route, "responses_"), bodyScenario)
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
				result, err := cacheGuardHTTPOutcomeForward(svc, ctx, c, resp, &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, route)
				require.NotNil(t, result)
				// Only a fully delivered successful response may survive this
				// later cancellation at the handler/observer handoff.
				cancel()
				FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
				require.Len(t, store.events, 1)
				require.Equal(t, scenario == "success", store.events[0].ValidSample())
				if scenario == "success" {
					require.WithinDuration(t, time.Now(), store.events[0].RawUsage.CompletedAt, time.Second)
				}
			})
		}
	}
}
