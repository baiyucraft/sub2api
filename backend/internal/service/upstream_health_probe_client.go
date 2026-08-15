package service

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

const (
	upstreamHealthProbeMaxOutputTokens = 50
	upstreamHealthProbeBodyLimit       = 8 << 10

	upstreamHealthProbeProtocolOpenAI    = "openai_responses"
	upstreamHealthProbeProtocolAnthropic = "anthropic_messages"
	upstreamHealthProbeProtocolGemini    = "gemini_stream_generate_content"
)

// UpstreamHealthProbeResult is deliberately independent from account-test
// results. Timings are measured against the actual provider stream: TTFT starts
// at request dispatch and ends only at the first non-empty text delta.
type UpstreamHealthProbeResult struct {
	Model        string
	Protocol     string
	Result       string
	Reason       string
	HTTPStatus   *int
	TTFTMs       *int64
	DurationMs   *int64
	InputTokens  *int64
	OutputTokens *int64
	OutputTPS    *float64
	FinishReason string
}

type upstreamHealthChallenge struct {
	Prompt   string
	Expected string
}

func newUpstreamHealthChallenge() (upstreamHealthChallenge, error) {
	left, err := cryptorand.Int(cryptorand.Reader, big.NewInt(80))
	if err != nil {
		return upstreamHealthChallenge{}, err
	}
	right, err := cryptorand.Int(cryptorand.Reader, big.NewInt(80))
	if err != nil {
		return upstreamHealthChallenge{}, err
	}
	a := left.Int64() + 10
	b := right.Int64() + 10
	return upstreamHealthChallenge{
		Prompt:   fmt.Sprintf("What is %d + %d? Reply with only the decimal number.", a, b),
		Expected: fmt.Sprintf("%d", a+b),
	}, nil
}

func (s *AccountTestService) RunUpstreamHealthProbe(ctx context.Context, account *Account, model string) (UpstreamHealthProbeResult, error) {
	result := UpstreamHealthProbeResult{Model: strings.TrimSpace(model)}
	if account == nil || account.Type != AccountTypeAPIKey {
		return failUpstreamHealthProbe(result, "unsupported_account", "probe_account_unsupported", errors.New("upstream health probes require an API key account"))
	}
	if s == nil || s.httpUpstream == nil {
		return failUpstreamHealthProbe(result, "unavailable", "probe_transport_unavailable", errors.New("upstream HTTP client is unavailable"))
	}
	challenge, err := newUpstreamHealthChallenge()
	if err != nil {
		return failUpstreamHealthProbe(result, "challenge_error", "probe_challenge_error", err)
	}

	switch strings.ToLower(strings.TrimSpace(account.Platform)) {
	case PlatformOpenAI:
		return s.runOpenAIUpstreamHealthProbe(ctx, account, result, challenge)
	case PlatformAnthropic:
		return s.runAnthropicUpstreamHealthProbe(ctx, account, result, challenge)
	case PlatformGemini:
		return s.runGeminiUpstreamHealthProbe(ctx, account, result, challenge)
	default:
		return failUpstreamHealthProbe(result, "unsupported_platform", "probe_platform_unsupported", errors.New("platform does not support active probing"))
	}
}

func (s *AccountTestService) runOpenAIUpstreamHealthProbe(ctx context.Context, account *Account, result UpstreamHealthProbeResult, challenge upstreamHealthChallenge) (UpstreamHealthProbeResult, error) {
	result.Protocol = upstreamHealthProbeProtocolOpenAI
	result.Model = account.GetMappedModel(result.Model)
	apiKey := strings.TrimSpace(account.GetOpenAIApiKey())
	if apiKey == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("OpenAI API key is missing"))
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	payload, err := json.Marshal(map[string]any{
		"model":             result.Model,
		"instructions":      "Solve the arithmetic challenge and return only the decimal answer, with no explanation.",
		"input":             challenge.Prompt,
		"max_output_tokens": upstreamHealthProbeMaxOutputTokens,
		"stream":            true,
	})
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIResponsesURL(baseURL), bytes.NewReader(payload))
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	applyOpenAICodexProbeHeaders(req.Header)
	account.ApplyHeaderOverrides(req.Header)
	return s.executeUpstreamHealthProbe(req, account, result, challenge.Expected, parseOpenAIUpstreamHealthStream)
}

