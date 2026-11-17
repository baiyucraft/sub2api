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

// Real HTTP parsers must retain both billable usage and the independent
// completion facts consumed by RecordUsage's health-recovery guard.
func TestOpenAIHTTPStreamHealthCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	progress := "data: " + `{"type":"response.output_text.delta","delta":"partial","response":{"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n"
	completed := "data: " + `{"type":"response.completed","response":{"id":"resp_health","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n"
	for _, transport := range []string{"native", "passthrough"} {
		for _, tc := range []struct {
			name         string
			terminal     string
			incomplete   bool
			writeFailure bool
			wantError    bool
			lateCancel   bool
		}{
			{name: "completed_without_cache_fields", terminal: completed},
			{name: "incomplete", terminal: "data: " + `{"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n", incomplete: true},
			{name: "completed_with_incomplete_status", terminal: "data: " + `{"type":"response.completed","response":{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n", incomplete: true},
			{name: "incomplete_then_done", terminal: "data: " + `{"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\ndata: [DONE]\n\n", incomplete: true},
			{name: "missing_terminal", incomplete: true, wantError: true},
			{name: "write_failure_still_drains_usage", terminal: completed, writeFailure: true},
			{name: "done_compatibility", terminal: "data: [DONE]\n\n"},
			{name: "cancel_after_completed", terminal: completed, lateCancel: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				recorder := newOpenAIResponseFlushRecorder()
				if tc.writeFailure {
					recorder.failAfterWrites = 1
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(progress + tc.terminal))}
				var usage *OpenAIUsage
				var incomplete, disconnected bool
				var err error
				if transport == "native" {
					var result *openaiStreamingResult
					result, err = svc.handleStreamingResponse(ctx, resp, c, account, time.Now(), "gpt-5", "gpt-5")
					require.NotNil(t, result)
					usage, incomplete, disconnected = result.usage, result.upstreamIncomplete, result.clientDisconnect
				} else {
					var result *openaiStreamingResultPassthrough
					result, err = svc.handleStreamingResponsePassthrough(ctx, resp, c, account, time.Now(), "gpt-5", "gpt-5")
					require.NotNil(t, result)
					usage, incomplete, disconnected = result.usage, result.upstreamIncomplete, result.clientDisconnect
				}
				if tc.lateCancel {
					cancel()
				}
				if tc.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.incomplete, incomplete)
				require.Equal(t, tc.writeFailure, disconnected)
				require.NotNil(t, usage)
				require.Equal(t, 100, usage.InputTokens, "unsuccessful delivery must retain billable usage")
				require.Equal(t, 7, usage.OutputTokens)
			})
		}
	}
}

func TestOpenAIHTTPNonStreamDeliveryFailureRetainsUsage(t *testing.T) {
	for _, transport := range []string{"native", "passthrough"} {
		for _, compact := range []bool{false, true} {
			for _, sse := range []bool{false, true} {
				name := transport
				if compact {
					name += "/compact"
				}
				if sse {
					name += "/sse"
				}
				t.Run(name, func(t *testing.T) {
					recorder := newOpenAIResponseFlushRecorder()
					recorder.failAfterWrites = 0
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					if compact {
						MarkOpenAICompactClientStream(c)
					}
					body := `{"id":"resp_health","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":7}}`
					contentType := "application/json"
					if sse {
						body = "data: " + `{"type":"response.completed","response":` + body + "}\n\n"
						contentType = "text/event-stream"
					}
					svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
					account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
					resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
					var usage *OpenAIUsage
					var disconnected, incomplete bool
					if transport == "native" {
						result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5", "gpt-5")
						require.NoError(t, err)
						usage, disconnected, incomplete = result.usage, result.clientDisconnect, result.upstreamIncomplete
					} else {
						result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "gpt-5", "gpt-5")
						require.NoError(t, err)
						usage, disconnected, incomplete = result.usage, result.clientDisconnect, result.upstreamIncomplete
					}
					require.True(t, disconnected)
					require.False(t, incomplete)
					require.Equal(t, 100, usage.InputTokens)
					require.Equal(t, 7, usage.OutputTokens)
				})
			}
		}
	}
}

func TestOpenAIHTTPNonStreamHealthCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, transport := range []string{"native", "passthrough"} {
		for _, tc := range []struct {
			name        string
			body        string
			contentType string
			incomplete  bool
		}{
			{name: "json_completed_without_cache_fields", contentType: "application/json", body: `{"id":"resp_health","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":7}}`},
			{name: "json_incomplete", contentType: "application/json", incomplete: true, body: `{"id":"resp_health","status":"incomplete","output":[],"usage":{"input_tokens":100,"output_tokens":7}}`},
			{name: "json_failed", contentType: "application/json", incomplete: true, body: `{"id":"resp_health","status":"failed","output":[],"usage":{"input_tokens":100,"output_tokens":7}}`},
			{name: "sse_completed", contentType: "text/event-stream", body: "data: " + `{"type":"response.completed","response":{"id":"resp_health","status":"completed","output":[],"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\ndata: [DONE]\n\n"},
			{name: "sse_incomplete", contentType: "text/event-stream", incomplete: true, body: "data: " + `{"type":"response.incomplete","response":{"id":"resp_health","status":"incomplete","output":[],"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n"},
			{name: "sse_completed_with_incomplete_status", contentType: "text/event-stream", incomplete: true, body: "data: " + `{"type":"response.completed","response":{"id":"resp_health","status":"incomplete","output":[],"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n"},
			{name: "sse_missing_terminal", contentType: "text/event-stream", incomplete: true, body: "data: " + `{"type":"response.output_text.delta","delta":"partial","response":{"usage":{"input_tokens":100,"output_tokens":7}}}` + "\n\n"},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {tc.contentType}}, Body: io.NopCloser(strings.NewReader(tc.body))}
				var usage *OpenAIUsage
				var incomplete bool
				if transport == "native" {
					result, err := svc.handleNonStreamingResponse(context.Background(), resp, c, account, "gpt-5", "gpt-5")
					require.NoError(t, err)
					require.NotNil(t, result)
					usage, incomplete = result.usage, result.upstreamIncomplete
				} else {
					result, err := svc.handleNonStreamingResponsePassthrough(context.Background(), resp, c, account, "gpt-5", "gpt-5")
					require.NoError(t, err)
					require.NotNil(t, result)
					usage, incomplete = result.usage, result.upstreamIncomplete
				}
				require.Equal(t, tc.incomplete, incomplete)
				require.NotNil(t, usage)
				require.Equal(t, 100, usage.InputTokens)
				require.Equal(t, 7, usage.OutputTokens)
			})
		}
	}
}
