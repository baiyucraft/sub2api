package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service/modeltrace_numeric"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRunUpstreamHealthReferenceProbePayloads(t *testing.T) {
	cases := []struct {
		name        string
		platform    string
		protocol    string
		apiProtocol string
		chat        bool
		promptPath  string
		capPath     string
		response    string
	}{
		{"openai responses", PlatformOpenAI, upstreamHealthProbeProtocolOpenAI, "", false, "input", "max_output_tokens", `{"status":"completed","output_text":"reference response"}`},
		{"openai chat", PlatformOpenAI, upstreamHealthProbeProtocolOpenAIChat, "", true, "messages.0.content", "max_tokens", `{"choices":[{"message":{"content":"reference response"},"finish_reason":"stop"}]}`},
		{"anthropic", PlatformAnthropic, upstreamHealthProbeProtocolAnthropic, "", false, "messages.0.content.0.text", "max_tokens", `{"content":[{"type":"text","text":"reference response"}],"stop_reason":"end_turn"}`},
		{"gemini", PlatformGemini, upstreamHealthProbeProtocolGemini, "", false, "contents.0.parts.0.text", "generationConfig.maxOutputTokens", `{"candidates":[{"content":{"parts":[{"text":"reference response"}]},"finishReason":"STOP"}]}`},
		{"deepseek responses", PlatformDeepseek, upstreamHealthProbeProtocolOpenAI, APIProtocolResponses, false, "input", "max_output_tokens", `{"status":"completed","output_text":"reference response"}`},
		{"zhipu chat", PlatformZhipu, upstreamHealthProbeProtocolOpenAIChat, "", false, "messages.0.content", "max_tokens", `{"choices":[{"message":{"content":"reference response"},"finish_reason":"stop"}]}`},
		{"grok", PlatformGrok, upstreamHealthProbeProtocolGrok, "", false, "input", "max_output_tokens", `{"status":"completed","output_text":"reference response"}`},
	}
	methods := map[string][]string{
		"chanshui_behavioral": ChanshuiBehavioralPrompts(),
		"modeltrace_numeric":  modeltrace_numeric.ChallengePrompts(),
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for method, prompts := range methods {
				t.Run(method, func(t *testing.T) {
					for index, prompt := range prompts {
						account := &Account{ID: 1, Platform: tc.platform, Type: AccountTypeAPIKey, Concurrency: 1,
							ProbeMinInputTokens: 1000,
							Credentials:         map[string]any{"api_key": "test-key", "base_url": "https://probe.example/v1", "model_mapping": map[string]any{"probe-alias": "probe-model"}, "api_protocol": tc.apiProtocol},
						}
						if tc.chat {
							account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
						}
						upstream := &upstreamHealthProbeHTTPStub{stream: tc.response}
						svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
						attempt := &ConfidenceFingerprintAttempt{Method: method, MethodSequence: int64(index + 1)}
						ctx := context.WithValue(context.Background(), confidenceFingerprintAttemptContextKey{}, attempt)
						challenge, err := newUpstreamHealthChallengeForContext(ctx, account)
						require.NoError(t, err)
						require.Equal(t, method, challenge.ReferenceMethod)
						require.Equal(t, index, challenge.ReferenceIndex)
						require.Equal(t, prompt, challenge.Prompt, "reference challenges must not include account context padding")
						result, err := svc.RunUpstreamHealthProbe(ctx, account, "probe-alias")
						require.NoError(t, err)
						require.Equal(t, "success", result.Result)
						require.Equal(t, tc.protocol, result.Protocol)
						require.Equal(t, method, result.fingerprintMethod)
						require.Equal(t, "reference response", result.probeResponseText)
						require.Equal(t, "probe-model", result.Model)
						require.Equal(t, prompt, gjson.GetBytes(upstream.body, tc.promptPath).String())
						wantMaxTokens := upstreamReferenceProbeMaxTokens
						if method == "modeltrace_numeric" {
							wantMaxTokens = upstreamNumericReferenceProbeMaxTokens
						}
						require.Equal(t, int64(wantMaxTokens), gjson.GetBytes(upstream.body, tc.capPath).Int())
						require.False(t, gjson.GetBytes(upstream.body, "instructions").Exists())
						require.False(t, gjson.GetBytes(upstream.body, "system").Exists())
						require.False(t, gjson.GetBytes(upstream.body, "temperature").Exists())
						require.Len(t, upstream.requests, 1, "a reference challenge must reuse one health-probe request")
					}
				})
			}
		})
	}
}

