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

	upstreamHealthProbeProtocolOpenAI     = "openai_responses"
	upstreamHealthProbeProtocolOpenAIChat = "openai_chat_completions"
	upstreamHealthProbeProtocolAnthropic  = "anthropic_messages"
	upstreamHealthProbeProtocolGemini     = "gemini_stream_generate_content"
	upstreamConfidencePromptVersion       = "openai-confidence-v1"
	upstreamConfidenceDefaultEffort       = "high"
)

// UpstreamHealthProbeResult is deliberately independent from account-test
// results. Timings are measured against the actual provider stream: TTFT starts
// at request dispatch and ends only at the first non-empty text delta.
type UpstreamHealthProbeResult struct {
	Model                   string
	Protocol                string
	Result                  string
	Reason                  string
	HTTPStatus              *int
	TTFTMs                  *int64
	DurationMs              *int64
	InputTokens             *int64
	OutputTokens            *int64
	OutputTPS               *float64
	FinishReason            string
	ConfidenceScore         *int
	ConfidencePromptVersion string
	RequestedEffort         string
	ReasoningTokens         *int64
	ConfidenceChecks        map[string]int
	ConfidenceStatus        string
	confidenceChallenge     *upstreamHealthChallenge
	confidenceThreshold     int
}

type upstreamHealthChallenge struct {
	Prompt         string
	LegacyPrompt   string
	Expected       string
	Marker         string
	Constraint     string
	ContextNeedle  string
	ContextEnabled bool
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
	markerBytes := make([]byte, 6)
	if _, err := cryptorand.Read(markerBytes); err != nil {
		return upstreamHealthChallenge{}, err
	}
	marker := fmt.Sprintf("probe-%x", markerBytes)
	constraint := "constraint-ok"
	prompt := fmt.Sprintf("What is %d + %d? Also return ONLY this JSON object with string marker %q, calculation as a number, constraint %q, and context_check %q. No markdown or extra text.", a, b, marker, constraint, "not_requested")
	return upstreamHealthChallenge{Prompt: prompt, LegacyPrompt: fmt.Sprintf("What is %d + %d? Reply with only the decimal number.", a, b), Expected: fmt.Sprintf("%d", a+b), Marker: marker, Constraint: constraint, ContextNeedle: "not_requested"}, nil
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
	case PlatformKimi, PlatformZhipu, PlatformDeepseek:
		// These providers are OpenAI-compatible but their default contract is
		// Chat Completions, not OpenAI Responses. Keep the probe protocol
		// explicit so platform-specific base URLs and credentials are preserved.
		if account.GetAPIProtocol() == APIProtocolAnthropic {
			return s.runAnthropicUpstreamHealthProbe(ctx, account, result, challenge)
		}
		if account.GetAPIProtocol() == APIProtocolResponses {
			return failUpstreamHealthProbe(result, "unsupported_protocol", "probe_protocol_unsupported", errors.New("configured responses protocol is not supported by the generic CN provider probe"))
		}
		return s.runOpenAIChatCompletionsUpstreamHealthProbe(ctx, account, result, challenge)
	default:
		return failUpstreamHealthProbe(result, "unsupported_platform", "probe_platform_unsupported", errors.New("platform does not support active probing"))
	}
}

func (s *AccountTestService) runOpenAIChatCompletionsUpstreamHealthProbe(ctx context.Context, account *Account, result UpstreamHealthProbeResult, challenge upstreamHealthChallenge) (UpstreamHealthProbeResult, error) {
	result.Protocol = upstreamHealthProbeProtocolOpenAIChat
	result.Model = account.GetMappedModel(result.Model)
	authToken := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("OpenAI-compatible API key is missing"))
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetOpenAIFormatBaseURL())
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	payload, err := json.Marshal(map[string]any{
		"model":      result.Model,
		"messages":   []map[string]string{{"role": "user", "content": challenge.Prompt}},
		"max_tokens": upstreamHealthProbeMaxOutputTokens,
		"stream":     true,
	})
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildOpenAIChatCompletionsURL(baseURL), bytes.NewReader(payload))
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	account.ApplyHeaderOverrides(req.Header)
	return s.executeUpstreamHealthProbe(req, account, result, challenge.Expected, parseOpenAIChatCompletionsUpstreamHealthStream)
}

