package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
)

type distributionProbeContextKey struct{}
type distributionProbeClaim func(context.Context, string) (*DistributionAttempt, error)

var errDistributionProbeBusy = errors.New("confidence distribution probe is already in progress")

func distributionAccountProtocol(account *Account) string {
	if !openai_compat.ShouldUseResponsesAPI(account.Extra) {
		return "chat_completions"
	}
	return "responses"
}

// Hash configuration only; neither credentials nor the hash input are persisted
// or exposed in the public evidence. Changes create an independent series.
func distributionAccountFingerprint(account *Account) string {
	protocol := distributionAccountProtocol(account)
	value := map[string]any{
		"account_id": account.ID, "key_id": account.UpstreamKeyID,
		"protocol": protocol, "credentials": account.Credentials,
		"effective_model": account.GetMappedModel(UpstreamConfidenceDistributionClaimedModel),
		"contract":        UpstreamConfidenceDistributionPromptVersion,
		"baseline":        DistributionBaselineVersion(protocol), "proxy_id": account.ProxyID,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// The claim is made after all local request checks, immediately before dispatch.
// This consumes one durable slot, with no retry or alternate protocol request.
func (s *AccountTestService) runOpenAIDistributionHealthProbe(ctx context.Context, account *Account) (UpstreamHealthProbeResult, error) {
	protocol := distributionAccountProtocol(account)
	result := UpstreamHealthProbeResult{Model: account.GetMappedModel(UpstreamConfidenceDistributionClaimedModel),
		ConfidencePromptVersion: UpstreamConfidenceDistributionPromptVersion, RequestedEffort: "low"}
	result.Protocol = upstreamHealthProbeProtocolOpenAI
	apiKey, baseURL := account.GetOpenAIApiKey(), account.GetOpenAIBaseURL()
	parser := parseOpenAIUpstreamHealthStream
	if protocol == "chat_completions" {
		result.Protocol = upstreamHealthProbeProtocolOpenAIChat
		apiKey, baseURL = account.GetOpenAIProtocolAPIKey(), account.GetOpenAIFormatBaseURL()
		parser = parseOpenAIChatCompletionsUpstreamHealthStream
	}
	if strings.TrimSpace(result.Model) == "" || !account.IsModelSupported(UpstreamConfidenceDistributionClaimedModel) || isTextProbeUnsupportedModel(result.Model) {
		return failUpstreamHealthProbe(result, "unsupported_model", "probe_model_unsupported", errors.New("account does not support the Sol distribution probe model"))
	}
	if strings.TrimSpace(apiKey) == "" {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_credentials_missing", errors.New("OpenAI probe credentials are missing"))
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return failUpstreamHealthProbe(result, "configuration_error", "probe_base_url_invalid", err)
	}
	endpoint := buildOpenAIResponsesURL(validatedURL)
	if protocol == "chat_completions" {
		endpoint = buildOpenAIChatCompletionsURL(validatedURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	claim, ok := ctx.Value(distributionProbeContextKey{}).(distributionProbeClaim)
	if !ok {
		return result, errors.New("persistent confidence distribution collector is unavailable")
	}
	attempt, err := claim(ctx, protocol)
	if err != nil {
		return result, err
	}
	if attempt == nil {
		return result, errDistributionProbeBusy
	}
	result.distributionAttempt = attempt
	result.ConfidenceProbeKind = attempt.ProbeID
	result.ConfidenceEvidence = map[string]any{
		"kind": "distribution", "probe_id": attempt.ProbeID, "series_id": attempt.SeriesID,
		"sequence": attempt.Sequence, "claimed_model": UpstreamConfidenceDistributionClaimedModel,
		"requested_model": UpstreamConfidenceDistributionClaimedModel, "effective_model": result.Model,
		"requested_effort": "low", "baseline_version": DistributionBaselineVersion(protocol),
	}
	messages := []map[string]string{{"role": "system", "content": "."}, {"role": "user", "content": DistributionPrompt(attempt.ProbeID)}}
	payload := map[string]any{"model": result.Model, "stream": true}
	if protocol == "responses" {
		payload["input"], payload["store"] = messages, false
		payload["reasoning"], payload["max_output_tokens"] = map[string]string{"effort": "low"}, 128
	} else {
		payload["messages"], payload["max_tokens"], payload["reasoning_effort"] = messages, 128, "low"
		payload["stream_options"] = map[string]bool{"include_usage": true}
	}
	body, _ := json.Marshal(payload) // All values are fixed primitives.
	req.Body, req.ContentLength = http.NoBody, int64(len(body))
	req.Body = io.NopCloser(bytes.NewReader(body))
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI)))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if protocol == "responses" {
		applyOpenAICodexProbeHeaders(req.Header)
	}
	account.ApplyHeaderOverrides(req.Header)
	result, probeErr := s.executeUpstreamHealthProbe(req, account, result, "", parser)
	if probeErr != nil {
		result.distributionSample.Valid = false
		result.distributionSample.Answer = ""
		result.distributionSample.Reason = result.Reason
		if result.distributionSample.Reason == "" {
			result.distributionSample.Reason = "request_failed"
		}
	}
	result.distributionSample.Sequence = attempt.Sequence
	result.distributionSample.ProbeID = attempt.ProbeID
	result.distributionSample.ObservedAt = time.Now().UTC()
	result.ConfidenceChecks = map[string]int{"attempted": 1, "valid_completed": 0}
	if result.distributionSample.Valid {
		result.ConfidenceChecks["valid_completed"] = 1
	}
	result.ConfidenceEvidence["normalized_value"] = result.distributionSample.Answer
	result.ConfidenceEvidence["valid"] = result.distributionSample.Valid
	result.ConfidenceEvidence["failure_reason"] = result.distributionSample.Reason
	return result, probeErr
}
