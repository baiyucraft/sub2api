package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheGuardGeminiFailWriter struct {
	*httptest.ResponseRecorder
	marker string
	failed bool
}

func (w *cacheGuardGeminiFailWriter) Write(p []byte) (int, error) {
	if w.marker != "" && bytes.Contains(p, []byte(w.marker)) {
		w.failed = true
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(p)
}

func (w *cacheGuardGeminiFailWriter) WriteString(p string) (int, error) {
	return w.Write([]byte(p))
}

type cacheGuardGeminiCanceledReader struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (r *cacheGuardGeminiCanceledReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.cancel()
		return n, context.Canceled
	}
	return n, err
}

func TestCacheGuardGeminiCanceledReadAfterTerminalPreservesBillingWithoutWarmSample(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"native", "messages", "chat"} {
		t.Run(route, func(t *testing.T) {
			store := cacheGuardTestRunner(t)
			ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
			defer cancel()
			ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt).admission.Request.Protocol = "gemini"
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
			body := `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"candidatesTokenCount":7}}` + "\n\n"
			resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(&cacheGuardGeminiCanceledReader{Reader: strings.NewReader(body), cancel: cancel})}
			svc := &GeminiMessagesCompatService{}
			var usage *ClaudeUsage
			if route == "native" {
				result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, &Account{ID: 1}, "test-request")
				require.NoError(t, err)
				require.True(t, result.clientDisconnect)
				usage = result.usage
			} else if route == "messages" {
				result, err := svc.handleStreamingResponse(c, resp, time.Now(), "gemini-test")
				require.NoError(t, err)
				require.True(t, result.clientDisconnect)
				usage = result.usage
			} else {
				result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false, true)
				require.NoError(t, err)
				require.True(t, result.clientDisconnect)
				usage = result.usage
			}
			require.Equal(t, 10, usage.InputTokens)
			require.Equal(t, 90, usage.CacheReadInputTokens)
			require.Equal(t, 7, usage.OutputTokens)
			require.NotNil(t, usage.CacheUsageEvidence)
			require.True(t, usage.CacheUsageEvidence.StreamIncomplete)
			FinishGatewayUpstreamCacheGuardAttempt(ctx, &ForwardResult{Usage: *usage, Stream: true}, nil)
			require.Len(t, store.events, 1)
			require.False(t, store.events[0].ValidSample())
		})
	}
}

func TestCacheGuardGeminiBusinessStreamRequiresTerminalAndClientOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"native", "messages", "chat"} {
		for _, scenario := range []string{"completed", "budget_completed", "missing_terminal", "output_write_failed", "terminal_write_failed"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				store := cacheGuardTestRunner(t)
				ctx := cacheGuardTestAdmittedContext()
				holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
				holder.admission.Request.Protocol = "gemini"
				writer := &cacheGuardGeminiFailWriter{ResponseRecorder: httptest.NewRecorder()}
				if scenario == "output_write_failed" {
					writer.marker = "hello"
				}
				if scenario == "terminal_write_failed" {
					switch route {
					case "native":
						writer.marker = `"finishReason"`
					case "messages":
						writer.marker = "event: message_stop"
					case "chat":
						writer.marker = "data: [DONE]"
					}
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				finish := `,"finishReason":"STOP"`
				if scenario == "budget_completed" {
					finish = `,"finishReason":"MAX_TOKENS"`
				}
				if scenario == "missing_terminal" {
					finish = ""
				}
				body := `data: {"candidates":[{"content":{"parts":[]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"candidatesTokenCount":0}}` + "\n\n" +
					`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}` + finish + `}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"candidatesTokenCount":7}}` + "\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				svc := &GeminiMessagesCompatService{}
				var usage *ClaudeUsage
				switch route {
				case "native":
					result, err := svc.handleNativeStreamingResponse(c, resp, time.Now(), false, nil, "test-request")
					require.NoError(t, err)
					require.NotNil(t, result)
					usage = result.usage
				case "messages":
					result, err := svc.handleStreamingResponse(c, resp, time.Now(), "gemini-test")
					require.NoError(t, err)
					require.NotNil(t, result)
					usage = result.usage
				case "chat":
					result, err := svc.handleChatCompletionsStreamingResponseFromGemini(c, resp, time.Now(), "gemini-test", false, true)
					require.NoError(t, err)
					require.NotNil(t, result)
					usage = result.usage
				}
				require.NotNil(t, usage)
				// Output failures must not discard the upstream's billable buckets.
				require.Equal(t, 10, usage.InputTokens)
				require.Equal(t, 90, usage.CacheReadInputTokens)
				require.Equal(t, 7, usage.OutputTokens)
				require.NotNil(t, usage.CacheUsageEvidence)
				wantSuccess := scenario == "completed" || scenario == "budget_completed"
				require.Equal(t, !wantSuccess, usage.CacheUsageEvidence.StreamIncomplete)
				if strings.HasSuffix(scenario, "write_failed") {
					require.True(t, writer.failed, "test must exercise a real client write failure")
				}
				FinishGatewayUpstreamCacheGuardAttempt(ctx, &ForwardResult{Usage: *usage, Stream: true}, nil)
				require.Len(t, store.events, 1)
				require.EqualValues(t, 100, store.events[0].RawUsage.TotalInputTokens)
				require.EqualValues(t, 90, store.events[0].RawUsage.CacheReadTokens)
				require.Equal(t, wantSuccess, store.events[0].Success)
				require.Equal(t, wantSuccess, store.events[0].ValidSample())
			})
		}
	}
}
