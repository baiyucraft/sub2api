//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	monitorSSEChatTwo   = `{"choices":[{"delta":{"content":"2"}}]}`
	monitorSSEChatStop  = `{"choices":[{"finish_reason":"stop"}]}`
	monitorSSEChatUsage = `{"choices":[],"usage":{"completion_tokens":1}}`
	monitorSSEResponse  = `{"type":"response.output_text.delta","delta":"2"}`
	monitorSSEResDone   = `{"type":"response.completed","response":{"status":"completed"}}`
	monitorSSEAnthropic = `{"type":"content_block_delta","delta":{"type":"text_delta","text":"2"}}`
	monitorSSEAnthStop  = `{"type":"message_stop"}`
	monitorSSEGemini    = `{"candidates":[{"content":{"parts":[{"text":"2"}]}}]}`
	monitorSSEGemStop   = `{"candidates":[{"finishReason":"STOP"}]}`
)

func runMonitorSSEFixture(t *testing.T, body, provider, apiMode string) (monitorStreamResult, error) {
	t.Helper()
	swapMonitorHTTPClient(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	result, status, _, err := postRawMonitorStream(context.Background(), srv.URL, []byte("{}"), nil, provider, apiMode)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	return result, err
}

func monitorSSEFrame(lines ...string) string {
	return "data: " + strings.Join(lines, "\ndata: ") + "\n\n"
}

func TestPostRawMonitorStream_CompatibleFraming(t *testing.T) {
	tests := []struct {
		name string
		body string
		text string
	}{
		{
			name: "standard frames",
			body: monitorSSEFrame(monitorSSEChatTwo) + monitorSSEFrame(monitorSSEChatStop),
			text: "2",
		},
		{
			name: "standard multiline JSON takes precedence",
			body: monitorSSEFrame(`{"choices":[`, `{"delta":{"content":"2"}}`, `]}`) + monitorSSEFrame(monitorSSEChatStop),
			text: "2",
		},
		{
			name: "missing blank event separators retains identical deltas",
			body: monitorSSEFrame(monitorSSEChatTwo, monitorSSEChatTwo, monitorSSEChatStop, monitorSSEChatUsage, "[DONE]"),
			text: "22",
		},
		{
			name: "single data line contains adjacent JSON objects",
			body: monitorSSEFrame(monitorSSEChatTwo + monitorSSEChatTwo + monitorSSEChatStop + monitorSSEChatUsage),
			text: "22",
		},
		{
			name: "multiline JSON and adjacent objects before independent done",
			body: monitorSSEFrame(`{"choices":[`, `{"delta":{"content":"2"}}`, `]}`+monitorSSEChatStop, "[DONE]", monitorSSEChatUsage, "[DONE]"),
			text: "2",
		},
		{
			name: "done marker inside JSON text is not a delimiter",
			body: monitorSSEFrame(`{"choices":[{"delta":{"content":"[DONE]"}}]}`, monitorSSEChatStop),
			text: "[DONE]",
		},
		{
			name: "last compatible frame without trailing blank line",
			body: "data: " + monitorSSEChatTwo + "\ndata: " + monitorSSEChatStop,
			text: "2",
		},
		{
			name: "CRLF comments and event names",
			body: ": heartbeat\r\n\r\nevent: message\r\ndata: " + monitorSSEChatTwo + "\r\n\r\ndata: " + monitorSSEChatStop + "\r\n\r\n",
			text: "2",
		},
		{
			name: "null error envelope remains compatible",
			body: monitorSSEFrame(`{"error":null,"choices":[{"delta":{"content":"2"}}]}`, monitorSSEChatStop),
			text: "2",
		},
		{
			name: "terminal frame does not wait for later frames",
			body: monitorSSEFrame(monitorSSEChatTwo, monitorSSEChatStop) + "data: {\n\n",
			text: "2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runMonitorSSEFixture(t, tt.body, MonitorProviderKimi, MonitorAPIModeChatCompletions)
			if err != nil {
				t.Fatalf("postRawMonitorStream: %v", err)
			}
			if !result.Completed || result.Text != tt.text || result.TTFTMs == nil || *result.TTFTMs < 1 || result.FinishReason != "stop" {
				t.Fatalf("result = %+v, want completed text %q and first finish reason stop", result, tt.text)
			}
		})
	}
}

