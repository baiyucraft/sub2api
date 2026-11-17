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

func TestAntigravityCompletionIndependentOfCacheUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"gemini_stream", "claude_stream", "chat_stream", "responses_stream", "gemini_collect", "claude_collect", "chat_collect", "responses_collect"} {
		for _, outcome := range []string{"complete", "missing_terminal", "failed_terminal", "write_failed", "late_cancel"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				svc := newAntigravityTestService(&config.Config{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
				if outcome == "write_failed" {
					c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
				}
				terminal := `,"finishReason":"STOP"`
				if outcome == "missing_terminal" {
					terminal = ""
				}
				if outcome == "failed_terminal" {
					terminal = `,"finishReason":"SAFETY"`
				}
				body := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + terminal + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}}` + "\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
				var got *antigravityStreamResult
				var err error
				switch route {
				case "gemini_stream":
					got, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
				case "claude_stream":
					got, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4")
				case "chat_stream":
					got, err = svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "gemini-test", true)
				case "responses_stream":
					got, err = svc.handleResponsesStreamingFromAntigravity(c, resp, time.Now(), "gemini-test")
				case "gemini_collect":
					got, err = svc.handleGeminiStreamToNonStreaming(c, resp, time.Now())
				case "claude_collect":
					got, err = svc.handleClaudeStreamToNonStreaming(c, resp, time.Now(), "claude-sonnet-4")
				case "chat_collect":
					got, err = svc.handleChatCompletionsNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-test")
				case "responses_collect":
					got, err = svc.handleResponsesNonStreamingFromAntigravity(c, resp, time.Now(), "gemini-test")
				}
				require.NoError(t, err)
				require.NotNil(t, got)
				if outcome == "late_cancel" {
					cancel()
				}
				require.Equal(t, outcome == "complete" || outcome == "late_cancel", got.succeeded)
				require.NotNil(t, got.usage)
				require.Equal(t, 100, got.usage.InputTokens, "completion eligibility must preserve billing")
				require.Equal(t, 7, got.usage.OutputTokens)
			})
		}
	}
}

func TestAntigravityUpstreamTerminalAndNeutralLocalResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, outcome := range []string{"complete", "missing_terminal", "failed_terminal", "write_failed", "late_cancel"} {
		t.Run(outcome, func(t *testing.T) {
			svc := newAntigravityTestService(&config.Config{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
			beginUpstreamResponseModelObservation(c)
			if outcome == "write_failed" {
				c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
			}
			body := "data: " + `{"type":"message_start","message":{"usage":{"input_tokens":100}}}` + "\n\n" +
				"data: " + `{"type":"message_delta","usage":{"output_tokens":7}}` + "\n\n"
			if outcome != "missing_terminal" {
				body += "data: " + `{"type":"message_stop"}` + "\n\n"
			}
			if outcome == "failed_terminal" {
				body += "data: " + `{"type":"error","error":{"type":"overloaded_error"}}` + "\n\n"
			}
			got := svc.streamUpstreamResponse(c, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, time.Now())
			if outcome == "late_cancel" {
				cancel()
			}
			require.Equal(t, outcome == "complete" || outcome == "late_cancel", got.succeeded)
			require.Equal(t, 100, got.usage.InputTokens)
			require.Equal(t, 7, got.usage.OutputTokens)
		})
	}
	t.Run("local_count_tokens", func(t *testing.T) {
		svc := newAntigravityTestService(&config.Config{})
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		got, err := svc.ForwardGemini(context.Background(), c, &Account{}, "gemini-test", "countTokens", false, []byte(`{}`), false)
		require.NoError(t, err)
		require.True(t, got.UpstreamIncomplete)
	})
	t.Run("upstream_http_error", func(t *testing.T) {
		svc := newAntigravityTestService(&config.Config{})
		svc.httpUpstream = &httpUpstreamStub{resp: &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`))}}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/", nil)
		got, err := svc.ForwardUpstream(context.Background(), c, &Account{Credentials: map[string]any{"base_url": "https://example.invalid", "api_key": "test"}}, []byte(`{"model":"claude-sonnet-4"}`))
		require.NoError(t, err)
		require.True(t, got.UpstreamIncomplete)
	})
}

// The scanner emits the successful terminal before its read cancellation, so
// the parser receives the cancellation while it is still draining, not after
// the forwarding function has returned.
type antigravityCancelAfterPayloadReader struct{ payload *strings.Reader }

func (r *antigravityCancelAfterPayloadReader) Read(p []byte) (int, error) {
	n, err := r.payload.Read(p)
	if err == io.EOF {
		return n, context.Canceled
	}
	return n, err
}

func (*antigravityCancelAfterPayloadReader) Close() error { return nil }

func TestAntigravityReadCancellationAfterTerminalPreservesHealthCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"gemini", "claude", "chat", "responses", "native"} {
		for _, outcome := range []string{"complete", "missing_terminal", "failed_terminal", "write_failed"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				svc := newAntigravityTestService(&config.Config{})
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
				beginUpstreamResponseModelObservation(c)
				if outcome == "write_failed" {
					c.Writer = &antigravityFailingWriter{ResponseWriter: c.Writer, failAfter: 0}
				}
				terminal := `,"finishReason":"STOP"`
				if outcome == "missing_terminal" {
					terminal = ""
				} else if outcome == "failed_terminal" {
					terminal = `,"finishReason":"SAFETY"`
				}
				body := `data: {"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + terminal + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}}` + "\n\n"
				if route == "native" {
					body = "data: " + `{"type":"message_start","message":{"usage":{"input_tokens":100}}}` + "\n\n" +
						"data: " + `{"type":"message_delta","usage":{"output_tokens":7}}` + "\n\n"
					if outcome != "missing_terminal" {
						body += "data: " + `{"type":"message_stop"}` + "\n\n"
					}
					if outcome == "failed_terminal" {
						body += "data: " + `{"type":"error","error":{"type":"overloaded_error"}}` + "\n\n"
					}
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &antigravityCancelAfterPayloadReader{payload: strings.NewReader(body)}}
				var got *antigravityStreamResult
				var err error
				switch route {
				case "gemini":
					got, err = svc.handleGeminiStreamingResponse(c, resp, time.Now())
				case "claude":
					got, err = svc.handleClaudeStreamingResponse(c, resp, time.Now(), "claude-sonnet-4")
				case "chat":
					got, err = svc.handleChatCompletionsStreamingFromAntigravity(c, resp, time.Now(), "gemini-test", true)
				case "responses":
					got, err = svc.handleResponsesStreamingFromAntigravity(c, resp, time.Now(), "gemini-test")
				case "native":
					got = svc.streamUpstreamResponse(c, resp, time.Now())
				}
				require.NoError(t, err)
				require.NotNil(t, got)
				require.True(t, got.clientDisconnect, "the original disconnect remains available to billing consumers")
				require.Equal(t, outcome == "complete", got.succeeded, "only a real successful terminal already delivered survives later read cancellation")
				require.Equal(t, 100, got.usage.InputTokens)
				require.Equal(t, 7, got.usage.OutputTokens)
			})
		}
	}
}
