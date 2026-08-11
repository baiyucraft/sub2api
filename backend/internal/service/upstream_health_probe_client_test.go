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
				require.Equal(t, int64(50), gjson.GetBytes(body, "max_output_tokens").Int())
				require.NotEmpty(t, gjson.GetBytes(body, "instructions").String())
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

	upstream := &upstreamHealthProbeHTTPStub{stream: "data: {\"type\":\"response.failed\"}\n\n"}
	result, err := (&AccountTestService{httpUpstream: upstream, cfg: cfg}).RunUpstreamHealthProbe(context.Background(), account, "gpt-probe")
	require.Error(t, err)
	require.Equal(t, "failed", result.Result)
	require.Equal(t, "probe_response_failed", result.Reason)

	upstream = &upstreamHealthProbeHTTPStub{statusCode: http.StatusTooManyRequests, stream: `{}`}
	result, err = (&AccountTestService{httpUpstream: upstream, cfg: cfg}).RunUpstreamHealthProbe(context.Background(), account, "gpt-probe")
	require.Error(t, err)
	require.Equal(t, "429", result.Result)
	require.Equal(t, "capacity_limited", result.Reason)
	require.NotNil(t, result.DurationMs)
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
