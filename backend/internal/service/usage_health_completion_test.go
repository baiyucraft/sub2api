//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type usageCompletionHealthRecorder struct{ successes int }

func (r *usageCompletionHealthRecorder) RecordUpstreamTrafficSuccess(context.Context, *Account, int) {
	r.successes++
}
func (*usageCompletionHealthRecorder) RecordUpstreamTrafficFailure(context.Context, *Account, int) {}

func TestRecordPartialUsageDoesNotRecoverUpstreamHealth(t *testing.T) {
	for _, platform := range []string{"gateway", "openai"} {
		for _, outcome := range []string{"complete_without_cache_usage", "forward_failed", "client_disconnected", "upstream_incomplete", "ws_failed", "ws_incomplete", "ws_cancelled", "ws_missing_terminal", "ws_complete", "nonstream_without_cache_usage", "canceled_after_completion", "gateway_completed_then_disconnect", "gateway_completed_then_forward_error", "gateway_completed_then_incomplete"} {
			if platform == "openai" && strings.HasPrefix(outcome, "gateway_") {
				continue
			}
			if platform == "gateway" && strings.HasPrefix(outcome, "ws_") {
				continue
			}
			t.Run(platform+"/"+outcome, func(t *testing.T) {
				previous := upstreamHealthEvidenceRecorder()
				recorder := &usageCompletionHealthRecorder{}
				SetGlobalUpstreamHealthEvidenceRecorder(recorder)
				t.Cleanup(func() { SetGlobalUpstreamHealthEvidenceRecorder(previous) })
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				users := &openAIRecordUsageUserRepoStub{}
				user, key, account := &User{ID: 11}, &APIKey{ID: 12}, &Account{ID: 13}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if outcome == "canceled_after_completion" {
					cancel()
				}
				stream := outcome != "nonstream_without_cache_usage"
				failed := outcome == "forward_failed" || outcome == "gateway_completed_then_forward_error"
				disconnect := outcome == "client_disconnected" || strings.HasPrefix(outcome, "gateway_completed_")
				incomplete := outcome == "upstream_incomplete" || outcome == "gateway_completed_then_incomplete"
				if platform == "gateway" {
					svc := newGatewayRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{})
					require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{
						Result: &ForwardResult{RequestID: "partial-gateway", Model: "claude-sonnet-4", Stream: stream, Duration: time.Second, ClientDisconnect: disconnect, UpstreamIncomplete: incomplete,
							UpstreamCompleted: strings.HasPrefix(outcome, "gateway_completed_"),
							Usage:             ClaudeUsage{InputTokens: 100, OutputTokens: 7}},
						ForwardFailed: failed, APIKey: key, User: user, Account: account,
					}))
				} else {
					svc := newOpenAIRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{}, nil)
					result := &OpenAIForwardResult{RequestID: "partial-openai", Model: "gpt-5.1", Stream: stream, Duration: time.Second, ClientDisconnect: disconnect, UpstreamIncomplete: incomplete,
						Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 7}}
					switch outcome {
					case "ws_failed":
						result.OpenAIWSMode, result.UpstreamTerminalEvent = true, "response.failed"
					case "ws_incomplete":
						result.OpenAIWSMode, result.UpstreamTerminalEvent = true, "response.incomplete"
					case "ws_cancelled":
						result.OpenAIWSMode, result.UpstreamTerminalEvent = true, "response.cancelled"
					case "ws_missing_terminal":
						result.OpenAIWSMode = true
					case "ws_complete":
						result.OpenAIWSMode, result.UpstreamTerminalEvent = true, "response.completed"
					}
					// Known raw cache fields do not establish terminal success.
					if outcome != "complete_without_cache_usage" && outcome != "nonstream_without_cache_usage" {
						result.CacheUsageEvidence = &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 80}
					}
					require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, ForwardFailed: failed, APIKey: key, User: user, Account: account}))
				}
				expected := 0
				if outcome == "complete_without_cache_usage" || outcome == "ws_complete" || outcome == "nonstream_without_cache_usage" || outcome == "canceled_after_completion" || outcome == "gateway_completed_then_disconnect" {
					expected = 1
				}
				require.Equal(t, expected, recorder.successes)
				require.Equal(t, 1, logs.calls, "partial upstream usage remains billable")
				require.Equal(t, 1, users.deductCalls, "health eligibility must not suppress billing")
				require.Equal(t, 100, logs.lastLog.InputTokens)
				require.Equal(t, 7, logs.lastLog.OutputTokens)
			})
		}
	}
}