func TestUpstreamHealthReferenceProbeRejectsEmptyAndFailedResponses(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		parser   upstreamHealthStreamParser
		stream   string
	}{
		{"responses terminal only", upstreamHealthProbeProtocolOpenAI, parseOpenAIUpstreamHealthStream, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"},
		{"chat terminal only", upstreamHealthProbeProtocolOpenAIChat, parseOpenAIChatCompletionsUpstreamHealthStream, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"},
		{"anthropic terminal only", upstreamHealthProbeProtocolAnthropic, parseAnthropicUpstreamHealthStream, "data: {\"type\":\"message_stop\"}\n\n"},
		{"gemini terminal only", upstreamHealthProbeProtocolGemini, parseGeminiUpstreamHealthStream, "data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n"},
		{"responses whitespace", upstreamHealthProbeProtocolOpenAI, parseOpenAIUpstreamHealthStream, `{"status":"completed","output_text":"  "}`},
		{"responses error after text", upstreamHealthProbeProtocolOpenAI, parseOpenAIUpstreamHealthStream, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\"}\n\n"},
	}
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		t.Run(method, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					upstream := &upstreamHealthProbeHTTPStub{stream: tc.stream}
					svc := &AccountTestService{httpUpstream: upstream}
					req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://probe.example/v1/responses", http.NoBody)
					require.NoError(t, err)
					result, err := svc.executeUpstreamHealthProbeWithValidator(req, &Account{ID: 1}, UpstreamHealthProbeResult{Protocol: tc.protocol, fingerprintMethod: method}, func(string) bool { return true }, tc.parser)
					require.Error(t, err)
					require.NotEqual(t, "success", result.Result)
					require.Empty(t, result.probeResponseText)
					require.Len(t, upstream.requests, 1)
				})
			}
		})
	}
}

func TestPadUpstreamHealthReferenceChallengePreservesExactPrompt(t *testing.T) {
	account := &Account{ProbeMinInputTokens: 1000}
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		challenge := upstreamHealthChallenge{Prompt: "fixed prompt", LegacyPrompt: "fixed prompt", ReferenceMethod: method, ReferenceIndex: 1, MaxOutputTokens: upstreamReferenceProbeMaxTokens}
		require.Equal(t, challenge, padUpstreamHealthChallenge(account, challenge))
	}
	ordinary := padUpstreamHealthChallenge(account, upstreamHealthChallenge{Prompt: "short", LegacyPrompt: "short"})
	require.Greater(t, len(ordinary.Prompt), 1000)
	require.Contains(t, ordinary.Prompt, "short")
}

