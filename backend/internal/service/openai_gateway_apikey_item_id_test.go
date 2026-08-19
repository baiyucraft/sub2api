//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidInputItemIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true}

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"input":[
			{"type":"message","id":"item_bad_message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
			{"type":"function_call","id":"item_bad_call","call_id":"call_123","name":"exec_command","arguments":"{}"},
			{"type":"message","id":"msg_valid","role":"user","content":[{"type":"input_text","text":"continue"}]},
			{"type":"function_call","id":"fc_valid","call_id":"call_456","name":"apply_patch","arguments":"{}"},
			{"type":"custom_tool_call","id":"fc_wrong_custom","call_id":"call_custom_wrong","name":"exec","input":"dir"},
			{"type":"custom_tool_call","id":"ctc_valid","call_id":"call_custom_valid","name":"apply_patch","input":"*** Begin Patch"},
			{"type":"function_call_output","id":"item_output","call_id":"call_123","output":"done"},
			{"type":"custom_tool_call_output","id":"item_custom_output","call_id":"call_custom_wrong","output":"ok"},
			{"type":"web_search_call","id":"item_unconstrained"}
		]
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	forwarded := upstream.lastBody
	require.False(t, gjson.GetBytes(forwarded, "input.0.id").Exists())
	require.Equal(t, "hello", gjson.GetBytes(forwarded, "input.0.content.0.text").String())
	require.False(t, gjson.GetBytes(forwarded, "input.1.id").Exists())
	require.Equal(t, "call_123", gjson.GetBytes(forwarded, "input.1.call_id").String())
	require.Equal(t, "exec_command", gjson.GetBytes(forwarded, "input.1.name").String())
	require.Equal(t, "{}", gjson.GetBytes(forwarded, "input.1.arguments").String())
	require.Equal(t, "msg_valid", gjson.GetBytes(forwarded, "input.2.id").String())
	require.Equal(t, "fc_valid", gjson.GetBytes(forwarded, "input.3.id").String())
	require.False(t, gjson.GetBytes(forwarded, "input.4.id").Exists())
	require.Equal(t, "call_custom_wrong", gjson.GetBytes(forwarded, "input.4.call_id").String())
	require.Equal(t, "exec", gjson.GetBytes(forwarded, "input.4.name").String())
	require.Equal(t, "dir", gjson.GetBytes(forwarded, "input.4.input").String())
	require.Equal(t, "ctc_valid", gjson.GetBytes(forwarded, "input.5.id").String())
	require.Equal(t, "call_custom_valid", gjson.GetBytes(forwarded, "input.5.call_id").String())
	require.Equal(t, "apply_patch", gjson.GetBytes(forwarded, "input.5.name").String())
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(forwarded, "input.5.input").String())
	require.Equal(t, "item_output", gjson.GetBytes(forwarded, "input.6.id").String())
	require.Equal(t, "call_123", gjson.GetBytes(forwarded, "input.6.call_id").String())
	require.Equal(t, "item_custom_output", gjson.GetBytes(forwarded, "input.7.id").String())
	require.Equal(t, "call_custom_wrong", gjson.GetBytes(forwarded, "input.7.call_id").String())
	require.Equal(t, "item_unconstrained", gjson.GetBytes(forwarded, "input.8.id").String())
}

func TestOpenAIGatewayService_APIKeyNonPassthrough_StripsInvalidCustomToolCallID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"input":[
			{"type":"custom_tool_call","id":"fc_incompatible","call_id":"call_90851","name":"exec","input":"dir"},
			{"type":"custom_tool_call","id":"ctc_compatible","call_id":"call_90850","name":"apply_patch","input":"*** Begin Patch"}
		]
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	forwarded := upstream.lastBody
	require.False(t, gjson.GetBytes(forwarded, "input.0.id").Exists())
	require.Equal(t, "call_90851", gjson.GetBytes(forwarded, "input.0.call_id").String())
	require.Equal(t, "exec", gjson.GetBytes(forwarded, "input.0.name").String())
	require.Equal(t, "dir", gjson.GetBytes(forwarded, "input.0.input").String())
	require.Equal(t, "ctc_compatible", gjson.GetBytes(forwarded, "input.1.id").String())
	require.Equal(t, "call_90850", gjson.GetBytes(forwarded, "input.1.call_id").String())
	require.Equal(t, "apply_patch", gjson.GetBytes(forwarded, "input.1.name").String())
	require.Equal(t, "*** Begin Patch", gjson.GetBytes(forwarded, "input.1.input").String())
}

// TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidReasoningItemIDs
// verifies that reasoning items with a non-rs id (e.g. item_*) are stripped
// before forwarding. OpenAI upstream requires reasoning ids to begin with
// "rs" and rejects item_* with 400:
// "Expected an ID that begins with 'rs'." (#5410)
func TestOpenAIGatewayService_APIKeyPassthrough_StripsInvalidReasoningItemIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_test","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		)),
	}}
	svc := newOpenAIImageGenerationControlTestService(upstream)
	c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
	account := newOpenAIImageGenerationControlTestAccount()
	account.Extra = map[string]any{"openai_passthrough": true}

	body := []byte(`{
		"model":"gpt-5.6-sol",
		"stream":false,
		"input":[
			{"type":"reasoning","id":"item_bad_reasoning","summary":[]},
			{"type":"reasoning","id":"rs_valid","summary":[]},
			{"type":"message","id":"msg_valid","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	forwarded := upstream.lastBody
	require.False(t, gjson.GetBytes(forwarded, "input.0.id").Exists(),
		"item_* id should be stripped from reasoning")
	require.Equal(t, "rs_valid", gjson.GetBytes(forwarded, "input.1.id").String(),
		"valid rs* id must be preserved")
	require.Equal(t, "msg_valid", gjson.GetBytes(forwarded, "input.2.id").String())
}

func TestShouldStripOpenAIResponsesInputItemID_Reasoning(t *testing.T) {
	cases := []struct {
		name     string
		itemType string
		id       string
		want     bool
	}{
		{"reasoning item_* id", "reasoning", "item_bad_reasoning", true},
		{"reasoning rs id", "reasoning", "rs_abc123", false},
		{"reasoning empty id", "reasoning", "", false},
		{"message msg id", "message", "msg_abc", false},
		{"message item id", "message", "item_x", true},
		{"function_call fc id", "function_call", "fc_abc", false},
		{"function_call fc without underscore", "function_call", "fcabc", true},
		{"function_call item id", "function_call", "item_x", true},
		{"custom_tool_call ctc id", "custom_tool_call", "ctc_abc", false},
		{"custom_tool_call fc id", "custom_tool_call", "fc_abc", true},
		{"custom_tool_call item id", "custom_tool_call", "item_x", true},
		{"unconstrained type", "web_search_call", "ws_001", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, shouldStripOpenAIResponsesInputItemID(tc.itemType, tc.id))
		})
	}
}

func TestSanitizeOpenAIResponsesInputItemIDs_AllocationGrowthIsLinear(t *testing.T) {
	makeBody := func(itemCount int) []byte {
		items := make([]string, itemCount)
		for i := range items {
			items[i] = fmt.Sprintf(`{"type":"message","id":"item_%d","role":"user","content":[{"type":"input_text","text":"hello"}]}`, i)
		}
		return []byte(`{"model":"gpt-5.6-sol","input":[` + strings.Join(items, ",") + `]}`)
	}
	allocatedBytes := func(body []byte) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		sanitized, changed, err := sanitizeOpenAIResponsesInputItemIDs(body)
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		require.True(t, changed)
		require.NotEmpty(t, sanitized)
		return after.TotalAlloc - before.TotalAlloc
	}

	smallAllocated := allocatedBytes(makeBody(20))
	largeAllocated := allocatedBytes(makeBody(200))
	require.Less(t, largeAllocated, smallAllocated*30,
		"10x more input items must not cause quadratic whole-body allocation growth")
}
