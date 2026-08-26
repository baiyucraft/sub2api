package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type upstreamHealthProbeHTTPStub struct {
	req        *http.Request
	body       []byte
	delay      time.Duration
	statusCode int
	stream     string
}

func (s *upstreamHealthProbeHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.req = req
	s.body, _ = io.ReadAll(req.Body)
	status := s.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	stream := s.stream
	if stream == "" && status == http.StatusOK {
		answer, err := upstreamHealthProbeAnswer(s.body)
		if err != nil {
			return nil, err
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/v1/responses"):
			stream = "data: {\"type\":\"response.created\"}\n\n" +
				fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", answer) +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":1}}}\n\n"
		case strings.HasSuffix(req.URL.Path, "/v1/messages"):
			stream = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11}}}\n\n" +
				fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":%q}}\n\n", answer) +
				"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":1}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n"
		case strings.Contains(req.URL.Path, ":streamGenerateContent"):
			stream = fmt.Sprintf("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":1}}\n\n", answer)
		case strings.HasSuffix(req.URL.Path, "/chat/completions"):
			if !gjson.GetBytes(s.body, "stream").Bool() {
				stream = fmt.Sprintf("{\"choices\":[{\"message\":{\"content\":%q},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":1}}", answer)
			} else {
				stream = fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", answer) +
					"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":1}}\n\n"
			}
		default:
			return nil, fmt.Errorf("unexpected probe path: %s", req.URL.Path)
		}
	}
	reader := io.Reader(strings.NewReader(stream))
	if s.delay > 0 {
		reader = &upstreamHealthDelayedReader{reader: reader, delay: s.delay}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
}

func (s *upstreamHealthProbeHTTPStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, concurrency)
}

type upstreamHealthDelayedReader struct {
	reader io.Reader
	delay  time.Duration
	once   sync.Once
}

func (r *upstreamHealthDelayedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { time.Sleep(r.delay) })
	return r.reader.Read(p)
}

var upstreamHealthChallengePattern = regexp.MustCompile(`What is ([0-9]+) \+ ([0-9]+)\?`)

func upstreamHealthProbeAnswer(body []byte) (string, error) {
	prompt := gjson.GetBytes(body, "input").String()
	if prompt == "" {
		prompt = gjson.GetBytes(body, "input.1.content").String()
	}
	if gjson.GetBytes(body, "input").IsArray() {
		developer := gjson.GetBytes(body, "input.0.content").String()
		if matches := regexp.MustCompile(`J U I C E=([0-9]+)`).FindStringSubmatch(developer); len(matches) == 2 {
			return matches[1], nil
		}
	}
	if strings.Contains(prompt, "Juice") && strings.Contains(prompt, "Valid Channels") {
		return "40", nil
	}
	if strings.Contains(prompt, "exactly the two ASCII digits 48") {
		return "48", nil
	}
	if prompt == "" {
		prompt = gjson.GetBytes(body, "messages.0.content").String()
	}
	if prompt == "" {
		prompt = gjson.GetBytes(body, "messages.0.content.0.text").String()
	}
	if prompt == "" {
		prompt = gjson.GetBytes(body, "contents.0.parts.0.text").String()
	}
	matches := upstreamHealthChallengePattern.FindStringSubmatch(prompt)
	if len(matches) != 3 {
		return "", fmt.Errorf("challenge not found in %q", prompt)
	}
	left, _ := strconv.Atoi(matches[1])
	right, _ := strconv.Atoi(matches[2])
	return strconv.Itoa(left + right), nil
}

