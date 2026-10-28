package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiChatForwardKeepsCacheEvidenceAndImageBillingIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		model    string
		stream   bool
		terminal bool
		image    bool
	}{
		{"text_json", "gemini-2.5-flash", false, true, false},
		{"image_json", "gemini-3-pro-image", false, true, true},
		{"text_stream", "gemini-2.5-flash", true, true, false},
		{"image_stream", "gemini-3-pro-image", true, true, true},
		{"missing_terminal_is_not_warm", "gemini-2.5-flash", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finish := `,"finishReason":"STOP"`
			if !tc.terminal {
				finish = ""
			}
			upstreamBody := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}` + finish + `}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":90,"candidatesTokenCount":7}}`
			contentType := "application/json"
			if tc.stream {
				upstreamBody = "data: " + upstreamBody + "\n\n"
				contentType = "text/event-stream"
			}
			upstream := &geminiCompatHTTPUpstreamStub{response: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{contentType}},
				Body:       io.NopCloser(strings.NewReader(upstreamBody)),
			}}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			account := &Account{ID: 1, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
			body := []byte(fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"user","content":"hello"}]}`, tc.model, tc.stream))
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
			result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls, "the test must use only the offline upstream stub")
			require.Equal(t, tc.image, result.ImageEndpoint)
			require.Equal(t, tc.image, result.ImageCount > 0)
			require.NotNil(t, result.CacheUsageEvidence)
			require.Same(t, result.Usage.CacheUsageEvidence, result.CacheUsageEvidence)
			require.Equal(t, upstreamcacheusage.Known, result.CacheUsageEvidence.Status)
			require.EqualValues(t, 100, result.CacheUsageEvidence.TotalInputTokens)
			require.EqualValues(t, 90, result.CacheUsageEvidence.CacheReadTokens)
			require.Equal(t, tc.stream && !tc.terminal, result.CacheUsageEvidence.StreamIncomplete)
			require.Equal(t, 10, result.Usage.InputTokens, "billing normalization must not mutate raw input facts")
			require.Equal(t, 90, result.Usage.CacheReadInputTokens)
			if tc.stream {
				require.NotNil(t, result.FirstTokenMs)
			}
		})
	}
}

func TestGeminiResponseToChatCompletionsPreservesInlineData(t *testing.T) {
	tests := []struct {
		name  string
		parts []any
		want  string
	}{
		{
			name: "image only",
			parts: []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aW1hZ2U="}},
			},
			want: "![image](data:image/png;base64,aW1hZ2U=)",
		},
		{
			name: "text and image",
			parts: []any{
				map[string]any{"text": "rendered image:\n"},
				map[string]any{"inlineData": map[string]any{"mimeType": "image/webp", "data": "d2VicA=="}},
			},
			want: "rendered image:\n![image](data:image/webp;base64,d2VicA==)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geminiResp := map[string]any{
				"candidates": []any{map[string]any{
					"content":      map[string]any{"parts": tt.parts},
					"finishReason": "STOP",
				}},
			}
			rawData, err := json.Marshal(geminiResp)
			require.NoError(t, err)

			got, _, err := geminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
			require.NoError(t, err)
			require.Len(t, got.Choices, 1)

			var content string
			require.NoError(t, json.Unmarshal(got.Choices[0].Message.Content, &content))
			require.Equal(t, tt.want, content)
			require.Equal(t, "stop", got.Choices[0].FinishReason)
		})
	}
}

func TestGeminiResponseToChatCompletionsOmitsInvalidInlineData(t *testing.T) {
	tests := []struct {
		name       string
		inlineData map[string]any
	}{
		{
			name:       "unsupported MIME type",
			inlineData: map[string]any{"mimeType": "image/svg+xml", "data": "PHN2Zz48L3N2Zz4="},
		},
		{
			name:       "malformed MIME type",
			inlineData: map[string]any{"mimeType": "image/png; charset=utf-8", "data": "aW1hZ2U="},
		},
		{
			name:       "malformed base64",
			inlineData: map[string]any{"mimeType": "image/png", "data": "not-valid-base64!!!"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			geminiResp := map[string]any{
				"candidates": []any{map[string]any{
					"content":      map[string]any{"parts": []any{map[string]any{"text": "before"}, map[string]any{"inlineData": tt.inlineData}, map[string]any{"text": "after"}}},
					"finishReason": "STOP",
				}},
			}
			rawData, err := json.Marshal(geminiResp)
			require.NoError(t, err)

			got, _, err := geminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
			require.NoError(t, err)

			var content string
			require.NoError(t, json.Unmarshal(got.Choices[0].Message.Content, &content))
			require.Equal(t, "beforeafter", content)
		})
	}
}

func TestConvertGeminiToClaudeMessageOmitsInlineDataForAnthropicMessages(t *testing.T) {
	geminiResp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "before"},
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aW1hZ2U="}},
				map[string]any{"functionCall": map[string]any{"name": "get_weather", "args": map[string]any{"city": "Paris"}}},
				map[string]any{"text": "after"},
			}},
			"finishReason": "STOP",
		}},
	}
	rawData, err := json.Marshal(geminiResp)
	require.NoError(t, err)

	withInlineData, _ := convertGeminiToClaudeMessage(geminiResp, "gemini-test", rawData, true)
	contentWithInlineData, ok := withInlineData["content"].([]any)
	require.True(t, ok)
	require.Len(t, contentWithInlineData, 4)
	require.Equal(t, map[string]any{"type": "text", "text": "before"}, contentWithInlineData[0])
	require.Equal(t, map[string]any{"type": "text", "text": "![image](data:image/png;base64,aW1hZ2U=)"}, contentWithInlineData[1])
	toolUse, ok := contentWithInlineData[2].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool_use", toolUse["type"])
	require.Equal(t, "get_weather", toolUse["name"])
	require.Equal(t, map[string]any{"type": "text", "text": "after"}, contentWithInlineData[3])

	withoutInlineData, _ := convertGeminiToClaudeMessage(geminiResp, "gemini-test", rawData, false)
	contentWithoutInlineData, ok := withoutInlineData["content"].([]any)
	require.True(t, ok)
	require.Len(t, contentWithoutInlineData, 3)
	require.Equal(t, map[string]any{"type": "text", "text": "before"}, contentWithoutInlineData[0])
	toolUseWithoutInlineData, ok := contentWithoutInlineData[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "tool_use", toolUseWithoutInlineData["type"])
	require.Equal(t, "get_weather", toolUseWithoutInlineData["name"])
	require.Equal(t, map[string]any{"type": "text", "text": "after"}, contentWithoutInlineData[2])
}

func TestGeminiResponseToChatCompletionsRetainsTextAndToolBehavior(t *testing.T) {
	geminiResp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"parts": []any{
				map[string]any{"text": "checking"},
				map[string]any{"functionCall": map[string]any{
					"name": "get_weather",
					"args": map[string]any{"city": "Paris"},
				}},
			}},
			"finishReason": "STOP",
		}},
	}
	rawData, err := json.Marshal(geminiResp)
	require.NoError(t, err)

	got, _, err := geminiResponseToChatCompletions(geminiResp, "gemini-test", rawData, nil)
	require.NoError(t, err)
	require.Len(t, got.Choices, 1)

	choice := got.Choices[0]
	var content string
	require.NoError(t, json.Unmarshal(choice.Message.Content, &content))
	require.Equal(t, "checking", content)
	require.Equal(t, "tool_calls", choice.FinishReason)
	require.Len(t, choice.Message.ToolCalls, 1)
	require.Equal(t, "get_weather", choice.Message.ToolCalls[0].Function.Name)
	require.JSONEq(t, `{"city":"Paris"}`, choice.Message.ToolCalls[0].Function.Arguments)
}
