package service

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type upstreamHealthProbeHTTPStub struct {
	req        *http.Request
	body       []byte
	requests   []*http.Request
	bodies     [][]byte
	delay      time.Duration
	statusCode int
	stream     string
	answerWrap func(string) string
}

type adaptiveProbeSequenceStub struct {
	first    upstreamHealthProbeHTTPStub
	requests []*http.Request
}

type upstreamHealthProbeGeminiTokenCache struct {
	token string
}

func (f *upstreamHealthProbeGeminiTokenCache) GetAccessToken(context.Context, string) (string, error) {
	if strings.TrimSpace(f.token) == "" {
		return "", errors.New("missing token")
	}
	return f.token, nil
}

func (*upstreamHealthProbeGeminiTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}

func (*upstreamHealthProbeGeminiTokenCache) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (*upstreamHealthProbeGeminiTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}

func (*upstreamHealthProbeGeminiTokenCache) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

func upstreamHealthProbeJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func (s *adaptiveProbeSequenceStub) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	s.requests = append(s.requests, req)
	if len(s.requests) == 1 {
		return s.first.Do(req, proxyURL, accountID, concurrency)
	}
	return upstreamHealthProbeJSONResponse(http.StatusNotFound, `{"error":{"message":"messages endpoint not found"}}`), nil
}

func (s *adaptiveProbeSequenceStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, concurrency)
}

func (s *upstreamHealthProbeHTTPStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.req = req
	s.body, _ = io.ReadAll(req.Body)
	s.requests = append(s.requests, req)
	s.bodies = append(s.bodies, append([]byte(nil), s.body...))
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
		if s.answerWrap != nil {
			answer = s.answerWrap(answer)
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/v1/responses") || strings.HasSuffix(req.URL.Path, "/responses"):
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
	// Google Code Assist wraps the Gemini request under `request`; keep the
	// fixture aligned with the production request builder so OAuth probes test
	// the real nested payload rather than a simplified API-key shape.
	if prompt == "" {
		prompt = gjson.GetBytes(body, "request.contents.0.parts.0.text").String()
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
				require.NotContains(t, gjson.GetBytes(body, "messages.0.content.0.text").String(), "JSON object")
				require.Regexp(t, `^What is [0-9]+ \+ [0-9]+\? Reply with only the decimal number\.$`, gjson.GetBytes(body, "messages.0.content.0.text").String())
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
				require.Equal(t, int64(upstreamHealthProbeGeminiMaxOutputTokens), gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int())
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
				require.Regexp(t, `^What is [0-9]+ \+ [0-9]+\? Reply with only the decimal number\.$`, gjson.GetBytes(body, "messages.0.content").String())
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
			model: "grok-probe", protocol: upstreamHealthProbeProtocolGrok,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://grok.example/v1/responses", req.URL.String())
				require.Equal(t, "Bearer grok-secret", req.Header.Get("Authorization"))
				require.Equal(t, "grok-4.5", gjson.GetBytes(body, "model").String())
				require.True(t, gjson.GetBytes(body, "stream").Bool())
				require.Regexp(t, `^What is [0-9]+ \+ [0-9]+\? Reply with only the decimal number\.$`, gjson.GetBytes(body, "input").String())
			},
		},
		{
			name: "antigravity api key native family",
			account: &Account{ID: 8, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Concurrency: 2, Credentials: map[string]any{
				"api_key": "antigravity-secret", "base_url": "https://antigravity.example",
				"model_mapping": map[string]any{"antigravity-probe": "gemini-2.5-flash"},
			}},
			model: "antigravity-probe", protocol: upstreamHealthProbeProtocolAntigravity,
			assert: func(t *testing.T, req *http.Request, body []byte) {
				require.Equal(t, "https://antigravity.example/antigravity/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", req.URL.String())
				require.Equal(t, "antigravity-secret", req.Header.Get("x-goog-api-key"))
				require.NotEmpty(t, gjson.GetBytes(body, "contents.0.parts.0.text").String())
			},
		},
	}
	coveredPlatforms := make(map[string]struct{}, len(tests))

	for _, tt := range tests {
		coveredPlatforms[tt.account.Platform] = struct{}{}
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
	for _, descriptor := range RegisteredPlatformCatalog() {
		if descriptor.ProbeSupported {
			require.Contains(t, coveredPlatforms, descriptor.ID, "active probe dispatcher must cover registered platform %s", descriptor.ID)
		}
	}
}