func (s *AccountTestService) runAnthropicUpstreamHealthProbe(ctx context.Context, account *Account, result UpstreamHealthProbeResult, challenge upstreamHealthChallenge) (UpstreamHealthProbeResult, error) {
	result.Protocol = upstreamHealthProbeProtocolAnthropic
	result.Model = account.GetMappedModel(result.Model)
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("Anthropic API key is missing"))
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetBaseURL())
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	sessionID, err := generateSessionString()
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	payload, err := json.Marshal(map[string]any{
		"model": result.Model,
		"messages": []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": challenge.Prompt, "cache_control": map[string]string{"type": "ephemeral"}}},
		}},
		"system":      []map[string]any{{"type": "text", "text": claudeCodeSystemPrompt, "cache_control": map[string]string{"type": "ephemeral"}}},
		"metadata":    map[string]string{"user_id": sessionID},
		"max_tokens":  upstreamHealthProbeMaxOutputTokens,
		"temperature": 1,
		"stream":      true,
	})
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/messages?beta=true", bytes.NewReader(payload))
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	for key, value := range claude.DefaultHeaders {
		req.Header.Set(key, value)
	}
	req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
	setAnthropicAPIKeyAuthHeader(req.Header, account, apiKey)
	account.ApplyHeaderOverrides(req.Header)
	return s.executeUpstreamHealthProbe(req, account, result, challenge.Expected, parseAnthropicUpstreamHealthStream)
}

func (s *AccountTestService) runGeminiUpstreamHealthProbe(ctx context.Context, account *Account, result UpstreamHealthProbeResult, challenge upstreamHealthChallenge) (UpstreamHealthProbeResult, error) {
	result.Protocol = upstreamHealthProbeProtocolGemini
	result.Model = account.GetMappedModel(result.Model)
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("Gemini API key is missing"))
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetGeminiBaseURL(geminicli.AIStudioBaseURL))
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	fullURL, err := buildGeminiAIStudioModelActionURL(baseURL, result.Model, "streamGenerateContent", true)
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	payload, err := json.Marshal(map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": []map[string]any{{"text": challenge.Prompt}}}},
		"generationConfig": map[string]any{"maxOutputTokens": upstreamHealthProbeMaxOutputTokens},
	})
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(payload))
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
	account.ApplyHeaderOverrides(req.Header)
	return s.executeUpstreamHealthProbe(req, account, result, challenge.Expected, parseGeminiUpstreamHealthStream)
}

type upstreamHealthStreamParser func(io.Reader, time.Time, *UpstreamHealthProbeResult) (string, error)

func (s *AccountTestService) executeUpstreamHealthProbe(req *http.Request, account *Account, result UpstreamHealthProbeResult, expected string, parse upstreamHealthStreamParser) (UpstreamHealthProbeResult, error) {
	started := time.Now()
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	var profile *tlsfingerprint.Profile
	if s.tlsFPProfileService != nil {
		profile = s.tlsFPProfileService.ResolveTLSProfile(account)
	}
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, profile)
	if err != nil {
		setUpstreamHealthProbeDuration(&result, started)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(req.Context().Err(), context.DeadlineExceeded) {
			return failUpstreamHealthProbe(result, "timeout", "probe_timeout", err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(req.Context().Err(), context.Canceled) {
			return failUpstreamHealthProbe(result, "cancelled", "probe_cancelled", err)
		}
		return failUpstreamHealthProbe(result, "transport_error", "probe_transport_error", err)
	}
	if resp == nil {
		setUpstreamHealthProbeDuration(&result, started)
		return failUpstreamHealthProbe(result, "transport_error", "probe_transport_error", errors.New("upstream returned no response"))
	}
	defer func() { _ = resp.Body.Close() }()
	status := resp.StatusCode
	result.HTTPStatus = &status
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamHealthProbeBodyLimit))
		setUpstreamHealthProbeDuration(&result, started)
		reason := classifyUpstreamHealthProbeHTTPReason(status)
		if status == http.StatusForbidden && looksLikeGatewayIntercepted(body, resp.Header.Get("Content-Type")) {
			reason = "gateway_intercepted"
		}
		return failUpstreamHealthProbe(result, fmt.Sprintf("%d", status), reason, fmt.Errorf("upstream returned HTTP %d", status))
	}
	text, err := parse(resp.Body, started, &result)
	setUpstreamHealthProbeDuration(&result, started)
	if err != nil {
		if errors.Is(req.Context().Err(), context.DeadlineExceeded) {
			return failUpstreamHealthProbe(result, "timeout", "probe_timeout", req.Context().Err())
		}
		if errors.Is(req.Context().Err(), context.Canceled) {
			return failUpstreamHealthProbe(result, "cancelled", "probe_cancelled", req.Context().Err())
		}
		return result, err
	}
	if strings.TrimSpace(text) != expected {
		return failUpstreamHealthProbe(result, "invalid_response", "probe_response_mismatch", errors.New("probe challenge response did not match"))
	}
	result.Result = "success"
	result.Reason = "probe_succeeded"
	setUpstreamHealthProbeOutputTPS(&result)
	return result, nil
}