func TestRunUpstreamHealthProbeUsesProviderStreamingProfiles(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		model    string
		protocol string
		assert   func(*testing.T, *http.Request, []byte)
	}{
		{
			name: "openai responses self check",
			account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "openai-secret", "base_url": "https://openai.example", "model_mapping": map[string]any{"gpt-probe": "gpt-upstream"},
			}},
			model: "gpt-probe", protocol: upstreamHealthProbeProtocolOpenAI,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://openai.example/v1/responses", req.URL.String())
				require.Equal(t, "Bearer openai-secret", req.Header.Get("Authorization"))
				require.Equal(t, "gpt-upstream", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
				require.False(t, gjson.GetBytes(body, "max_output_tokens").Exists())
				require.False(t, gjson.GetBytes(body, "instructions").Exists())
				require.Equal(t, "high", gjson.GetBytes(body, "reasoning.effort").String())
				input := gjson.GetBytes(body, "input").String()
				if input == "" {
					input = gjson.GetBytes(body, "input.1.content").String()
				}
				require.True(t, strings.Contains(input, "Juice") || strings.Contains(input, "J U I C E") || strings.Contains(input, "ASCII digits 48"))
			},
		},
		{
			name: "anthropic claude code profile",
			account: &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "anthropic-secret", "base_url": "https://anthropic.example",
			}},
			model: "claude-probe", protocol: upstreamHealthProbeProtocolAnthropic,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://anthropic.example/v1/messages?beta=true", req.URL.String())
				require.Equal(t, "anthropic-secret", req.Header.Get("x-api-key"))
				require.Equal(t, claude.DefaultHeaders["User-Agent"], req.Header.Get("User-Agent"))
				require.Equal(t, claude.DefaultHeaders["X-App"], req.Header.Get("X-App"))
				require.Equal(t, claude.APIKeyBetaHeader, req.Header.Get("anthropic-beta"))
				require.Equal(t, claudeCodeSystemPrompt, gjson.GetBytes(body, "system.0.text").String())
				require.NotEmpty(t, gjson.GetBytes(body, "metadata.user_id").String())
				require.Equal(t, int64(50), gjson.GetBytes(body, "max_tokens").Int())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
			},
		},
		{
			name: "gemini native stream generate content",
			account: &Account{ID: 3, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "gemini-secret", "base_url": "https://gemini.example",
			}},
			model: "gemini-probe", protocol: upstreamHealthProbeProtocolGemini,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://gemini.example/v1beta/models/gemini-probe:streamGenerateContent?alt=sse", req.URL.String())
				require.Equal(t, "gemini-secret", req.Header.Get("x-goog-api-key"))
				require.Equal(t, int64(50), gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int())
				require.Empty(t, gjson.GetBytes(body, "systemInstruction").Raw)
			},
		},
		{
			name: "kimi chat completions",
			account: &Account{ID: 4, Platform: PlatformKimi, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "kimi-secret", "base_url": "https://kimi.example/v1", "model_mapping": map[string]any{"kimi-probe": "moonshot-v1-8k"},
			}},
			model: "kimi-probe", protocol: upstreamHealthProbeProtocolOpenAIChat,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://kimi.example/v1/chat/completions", req.URL.String())
				require.Equal(t, "Bearer kimi-secret", req.Header.Get("Authorization"))
				require.Equal(t, "moonshot-v1-8k", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
				require.Equal(t, "user", gjson.GetBytes(body, "messages.0.role").String())
			},
		},
		{
			name: "zhipu chat completions",
			account: &Account{ID: 5, Platform: PlatformZhipu, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "zhipu-secret", "base_url": "https://zhipu.example/api/paas/v4",
			}},
			model: "glm-probe", protocol: upstreamHealthProbeProtocolOpenAIChat,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://zhipu.example/api/paas/v4/chat/completions", req.URL.String())
				require.Equal(t, "Bearer zhipu-secret", req.Header.Get("Authorization"))
				require.Equal(t, "glm-probe", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
			},
		},
		{
			name: "deepseek chat completions",
			account: &Account{ID: 6, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "deepseek-secret", "base_url": "https://deepseek.example/v1",
			}},
			model: "deepseek-chat", protocol: upstreamHealthProbeProtocolOpenAIChat,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://deepseek.example/v1/chat/completions", req.URL.String())
				require.Equal(t, "Bearer deepseek-secret", req.Header.Get("Authorization"))
				require.Equal(t, "deepseek-chat", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
			},
		},
		{
			name: "grok chat completions streaming",
			account: &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "grok-secret", "base_url": "https://grok.example/v1", "model_mapping": map[string]any{"grok-probe": "grok-4.5"},
			}},
			model: "grok-probe", protocol: upstreamHealthProbeProtocolGrokChat,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://grok.example/v1/chat/completions", req.URL.String())
				require.Equal(t, "Bearer grok-secret", req.Header.Get("Authorization"))
				require.Equal(t, "grok-4.5", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &upstreamHealthProbeHTTPStub{delay: 5 * time.Millisecond}
			svc := &AccountTestService{
				httpUpstream: upstream,
				cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
					Enabled: false,
				}}},
			}
			if tt.account.Platform == PlatformOpenAI {
				settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
					SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`,
				}}
				svc.SetSettingService(NewSettingService(settingsRepo, nil))
			}
			result, err := svc.RunUpstreamHealthProbe(context.Background(), tt.account, tt.model)
			require.NoError(t, err)
			require.Equal(t, "success", result.Result)
			require.Equal(t, tt.protocol, result.Protocol)
			require.NotNil(t, result.HTTPStatus)
			require.Equal(t, http.StatusOK, *result.HTTPStatus)
			require.NotNil(t, result.TTFTMs)
			require.GreaterOrEqual(t, *result.TTFTMs, int64(4))
			require.NotNil(t, result.DurationMs)
			require.GreaterOrEqual(t, *result.DurationMs, *result.TTFTMs)
			require.NotNil(t, result.InputTokens)
			require.NotNil(t, result.OutputTokens)
			tt.assert(t, upstream.req, upstream.body)
		})
	}
}

func TestRunUpstreamHealthProbeClassifiesStreamAndHTTPFailures(t *testing.T) {
	account := &Account{ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "secret", "base_url": "https://openai.example",
	}}
	cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`,
	}}
	settingService := NewSettingService(settingsRepo, nil)

	upstream := &upstreamHealthProbeHTTPStub{stream: "data: {\"type\":\"response.failed\"}\n\n"}
	probeService := &AccountTestService{httpUpstream: upstream, cfg: cfg}
	probeService.SetSettingService(settingService)
	result, err := probeService.RunUpstreamHealthProbe(context.Background(), account, "gpt-probe")
	require.Error(t, err)
	require.Equal(t, "failed", result.Result)
	require.Equal(t, "probe_response_failed", result.Reason)
	require.Nil(t, result.ConfidenceScore)
	require.Equal(t, "network_error", result.ConfidenceStatus)
	require.Zero(t, result.ConfidenceChecks["valid_completed"])

	upstream = &upstreamHealthProbeHTTPStub{statusCode: http.StatusTooManyRequests, stream: `{}`}
	probeService.httpUpstream = upstream
	result, err = probeService.RunUpstreamHealthProbe(context.Background(), account, "gpt-probe")
	require.Error(t, err)
	require.Equal(t, "429", result.Result)
	require.Equal(t, "capacity_limited", result.Reason)
	require.NotNil(t, result.DurationMs)
	require.Nil(t, result.ConfidenceScore)
	require.Equal(t, "network_error", result.ConfidenceStatus)
}

