package service

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func fusionReference(method, candidate string) ConfidenceFingerprintMethodSummary {
	return ConfidenceFingerprintMethodSummary{Status: "reference_only", SampleCount: 3, MinimumSamples: 3, CandidateModel: candidate, ConfirmedRounds: 2, Participating: true, Weight: confidenceMethodWeight(method)}
}

func TestConfidenceSummaryFourStatesAndWeightedQuorum(t *testing.T) {
	for _, tc := range []struct {
		name         string
		distribution *UpstreamConfidenceDistribution
		methods      map[string]ConfidenceFingerprintMethodSummary
		want, reason string
		inference    bool
	}{
		{"no baseline", nil, nil, "insufficient", "no_matching_baseline", false},
		{"collecting", nil, map[string]ConfidenceFingerprintMethodSummary{"chanshui_behavioral": {Status: "insufficient", SampleCount: 1, MinimumSamples: 3}, "modeltrace_numeric": {Status: "insufficient", SampleCount: 0, MinimumSamples: 3}}, "collecting", "collecting_samples", false},
		{"distribution alone", &UpstreamConfidenceDistribution{Status: "match", ClaimedModel: "gpt-5.6-sol", ClosestModel: "gpt-5.6-sol", ValidSamples: 128}, nil, "match", "evidence_matches_target", false},
		{"distribution other", &UpstreamConfidenceDistribution{Status: "mismatch", ClaimedModel: "gpt-5.6-sol", ClosestModel: distributionOther, ValidSamples: 128}, nil, "suspected", "evidence_supports_other", false},
		{"numeric cannot expand weight", nil, map[string]ConfidenceFingerprintMethodSummary{"modeltrace_numeric": fusionReference("modeltrace_numeric", "gpt-5.6-sol")}, "insufficient", "insufficient_weight", false},
		{"both support target", nil, map[string]ConfidenceFingerprintMethodSummary{"modeltrace_numeric": fusionReference("modeltrace_numeric", "gpt-5.6-sol"), "chanshui_behavioral": fusionReference("chanshui_behavioral", "openai/gpt-5.6-sol")}, "match", "reference_consensus", true},
		{"both support alternative", nil, map[string]ConfidenceFingerprintMethodSummary{"modeltrace_numeric": fusionReference("modeltrace_numeric", "gpt-5.6-terra"), "chanshui_behavioral": fusionReference("chanshui_behavioral", "openai/gpt-5.6-terra")}, "suspected", "reference_consensus", true},
		{"explicit conflict", &UpstreamConfidenceDistribution{Status: "match", ClaimedModel: "gpt-5.6-sol", ClosestModel: "gpt-5.6-sol", ValidSamples: 128}, map[string]ConfidenceFingerprintMethodSummary{"chanshui_behavioral": fusionReference("chanshui_behavioral", "gpt-5.6-terra")}, "insufficient", "methods_conflict", false},
		{"unknown target cannot borrow", nil, map[string]ConfidenceFingerprintMethodSummary{"modeltrace_numeric": fusionReference("modeltrace_numeric", "gpt-6.1-sol"), "chanshui_behavioral": fusionReference("chanshui_behavioral", "gpt-6.1-sol")}, "insufficient", "no_matching_baseline", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildConfidenceSummary(tc.distribution, &ConfidenceFingerprintAttribution{TargetModel: "gpt-5.6-sol", Model: "gpt-5.6-sol", Methods: tc.methods})
			require.Equal(t, tc.want, got.Status)
			require.Equal(t, tc.reason, got.Reason)
			require.Equal(t, tc.inference, got.Inference)
			for name, method := range got.Methods {
				if !method.Participating {
					require.Zero(t, method.EffectiveWeight)
				} else {
					require.Equal(t, confidenceMethodWeight(name), method.EffectiveWeight)
				}
			}
		})
	}
}

func TestConfidenceSummaryMappingAndExactCoverage(t *testing.T) {
	a := &ConfidenceFingerprintAttribution{TargetModel: "gpt-6.1-sol", Model: "gpt-6-sol", Methods: map[string]ConfidenceFingerprintMethodSummary{"modeltrace_numeric": fusionReference("modeltrace_numeric", "gpt-6-sol")}}
	summary := BuildConfidenceSummary(nil, a)
	require.Equal(t, "insufficient", summary.Status)
	require.Equal(t, "target_model_mismatch", summary.Reason)
	require.False(t, summary.Methods["modeltrace_numeric"].Participating)
	require.Equal(t, "gpt-6-sol", canonicalConfidenceModel(" OPENAI/gpt-6-sol "))
	require.NotEqual(t, canonicalConfidenceModel("gpt-6-sol"), canonicalConfidenceModel("gpt-6.1-sol"))
	require.False(t, confidenceReferenceCovers("modeltrace_numeric", "gpt-6.1-sol"))
	require.False(t, confidenceReferenceCovers("chanshui_behavioral", "gpt-6.1-sol"))
}