func looksLikeGatewayIntercepted(body []byte, contentType string) bool {
	if len(body) == 0 {
		return false
	}
	text := strings.ToLower(string(body))
	contentType = strings.ToLower(contentType)
	if !strings.Contains(contentType, "text/html") && !strings.Contains(text, "<html") && !strings.Contains(text, "<!doctype html") {
		return false
	}
	for _, marker := range []string{"cloudflare", "access denied", "request blocked", "web application firewall", "waf", "nginx"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(contentType, "text/html")
}

func classifyUpstreamHealthProbeHTTPReason(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "authentication_failed"
	case status == http.StatusTooManyRequests || status == 529:
		return "capacity_limited"
	case status >= http.StatusInternalServerError:
		return "upstream_server_error"
	default:
		return "upstream_http_error"
	}
}

func failUpstreamHealthProbe(result UpstreamHealthProbeResult, status, reason string, err error) (UpstreamHealthProbeResult, error) {
	result.Result = strings.TrimSpace(status)
	result.Reason = strings.TrimSpace(reason)
	if result.Result == "" {
		result.Result = "probe_failed"
	}
	if result.Reason == "" {
		result.Reason = "probe_failed"
	}
	return result, err
}

func setUpstreamHealthProbeDuration(result *UpstreamHealthProbeResult, started time.Time) {
	if result == nil {
		return
	}
	value := time.Since(started).Milliseconds()
	if value < 0 {
		value = 0
	}
	result.DurationMs = &value
}

func setUpstreamHealthProbeTTFT(result *UpstreamHealthProbeResult, started time.Time) {
	if result == nil || result.TTFTMs != nil {
		return
	}
	value := time.Since(started).Milliseconds()
	if value < 0 {
		value = 0
	}
	result.TTFTMs = &value
}

func setUpstreamHealthProbeOutputTPS(result *UpstreamHealthProbeResult) {
	if result == nil || result.OutputTokens == nil || result.TTFTMs == nil || result.DurationMs == nil || *result.OutputTokens <= 0 {
		return
	}
	generationMs := *result.DurationMs - *result.TTFTMs
	if generationMs <= 0 {
		return
	}
	value := float64(*result.OutputTokens) * 1000 / float64(generationMs)
	result.OutputTPS = &value
}