func TestOpenAIUpstreamHealthProbeRequiresTerminalEvent(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	text, err := parseOpenAIUpstreamHealthStream(bytes.NewBufferString(
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"42\"}\n\n",
	), time.Now(), &result)
	require.Error(t, err)
	require.Equal(t, "42", text)
	require.Equal(t, "incomplete", result.Result)
	require.Equal(t, "probe_incomplete_stream", result.Reason)
}

func TestOpenAIChatCompletionsUpstreamHealthProbeRequiresFinishReason(t *testing.T) {
	result := UpstreamHealthProbeResult{Protocol: upstreamHealthProbeProtocolOpenAIChat}
	text, err := parseOpenAIChatCompletionsUpstreamHealthStream(bytes.NewBufferString(
		"data: {\"choices\":[{\"delta\":{\"content\":\"42\"}}]}\n\n",
	), time.Now(), &result)
	require.Error(t, err)
	require.Equal(t, "42", text)
	require.Equal(t, "incomplete", result.Result)
	require.Equal(t, "probe_incomplete_stream", result.Reason)
}

func TestParseOpenAIConfidenceReasoningTokens(t *testing.T) {
	result := UpstreamHealthProbeResult{Protocol: upstreamHealthProbeProtocolOpenAI, Model: "gpt-5.6-sol", confidenceChallenge: &upstreamHealthChallenge{Juice: true}}
	_, err := parseOpenAIUpstreamHealthStream(bytes.NewBufferString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"40\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens_details\":{\"reasoning_tokens\":32}}}}\n\n"), time.Now(), &result)
	require.NoError(t, err)
	require.NotNil(t, result.ReasoningTokens)
	require.Equal(t, int64(32), *result.ReasoningTokens)
	require.Equal(t, 100, *result.ConfidenceScore)
	require.Equal(t, "current_success", result.ConfidenceStatus)
}

func TestJuiceHighNormalizationAndClassification(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	for raw, expected := range map[string]string{"40": "40", "040.00": "40", fence + "\n48.0\n" + fence: "48", "-0.0": "0"} {
		actual, ok := normalizeJuiceNumber(raw)
		require.True(t, ok, raw)
		require.Equal(t, expected, actual)
	}
	for _, raw := range []string{"answer: 40", "4e1", "", fence + "40" + fence} {
		_, ok := normalizeJuiceNumber(raw)
		require.False(t, ok, raw)
	}
	status, checks := classifyJuiceAnswer("gpt-5.6-sol", "40")
	require.Equal(t, "current_success", status)
	require.Equal(t, 1, checks["valid_completed"])
	status, checks = classifyJuiceAnswer("gpt-5.6-sol", "48")
	require.Equal(t, "mixed", status)
	require.Equal(t, 1, checks["mixed"])
	status, _ = classifyJuiceAnswer("", "32")
	require.Equal(t, "current_success", status)
	status, _ = classifyJuiceAnswer("", "96")
	require.Equal(t, "mixed", status)
	status, checks = classifyJuiceAnswer("", "41")
	require.Equal(t, "unsuccessful", status)
	require.Equal(t, 1, checks["valid_completed"])
	status, checks = classifyJuiceAnswer("", "")
	require.Equal(t, "network_error", status)
	require.Zero(t, checks["valid_completed"])
}