func TestConfidenceTargetBindingMatchesDispatchAndPreservesOldSeries(t *testing.T) {
	account := distributionIdentityTestAccount()
	direct, ok := ConfidenceFingerprintIdentityForEffectiveModel(account, "gpt-6.1-sol", "")
	require.True(t, ok)
	old, ok := ConfidenceFingerprintIdentityForAccount(account, "gpt-6.1-sol")
	require.True(t, ok)
	require.Equal(t, direct.Fingerprint, old.Fingerprint, "same-model old observations must remain reusable")
	account.Credentials["model_mapping"] = map[string]any{"selected-alias": "gpt-6-astra"}
	claimed, ok := ConfidenceFingerprintIdentityForAccount(account, "selected-alias")
	require.True(t, ok)
	dispatched, ok := ConfidenceFingerprintIdentityForEffectiveModel(account, "gpt-6-astra", "")
	require.True(t, ok)
	finished := bindConfidenceFingerprintTarget(dispatched, "selected-alias")
	require.Equal(t, claimed.Fingerprint, finished.Fingerprint)
	require.Equal(t, "selected-alias", finished.TargetModel)
	require.NotEqual(t, dispatched.Fingerprint, finished.Fingerprint, "a different requested target cannot borrow an effective-only series")
}

func TestConfidenceSamplingOnlyUsesApplicableMethods(t *testing.T) {
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		model         string
		first, second string
	}{
		{"gpt-5.6-sol", "chanshui_behavioral", "modeltrace_numeric"},
		{"gpt-unknown-version", "protocol", "protocol"},
		{"gpt-6.1-sol", "distribution", "distribution"},
	} {
		identity := NewConfidenceFingerprintIdentity(PlatformOpenAI, tc.model, upstreamHealthProbeProtocolOpenAI, "https://test.invalid")
		state, err := NewConfidenceFingerprintState(identity)
		require.NoError(t, err)
		require.Equal(t, tc.first, state.samplingMethod(1))
		require.Equal(t, tc.second, state.samplingMethod(2))
		attempt, err := state.Claim(now)
		require.NoError(t, err)
		require.Equal(t, tc.first, attempt.Method)
	}
}

func TestConfidenceSummaryUnavailableIsNotCollecting(t *testing.T) {
	a := &ConfidenceFingerprintAttribution{TargetModel: "gpt-5.6-sol", Model: "gpt-5.6-sol", Reason: "fingerprint_read_unavailable", Methods: map[string]ConfidenceFingerprintMethodSummary{
		"chanshui_behavioral": {Status: "insufficient", SampleCount: 0, MinimumSamples: 3, Reason: "fingerprint_read_unavailable"},
		"modeltrace_numeric":  {Status: "insufficient", SampleCount: 0, MinimumSamples: 3, Reason: "fingerprint_read_unavailable"},
	}}
	summary := BuildConfidenceSummary(nil, a)
	require.Equal(t, "insufficient", summary.Status)
	require.Equal(t, "confidence_read_unavailable", summary.Reason)
	d := &UpstreamConfidenceDistribution{Status: "insufficient", ClaimedModel: "gpt-custom", Reasons: []string{"no_matching_baseline"}}
	summary = BuildConfidenceSummary(d, nil)
	require.Equal(t, "insufficient", summary.Status)
	d = &UpstreamConfidenceDistribution{Status: "match", ClaimedModel: "gpt-6.1-sol", ClosestModel: "gpt-6.1-sol", ValidSamples: 128}
	summary = BuildConfidenceSummary(d, &ConfidenceFingerprintAttribution{TargetModel: "gpt-6-luna", Model: "gpt-6-luna"})
	require.Equal(t, "insufficient", summary.Status)
	require.False(t, summary.Methods["distribution"].Participating)
}

func TestReferenceCandidateGateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		method      string
		top, second float64
		want        bool
	}{
		{"modeltrace_numeric", .80, .15, true}, {"modeltrace_numeric", .799, .10, false}, {"modeltrace_numeric", .90, .26, false},
		{"chanshui_behavioral", .88, .85, true}, {"chanshui_behavioral", .879, .80, false}, {"chanshui_behavioral", .90, .88, false},
		{"chanshui_behavioral", math.NaN(), .1, false},
	} {
		s := ConfidenceFingerprintMethodSummary{Status: "reference_only", SampleCount: 3, Candidates: []ConfidenceFingerprintCandidate{{ModelID: "gpt-5.6-sol", Score: tc.top, ClosedSetSupport: tc.top}, {ModelID: "gpt-5.6-terra", Score: tc.second, ClosedSetSupport: tc.second}}}
		require.Equal(t, tc.want, qualifiedReferenceCandidate(tc.method, s) != "", tc)
		s.SampleCount = 2
		require.Empty(t, qualifiedReferenceCandidate(tc.method, s))
	}
}