func TestPostRawMonitorStream_CompatibleProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		mode     string
		text     string
		terminal string
	}{
		{"OpenAI Chat", MonitorProviderOpenAI, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"Kimi Chat", MonitorProviderKimi, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"DeepSeek Chat", MonitorProviderDeepseek, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"Grok Chat", MonitorProviderGrok, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"Zhipu Chat", MonitorProviderZhipu, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"MiniMax Chat", MonitorProviderMiniMax, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"OpenCode Go Chat", MonitorProviderOpenCodeGo, MonitorAPIModeChatCompletions, monitorSSEChatTwo, monitorSSEChatStop},
		{"OpenAI Responses", MonitorProviderOpenAI, MonitorAPIModeResponses, monitorSSEResponse, monitorSSEResDone},
		{"Zhipu Responses", MonitorProviderZhipu, MonitorAPIModeResponses, monitorSSEResponse, monitorSSEResDone},
		{"Anthropic", MonitorProviderAnthropic, "", monitorSSEAnthropic, monitorSSEAnthStop},
		{"Gemini", MonitorProviderGemini, "", monitorSSEGemini, monitorSSEGemStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runMonitorSSEFixture(t, monitorSSEFrame(tt.text, tt.terminal, `{"usage":{"output_tokens":1}}`, "[DONE]"), tt.provider, tt.mode)
			if err != nil {
				t.Fatalf("postRawMonitorStream: %v", err)
			}
			if !result.Completed || result.Text != "2" || result.TTFTMs == nil {
				t.Fatalf("result = %+v, want completed text 2 with TTFT", result)
			}
		})
	}
}

func TestPostRawMonitorStream_CompatibleFrameRejectsInvalidTail(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		mode     string
		lines    []string
		wantErr  string
	}{
		{"terminal then malformed JSON", MonitorProviderKimi, "", []string{monitorSSEChatTwo, monitorSSEChatStop, `{"broken":`}, "decode monitor chat event"},
		{"done then malformed JSON", MonitorProviderKimi, "", []string{monitorSSEChatTwo, "[DONE]", `{"broken":`}, "decode monitor chat event"},
		{"adjacent JSON has malformed tail", MonitorProviderKimi, "", []string{monitorSSEChatTwo + monitorSSEChatStop + `{"broken":`}, "decode monitor chat event"},
		{"terminal then text", MonitorProviderKimi, "", []string{monitorSSEChatTwo, monitorSSEChatStop, monitorSSEChatTwo}, "text after completion"},
		{"done then text", MonitorProviderKimi, "", []string{monitorSSEChatTwo, "[DONE]", monitorSSEChatTwo}, "text after completion"},
		{"terminal then chat error", MonitorProviderKimi, "", []string{monitorSSEChatTwo, monitorSSEChatStop, `{"error":{"message":"sk-do-not-print"}}`}, "monitor chat stream reported error"},
		{"done then chat error", MonitorProviderKimi, "", []string{monitorSSEChatTwo, "[DONE]", `{"error":"sk-do-not-print"}`}, "monitor chat stream reported error"},
		{"responses terminal then failed", MonitorProviderOpenAI, MonitorAPIModeResponses, []string{monitorSSEResponse, monitorSSEResDone, `{"type":"response.failed","response":{"error":{"message":"sk-do-not-print"}}}`}, "reported response.failed"},
		{"responses terminal then error", MonitorProviderOpenAI, MonitorAPIModeResponses, []string{monitorSSEResponse, monitorSSEResDone, `{"type":"error","message":"sk-do-not-print"}`}, "monitor responses stream reported error"},
		{"responses terminal then text", MonitorProviderOpenAI, MonitorAPIModeResponses, []string{monitorSSEResponse, monitorSSEResDone, monitorSSEResponse}, "text after completion"},
		{"Anthropic terminal then error", MonitorProviderAnthropic, "", []string{monitorSSEAnthropic, monitorSSEAnthStop, `{"type":"error","error":{"message":"sk-do-not-print"}}`}, "monitor Anthropic stream reported error"},
		{"Anthropic standard terminal includes error", MonitorProviderAnthropic, "", []string{`{"type":"message_stop","error":{"message":"sk-do-not-print"}}`}, "monitor Anthropic stream reported error"},
		{"Anthropic terminal then terminal includes error", MonitorProviderAnthropic, "", []string{monitorSSEAnthropic, monitorSSEAnthStop, `{"type":"message_stop","error":{"message":"sk-do-not-print"}}`}, "monitor Anthropic stream reported error"},
		{"Anthropic terminal then text", MonitorProviderAnthropic, "", []string{monitorSSEAnthropic, monitorSSEAnthStop, monitorSSEAnthropic}, "text after completion"},
		{"Gemini terminal then error", MonitorProviderGemini, "", []string{monitorSSEGemini, monitorSSEGemStop, `{"error":{"message":"sk-do-not-print"}}`}, "monitor Gemini stream reported error"},
		{"Gemini terminal then text", MonitorProviderGemini, "", []string{monitorSSEGemini, monitorSSEGemStop, monitorSSEGemini}, "text after completion"},
		{"scalar after valid object", MonitorProviderKimi, "", []string{monitorSSEChatTwo, `null`, monitorSSEChatStop}, "payload must be a JSON object"},
		{"array after valid object", MonitorProviderKimi, "", []string{monitorSSEChatTwo, `[]`, monitorSSEChatStop}, "payload must be a JSON object"},
		{"partial JSON before independent done", MonitorProviderKimi, "", []string{`{"choices":`, "[DONE]"}, "decode monitor chat event"},
		{"marker embedded in non-JSON data is not repaired", MonitorProviderKimi, "", []string{monitorSSEChatTwo + "[DONE]"}, "decode monitor chat event"},
		{"valid JSON invalid protocol shape is not retried", MonitorProviderKimi, "", []string{`{"choices":[{"delta":{"content":[]}}]}`}, "decode monitor chat event"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runMonitorSSEFixture(t, monitorSSEFrame(tt.lines...), tt.provider, tt.mode)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || strings.Contains(err.Error(), "sk-do-not-print") {
				t.Fatalf("error = %v, want safe error containing %q", err, tt.wantErr)
			}
			if result.Completed || result.Text != "" || result.TTFTMs != nil || result.FinishReason != "" {
				t.Fatalf("failed frame partially committed: %+v", result)
			}
		})
	}
}