func scanUpstreamHealthSSE(reader io.Reader, handle func([]byte) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	dataLines := make([]string, 0, 1)
	dispatch := func() (bool, error) {
		if len(dataLines) == 0 {
			return false, nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if string(data) == "[DONE]" {
			return false, nil
		}
		return handle(data)
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			done, err := dispatch()
			if err != nil || done {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := dispatch()
	return err
}

func parseOpenAIUpstreamHealthStream(reader io.Reader, started time.Time, result *UpstreamHealthProbeResult) (string, error) {
	var output strings.Builder
	completed := false
	err := scanUpstreamHealthSSE(reader, func(data []byte) (bool, error) {
		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Status string `json:"status"`
				Usage  struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return false, fmt.Errorf("decode OpenAI probe event: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			if strings.TrimSpace(event.Delta) != "" {
				setUpstreamHealthProbeTTFT(result, started)
				output.WriteString(event.Delta)
			}
		case "response.completed", "response.done":
			completed = true
			result.FinishReason = strings.TrimSpace(event.Response.Status)
			if result.FinishReason == "" {
				result.FinishReason = "completed"
			}
			if event.Response.Usage.InputTokens > 0 {
				value := event.Response.Usage.InputTokens
				result.InputTokens = &value
			}
			if event.Response.Usage.OutputTokens > 0 {
				value := event.Response.Usage.OutputTokens
				result.OutputTokens = &value
			}
			return true, nil
		case "response.failed":
			result.Result = "failed"
			result.Reason = "probe_response_failed"
			return false, errors.New("OpenAI probe stream reported response.failed")
		case "response.incomplete":
			result.Result = "incomplete"
			result.Reason = "probe_response_incomplete"
			return false, errors.New("OpenAI probe stream reported response.incomplete")
		}
		return false, nil
	})
	if err != nil {
		if result.Result == "" {
			result.Result = "stream_error"
			result.Reason = "probe_stream_error"
		}
		return output.String(), err
	}
	if !completed {
		result.Result = "incomplete"
		result.Reason = "probe_incomplete_stream"
		return output.String(), errors.New("OpenAI probe stream ended without a terminal event")
	}
	return output.String(), nil
}

func parseAnthropicUpstreamHealthStream(reader io.Reader, started time.Time, result *UpstreamHealthProbeResult) (string, error) {
	var output strings.Builder
	completed := false
	err := scanUpstreamHealthSSE(reader, func(data []byte) (bool, error) {
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Message struct {
				Usage struct {
					InputTokens int64 `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return false, fmt.Errorf("decode Anthropic probe event: %w", err)
		}
		switch event.Type {
		case "message_start":
			if event.Message.Usage.InputTokens > 0 {
				value := event.Message.Usage.InputTokens
				result.InputTokens = &value
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && strings.TrimSpace(event.Delta.Text) != "" {
				setUpstreamHealthProbeTTFT(result, started)
				output.WriteString(event.Delta.Text)
			}
		case "message_delta":
			if strings.TrimSpace(event.Delta.StopReason) != "" {
				result.FinishReason = strings.TrimSpace(event.Delta.StopReason)
			}
			if event.Usage.OutputTokens > 0 {
				value := event.Usage.OutputTokens
				result.OutputTokens = &value
			}
		case "message_stop":
			completed = true
			return true, nil
		case "error":
			result.Result = "failed"
			result.Reason = "probe_response_failed"
			return false, errors.New("Anthropic probe stream reported an error")
		}
		return false, nil
	})
	if err != nil {
		if result.Result == "" {
			result.Result = "stream_error"
			result.Reason = "probe_stream_error"
		}
		return output.String(), err
	}
	if !completed {
		result.Result = "incomplete"
		result.Reason = "probe_incomplete_stream"
		return output.String(), errors.New("Anthropic probe stream ended without message_stop")
	}
	return output.String(), nil
}

func parseGeminiUpstreamHealthStream(reader io.Reader, started time.Time, result *UpstreamHealthProbeResult) (string, error) {
	var output strings.Builder
	err := scanUpstreamHealthSSE(reader, func(data []byte) (bool, error) {
		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata struct {
				PromptTokenCount     int64 `json:"promptTokenCount"`
				CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(data, &chunk); err != nil {
			return false, fmt.Errorf("decode Gemini probe event: %w", err)
		}
		for _, candidate := range chunk.Candidates {
			if strings.TrimSpace(candidate.FinishReason) != "" {
				result.FinishReason = strings.TrimSpace(candidate.FinishReason)
			}
			for _, part := range candidate.Content.Parts {
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				setUpstreamHealthProbeTTFT(result, started)
				output.WriteString(part.Text)
			}
		}
		if chunk.UsageMetadata.PromptTokenCount > 0 {
			value := chunk.UsageMetadata.PromptTokenCount
			result.InputTokens = &value
		}
		if chunk.UsageMetadata.CandidatesTokenCount > 0 {
			value := chunk.UsageMetadata.CandidatesTokenCount
			result.OutputTokens = &value
		}
		return false, nil
	})
	if err != nil {
		if result.Result == "" {
			result.Result = "stream_error"
			result.Reason = "probe_stream_error"
		}
		return output.String(), err
	}
	if result.TTFTMs == nil {
		result.Result = "incomplete"
		result.Reason = "probe_incomplete_stream"
		return output.String(), errors.New("Gemini probe stream ended without text")
	}
	return output.String(), nil
}