func TestUpstreamHealthReferenceProbeDoesNotCollectTruncatedText(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		parser   upstreamHealthStreamParser
		response string
		finish   string
	}{
		{"responses", upstreamHealthProbeProtocolOpenAI, parseOpenAIUpstreamHealthStream, `{"status":"incomplete","output_text":%q}`, "incomplete"},
		{"chat", upstreamHealthProbeProtocolOpenAIChat, parseOpenAIChatCompletionsUpstreamHealthStream, `{"choices":[{"message":{"content":%q},"finish_reason":"length"}]}`, "length"},
		{"anthropic", upstreamHealthProbeProtocolAnthropic, parseAnthropicUpstreamHealthStream, `{"content":[{"type":"text","text":%q}],"stop_reason":"max_tokens"}`, "max_tokens"},
		{"gemini", upstreamHealthProbeProtocolGemini, parseGeminiUpstreamHealthStream, `{"candidates":[{"content":{"parts":[{"text":%q}]},"finishReason":"MAX_TOKENS"}]}`, "MAX_TOKENS"},
	}
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		t.Run(method, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					// Even a long numeric prefix exceeding the source's completion
					// threshold must not turn a truncated answer into a valid sample.
					responseText := strings.Repeat("123, ", 240)
					upstream := &upstreamHealthProbeHTTPStub{stream: fmt.Sprintf(tc.response, responseText)}
					svc := &AccountTestService{httpUpstream: upstream}
					req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://probe.example/v1/responses", http.NoBody)
					require.NoError(t, err)
					result, err := svc.executeUpstreamHealthProbeWithValidator(req, &Account{ID: 1}, UpstreamHealthProbeResult{Protocol: tc.protocol, fingerprintMethod: method}, func(string) bool { return true }, tc.parser)
					require.NoError(t, err, "reference evidence must not change the existing health parser decision")
					require.Equal(t, "success", result.Result)
					require.Equal(t, tc.finish, result.FinishReason)
					evidence := confidenceFingerprintEvidenceFromProbe(result, result.Protocol)
					require.Equal(t, "finish_reason:"+strings.ToLower(tc.finish), evidence.FinishClass)
					addReferenceFingerprintPayload(&evidence, &ConfidenceFingerprintAttempt{Method: method, MethodSequence: 1}, result.probeResponseText)
					require.Empty(t, evidence.Feature)
					require.Empty(t, evidence.Numbers)
				})
			}
		})
	}
}

func TestUpstreamHealthReferenceProbePreservesWhitespaceAcrossFormats(t *testing.T) {
	chunks := []string{" \n", "# 建议", "\n", "1. 测试", "\n\n", "2. 复核 \n"}
	want := strings.Join(chunks, "")
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		return string(raw)
	}
	cases := []struct {
		name     string
		parser   upstreamHealthStreamParser
		jsonBody string
		chunk    func(string) any
		terminal string
	}{
		{"responses", parseOpenAIUpstreamHealthStream,
			encode(map[string]any{"status": "completed", "output_text": want}),
			func(text string) any { return map[string]any{"type": "response.output_text.delta", "delta": text} },
			encode(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}})},
		{"chat", parseOpenAIChatCompletionsUpstreamHealthStream,
			encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": want}, "finish_reason": "stop"}}}),
			func(text string) any {
				return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": text}}}}
			},
			encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop"}}})},
		{"anthropic", parseAnthropicUpstreamHealthStream,
			encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": want}}, "stop_reason": "end_turn"}),
			func(text string) any {
				return map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": text}}
			},
			encode(map[string]any{"type": "message_stop"})},
		{"gemini", parseGeminiUpstreamHealthStream,
			encode(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": want}}}, "finishReason": "STOP"}}}),
			func(text string) any {
				return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}}}
			},
			encode(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP"}}})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sse strings.Builder
			for _, chunk := range chunks {
				fmt.Fprintf(&sse, "data: %s\n\n", encode(tc.chunk(chunk)))
			}
			fmt.Fprintf(&sse, "data: %s\n\n", tc.terminal)
			for _, body := range []string{tc.jsonBody, sse.String()} {
				for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
					result := UpstreamHealthProbeResult{fingerprintMethod: method}
					text, err := tc.parser(strings.NewReader(body), time.Now(), &result)
					require.NoError(t, err)
					require.Equal(t, want, text, "reference features must see the source text, including whitespace")
					require.NotNil(t, result.TTFTMs)
				}
			}
		})
	}
}

func TestUpstreamHealthReferenceProbeTerminalTextAfterWhitespace(t *testing.T) {
	const want = " \n# 建议\n1. 写测试\n"
	for _, terminal := range []string{
		fmt.Sprintf("data: {\"type\":\"response.output_text.done\",\"text\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", want),
		fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output_text\":%q}}\n\n", want),
	} {
		body := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"\\n\"}\n\n" + terminal
		result := UpstreamHealthProbeResult{fingerprintMethod: "chanshui_behavioral"}
		text, err := parseOpenAIUpstreamHealthStream(strings.NewReader(body), time.Now(), &result)
		require.NoError(t, err)
		require.Equal(t, want, text)
	}
}
