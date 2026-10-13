//go:build unit

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

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheGuardAnthropicReadError struct{}

func (cacheGuardAnthropicReadError) Read([]byte) (int, error) {
	return 0, errors.New("upstream read interrupted")
}

type cacheGuardAnthropicConvertedResult struct {
	input, output, cached int
	raw                   *UpstreamCacheUsageEvidence
	disconnected          bool
}

// Exercise all four real conversion loops and the durable completion observer.
// Finalizing a client-facing response must not manufacture upstream success.
func TestCacheGuardAnthropicConvertersRequireSuccessfulCompleteStream(t *testing.T) {
	start := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-test\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":90,\"output_tokens\":7}}}\n\n"
	stop := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	converters := []struct {
		name      string
		wantInput int
		run       func(*testing.T, *gin.Context, *http.Response) (cacheGuardAnthropicConvertedResult, error)
	}{
		{"gateway_chat", 10, func(t *testing.T, c *gin.Context, resp *http.Response) (cacheGuardAnthropicConvertedResult, error) {
			result, err := (&GatewayService{}).handleCCStreamingFromAnthropic(resp, c, "claude-test", "claude-test", nil, time.Now())
			require.NotNil(t, result)
			FinishGatewayUpstreamCacheGuardAttempt(c.Request.Context(), result, err)
			return cacheGuardAnthropicConvertedResult{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadInputTokens, result.CacheUsageEvidence, result.ClientDisconnect}, err
		}},
		{"gateway_responses", 10, func(t *testing.T, c *gin.Context, resp *http.Response) (cacheGuardAnthropicConvertedResult, error) {
			result, err := (&GatewayService{}).handleResponsesStreamingResponse(resp, c, "claude-test", "claude-test", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			require.NotNil(t, result)
			FinishGatewayUpstreamCacheGuardAttempt(c.Request.Context(), result, err)
			return cacheGuardAnthropicConvertedResult{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadInputTokens, result.CacheUsageEvidence, result.ClientDisconnect}, err
		}},
		{"native_chat", 100, func(t *testing.T, c *gin.Context, resp *http.Response) (cacheGuardAnthropicConvertedResult, error) {
			result, err := (&OpenAIGatewayService{}).handleCCStreamingFromNativeAnthropic(resp, c, "claude-test", "claude-test", "claude-test", nil, time.Now())
			require.NotNil(t, result)
			FinishOpenAIUpstreamCacheGuardAttempt(c.Request.Context(), result, err)
			return cacheGuardAnthropicConvertedResult{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadInputTokens, result.Usage.CacheUsageEvidence, result.ClientDisconnect}, err
		}},
		{"native_responses", 100, func(t *testing.T, c *gin.Context, resp *http.Response) (cacheGuardAnthropicConvertedResult, error) {
			result, err := (&OpenAIGatewayService{}).handleResponsesStreamingFromNativeAnthropic(resp, c, "claude-test", "claude-test", "claude-test", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
			require.NotNil(t, result)
			FinishOpenAIUpstreamCacheGuardAttempt(c.Request.Context(), result, err)
			return cacheGuardAnthropicConvertedResult{result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadInputTokens, result.Usage.CacheUsageEvidence, result.ClientDisconnect}, err
		}},
	}
	for _, converter := range converters {
		t.Run(converter.name, func(t *testing.T) {
			for _, tc := range []struct {
				name                 string
				complete, readFailed bool
				writeFailed, cancel  bool
			}{
				{name: "completed", complete: true},
				{name: "missing_terminal_eof"},
				{name: "missing_terminal_read_error", readFailed: true},
				{name: "client_write_failed", complete: true, writeFailed: true},
				{name: "request_cancelled", complete: true, cancel: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					store := cacheGuardTestRunner(t)
					ctx, cancel := context.WithCancel(cacheGuardTestAdmittedContext())
					defer cancel()
					if tc.cancel {
						cancel()
					}
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
					if tc.writeFailed {
						c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
					}
					payload := start
					if tc.complete {
						payload += stop
					}
					var reader io.Reader = strings.NewReader(payload)
					if tc.readFailed {
						reader = io.MultiReader(reader, cacheGuardAnthropicReadError{})
					}
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(reader)}
					defer resp.Body.Close()
					result, err := converter.run(t, c, resp)
					require.NoError(t, err, "retain the converter's existing client-facing completion policy")
					require.Equal(t, converter.wantInput, result.input, "retain observed billing input")
					require.Equal(t, 7, result.output, "retain observed billing output")
					require.Equal(t, 90, result.cached, "retain observed billing cache tokens")
					require.NotNil(t, result.raw)
					require.True(t, result.raw.Known(), "keep raw token presence independent of lifecycle success")
					require.Equal(t, int64(100), result.raw.TotalInputTokens)
					require.Equal(t, int64(90), result.raw.CacheReadTokens)
					require.Equal(t, tc.writeFailed, result.disconnected)
					require.Len(t, store.events, 1)
					wantSample := tc.complete && !tc.readFailed && !tc.writeFailed && !tc.cancel
					require.Equal(t, wantSample, store.events[0].Success)
					require.Equal(t, wantSample, store.events[0].ValidSample())
					if !tc.complete || tc.readFailed {
						require.True(t, result.raw.StreamIncomplete)
					}
				})
			}
		})
	}
}