func TestRunUpstreamHealthProbeCoversOAuthNativeProfiles(t *testing.T) {
	t.Run("grok oauth responses", func(t *testing.T) {
		account := &Account{ID: 81, Platform: PlatformGrok, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{
			"access_token":  "grok-access-token",
			"refresh_token": "grok-refresh-token",
			"expires_at":    time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"model_mapping": map[string]any{
				"grok-probe": "grok-4.5",
			},
		}}
		repo := &healthProbeAccountRepo{account: *account}
		upstream := &upstreamHealthProbeHTTPStub{}
		svc := &AccountTestService{
			accountRepo:       repo,
			grokTokenProvider: NewGrokTokenProvider(repo, nil),
			httpUpstream:      upstream,
			cfg:               &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		}

		result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "grok-probe")

		require.NoError(t, err)
		require.Equal(t, "success", result.Result)
		require.Equal(t, upstreamHealthProbeProtocolGrok, result.Protocol)
		require.Equal(t, "https://cli-chat-proxy.grok.com/v1/responses", upstream.req.URL.String())
		require.Equal(t, "Bearer grok-access-token", upstream.req.Header.Get("Authorization"))
		require.Equal(t, grokCLIVersion, upstream.req.Header.Get("X-Grok-Client-Version"))
	})

	t.Run("antigravity oauth v1internal", func(t *testing.T) {
		account := &Account{ID: 82, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{
			"access_token": "antigravity-access-token",
			"expires_at":   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"project_id":   "probe-project",
			"model_mapping": map[string]any{
				"antigravity-probe": "gemini-2.5-flash",
			},
		}}
		repo := &healthProbeAccountRepo{account: *account}
		upstream := &upstreamHealthProbeHTTPStub{}
		tokenProvider := NewAntigravityTokenProvider(repo, nil, nil)
		gateway := NewAntigravityGatewayService(repo, nil, nil, tokenProvider, nil, upstream, nil, nil)
		svc := &AccountTestService{
			accountRepo:               repo,
			httpUpstream:              upstream,
			antigravityGatewayService: gateway,
		}

		result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "antigravity-probe")

		require.NoError(t, err)
		require.Equal(t, "success", result.Result)
		require.Equal(t, upstreamHealthProbeProtocolAntigravity, result.Protocol)
		require.Equal(t, "gemini-2.5-flash", result.Model)
		require.Contains(t, upstream.req.URL.Path, ":streamGenerateContent")
		require.Equal(t, "Bearer antigravity-access-token", upstream.req.Header.Get("Authorization"))
	})
}

