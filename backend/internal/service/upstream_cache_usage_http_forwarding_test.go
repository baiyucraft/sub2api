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
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Run the actual stream readers/converters through Finish and the installed
// outbox seam. Billing numbers must remain independent of sample eligibility.
func TestCacheGuardHTTPForwardingOutcomeMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses_native", "responses_passthrough", "responses_to_cc", "responses_to_messages", "cc_native", "cc_to_responses", "cc_to_messages"} {
		responses := strings.HasPrefix(route, "responses_")
		cases := []string{"success", "missing_terminal", "missing_cache", "zero_usage", "invalid_cache", "writer_failure", "cancel_before_finish"}
		if responses {
			cases = append(cases, "missing_usage", "response.incomplete", "response.cancelled", "response.canceled", "response.failed", "completed_incomplete_status")
		} else {
			cases = append(cases, "length", "content_filter")
		}
		for _, outcome := range cases {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				store := cacheGuardTestRunner(t)
				ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
				t.Cleanup(cancel)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				if outcome == "writer_failure" {
					c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
				}
				cfg := &config.Config{}
				cfg.Gateway.MaxLineSize = defaultMaxLineSize
				svc := &OpenAIGatewayService{cfg: cfg, toolCorrector: NewCodexToolCorrector()}
				account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
				body := cacheGuardHTTPOutcomeSSE(responses, outcome)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				result, err := cacheGuardHTTPOutcomeForward(svc, ctx, c, resp, account, route)
				require.NotNil(t, result, "collected usage must survive the actual stream path")
				raw := result.CacheUsageEvidence
				if raw == nil {
					raw = result.Usage.CacheUsageEvidence
				}
				require.NotNil(t, raw, "typed billing copies must retain the raw same-frame evidence")
				wantValid := outcome == "success" || outcome == "length"
				if wantValid {
					require.NoError(t, err)
					require.True(t, raw.Known())
					require.False(t, raw.StreamIncomplete)
				}
				switch outcome {
				case "missing_cache", "missing_usage":
					require.Equal(t, upstreamcacheusage.Absent, raw.Status)
				case "invalid_cache":
					require.Equal(t, upstreamcacheusage.Invalid, raw.Status)
				case "zero_usage":
					require.Zero(t, raw.TotalInputTokens)
					require.False(t, raw.Known())
				default:
					require.True(t, raw.Known(), "failure fixture must contain otherwise valid usage")
				}
				if outcome != "zero_usage" {
					require.Equal(t, 100, result.Usage.InputTokens)
					require.Equal(t, 7, result.Usage.OutputTokens)
					wantCache := 80
					if outcome == "missing_cache" || outcome == "invalid_cache" {
						wantCache = 0
					}
					require.Equal(t, wantCache, result.Usage.CacheReadInputTokens)
				} else if route == "responses_native" || route == "responses_passthrough" {
					require.Equal(t, 100, result.Usage.InputTokens, "native terminal-zero billing fallback remains")
					require.Equal(t, 7, result.Usage.OutputTokens)
					require.Equal(t, 80, result.Usage.CacheReadInputTokens)
				} else {
					require.Zero(t, result.Usage.InputTokens, "typed/raw Chat billing preserves its existing terminal-zero semantics")
					require.Zero(t, result.Usage.OutputTokens)
					require.Zero(t, result.Usage.CacheReadInputTokens)
				}
				if outcome == "cancel_before_finish" {
					// Cancellation at the observer handoff must invalidate even a
					// fully read upstream terminal with otherwise known usage.
					cancel()
				}
				FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
				require.Len(t, store.events, 1)
				if wantValid {
					require.True(t, store.events[0].Success)
					require.True(t, store.events[0].ValidSample())
					rate, ok := store.events[0].RawUsage.Rate()
					require.True(t, ok)
					require.Equal(t, float64(80), rate)
				} else {
					require.False(t, store.events[0].Success)
					require.False(t, store.events[0].ValidSample())
				}
			})
		}
	}
}

