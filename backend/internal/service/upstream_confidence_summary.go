package service

import (
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service/modeltrace_numeric"
)

const ConfidenceFusionVersion = 1

// ConfidenceSummary is the sole public model-identity diagnostic. Weights are
// engineering evidence priorities, never probabilities of backend identity.
type ConfidenceSummary struct {
	Status           string                                        `json:"status"`
	TargetModel      string                                        `json:"target_model"`
	EffectiveModel   string                                        `json:"effective_model"`
	CandidateModel   string                                        `json:"candidate_model,omitempty"`
	Reason           string                                        `json:"reason"`
	FusionVersion    int                                           `json:"fusion_version"`
	Inference        bool                                          `json:"inference"`
	Protocol         string                                        `json:"protocol,omitempty"`
	ObservedProtocol string                                        `json:"observed_protocol,omitempty"`
	ProtocolStatus   string                                        `json:"protocol_status,omitempty"`
	ProtocolReason   string                                        `json:"protocol_reason,omitempty"`
	Methods          map[string]ConfidenceFingerprintMethodSummary `json:"methods"`
}

func confidenceMethodWeight(method string) int {
	switch method {
	case "distribution":
		return 3
	case "modeltrace_numeric":
		return 2
	case "chanshui_behavioral":
		return 1
	}
	return 0
}

func confidenceMethodUnavailable(reason string) bool {
	if strings.HasSuffix(reason, "_unavailable") {
		return true
	}
	switch reason {
	case "no_matching_baseline", "probe_model_unsupported", "probe_credentials_missing", "probe_base_url_invalid", "probe_request_invalid", "evidence_target_mismatch":
		return true
	}
	return false
}

// The only spelling equivalence is an explicitly recognized provider prefix.
// Versions, dates, family names, and punctuation are not fuzzy aliases.
func canonicalConfidenceModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai/", "anthropic/", "google/", "deepseek/", "x-ai/", "z-ai/"} {
		if strings.HasPrefix(model, prefix) {
			return strings.TrimPrefix(model, prefix)
		}
	}
	return model
}

func qualifiedReferenceCandidate(method string, summary ConfidenceFingerprintMethodSummary) string {
	if summary.SampleCount != 3 || len(summary.Candidates) < 2 || summary.Status != "reference_only" {
		return ""
	}
	first, second := summary.Candidates[0], summary.Candidates[1]
	var top, next, threshold, margin float64
	switch method {
	case "modeltrace_numeric":
		top, next, threshold, margin = first.ClosedSetSupport, second.ClosedSetSupport, .80, .65
	case "chanshui_behavioral":
		top, next, threshold, margin = first.Score, second.Score, .88, .03
	default:
		return ""
	}
	if math.IsNaN(top) || math.IsInf(top, 0) || math.IsNaN(next) || math.IsInf(next, 0) || top < threshold || top-next < margin {
		return ""
	}
	return canonicalConfidenceModel(first.ModelID)
}

func confidenceReferenceCovers(method, candidate string) bool {
	candidate = canonicalConfidenceModel(candidate)
	if method == "modeltrace_numeric" {
		return modeltrace_numeric.HasTarget(candidate)
	}
	if method != "chanshui_behavioral" {
		return false
	}
	for _, prefix := range []string{"openai/", "anthropic/", "google/", "deepseek/", "x-ai/", "z-ai/"} {
		if HasChanshuiBehavioralBaseline(prefix + candidate) {
			return true
		}
	}
	return false
}