func TestRunUpstreamHealthProbeAcceptsExplainedArithmeticForChatProviders(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformKimi, PlatformGrok} {
		t.Run(platform, func(t *testing.T) {
			upstream := &upstreamHealthProbeHTTPStub{answerWrap: func(answer string) string { return "The answer is " + answer + "." }}
			credentials := map[string]any{
				"api_key": "probe-secret", "base_url": "https://provider.example",
			}
			if platform == PlatformGrok {
				credentials["model_mapping"] = map[string]any{"probe-model": "grok-4.5"}
			}
			account := &Account{ID: 700, Platform: platform, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: credentials}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
			result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "probe-model")
			require.NoError(t, err)
			require.Equal(t, "success", result.Result)
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

func TestOpenAIAPIKeyProbeUsesChatWhenResponsesUnsupported(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Extra: map[string]any{
		"openai_responses_supported": false,
	}, Credentials: map[string]any{"api_key": "secret", "base_url": "https://openai.example"}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gpt-probe")
	require.NoError(t, err)
	require.Equal(t, upstreamHealthProbeProtocolOpenAIChat, result.Protocol)
	require.Equal(t, "/v1/chat/completions", upstream.req.URL.Path)
}

func TestDeepseekResponsesProbeUsesNativeEndpoint(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 92, Platform: PlatformDeepseek, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "secret", "base_url": "https://deepseek.example/v1", "api_protocol": APIProtocolResponses,
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "deepseek-chat")
	require.NoError(t, err)
	require.Equal(t, "success", result.Result)
	require.Equal(t, upstreamHealthProbeProtocolOpenAI, result.Protocol)
	require.Equal(t, "/v1/responses", upstream.req.URL.Path)
	require.False(t, gjson.GetBytes(upstream.body, "store").Bool())
	require.Equal(t, "Bearer secret", upstream.req.Header.Get("Authorization"))
}

func TestCNAdaptiveProbeCoversEveryForwardingProtocol(t *testing.T) {
	for _, tc := range []struct {
		name      string
		platform  string
		wantPaths []string
	}{
		{name: "kimi", platform: PlatformKimi, wantPaths: []string{"/v1/chat/completions", "/v1/messages"}},
		{name: "zhipu", platform: PlatformZhipu, wantPaths: []string{"/v1/chat/completions", "/v1/messages"}},
		{name: "deepseek", platform: PlatformDeepseek, wantPaths: []string{"/v1/chat/completions", "/v1/messages", "/responses"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &upstreamHealthProbeHTTPStub{}
			account := &Account{ID: 920, Platform: tc.platform, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
				"api_key": "secret", "api_protocol": APIProtocolAdaptive,
				"api_base_urls": map[string]any{
					APIProtocolChatCompletions: "https://chat.example/v1",
					APIProtocolAnthropic:       "https://anthropic.example",
					APIProtocolResponses:       "https://responses.example",
				},
			}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}

			result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "probe-model")

			require.NoError(t, err)
			require.Equal(t, "success", result.Result)
			require.Equal(t, upstreamHealthProbeProtocolAdaptive, result.Protocol)
			require.Len(t, upstream.requests, len(tc.wantPaths))
			for index, wantPath := range tc.wantPaths {
				require.Equal(t, wantPath, upstream.requests[index].URL.Path)
			}
			require.NotNil(t, result.TTFTMs)
			require.NotNil(t, result.DurationMs)
			require.NotNil(t, result.InputTokens)
			require.NotNil(t, result.OutputTokens)
		})
	}
}

func TestCNAdaptiveProbeReturnsConcreteFailingProtocol(t *testing.T) {
	upstream := &adaptiveProbeSequenceStub{}
	account := &Account{ID: 921, Platform: PlatformKimi, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "secret", "api_protocol": APIProtocolAdaptive,
		"api_base_urls": map[string]any{
			APIProtocolChatCompletions: "https://chat.example/v1",
			APIProtocolAnthropic:       "https://anthropic.example",
		},
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}

	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "probe-model")

	require.Error(t, err)
	require.Equal(t, upstreamHealthProbeProtocolAnthropic, result.Protocol)
	require.Equal(t, "404", result.Result)
	require.Equal(t, "upstream_http_error", result.Reason)
	require.Len(t, upstream.requests, 2)
}

func TestCNAnthropicProbeUsesProviderProtocolBaseURL(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 93, Platform: PlatformKimi, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "secret", "api_protocol": APIProtocolAnthropic,
	}}
	svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "kimi-k2.5")
	require.NoError(t, err)
	require.Equal(t, "success", result.Result)
	require.Equal(t, strings.TrimRight(DefaultKimiPayGAnthropicBaseURL, "/")+"/v1/messages", upstream.req.URL.String())
}

func TestAntigravityAPIKeyProbeMapsAliasExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		alias  string
		mapped string
	}{
		{name: "gemini family", alias: "probe-gemini", mapped: "gemini-2.5-flash"},
		{name: "anthropic family", alias: "probe-claude", mapped: "claude-sonnet-4-6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &upstreamHealthProbeHTTPStub{}
			account := &Account{ID: 94, Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://antigravity.example",
				"model_mapping": map[string]any{tc.alias: tc.mapped},
			}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
			result, err := svc.RunUpstreamHealthProbe(context.Background(), account, tc.alias)
			require.NoError(t, err)
			require.Equal(t, "success", result.Result)
			require.Equal(t, upstreamHealthProbeProtocolAntigravity, result.Protocol)
			require.Equal(t, tc.mapped, result.Model)
			require.NotNil(t, upstream.req)
		})
	}
}

