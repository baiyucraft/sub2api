package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Codex local compaction replays hosted web_search_call history with tools:[];
// ChatGPT then fails with "response protection is unavailable" (#7927).
const openAIWebSearchHistoryCompactionBody = `{"model":"gpt-5.5","instructions":"x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look it up"}]},{"type":"web_search_call","id":"ws_123","status":"completed","action":{"type":"search","query":"kwin inputmethod"}},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"found"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"summarize"}]}],"tools":[],"tool_choice":"auto","parallel_tool_calls":false,"stream":true,"store":false}`

func TestOAuthWebSearchHistoryToolAcrossPaths(t *testing.T) {
	tests := []struct {
		name      string
		normalize func([]byte) ([]byte, bool, error)
		inject    bool
	}{
		{"transformed OAuth", func(b []byte) ([]byte, bool, error) {
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				return nil, false, err
			}
			result := applyCodexOAuthTransform(req, true, false)
			out, err := json.Marshal(req)
			return out, result.Modified, err
		}, true},
		{"transformed OAuth compact", func(b []byte) ([]byte, bool, error) {
			var req map[string]any
			if err := json.Unmarshal(b, &req); err != nil {
				return nil, false, err
			}
			result := applyCodexOAuthTransform(req, true, true)
			out, err := json.Marshal(req)
			return out, result.Modified, err
		}, false},
		{"OAuth passthrough", func(b []byte) ([]byte, bool, error) { return normalizeOpenAIPassthroughOAuthBody(b, false) }, true},
		{"OAuth passthrough compact", func(b []byte) ([]byte, bool, error) { return normalizeOpenAIPassthroughOAuthBody(b, true) }, false},
		{"OAuth websocket", func(b []byte) ([]byte, bool, error) {
			return normalizeOpenAIResponsesWebSocketCompatibilityBody(b, &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, false)
		}, true},
		{"API key websocket", func(b []byte) ([]byte, bool, error) {
			return normalizeOpenAIResponsesWebSocketCompatibilityBody(b, &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, false)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := tt.normalize([]byte(openAIWebSearchHistoryCompactionBody))
			require.NoError(t, err)
			require.Equal(t, "web_search_call", gjson.GetBytes(out, "input.1.type").String())
			if !tt.inject {
				require.False(t, gjsonToolsContainWebSearch(gjson.GetBytes(out, "tools")))
				return
			}
			tools := gjson.GetBytes(out, "tools").Array()
			require.Len(t, tools, 1)
			require.Equal(t, "web_search", tools[0].Get("type").String())
			require.False(t, tools[0].Get("external_web_access").Bool())
			require.True(t, tools[0].Get("external_web_access").Exists())
			require.Equal(t, "none", gjson.GetBytes(out, "tool_choice").String())
		})
	}
}

func TestEnsureOpenAIOAuthWebSearchToolForHistoryBody(t *testing.T) {
	const history = `[{"type":"message","role":"user","content":[{"type":"input_text","text":"q"}]},{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}]`
	tests := []struct {
		name       string
		body       string
		changed    bool
		toolCount  int
		toolChoice string
	}{
		{"tools absent", `{"input":` + history + `}`, true, 1, "none"},
		{"tools null", `{"input":` + history + `,"tools":null,"tool_choice":null}`, true, 1, "none"},
		{"explicit none kept", `{"input":` + history + `,"tools":[],"tool_choice":"none"}`, true, 1, "none"},
		{"required not rewritten", `{"input":` + history + `,"tools":[],"tool_choice":"required"}`, true, 1, "required"},
		{"caller tools keep choice", `{"input":` + history + `,"tools":[{"type":"function","name":"echo"}],"tool_choice":"auto"}`, true, 2, "auto"},
		{"web_search already declared", `{"input":` + history + `,"tools":[{"type":"web_search"}]}`, false, 1, ""},
		{"web_search_preview declared", `{"input":` + history + `,"tools":[{"type":"web_search_preview"}]}`, false, 1, ""},
		{"declared via additional_tools", `{"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]},{"type":"web_search_call","id":"ws_1","status":"completed"}]}`, false, 0, ""},
		{"no web_search_call item", `{"input":[{"type":"message","role":"user","content":"web_search_call"}],"tools":[]}`, false, 0, ""},
		{"string input", `{"input":"web_search_call"}`, false, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, changed, err := ensureOpenAIOAuthWebSearchToolForHistoryBody([]byte(tt.body))
			require.NoError(t, err)
			require.Equal(t, tt.changed, changed)
			if !tt.changed {
				require.JSONEq(t, tt.body, string(out))
				return
			}
			tools := gjson.GetBytes(out, "tools").Array()
			require.Len(t, tools, tt.toolCount)
			require.Equal(t, "web_search", tools[len(tools)-1].Get("type").String())
			require.Equal(t, tt.toolChoice, gjson.GetBytes(out, "tool_choice").String())

			// The map variant used by the Codex transform must agree.
			var req map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.body), &req))
			require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(req))
			mapOut, err := json.Marshal(req)
			require.NoError(t, err)
			require.JSONEq(t, string(out), string(mapOut))
		})
	}
}

func TestEnsureOpenAIOAuthWebSearchToolForHistoryDoesNotShareToolMap(t *testing.T) {
	first := map[string]any{"input": []any{map[string]any{"type": "web_search_call"}}}
	second := map[string]any{"input": []any{map[string]any{"type": "web_search_call"}}}
	require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(first))
	require.True(t, ensureOpenAIOAuthWebSearchToolForHistory(second))
	first["tools"].([]any)[0].(map[string]any)["external_web_access"] = true
	require.Equal(t, false, second["tools"].([]any)[0].(map[string]any)["external_web_access"])
	require.Equal(t, false, openAIWebSearchHistoryTool["external_web_access"])
}