func BuildConfidenceSummary(distribution *UpstreamConfidenceDistribution, attribution *ConfidenceFingerprintAttribution) *ConfidenceSummary {
	if distribution == nil && attribution == nil {
		return nil
	}
	summary := &ConfidenceSummary{Status: "insufficient", Reason: "no_matching_baseline", FusionVersion: ConfidenceFusionVersion, Methods: map[string]ConfidenceFingerprintMethodSummary{}}
	if attribution != nil {
		summary.TargetModel, summary.EffectiveModel = attribution.TargetModel, attribution.Model
		summary.Protocol, summary.ObservedProtocol = attribution.Protocol, attribution.ObservedProtocol
		summary.ProtocolStatus, summary.ProtocolReason = attribution.ProtocolStatus, attribution.ProtocolReason
		for _, name := range []string{"modeltrace_numeric", "chanshui_behavioral"} {
			if method, ok := attribution.Methods[name]; ok {
				summary.Methods[name] = method
			}
		}
		if attribution.Reason == "fingerprint_read_unavailable" || attribution.Reason == "fingerprint_storage_unavailable" {
			summary.Reason = "confidence_read_unavailable"
		}
	}
	if summary.EffectiveModel == "" && distribution != nil {
		summary.EffectiveModel = distribution.ClaimedModel
	}
	if summary.TargetModel == "" {
		summary.TargetModel = summary.EffectiveModel
	}
	if distribution != nil {
		method := ConfidenceFingerprintMethodSummary{Status: distribution.Status, SampleCount: distribution.ValidSamples, MinimumSamples: UpstreamConfidenceDistributionWindowSize, BaselineVersion: distribution.BaselineVersion, Weight: 3}
		if distribution.Status == "match" || distribution.Status == "mismatch" {
			method.CandidateModel = canonicalConfidenceModel(distribution.ClosestModel)
			if method.CandidateModel == distributionOther {
				method.CandidateModel = "other"
			}
			if method.CandidateModel == "" && distribution.Status == "match" {
				method.CandidateModel = canonicalConfidenceModel(distribution.ClaimedModel)
			}
			method.Participating = method.CandidateModel != "" && distribution.ValidSamples >= UpstreamConfidenceDistributionWindowSize && canonicalConfidenceModel(distribution.ClaimedModel) == canonicalConfidenceModel(summary.TargetModel)
			if canonicalConfidenceModel(distribution.ClaimedModel) != canonicalConfidenceModel(summary.TargetModel) {
				method.Reason = "evidence_target_mismatch"
			}
			if method.Participating {
				method.EffectiveWeight = 3
			}
		} else if len(distribution.Reasons) > 0 {
			method.Reason = distribution.Reasons[0]
		}
		summary.Methods["distribution"] = method
	}
	if canonicalConfidenceModel(summary.TargetModel) != canonicalConfidenceModel(summary.EffectiveModel) {
		summary.Reason = "target_model_mismatch"
		for name, method := range summary.Methods {
			method.Participating = false
			method.EffectiveWeight = 0
			summary.Methods[name] = method
		}
		return summary
	}
	target := canonicalConfidenceModel(summary.TargetModel)
	votes := map[string]int{}
	collecting, ambiguous := false, false
	possibleWeight := 0
	for name, method := range summary.Methods {
		method.Weight = confidenceMethodWeight(name)
		if !confidenceMethodUnavailable(method.Reason) && (name == "distribution" && method.Status != "unavailable" && method.Status != "unknown" || name != "distribution" && confidenceReferenceCovers(name, target)) {
			possibleWeight += method.Weight
		}
		if name != "distribution" {
			// Both the selected target and any asserted candidate need exact coverage.
			if !confidenceReferenceCovers(name, target) || !confidenceReferenceCovers(name, method.CandidateModel) || method.ConfirmedRounds < 2 {
				method.Participating = false
			}
		}
		if confidenceMethodUnavailable(method.Reason) {
			method.Participating = false
		}
		method.EffectiveWeight = 0
		if method.Participating && method.CandidateModel != "" {
			method.CandidateModel = canonicalConfidenceModel(method.CandidateModel)
			method.EffectiveWeight = method.Weight
			votes[method.CandidateModel] += method.Weight
		} else {
			if method.Reason == "weak_evidence" || method.Reason == "invalid_round" || method.Status == "insufficient" && method.SampleCount >= method.MinimumSamples && method.MinimumSamples > 0 {
				ambiguous = true
			}
			if !confidenceMethodUnavailable(method.Reason) && (method.Reason == "awaiting_confirmation" || method.Status == "collecting" || method.SampleCount < method.MinimumSamples && method.MinimumSamples > 0 && method.Status != "unavailable" && method.Status != "unknown") {
				collecting = true
			}
		}
		summary.Methods[name] = method
	}
	if len(votes) > 1 {
		summary.Reason = "methods_conflict"
		return summary
	}
	for candidate, weight := range votes {
		if weight < 3 {
			continue
		}
		// A reference-only verdict needs the two distinct methods' agreement.
		d, hasDistribution := summary.Methods["distribution"]
		if !hasDistribution || !d.Participating {
			n, nok := summary.Methods["modeltrace_numeric"]
			b, bok := summary.Methods["chanshui_behavioral"]
			if !nok || !bok || !n.Participating || !b.Participating || n.CandidateModel != candidate || b.CandidateModel != candidate {
				continue
			}
			summary.Inference = true
		}
		summary.CandidateModel = candidate
		summary.Status, summary.Reason = "suspected", "evidence_supports_other"
		if candidate == target {
			summary.Status, summary.Reason = "match", "evidence_matches_target"
		}
		if summary.Inference {
			summary.Reason = "reference_consensus"
		}
		return summary
	}
	if ambiguous {
		summary.Reason = "weak_evidence"
	} else if collecting && possibleWeight >= 3 {
		summary.Status, summary.Reason = "collecting", "collecting_samples"
	} else if len(votes) > 0 || collecting {
		summary.Reason = "insufficient_weight"
	}
	return summary
}