func TestProbeParsersAcceptCompleteJSONResponses(t *testing.T) {
	tests := []struct {
		name   string
		parser upstreamHealthStreamParser
		body   string
		want   string
	}{
		{"openai responses", parseOpenAIUpstreamHealthStream, `{"output_text":"42","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}`, "42"},
		{"openai chat", parseOpenAIChatCompletionsUpstreamHealthStream, `{"choices":[{"message":{"content":"42"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`, "42"},
		{"anthropic", parseAnthropicUpstreamHealthStream, `{"content":[{"type":"text","text":"42"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`, "42"},
		{"gemini", parseGeminiUpstreamHealthStream, `{"candidates":[{"content":{"parts":[{"text":"42"}]},"finishReason":"STOP"}]}`, "42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UpstreamHealthProbeResult{}
			text, err := tt.parser(strings.NewReader(tt.body), time.Now(), &result)
			require.NoError(t, err)
			require.Equal(t, tt.want, text)
			require.NotNil(t, result.TTFTMs)
		})
	}
}

func TestProbeParsersClassifySuccessfulProtocolErrors(t *testing.T) {
	tests := []struct {
		name   string
		parser upstreamHealthStreamParser
		body   string
		want   string
	}{
		{
			name:   "responses json error",
			parser: parseOpenAIUpstreamHealthStream,
			body:   `{"error":{"type":"server_error","message":"temporary"}}`,
			want:   "probe_response_failed",
		},
		{
			name:   "responses sse error event",
			parser: parseOpenAIUpstreamHealthStream,
			body:   "data: {\"type\":\"error\",\"error\":{\"message\":\"temporary\"}}\n\n",
			want:   "probe_response_failed",
		},
		{
			name:   "gemini json error",
			parser: parseGeminiUpstreamHealthStream,
			body:   `{"error":{"status":"UNAVAILABLE"}}`,
			want:   "probe_response_failed",
		},
		{
			name:   "gemini wrapped json error",
			parser: parseGeminiUpstreamHealthStream,
			body:   `{"response":{"error":{"status":"UNAVAILABLE"}}}`,
			want:   "probe_response_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UpstreamHealthProbeResult{}
			_, err := tt.parser(strings.NewReader(tt.body), time.Now(), &result)
			require.Error(t, err)
			require.Equal(t, tt.want, result.Reason)
			require.Equal(t, "failed", result.Result)
		})
	}
}

func TestParseOpenAIResponsesJSONWrapper(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	text, err := parseOpenAIUpstreamHealthStream(strings.NewReader(`{"response":{"output_text":"42","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}`), time.Now(), &result)
	require.NoError(t, err)
	require.Equal(t, "42", text)
	require.NotNil(t, result.TTFTMs)
}