func (s *AccountTestService) runOpenAIUpstreamHealthProbe(ctx context.Context, account *Account, result UpstreamHealthProbeResult, challenge upstreamHealthChallenge) (UpstreamHealthProbeResult, error) {
	result.Protocol = upstreamHealthProbeProtocolOpenAI
	result.Model = account.GetMappedModel(result.Model)
	result.ConfidencePromptVersion = upstreamConfidencePromptVersion
	result.RequestedEffort = upstreamConfidenceDefaultEffort
	result.confidenceThreshold = 70
	confidenceEnabled := true
	if s.settingService != nil {
		if configured, loadErr := s.settingService.GetUpstreamConfidenceProbeSettings(ctx); loadErr == nil {
			confidenceEnabled = configured.Enabled
			result.RequestedEffort = configured.ReasoningEffort
			result.ConfidencePromptVersion = configured.PromptVersion
			result.confidenceThreshold = configured.QualityDegradeThreshold
			challenge.ContextEnabled = configured.LongContextEnabled
			if challenge.ContextEnabled {
				challenge.ContextNeedle = fmt.Sprintf("needle-%d", configured.LongContextMaxTokens)
				challenge.Prompt += fmt.Sprintf(" Include context_check %q.", challenge.ContextNeedle)
			}
		}
	}
	if confidenceEnabled {
		result.ConfidenceScore = probeIntPtr(0)
		result.ConfidenceStatus = "failed"
		result.confidenceChallenge = &challenge
	} else {
		result.ConfidenceStatus = "disabled"
		result.confidenceChallenge = nil
	}
	apiKey := strings.TrimSpace(account.GetOpenAIApiKey())
	if apiKey == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("OpenAI API key is missing"))
	}
	baseURL, err := s.validateUpstreamBaseURL(account.GetOpenAIBaseURL())
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	instructions := "Complete the supplied verification task. Return only the requested JSON object with no explanation."
	input := challenge.Prompt
	payloadValue := map[string]any{"model": result.Model, "instructions": instructions, "input": input, "max_output_tokens": upstreamHealthProbeMaxOutputTokens, "stream": true}
	if !confidenceEnabled {
		instructions = "Solve the arithmetic challenge and return only the decimal answer, with no explanation."
		input = challenge.LegacyPrompt
		payloadValue = map[string]any{"model": result.Model, "instructions": instructions, "input": input, "max_output_tokens": upstreamHealthProbeMaxOutputTokens, "stream": true}
	} else {
		payloadValue["reasoning"] = map[string]string{"effort": result.RequestedEffort}
	}
	payload, err := json.Marshal(payloadValue)
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
	expected := challenge.Expected
	if confidenceEnabled {
		expected = ""
	}
	return s.executeUpstreamHealthProbe(req, account, result, expected, parseOpenAIUpstreamHealthStream)
}

