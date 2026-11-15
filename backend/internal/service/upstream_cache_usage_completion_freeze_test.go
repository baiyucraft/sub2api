package service

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
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheGuardErrorAfterReader struct{ io.Reader }

func (r *cacheGuardErrorAfterReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, errors.New("synthetic upstream read failure")
	}
	return n, err
}

func TestCacheGuardAntigravityExplicitOutputErrorRevokesCompletedSnapshot(t *testing.T) {
	for _, route := range []string{"claude", "responses", "chat"} {
		t.Run(route, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx := cacheGuardTestAdmittedContext()
			ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt).admission.Request.Protocol = "gemini"
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
			body := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"reply"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90}}}` + "\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(&cacheGuardErrorAfterReader{strings.NewReader(body)})}
			svc := &AntigravityGatewayService{settingService: &SettingService{cfg: &config.Config{}}}
			var err error
			switch route {
			case "claude":
				_, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "test")
			case "responses":
				_, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "test", newAntigravityResponsesStreamAdapter("test"), "test")
			case "chat":
				_, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "test", newAntigravityChatStreamAdapter("test", true), "test")
			}
			require.Error(t, err)
			require.Contains(t, recorder.Body.String(), "stream_read_error")
			FinishGatewayUpstreamCacheGuardAttempt(ctx, nil, err)
			require.Len(t, store.events, 1)
			require.False(t, store.events[0].ValidSample(), "an explicit client-facing failure must supersede the earlier terminal")
		})
	}
}

func TestCacheGuardCompletionFreezeRequiresDeliveredKnownSuccess(t *testing.T) {
	for _, scenario := range []string{"complete", "canceled_before", "missing_terminal", "undelivered", "unknown", "incomplete", "non_text"} {
		t.Run(scenario, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
			defer cancel()
			evidence := &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 100, CacheReadTokens: 90}
			if scenario == "canceled_before" {
				cancel()
			}
			if scenario == "unknown" {
				evidence.Status = upstreamcacheusage.Absent
			}
			if scenario == "incomplete" {
				evidence.StreamIncomplete = true
			}
			freezeUpstreamCacheGuardCompletion(ctx, evidence, scenario != "missing_terminal", scenario != "undelivered")
			// Mutating a later usage frame cannot alter the saved raw fact.
			evidence.CacheReadTokens = 1
			cancel()
			result := &OpenAIForwardResult{CacheUsageEvidence: evidence, ClientDisconnect: true}
			if scenario == "non_text" {
				result.ImageCount = 1
			}
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, context.Canceled)
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, nil)
			require.Len(t, store.events, 1)
			require.Equal(t, scenario == "complete", store.events[0].ValidSample())
			if scenario == "complete" {
				require.Equal(t, int64(90), store.events[0].RawUsage.CacheReadTokens)
			}
		})
	}
}

func TestCacheGuardAntigravityRequiresRealDeliveredTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"claude", "responses", "chat"} {
		for _, scenario := range []string{"completed", "missing_terminal", "output_only_usage", "invalid_revision", "failure_terminal"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				store := cacheGuardTestRunner(t)
				ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
				defer cancel()
				ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt).admission.Request.Protocol = "gemini"
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				body := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"reply"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90}}}` + "\n\n"
				switch scenario {
				case "missing_terminal":
					body = strings.ReplaceAll(body, `,"finishReason":"STOP"`, "")
				case "output_only_usage":
					body += `data: {"response":{"usageMetadata":{"candidatesTokenCount":8}}}` + "\n\n"
				case "invalid_revision":
					body += `data: {"response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":"bad"}}}` + "\n\n"
				case "failure_terminal":
					body += `data: {"response":{"candidates":[{"finishReason":"SAFETY"}]}}` + "\n\n"
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				svc := &AntigravityGatewayService{settingService: &SettingService{cfg: &config.Config{}}}
				var result *antigravityStreamResult
				var err error
				switch route {
				case "claude":
					result, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "test")
				case "responses":
					result, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "test", newAntigravityResponsesStreamAdapter("test"), "test")
				case "chat":
					result, err = svc.handleAntigravityCompatStream(c, resp, time.Now(), "test", newAntigravityChatStreamAdapter("test", true), "test")
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				cancel()
				FinishGatewayUpstreamCacheGuardAttempt(ctx, &ForwardResult{Usage: *result.usage, Stream: true, ClientDisconnect: result.clientDisconnect}, nil)
				require.Len(t, store.events, 1)
				require.Equal(t, scenario == "completed" || scenario == "output_only_usage", store.events[0].ValidSample())
			})
		}
	}
}

func TestCacheGuardRawChatFreezesAfterTerminalAndTrailingUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, scenario := range []string{"completed", "usage_after_terminal", "usage_corrected", "usage_invalid", "usage_absent", "failure_after_terminal", "missing_terminal", "write_failed"} {
		t.Run(scenario, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
			defer cancel()
			writer := &cacheGuardGeminiFailWriter{ResponseRecorder: httptest.NewRecorder()}
			if scenario == "write_failed" {
				writer.marker = "finish_reason"
			}
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
			content := `data: {"choices":[{"delta":{"content":"hello"}}]}` + "\n\n"
			terminal := `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
			usage := `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":90}}}` + "\n\n"
			body := content + usage + terminal
			if scenario == "usage_after_terminal" {
				body = content + terminal + usage
			}
			if scenario == "missing_terminal" {
				body = content + usage
			}
			switch scenario {
			case "usage_corrected":
				body += strings.ReplaceAll(usage, `"cached_tokens":90`, `"cached_tokens":10`)
			case "usage_invalid":
				body += strings.ReplaceAll(usage, `"cached_tokens":90`, `"cached_tokens":"bad"`)
			case "usage_absent":
				body += `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":2}}` + "\n\n"
			case "failure_after_terminal":
				body += `data: {"error":{"type":"upstream_error","message":"failed"}}` + "\n\n"
			}
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(&cacheGuardGeminiCanceledReader{Reader: strings.NewReader(body), cancel: cancel})}
			result, err := (&OpenAIGatewayService{}).streamRawChatCompletions(c, resp, &Account{ID: 12, Platform: PlatformDeepseek}, "deepseek-test", "deepseek-test", "deepseek-test", nil, nil, time.Now(), 100)
			require.NoError(t, err)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
			require.Len(t, store.events, 1)
			require.Equal(t, scenario == "completed" || scenario == "usage_after_terminal" || scenario == "usage_corrected", store.events[0].ValidSample())
			if scenario == "usage_corrected" {
				require.EqualValues(t, 10, store.events[0].RawUsage.CacheReadTokens)
			}
		})
	}
}