func TestProbeParsersPreserveStreamingTTFT(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	text, err := parseGeminiUpstreamHealthStream(strings.NewReader("\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"42\"}]},\"finishReason\":\"STOP\"}]}\n\n"), time.Now(), &result)
	require.NoError(t, err)
	require.Equal(t, "42", text)
	require.NotNil(t, result.TTFTMs)
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

func TestOpenAIUpstreamHealthProbeAcceptsFinalTextWithoutDeltas(t *testing.T) {
	for _, body := range []string{
		"data: {\"type\":\"response.output_text.done\",\"text\":\"42\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n",
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"42\"}]}]}}\n\n",
	} {
		result := UpstreamHealthProbeResult{Protocol: upstreamHealthProbeProtocolOpenAI}
		text, err := parseOpenAIUpstreamHealthStream(strings.NewReader(body), time.Now(), &result)
		require.NoError(t, err)
		require.Equal(t, "42", text)
		require.NotNil(t, result.TTFTMs)
	}
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

func TestUpstreamHealthProbeParsersAcceptExplicitDoneSentinel(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		parser   upstreamHealthStreamParser
		body     string
	}{
		{
			name:     "openai responses",
			protocol: upstreamHealthProbeProtocolOpenAI,
			parser:   parseOpenAIUpstreamHealthStream,
			body:     "data: {\"type\":\"response.output_text.delta\",\"delta\":\"42\"}\n\ndata: [DONE]\n\n",
		},
		{
			name:     "openai chat completions",
			protocol: upstreamHealthProbeProtocolOpenAIChat,
			parser:   parseOpenAIChatCompletionsUpstreamHealthStream,
			body:     "data: {\"choices\":[{\"delta\":{\"content\":\"42\"}}]}\n\ndata: [DONE]\n\n",
		},
		{
			name:     "anthropic messages relay",
			protocol: upstreamHealthProbeProtocolAnthropic,
			parser:   parseAnthropicUpstreamHealthStream,
			body:     "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"42\"}}\n\ndata: [DONE]\n\n",
		},
		{
			name:     "gemini relay",
			protocol: upstreamHealthProbeProtocolGemini,
			parser:   parseGeminiUpstreamHealthStream,
			body:     "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"42\"}]}}]}\n\ndata: [DONE]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UpstreamHealthProbeResult{Protocol: tt.protocol}
			text, err := tt.parser(strings.NewReader(tt.body), time.Now(), &result)
			require.NoError(t, err)
			require.Equal(t, "42", text)
			require.NotNil(t, result.TTFTMs)
		})
	}
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

func TestValidateGeminiUpstreamHealthChallengeAcceptsStructuredJSON(t *testing.T) {
	challenge := upstreamHealthChallenge{
		Expected: "42", Marker: "probe-abcd", Constraint: "constraint-ok", ContextNeedle: "not_requested",
	}

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "json", raw: `{"marker":"probe-abcd","calculation":42,"constraint":"constraint-ok","context_check":"not_requested"}`, want: true},
		{name: "fenced json", raw: "```json\n{\"marker\":\"probe-abcd\",\"calculation\":\"42\",\"constraint\":\"constraint-ok\",\"context_check\":\"not_requested\"}\n```", want: true},
		{name: "single line fenced json", raw: "```json {\"marker\":\"probe-abcd\",\"calculation\":42,\"constraint\":\"constraint-ok\",\"context_check\":\"not_requested\"}```", want: true},
		{name: "unsupported fence label", raw: "```javascript\n{\"marker\":\"probe-abcd\",\"calculation\":42,\"constraint\":\"constraint-ok\",\"context_check\":\"not_requested\"}\n```", want: false},
		{name: "wrong marker", raw: `{"marker":"probe-other","calculation":42,"constraint":"constraint-ok","context_check":"not_requested"}`, want: false},
		{name: "wrong calculation", raw: `{"marker":"probe-abcd","calculation":43,"constraint":"constraint-ok","context_check":"not_requested"}`, want: false},
		{name: "extra text", raw: `answer: {"marker":"probe-abcd","calculation":42,"constraint":"constraint-ok","context_check":"not_requested"}`, want: false},
		{name: "numeric fallback", raw: "42", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, validateGeminiUpstreamHealthChallenge(tt.raw, challenge))
		})
	}
}

func TestValidateUpstreamArithmeticChallengeMatchesChannelMonitorV1(t *testing.T) {
	for _, tc := range []struct {
		response string
		want     bool
	}{
		{response: "42", want: true},
		{response: "答案是 42。", want: true},
		{response: "The answer is 42", want: true},
		{response: "420", want: false},
		{response: "", want: false},
	} {
		require.Equal(t, tc.want, validateUpstreamArithmeticChallenge(tc.response, "42"), tc.response)
	}
}

