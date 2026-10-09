package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type distributionProbeContextKey struct{}
type distributionProbeClaim func(context.Context, ConfidenceDistributionIdentity) (*DistributionAttempt, error)

var errDistributionProbeBusy = errors.New("confidence distribution probe is already in progress")

// The claim is made after all local request checks, immediately before dispatch.
// This consumes one durable slot, with no retry or alternate protocol request.
func (s *AccountTestService) runOpenAIDistributionHealthProbe(ctx context.Context, account *Account) (UpstreamHealthProbeResult, error) {
	result := UpstreamHealthProbeResult{Model: account.GetMappedModel(UpstreamConfidenceDistributionClaimedModel),
		ConfidencePromptVersion: UpstreamConfidenceDistributionPromptVersion, RequestedEffort: "low"}
	result.Protocol = upstreamHealthProbeProtocolOpenAI
	if distributionAccountProtocol(account) == "chat_completions" {
		result.Protocol = upstreamHealthProbeProtocolOpenAIChat
	}
	spec, err := s.resolveDistributionRequest(account)
	if err != nil {
		var configErr *distributionConfigurationError
		if errors.As(err, &configErr) {
			return failUpstreamHealthProbe(result, configErr.result, configErr.reason, configErr.err)
		}
		return result, err
	}
	protocol := spec.identity.Protocol
	result.Model = spec.model
	parser := parseOpenAIUpstreamHealthStream
	if protocol == "chat_completions" {
		result.Protocol = upstreamHealthProbeProtocolOpenAIChat
		parser = parseOpenAIChatCompletionsUpstreamHealthStream
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, spec.endpoint, nil)
	if err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	claim, ok := ctx.Value(distributionProbeContextKey{}).(distributionProbeClaim)
	if !ok {
		return result, errors.New("persistent confidence distribution collector is unavailable")
	}
	attempt, err := claim(ctx, spec.identity)
	if err != nil {
		return result, err
	}
	if attempt == nil {
		return result, errDistributionProbeBusy
	}
	result.distributionAttempt = attempt
	if err := spec.pinClientVersion(attempt.ClientVersion); err != nil {
		return failUpstreamHealthProbe(result, "request_error", "probe_request_invalid", err)
	}
	req.Header = spec.headers.Clone()
	if protocol == "responses" {
		if _, overridden := spec.account.HeaderOverrideValue("x-codex-window-id"); !overridden {
			req.Header.Set("X-Codex-Window-ID", uuid.NewString())
		}
	}
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
	result, probeErr := s.executeUpstreamHealthProbe(req, &spec.account, result, "", parser)
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