func TestReferenceRoundsPersistAndRemainStableAcrossNextClaim(t *testing.T) {
	bank, err := loadChanshuiBehavioralBank()
	require.NoError(t, err)
	var selected *chanshuiBehavioralModel
	var feature []float64
	for i := range bank.Models {
		f := chanshuiBankBehaviorFeature(bank.Models[i])
		features := [][]float64{f[:], f[:], f[:]}
		result, err := CompareChanshuiBehaviorFeatures(features)
		require.NoError(t, err)
		if len(result.Top3) > 1 && result.Top3[0].Similarity >= .88 && result.Top3[0].Similarity-result.Top3[1].Similarity >= .03 {
			selected = &bank.Models[i]
			feature = append([]float64(nil), f[:]...)
			break
		}
	}
	require.NotNil(t, selected, "baseline must contain a separable behavioral profile")
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	identity := NewConfidenceFingerprintIdentity(PlatformOpenAI, "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://test.invalid")
	state, err := NewConfidenceFingerprintState(identity)
	require.NoError(t, err)
	state.MethodSequences = map[string]int64{"chanshui_behavioral": 6}
	for seq := int64(1); seq <= 6; seq++ {
		require.NoError(t, state.RecordMethodEvidence("chanshui_behavioral", seq, now, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", MethodSequence: seq, ChallengeIndex: int((seq - 1) % 3), Feature: feature}))
	}
	got := scoreReferenceMethod(identity, state, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now)
	require.Equal(t, 2, got.ConfirmedRounds)
	require.True(t, got.Participating)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	var restarted ConfidenceFingerprintState
	require.NoError(t, json.Unmarshal(raw, &restarted))
	restarted.MethodSequences["chanshui_behavioral"] = 7
	require.NoError(t, restarted.RecordMethodEvidence("chanshui_behavioral", 7, now, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", MethodSequence: 7, ChallengeIndex: 0, Feature: feature}))
	next := scoreReferenceMethod(identity, &restarted, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now)
	require.Equal(t, got.CandidateModel, next.CandidateModel)
	require.Equal(t, 2, next.ConfirmedRounds)
	require.True(t, next.Participating)
	require.Equal(t, 1, next.CurrentSampleCount)
	// Only an active final-slot lease may temporarily retain the last good round.
	restarted.MethodSequences["chanshui_behavioral"] = 9
	restarted.Pending = &ConfidenceFingerprintAttempt{Method: "chanshui_behavioral", MethodSequence: 9}
	restarted.LeaseExpires = now.Add(time.Second)
	live := scoreReferenceMethod(identity, &restarted, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now)
	require.True(t, live.Participating)
	timedOut := scoreReferenceMethod(identity, &restarted, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now.Add(time.Second))
	require.False(t, timedOut.Participating)
	restarted.Pending = nil
	// A completed failed round cannot be skipped to reuse the two old good rounds.
	restarted.MethodSequences["chanshui_behavioral"] = 9
	require.NoError(t, restarted.RecordMethodEvidence("chanshui_behavioral", 9, now, ConfidenceFingerprintEvidence{ProtocolStatus: "incompatible", MethodSequence: 9, ChallengeIndex: 2}))
	failed := scoreReferenceMethod(identity, &restarted, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now)
	require.False(t, failed.Participating)
	require.Zero(t, failed.ConfirmedRounds)
	expired := scoreReferenceMethod(identity, state, "chanshui_behavioral", ChanshuiBehavioralBaselineVersion, now.Add(UpstreamHealthObservationRetention+time.Nanosecond))
	require.False(t, expired.Participating)
	require.Empty(t, expired.Candidates)
}

func TestConfidenceSnapshotOnlyPublishesUnifiedSummary(t *testing.T) {
	snapshot := MergeUpstreamHealthConfidence(UpstreamHealthSnapshot{}, UpstreamHealthConfidenceSummary{Attribution: &ConfidenceFingerprintAttribution{Model: "gpt-5.6-sol"}})
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Contains(t, result, "confidence_summary")
	require.NotContains(t, result, "confidence_distribution")
	require.NotContains(t, result, "confidence_attribution")
}