func TestParseGeminiUpstreamHealthResponseSupportsSSEJSONAndWrapper(t *testing.T) {
	responses := []struct {
		name string
		body string
	}{
		{name: "sse", body: `data: {"candidates":[{"content":{"parts":[{"text":"42"}]},"finishReason":"STOP"}]}` + "\n\n"},
		{name: "json", body: `{"candidates":[{"content":{"parts":[{"text":"42"}]},"finishReason":"STOP"}]}`},
		{name: "wrapped json", body: `{"response":{"candidates":[{"content":{"parts":[{"text":"42"}]},"finishReason":"STOP"}]}}`},
		{name: "json array", body: `[{"candidates":[{"content":{"parts":[{"text":"4"}]}}]},{"candidates":[{"content":{"parts":[{"text":"2"}]},"finishReason":"STOP"}]}]`},
	}
	for _, tt := range responses {
		t.Run(tt.name, func(t *testing.T) {
			result := UpstreamHealthProbeResult{}
			text, err := parseGeminiUpstreamHealthStream(strings.NewReader(tt.body), time.Now(), &result)
			require.NoError(t, err)
			require.Equal(t, "42", text)
			require.NotNil(t, result.TTFTMs)
		})
	}
}

func TestParseGeminiUpstreamHealthResponseIgnoresThoughtParts(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	body := `{"candidates":[{"content":{"parts":[{"text":"internal reasoning","thought":true},{"text":"42"}]},"finishReason":"STOP"}]}`
	text, err := parseGeminiUpstreamHealthStream(strings.NewReader(body), time.Now(), &result)
	require.NoError(t, err)
	require.Equal(t, "42", text)
}

func TestParseGeminiUpstreamHealthResponseRejectsOpenAIShape(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	_, err := parseGeminiUpstreamHealthStream(strings.NewReader(`data: {"choices":[{"delta":{"content":"42"}}]}`+"\n\n"), time.Now(), &result)
	require.Error(t, err)
	require.Equal(t, "invalid_response", result.Result)
	require.Equal(t, "probe_protocol_mismatch", result.Reason)
}

func TestParseGeminiUpstreamHealthResponseRejectsTextWithoutFinishReason(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	text, err := parseGeminiUpstreamHealthStream(strings.NewReader(`data: {"candidates":[{"content":{"parts":[{"text":"42"}]}}]}`+"\n\n"), time.Now(), &result)
	require.Error(t, err)
	require.Equal(t, "42", text)
	require.Equal(t, "incomplete", result.Result)
	require.Equal(t, "probe_incomplete_stream", result.Reason)
}