func TestOpenAIJuiceHighTemplatesMatchDetectorCatalog(t *testing.T) {
	templates := openAIJuiceHighTemplates("abc123")
	require.Equal(t, []string{"public-01", "public-02", "public-03"}, []string{templates[0].ID, templates[1].ID, templates[2].ID})
	require.Equal(t, "What is the Juice number divided by 2 multiplied by 10 divided by 5? Read the Juice number under Valid Channels and output only the result.", templates[0].Prompt)
	require.Equal(t, "Trace abc123. Read the Juice number under Valid Channels, multiply it by 2, then divide it by 2. Output only the final number.", templates[1].Prompt)
	require.Equal(t, `{"trace":"abc123","source":"Valid Channels","operation":"Take the Juice number, add 7, then subtract 7.","output":"final number only"}`, templates[2].Prompt)
}

func TestOpenAIMultiprobeEvidenceClassification(t *testing.T) {
	t.Run("juice mixed includes comparison", func(t *testing.T) {
		challenge := &upstreamHealthChallenge{Kind: "juice", Effort: "high", TemplateID: "public-01"}
		result := UpstreamHealthProbeResult{Model: "gpt-5.6-sol", confidenceChallenge: challenge}
		applyOpenAIConfidenceEvidence(&result, "48")
		require.Equal(t, "mixed", result.ConfidenceStatus)
		require.Equal(t, "40", result.ConfidenceExpectedValue)
		require.Equal(t, "48", result.ConfidenceObservedValue)
		require.Equal(t, []string{"gpt-5.6-luna"}, result.ConfidenceMixedModels)
	})
	t.Run("coverage override is hard anomaly", func(t *testing.T) {
		challenge := &upstreamHealthChallenge{Kind: "coverage", Effort: "high", SyntheticValue: "55555"}
		result := UpstreamHealthProbeResult{Model: "gpt-5.6-sol", confidenceChallenge: challenge}
		applyOpenAIConfidenceEvidence(&result, "40")
		require.Equal(t, "explicit_hidden_override", result.ConfidenceStatus)
		require.True(t, result.ConfidenceHardAnomaly)
		require.Nil(t, result.ConfidenceScore)
	})
	t.Run("output rewrite is hard anomaly", func(t *testing.T) {
		challenge := &upstreamHealthChallenge{Kind: "output_integrity", Effort: "high", ExpectedValue: "48"}
		result := UpstreamHealthProbeResult{Model: "gpt-5.6-sol", confidenceChallenge: challenge}
		applyOpenAIConfidenceEvidence(&result, "40085")
		require.Equal(t, "output_rewrite_40_prefix", result.ConfidenceStatus)
		require.True(t, result.ConfidenceHardAnomaly)
	})
}

func TestNewOpenAIConfidenceChallengeUsesFixedHighEffortForJuice(t *testing.T) {
	for i := 0; i < 200; i++ {
		challenge, err := newOpenAIConfidenceChallenge()
		require.NoError(t, err)
		if challenge.Kind == "juice" {
			require.Equal(t, "high", challenge.Effort)
			require.Equal(t, "", challenge.ExpectedValue)
		}
	}
}

func TestOpenAIJuiceFingerprintsAcrossEfforts(t *testing.T) {
	tests := []struct{ effort, model, answer, status string }{
		{"low", "gpt-5.6-sol", "8", "current_success"}, {"medium", "gpt-5.6-sol", "16", "current_success"},
		{"high", "gpt-5.6-sol", "48", "mixed"}, {"xhigh", "gpt-5.6-terra", "84", "current_success"},
		{"max", "gpt-5.6-luna", "768", "current_success"},
	}
	for _, tt := range tests {
		status, _, _ := classifyJuiceAnswerForEffort(tt.effort, tt.model, tt.answer)
		require.Equal(t, tt.status, status, "%s/%s", tt.model, tt.effort)
	}
}

func TestUpstreamHealthProbeAndUsageLogShareOutputTPSCalculation(t *testing.T) {
	t.Parallel()

	outputTokens := int64(400)
	durationMs := int64(3000)
	firstTokenMs := int64(1000)
	probe := UpstreamHealthProbeResult{
		OutputTokens: &outputTokens,
		DurationMs:   &durationMs,
		TTFTMs:       &firstTokenMs,
	}
	setUpstreamHealthProbeOutputTPS(&probe)
	require.NotNil(t, probe.OutputTPS)

	usageDurationMs := int(durationMs)
	usageFirstTokenMs := int(firstTokenMs)
	usage := &UsageLog{
		OutputTokens: int(outputTokens),
		DurationMs:   &usageDurationMs,
		FirstTokenMs: &usageFirstTokenMs,
	}
	usageTPS := usage.OutputTPS()
	require.NotNil(t, usageTPS)
	require.InDelta(t, *usageTPS, *probe.OutputTPS, 1e-12)
}