func TestOpenAIWSTerminalStatusIndependentOfUsage(t *testing.T) {
	for _, tc := range []struct {
		payload    string
		incomplete bool
	}{
		{`{"type":"response.completed","response":{"status":"completed"}}`, false},
		{`{"type":"response.completed","response":{}}`, false},
		{`{"type":"response.completed","response":{"status":"incomplete"}}`, true},
		{`{"type":"response.done","response":{"status":"failed"}}`, true},
	} {
		require.Equal(t, tc.incomplete, openAIWSTerminalIncomplete([]byte(tc.payload)))
	}
}

// Exercise real provider terminal and downstream write behavior before billing.
func TestGeminiStreamCompletionControlsUsageHealthRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"native", "messages", "chat"} {
		for _, outcome := range []string{"complete_without_cache_usage", "missing_terminal", "failed_terminal", "write_failed", "canceled_after_completion"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				previous := upstreamHealthEvidenceRecorder()
				recorder := &usageCompletionHealthRecorder{}
				SetGlobalUpstreamHealthEvidenceRecorder(recorder)
				t.Cleanup(func() { SetGlobalUpstreamHealthEvidenceRecorder(previous) })
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writer := &cacheGuardGeminiFailWriter{ResponseRecorder: httptest.NewRecorder()}
				if outcome == "write_failed" {
					writer.marker = "hello"
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				terminal := `,"finishReason":"STOP"`
				if outcome == "missing_terminal" {
					terminal = ""
				}
				if outcome == "failed_terminal" {
					terminal = `,"finishReason":"SAFETY"`
				}
				body := `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}` + terminal + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}` + "\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				parser := &GeminiMessagesCompatService{}
				var usage *ClaudeUsage
				var succeeded, disconnected bool
				switch route {
				case "native":
					got, err := parser.handleNativeStreamingResponse(c, resp, time.Now(), false, &Account{ID: 13}, "test")
					require.NoError(t, err)
					usage, succeeded, disconnected = got.usage, got.succeeded, got.clientDisconnect
				case "messages":
					got, err := parser.handleStreamingResponse(c, resp, time.Now(), "gemini-test")
					require.NoError(t, err)
					usage, succeeded, disconnected = got.usage, got.succeeded, got.clientDisconnect
				case "chat":
					got, err := parser.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false, true)
					require.NoError(t, err)
					usage, succeeded, disconnected = got.usage, got.succeeded, got.clientDisconnect
				}
				if outcome == "canceled_after_completion" {
					cancel()
				}
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				users := &openAIRecordUsageUserRepoStub{}
				svc := newGatewayRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{})
				svc.billingService = NewBillingService(svc.cfg, &PricingService{pricingData: map[string]*LiteLLMModelPricing{
					"gemini-2.5-pro": {InputCostPerToken: 1e-6, OutputCostPerToken: 2e-6},
				}})
				svc.resolver = NewModelPricingResolver(nil, svc.billingService)
				require.NoError(t, svc.RecordUsage(ctx, &RecordUsageInput{
					Result: &ForwardResult{RequestID: "gemini-partial", Model: "gemini-2.5-pro", Stream: true, Usage: *usage, UpstreamIncomplete: !succeeded, ClientDisconnect: disconnected},
					User:   &User{ID: 11}, APIKey: &APIKey{ID: 12}, Account: &Account{ID: 13},
				}))
				expected := 0
				if outcome == "complete_without_cache_usage" || outcome == "canceled_after_completion" {
					expected = 1
				}
				require.Equal(t, expected, recorder.successes)
				require.Equal(t, 1, logs.calls)
				require.Equal(t, 1, users.deductCalls)
				require.Equal(t, 100, logs.lastLog.InputTokens)
				require.Equal(t, 7, logs.lastLog.OutputTokens)
				if outcome == "write_failed" {
					require.True(t, writer.failed)
				}
			})
		}
	}
}