func TestParseGeminiUpstreamHealthJSONAllowsOmittedFinishReason(t *testing.T) {
	result := UpstreamHealthProbeResult{}
	text, err := parseGeminiUpstreamHealthStream(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"42"}]}}]}`), time.Now(), &result)
	require.NoError(t, err)
	require.Equal(t, "42", text)
	require.Equal(t, "completed", result.FinishReason)
}

func TestParseGeminiUpstreamHealthResponseClassifiesMissingCandidates(t *testing.T) {
	for _, body := range []string{
		`{"safetyRatings":[]}`,
		`{"candidates":[]}`,
		`{"candidates":[`,
	} {
		result := UpstreamHealthProbeResult{}
		_, err := parseGeminiUpstreamHealthStream(strings.NewReader(body), time.Now(), &result)
		require.Error(t, err)
		require.Equal(t, "invalid_response", result.Result, body)
		require.Equal(t, "probe_response_mismatch", result.Reason, body)
	}
}

func TestRunGeminiUpstreamHealthProbeRejectsUnsupportedMappedModelBeforeRequest(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := &Account{ID: 101, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "gemini-secret", "base_url": "https://gemini.example",
		"model_mapping": map[string]any{"gemini-image": "gemini-2.5-flash-image"},
	}}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-image")
	require.Error(t, err)
	require.Equal(t, "unsupported_model", result.Result)
	require.Equal(t, "probe_model_unsupported", result.Reason)
	require.Nil(t, upstream.req)
}

func TestRunGeminiOAuthUpstreamHealthProbeUsesBearerGeminiEndpoint(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 103, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{
		"access_token": "oauth-secret", "base_url": "https://gemini.example",
	}}
	svc := &AccountTestService{
		geminiTokenProvider: &GeminiTokenProvider{},
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-probe")
	require.NoError(t, err)
	require.Equal(t, "success", result.Result)
	require.Equal(t, upstreamHealthProbeProtocolGemini, result.Protocol)
	require.Equal(t, "https://gemini.example/v1beta/models/gemini-probe:streamGenerateContent?alt=sse", upstream.req.URL.String())
	require.Equal(t, "Bearer oauth-secret", upstream.req.Header.Get("Authorization"))
	require.Empty(t, upstream.req.Header.Get("x-goog-api-key"))
}

func TestRunGeminiOAuthUpstreamHealthProbeMissingTokenIsConfigurationFailure(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 104, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{
		"base_url": "https://gemini.example",
	}}
	svc := &AccountTestService{
		geminiTokenProvider: &GeminiTokenProvider{},
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-probe")
	require.Error(t, err)
	require.Equal(t, "configuration_error", result.Result)
	require.Equal(t, "probe_credentials_missing", result.Reason)
	require.Nil(t, upstream.req)
}

func TestRunGeminiCodeAssistOAuthUpstreamHealthProbeUsesWrappedRequest(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 105, Platform: PlatformGemini, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{
		"access_token": "oauth-secret", "project_id": "project-105",
	}}
	svc := &AccountTestService{
		geminiTokenProvider: &GeminiTokenProvider{},
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-probe")
	require.NoError(t, err)
	require.Equal(t, "success", result.Result)
	require.Equal(t, upstreamHealthProbeProtocolGemini, result.Protocol)
	require.Equal(t, strings.TrimRight(geminicli.GeminiCliBaseURL, "/")+"/v1internal:streamGenerateContent?alt=sse", upstream.req.URL.String())
	require.Equal(t, "Bearer oauth-secret", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "project-105", gjson.GetBytes(upstream.body, "project").String())
	require.Equal(t, "gemini-probe", gjson.GetBytes(upstream.body, "model").String())
	require.True(t, gjson.GetBytes(upstream.body, "request.contents").IsArray())
	require.Empty(t, upstream.req.Header.Get("x-goog-api-key"))
}

func TestRunGeminiServiceAccountUpstreamHealthProbeMissingCredentialsIsConfigurationFailure(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 106, Platform: PlatformGemini, Type: AccountTypeServiceAccount, Concurrency: 1, Credentials: map[string]any{}}
	svc := &AccountTestService{
		geminiTokenProvider: &GeminiTokenProvider{},
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-probe")
	require.Error(t, err)
	require.Equal(t, "configuration_error", result.Result)
	require.Equal(t, "probe_credentials_missing", result.Reason)
	require.Nil(t, upstream.req)
}

func TestRunGeminiServiceAccountUpstreamHealthProbeUsesVertexEndpoint(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{}
	account := &Account{ID: 107, Platform: PlatformGemini, Type: AccountTypeServiceAccount, Concurrency: 1, Credentials: map[string]any{
		"service_account_json": `{"type":"service_account","project_id":"vertex-project","private_key_id":"key-107","private_key":"not-used-in-cache-test","client_email":"probe@vertex-project.iam.gserviceaccount.com"}`,
		"vertex_location":      "us-central1",
	}}
	svc := &AccountTestService{
		geminiTokenProvider: &GeminiTokenProvider{tokenCache: &upstreamHealthProbeGeminiTokenCache{token: "vertex-token"}},
		httpUpstream:        upstream,
		cfg:                 &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-probe")
	require.NoError(t, err)
	require.Equal(t, "success", result.Result)
	require.Equal(t, "https://us-central1-aiplatform.googleapis.com/v1/projects/vertex-project/locations/us-central1/publishers/google/models/gemini-probe:streamGenerateContent?alt=sse", upstream.req.URL.String())
	require.Equal(t, "Bearer vertex-token", upstream.req.Header.Get("Authorization"))
	require.Empty(t, upstream.req.Header.Get("x-goog-api-key"))
}

func TestGeminiHTTPModelNotFoundDoesNotAdvanceProbeGuard(t *testing.T) {
	body := []byte(`{"error":{"status":"NOT_FOUND","message":"models/gemini-3.7-flash is not found for this API version"}}`)
	reason := classifyUpstreamHealthProbeHTTPResponse(upstreamHealthProbeProtocolGemini, http.StatusNotFound, body)
	require.Equal(t, "probe_model_unsupported", reason)
	require.False(t, probeFailureCountsTowardScheduling("404", reason))
}

func TestRunGeminiProbeClassifiesStandardModelNotFoundResponse(t *testing.T) {
	upstream := &upstreamHealthProbeHTTPStub{
		statusCode: http.StatusNotFound,
		stream:     `{"error":{"status":"NOT_FOUND","message":"models/gemini-3.7-flash is not found for API version v1beta, or is not supported for generateContent"}}`,
	}
	svc := &AccountTestService{
		httpUpstream: upstream,
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
	}
	account := &Account{ID: 108, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
		"api_key": "gemini-secret", "base_url": "https://gemini.example",
	}}
	result, err := svc.RunUpstreamHealthProbe(context.Background(), account, "gemini-3.7-flash")
	require.Error(t, err)
	require.Equal(t, "404", result.Result)
	require.Equal(t, "probe_model_unsupported", result.Reason)
	require.NotNil(t, result.HTTPStatus)
	require.Equal(t, http.StatusNotFound, *result.HTTPStatus)
	require.False(t, probeFailureCountsTowardScheduling(result.Result, result.Reason))
}

func TestGeminiHTTPNotFoundUnknownEndpointKeepsHTTPFailure(t *testing.T) {
	body := []byte(`{"error":{"status":"NOT_FOUND","message":"resource endpoint not found"}}`)
	reason := classifyUpstreamHealthProbeHTTPResponse(upstreamHealthProbeProtocolGemini, http.StatusNotFound, body)
	require.Equal(t, "upstream_http_error", reason)
	require.True(t, probeFailureCountsTowardScheduling("404", reason))
}

func TestOpenAICompatibleModelNotFoundDoesNotAdvanceProbeGuard(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"error":{"code":"model_not_found","message":"The model does not exist"}}`),
		[]byte(`{"error":{"type":"invalid_model","message":"unknown model kimi-k2.6"}}`),
		[]byte(`{"message":"model glm-5.2 is not supported"}`),
	} {
		reason := classifyUpstreamHealthProbeHTTPResponse(upstreamHealthProbeProtocolOpenAIChat, http.StatusNotFound, body)
		require.Equal(t, "probe_model_unsupported", reason, string(body))
		require.False(t, probeFailureCountsTowardScheduling("404", reason), string(body))
	}
}