func cacheGuardHTTPOutcomeSSE(responses bool, outcome string) string {
	usage := `{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}`
	if !responses {
		usage = `{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":80}}`
	}
	terminalUsage := usage
	switch outcome {
	case "missing_cache":
		terminalUsage = `{"input_tokens":100,"output_tokens":7}`
		if !responses {
			terminalUsage = `{"prompt_tokens":100,"completion_tokens":7}`
		}
	case "zero_usage":
		terminalUsage = `{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}`
		if !responses {
			terminalUsage = `{"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}`
		}
	case "invalid_cache":
		terminalUsage = `{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":null}}`
		if !responses {
			terminalUsage = `{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":null}}`
		}
	}
	if responses {
		prefix := `data: {"type":"response.in_progress","response":{"id":"resp_http_cache","model":"gpt-test","usage":` + usage + `}}` + "\n\n" +
			`data: {"type":"response.output_text.delta","response_id":"resp_http_cache","output_index":0,"content_index":0,"delta":"hello"}` + "\n\n"
		if outcome == "missing_terminal" {
			return prefix
		}
		eventType, status := "response.completed", "completed"
		if strings.HasPrefix(outcome, "response.") {
			eventType, status = outcome, strings.TrimPrefix(outcome, "response.")
		}
		if outcome == "completed_incomplete_status" {
			status = "incomplete"
		}
		usageField := `,"usage":` + terminalUsage
		if outcome == "missing_usage" {
			usageField = ""
		}
		return prefix + `data: {"type":"` + eventType + `","response":{"id":"resp_http_cache","model":"gpt-test","status":"` + status + `","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]` + usageField + `}}` + "\n\n"
	}
	prefix := `data: {"id":"chat_http_cache","model":"gpt-test","error":null,"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}],"usage":` + usage + `}` + "\n\n"
	if outcome == "missing_terminal" {
		return prefix
	}
	finishReason := "stop"
	if outcome == "length" || outcome == "content_filter" {
		finishReason = outcome
	}
	return prefix + `data: {"id":"chat_http_cache","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"` + finishReason + `"}],"usage":` + terminalUsage + `}` + "\n\n" + "data: [DONE]\n\n"
}

func cacheGuardHTTPOutcomeForward(svc *OpenAIGatewayService, ctx context.Context, c *gin.Context, resp *http.Response, account *Account, route string) (*OpenAIForwardResult, error) {
	start := time.Now()
	switch route {
	case "responses_native":
		result, err := svc.handleStreamingResponse(ctx, resp, c, account, start, "gpt-test", "gpt-test")
		if result == nil {
			return nil, err
		}
		return &OpenAIForwardResult{Usage: *result.usage, CacheUsageEvidence: result.usage.CacheUsageEvidence, Stream: true}, err
	case "responses_passthrough":
		result, err := svc.handleStreamingResponsePassthrough(ctx, resp, c, account, start, "gpt-test", "gpt-test")
		if result == nil {
			return nil, err
		}
		return &OpenAIForwardResult{Usage: *result.usage, CacheUsageEvidence: result.usage.CacheUsageEvidence, Stream: true}, err
	case "responses_to_cc":
		return svc.handleChatStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start, 0)
	case "responses_to_messages":
		return svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start)
	case "cc_native":
		return svc.streamRawChatCompletions(c, resp, account, "gpt-test", "gpt-test", "gpt-test", nil, nil, start, 0)
	case "cc_to_responses":
		return svc.streamChatCompletionsAsResponses(c, resp, "gpt-test", nil, nil, false, nil, "gpt-test", "gpt-test", nil, nil, start)
	case "cc_to_messages":
		return svc.streamChatCompletionsAsAnthropic(c, resp, "gpt-test", "gpt-test", "gpt-test", nil, nil, start)
	default:
		panic("unexpected test route")
	}
}