func TestPostRawMonitorStream_RejectsNonObjectEvents(t *testing.T) {
	for _, payload := range []string{"null", "[]", "42", `"2"`, "true"} {
		t.Run(payload, func(t *testing.T) {
			result, err := runMonitorSSEFixture(t, monitorSSEFrame(payload), MonitorProviderKimi, "")
			if err == nil || !strings.Contains(err.Error(), "payload must be a JSON object") {
				t.Fatalf("error = %v, want non-object event rejection", err)
			}
			if result.Completed || result.Text != "" || result.TTFTMs != nil {
				t.Fatalf("non-object event committed: %+v", result)
			}
		})
	}
}

func TestPostRawMonitorStream_PreservesCommittedFrameOnLaterFailure(t *testing.T) {
	body := monitorSSEFrame(monitorSSEChatTwo) + monitorSSEFrame(monitorSSEChatTwo, monitorSSEChatStop, `{"broken":`)
	result, err := runMonitorSSEFixture(t, body, MonitorProviderDeepseek, "")
	if err == nil || !strings.Contains(err.Error(), "decode monitor chat event") {
		t.Fatalf("error = %v, want malformed tail error", err)
	}
	if result.Text != "2" || result.TTFTMs == nil || result.Completed || result.FinishReason != "" {
		t.Fatalf("result = %+v, want only earlier frame text and timing", result)
	}
}

func TestParseMonitorSSEFrame_PreservesFirstTimingAndTerminalReason(t *testing.T) {
	ttft := int64(17)
	previous := monitorStreamResult{TTFTMs: &ttft, Text: "earlier"}
	result, err := parseMonitorSSEFrame([]string{
		monitorSSEChatTwo, monitorSSEChatTwo, monitorSSEChatStop, monitorSSEChatUsage,
		`{"choices":[{"finish_reason":"length"}]}`, "[DONE]",
	}, MonitorProviderKimi, "", time.Now().Add(-time.Second), previous)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "22" || result.TTFTMs != previous.TTFTMs || *result.TTFTMs != 17 || result.FinishReason != "stop" || !result.Completed {
		t.Fatalf("result = %+v, want exact frame text, original TTFT and first reason", result)
	}
	if previous.Text != "earlier" || previous.Completed || previous.FinishReason != "" || ttft != 17 {
		t.Fatalf("previous state mutated: %+v", previous)
	}
}

func TestPostRawMonitorStream_CompatibleFramingPreservesStreamLimit(t *testing.T) {
	body := monitorSSEFrame(`{"padding":"`+strings.Repeat("x", monitorStreamMaxBytes)+`"}`, monitorSSEChatTwo, monitorSSEChatStop)
	result, err := runMonitorSSEFixture(t, body, MonitorProviderKimi, "")
	if err == nil || !strings.Contains(err.Error(), "monitor SSE response exceeded") {
		t.Fatalf("error = %v, want bounded response error", err)
	}
	if result.Completed || result.Text != "" || result.TTFTMs != nil {
		t.Fatalf("oversized frame committed: %+v", result)
	}
}

func TestPostRawMonitorStream_CompatibleFrameStillRequiresTerminal(t *testing.T) {
	result, err := runMonitorSSEFixture(t, monitorSSEFrame(monitorSSEChatTwo, monitorSSEChatTwo), MonitorProviderKimi, "")
	if err == nil || !strings.Contains(err.Error(), "monitor stream ended before completion") {
		t.Fatalf("error = %v, want missing terminal", err)
	}
	if result.Completed || result.Text != "22" || result.TTFTMs == nil {
		t.Fatalf("result = %+v, want observed text without successful completion", result)
	}
}

func TestPostRawMonitorStream_StopsReadingAfterCompleteFrame(t *testing.T) {
	swapMonitorHTTPClient(t)
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, monitorSSEFrame(monitorSSEChatTwo, monitorSSEChatStop))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(srv.Close)
	result, _, _, err := postRawMonitorStream(context.Background(), srv.URL, []byte("{}"), nil, MonitorProviderKimi, "")
	if err != nil || !result.Completed || result.Text != "2" {
		t.Fatalf("result = %+v, error = %v", result, err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("completed probe left the upstream response open")
	}
}