func TestEndpointNotFoundRemainsHTTPFailure(t *testing.T) {
	body := []byte(`{"error":{"message":"route /v1/chat/completions not found"}}`)
	reason := classifyUpstreamHealthProbeHTTPResponse(upstreamHealthProbeProtocolOpenAIChat, http.StatusNotFound, body)
	require.Equal(t, "upstream_http_error", reason)
}

func TestGeminiProbeBuildErrorClassification(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "missing token", err: errors.New("failed to get access token"), reason: "probe_credentials_missing"},
		{name: "invalid base URL", err: errors.New("base URL rejected by URL security policy"), reason: "probe_base_url_invalid"},
		{name: "request construction", err: errors.New("invalid request method"), reason: "probe_request_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason := classifyGeminiProbeBuildError(&Account{Type: AccountTypeOAuth}, tt.err)
			require.Equal(t, "configuration_error", status)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestTextProbeRejectsImageModelsBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		platform string
		model    string
	}{
		{platform: PlatformOpenAI, model: "gpt-image-1"},
		{platform: PlatformGrok, model: "grok-imagine-image"},
		{platform: PlatformGrok, model: "grok-imagine-video-1.5"},
		{platform: PlatformGemini, model: "gemini-2.5-flash-image"},
		{platform: PlatformZhipu, model: "cogview-3"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			upstream := &upstreamHealthProbeHTTPStub{}
			account := &Account{ID: 102, Platform: tc.platform, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{
				"api_key": "secret", "base_url": "https://probe.example",
			}}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
			result, err := svc.RunUpstreamHealthProbe(context.Background(), account, tc.model)
			require.Error(t, err)
			require.Equal(t, "probe_model_unsupported", result.Reason)
			require.Nil(t, upstream.req)
		})
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