func probeIntPtr(v int) *int { return &v }

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
	if result.Protocol == upstreamHealthProbeProtocolOpenAI && result.confidenceChallenge != nil {
		if result.ConfidenceScore == nil {
			zero := 0
			result.ConfidenceScore = &zero
		}
		if result.ConfidenceStatus == "" {
			result.ConfidenceStatus = "failed"
		}
	} else if strings.TrimSpace(text) != expected {
		return failUpstreamHealthProbe(result, "invalid_response", "probe_response_mismatch", errors.New("probe challenge response did not match"))
	}
	result.Result = "success"
	result.Reason = "probe_succeeded"
	if result.Protocol == upstreamHealthProbeProtocolOpenAI && result.ConfidenceScore != nil {
		threshold := result.confidenceThreshold
		if threshold <= 0 {
			threshold = 70
		}
		if *result.ConfidenceScore < threshold {
			result.ConfidenceStatus = "degraded"
		} else {
			result.ConfidenceStatus = "ok"
		}
	}
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
					InputTokens         int64 `json:"input_tokens"`
					OutputTokens        int64 `json:"output_tokens"`
					OutputTokensDetails struct {
						ReasoningTokens int64 `json:"reasoning_tokens"`
					} `json:"output_tokens_details"`
					CompletionTokensDetails struct {
						ReasoningTokens int64 `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
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
			reasoningTokens := event.Response.Usage.OutputTokensDetails.ReasoningTokens
			if reasoningTokens <= 0 {
				reasoningTokens = event.Response.Usage.CompletionTokensDetails.ReasoningTokens
			}
			if reasoningTokens > 0 {
				result.ReasoningTokens = &reasoningTokens
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
	if result.confidenceChallenge != nil {
		score, checks := scoreOpenAIConfidenceOutput(output.String(), *result.confidenceChallenge)
		result.ConfidenceScore = &score
		result.ConfidenceChecks = checks
		threshold := result.confidenceThreshold
		if threshold <= 0 {
			threshold = 70
		}
		if score < threshold {
			result.ConfidenceStatus = "degraded"
		} else {
			result.ConfidenceStatus = "ok"
		}
	}
	return output.String(), nil
}

// scoreOpenAIConfidenceOutput validates the single composite task. Each
// request produces exactly one score; failures are represented by score 0.
func scoreOpenAIConfidenceOutput(raw string, challenge upstreamHealthChallenge) (int, map[string]int) {
	checks := map[string]int{"completion": 10, "calculation": 0, "marker_constraint": 0, "schema": 0, "format": 0}
	trimmed := strings.TrimSpace(raw)
	// Keep compatibility with older fake-upstream fixtures while all real
	// OpenAI probes use the strict JSON contract below.
	if trimmed == challenge.Expected {
		checks["calculation"], checks["marker_constraint"], checks["schema"], checks["format"] = 35, 25, 20, 10
		return 100, checks
	}
	var value struct {
		Marker       string      `json:"marker"`
		Calculation  json.Number `json:"calculation"`
		Constraint   string      `json:"constraint"`
		ContextCheck string      `json:"context_check"`
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil || json.Unmarshal([]byte(trimmed), &rawFields) != nil {
		return 10, checks
	}
	checks["format"] = 10
	_, markerOK := rawFields["marker"]
	_, calcOK := rawFields["calculation"]
	_, constraintOK := rawFields["constraint"]
	_, contextOK := rawFields["context_check"]
	if markerOK && calcOK && constraintOK && contextOK && value.Marker != "" && value.Constraint != "" && value.ContextCheck != "" && value.Calculation != "" {
		checks["schema"] = 20
	}
	if value.Calculation == json.Number(challenge.Expected) {
		checks["calculation"] = 35
	}
	if value.Marker == challenge.Marker && value.Constraint == challenge.Constraint && value.ContextCheck == challenge.ContextNeedle {
		checks["marker_constraint"] = 25
	}
	return checks["completion"] + checks["calculation"] + checks["marker_constraint"] + checks["schema"] + checks["format"], checks
}

func parseOpenAIChatCompletionsUpstreamHealthStream(reader io.Reader, started time.Time, result *UpstreamHealthProbeResult) (string, error) {
	var output strings.Builder
	completed := false
	err := scanUpstreamHealthSSE(reader, func(data []byte) (bool, error) {
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			return false, fmt.Errorf("decode OpenAI Chat Completions probe event: %w", err)
		}
		for _, choice := range event.Choices {
			content := choice.Delta.Content
			if content == "" {
				content = choice.Message.Content
			}
			if strings.TrimSpace(content) != "" {
				setUpstreamHealthProbeTTFT(result, started)
				output.WriteString(content)
			}
			if strings.TrimSpace(choice.FinishReason) != "" {
				completed = true
				result.FinishReason = strings.TrimSpace(choice.FinishReason)
			}
		}
		if event.Usage.PromptTokens > 0 {
			value := event.Usage.PromptTokens
			result.InputTokens = &value
		}
		if event.Usage.CompletionTokens > 0 {
			value := event.Usage.CompletionTokens
			result.OutputTokens = &value
		}
		return completed, nil
	})
	if err != nil {
		return output.String(), err
	}
	if !completed {
		result.Result = "incomplete"
		result.Reason = "probe_incomplete_stream"
		return output.String(), errors.New("OpenAI Chat Completions probe stream ended before finish_reason")
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
