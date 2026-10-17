package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTTFTGuardCompatEventsRequireVisibleOutput(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		anthropic     bool
		want          bool
	}{
		{"responses_created", `{"type":"response.created"}`, false, false},
		{"responses_ping", `{"type":"keepalive"}`, false, false},
		{"responses_empty_delta", `{"type":"response.output_text.delta","delta":""}`, false, false},
		{"responses_empty_block", `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`, false, false},
		{"responses_failed", `{"type":"response.failed","response":{"error":{"message":"failed"}}}`, false, false},
		{"responses_empty_terminal", `{"type":"response.completed","response":{"output":[]}}`, false, false},
		{"responses_delta", `{"type":"response.output_text.delta","delta":"x"}`, false, true},
		{"responses_terminal_output", `{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"x"}]}]}}`, false, true},
		{"anthropic_start", `{"type":"message_start","message":{"content":[]}}`, true, false},
		{"anthropic_ping", `{"type":"ping"}`, true, false},
		{"anthropic_empty_block", `{"type":"content_block_start","content_block":{"type":"text","text":""}}`, true, false},
		{"anthropic_empty_tool", `{"type":"content_block_start","content_block":{"type":"tool_use","input":{}}}`, true, false},
		{"anthropic_usage", `{"type":"message_delta","usage":{"output_tokens":20}}`, true, false},
		{"anthropic_empty_delta", `{"type":"content_block_delta","delta":{"type":"text_delta","text":""}}`, true, false},
		{"anthropic_text", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`, true, true},
		{"anthropic_thinking", `{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"x"}}`, true, true},
		{"anthropic_tool_input", `{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}`, true, true},
		{"anthropic_populated_block", `{"type":"content_block_start","content_block":{"type":"text","text":"x"}}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, account, group := ttftAttemptService()
			ctx := ttftAttemptContext(svc, group, "explicit:visible")
			markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
			if tc.anthropic {
				markOpenAITTFTGuardForAnthropicPayload(c, tc.payload)
			} else {
				markOpenAITTFTGuardForResponsesEvent(c, tc.payload, "")
			}
			a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
			a.mu.Lock()
			got := !a.firstTokenAt.IsZero()
			a.mu.Unlock()
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTTFTGuardCompatParsersObserveTokenBeforeTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{
		"responses_to_messages_buffered", "responses_to_cc_buffered",
		"responses_to_messages_stream", "responses_to_cc_stream",
		"anthropic_to_cc_buffered", "anthropic_to_responses_buffered",
		"anthropic_to_cc_stream", "anthropic_to_responses_stream", "anthropic_native_stream",
	} {
		t.Run(route, func(t *testing.T) {
			svc, _, account, group := ttftAttemptService()
			ctx := ttftAttemptContext(svc, group, "explicit:buffered")
			markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
			a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}
			finished := make(chan error, 1)
			go func() {
				var err error
				start := time.Now()
				switch route {
				case "responses_to_messages_buffered":
					_, err = svc.handleAnthropicBufferedStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start)
				case "responses_to_cc_buffered":
					_, err = svc.handleChatBufferedStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start)
				case "responses_to_messages_stream":
					_, err = svc.handleAnthropicStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start)
				case "responses_to_cc_stream":
					_, err = svc.handleChatStreamingResponse(resp, c, account, "gpt-test", "gpt-test", "gpt-test", start, 100)
				case "anthropic_to_cc_buffered":
					_, err = svc.handleCCBufferedFromNativeAnthropic(resp, c, "gpt-test", "gpt-test", "gpt-test", nil, start)
				case "anthropic_to_responses_buffered":
					_, err = svc.handleResponsesBufferedFromNativeAnthropic(resp, c, "gpt-test", "gpt-test", "gpt-test", nil, start, apicompat.ResponsesClientToolMapping{})
				case "anthropic_to_cc_stream":
					_, err = svc.handleCCStreamingFromNativeAnthropic(resp, c, "gpt-test", "gpt-test", "gpt-test", nil, start)
				case "anthropic_to_responses_stream":
					_, err = svc.handleResponsesStreamingFromNativeAnthropic(resp, c, "gpt-test", "gpt-test", "gpt-test", nil, start, apicompat.ResponsesClientToolMapping{})
				case "anthropic_native_stream":
					_, err = svc.handleNativeAnthropicStreamingResponse(ctx, resp, c, account, "gpt-test", "gpt-test", "gpt-test", nil, start)
				}
				finished <- err
			}()
			prefix := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ttft\",\"model\":\"gpt-test\"}}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"output_index\":0,\"content_index\":0}\n\n"
			terminal := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ttft\",\"model\":\"gpt-test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n"
			if len(route) >= 9 && route[:9] == "anthropic" {
				frame := func(payload string) string {
					var event map[string]any
					_ = json.Unmarshal([]byte(payload), &event)
					return "event: " + event["type"].(string) + "\ndata: " + payload + "\n\n"
				}
				prefix = frame(`{"type":"message_start","message":{"id":"msg_ttft","type":"message","role":"assistant","model":"gpt-test","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`) +
					frame(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
					frame(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
				terminal = frame(`{"type":"content_block_stop","index":0}`) +
					frame(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`) + frame(`{"type":"message_stop"}`)
			}
			_, err := io.WriteString(writer, prefix)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				a.mu.Lock()
				defer a.mu.Unlock()
				return !a.firstTokenAt.IsZero()
			}, time.Second, time.Millisecond, "upstream token must be recorded while terminal is withheld")
			select {
			case err := <-finished:
				t.Fatalf("parser finished before terminal: %v", err)
			default:
			}
			_, err = io.WriteString(writer, terminal)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("parser did not finish after terminal")
			}
		})
	}
}
