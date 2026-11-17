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

func TestGeminiNonStreamingForwardCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"native", "messages", "chat"} {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeAPIKey} {
			for _, outcome := range []string{"complete", "missing_terminal", "failed_terminal", "write_failed", "late_cancel"} {
				t.Run(route+"/"+accountType+"/"+outcome, func(t *testing.T) {
					terminal := `,"finishReason":"STOP"`
					if outcome == "missing_terminal" {
						terminal = ""
					} else if outcome == "failed_terminal" {
						terminal = `,"finishReason":"SAFETY"`
					}
					payload := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + terminal + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}`
					contentType := "application/json"
					if accountType == AccountTypeOAuth {
						payload = "data: {\"response\":" + payload + "}\n\ndata: [DONE]\n\n"
						contentType = "text/event-stream"
					}
					upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}}
					svc := &GeminiMessagesCompatService{cfg: &config.Config{}, tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream}
					account := &Account{ID: 13, Platform: PlatformGemini, Type: accountType, Credentials: map[string]any{"api_key": "test-key", "access_token": "test-token", "project_id": "test-project"}}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					writer := &cacheGuardGeminiFailWriter{ResponseRecorder: httptest.NewRecorder()}
					if outcome == "write_failed" {
						writer.marker = "hello"
					}
					c, _ := gin.CreateTestContext(writer)
					c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
					var got *ForwardResult
					var err error
					switch route {
					case "native":
						got, err = svc.ForwardNative(ctx, c, account, "gemini-2.5-flash", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
					case "messages":
						got, err = svc.Forward(ctx, c, account, []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
					case "chat":
						got, err = svc.ForwardAsChatCompletions(ctx, c, account, []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`))
					}
					require.NoError(t, err)
					require.NotNil(t, got)
					if outcome == "late_cancel" {
						cancel()
					}
					wantSuccess := outcome == "complete" || outcome == "late_cancel"
					require.Equal(t, !wantSuccess, got.UpstreamIncomplete)
					require.Equal(t, wantSuccess, got.UpstreamCompleted)
					require.Equal(t, 100, got.Usage.InputTokens, "completion status must preserve billing")
					require.Equal(t, 7, got.Usage.OutputTokens)
					require.Equal(t, 1, upstream.calls)
				})
			}
		}
	}
}

type geminiCancelAfterTerminalReader struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (r *geminiCancelAfterTerminalReader) Read(p []byte) (int, error) {
	if r.Len() == 0 {
		r.cancel()
		return 0, context.Canceled
	}
	return r.Reader.Read(p)
}

func TestGeminiNativeCompletedBeforeReadCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, outcome := range []string{"missing_terminal", "complete", "failed_after_complete"} {
		for _, oauth := range []bool{false, true} {
			t.Run(outcome+map[bool]string{false: "/native", true: "/oauth"}[oauth], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				terminal := ""
				if outcome != "missing_terminal" {
					terminal = `,"finishReason":"STOP"`
				}
				payload := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + terminal + `}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}`
				if oauth {
					payload = `{"response":` + payload + `}`
				}
				body := "data: " + payload + "\n\n"
				if outcome == "failed_after_complete" {
					failure := `{"error":{"code":500,"message":"failed"}}`
					if oauth {
						failure = `{"response":` + failure + `}`
					}
					body += "data: " + failure + "\n\n"
				}
				reader := &geminiCancelAfterTerminalReader{Reader: strings.NewReader(body), cancel: cancel}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				svc := &GeminiMessagesCompatService{}
				got, err := svc.handleNativeStreamingResponse(c, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}, time.Now(), oauth, &Account{ID: 13}, "test")
				require.NoError(t, err)
				require.NotNil(t, got)
				require.Equal(t, outcome == "complete", got.succeeded)
				require.True(t, got.clientDisconnect, "retain the transport outcome separately")
				require.Equal(t, 100, got.usage.InputTokens)
				require.Equal(t, 7, got.usage.OutputTokens)
			})
		}
	}
}

func TestGeminiNonStreamingCancellationBeforeDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
	svc := &GeminiMessagesCompatService{}
	usage, succeeded, err := svc.handleNativeNonStreamingResponseWithCompletion(c, &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7}}`))}, false, nil, "test")
	require.NoError(t, err)
	require.False(t, succeeded)
	require.Equal(t, 100, usage.InputTokens)
}
