package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConfidenceDistributionStateEveryWindowHasExactQuota(t *testing.T) {
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	state, err := NewConfidenceDistributionState("fingerprint", "responses")
	require.NoError(t, err)
	originalOrder := append([]string(nil), state.ProbeOrder...)
	for i := 0; i < 385; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		attempt, err := state.Claim(at)
		require.NoError(t, err)
		require.Equal(t, int64(i+1), attempt.Sequence)
		_, _, err = state.Finish(attempt, DistributionSample{Valid: true, Answer: "unseen-fixture-answer"}, at.Add(time.Second))
		require.NoError(t, err)
		require.Equal(t, originalOrder, state.ProbeOrder)
		if i < 127 {
			continue
		}
		require.Len(t, state.Samples, 128)
		require.Equal(t, int64(i-126), state.Samples[0].Sequence)
		counts := map[string]int{}
		for _, sample := range state.Samples {
			counts[sample.ProbeID]++
		}
		require.Equal(t, DistributionProbeQuotas(), counts)
	}
}

func TestConfidenceDistributionStateRecoverConsumesLostAttempt(t *testing.T) {
	now := time.Now().UTC()
	state, err := NewConfidenceDistributionState("fingerprint", "responses")
	require.NoError(t, err)
	first, err := state.Claim(now)
	require.NoError(t, err)
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	restored := new(ConfidenceDistributionState)
	require.NoError(t, json.Unmarshal(encoded, restored))
	busy, err := restored.Claim(now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, busy)
	second, err := restored.Claim(now.Add(UpstreamConfidenceDistributionLease))
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Sequence)
	require.Equal(t, "lease_expired", restored.Samples[0].Reason)
	require.False(t, restored.Samples[0].Valid)
	_, _, err = restored.Finish(first, DistributionSample{Valid: true, Answer: "42"}, now.Add(UpstreamConfidenceDistributionLease+time.Second))
	require.ErrorIs(t, err, ErrConfidenceDistributionLeaseLost)
}

func TestConfidenceDistributionStateRetentionKeepsAttemptSequence(t *testing.T) {
	now := time.Now().UTC()
	state, err := NewConfidenceDistributionState("fingerprint", "responses")
	require.NoError(t, err)
	first, err := state.Claim(now)
	require.NoError(t, err)
	_, _, err = state.Finish(first, DistributionSample{Reason: "network_error"}, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, state.Recover(now.Add(UpstreamHealthObservationRetention+time.Second)))
	require.Empty(t, state.Samples)
	second, err := state.Claim(now.Add(UpstreamHealthObservationRetention + 2*time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Sequence)
	require.Equal(t, state.ProbeOrder[1], second.ProbeID)
}

func TestConfidenceDistributionStateDecisiveAlertPolicy(t *testing.T) {
	state := &ConfidenceDistributionState{}
	require.False(t, state.RecordDecisive("collecting"))
	require.False(t, state.RecordDecisive("match"))
	require.True(t, state.RecordDecisive("mismatch"))
	require.False(t, state.RecordDecisive("mismatch"))
	require.False(t, state.RecordDecisive("insufficient"))
	require.Equal(t, "mismatch", state.LastDecisive)
	require.False(t, state.RecordDecisive("mismatch"))
	require.True(t, state.RecordDecisive("match"))
	require.False(t, state.RecordDecisive("match"))
	state = &ConfidenceDistributionState{}
	require.True(t, state.RecordDecisive("mismatch"))
}

func TestConfidenceDistributionStateDoesNotJudgePending128thAttempt(t *testing.T) {
	now := time.Now().UTC()
	state, err := NewConfidenceDistributionState("series", "responses")
	require.NoError(t, err)
	for i := 0; i < 127; i++ {
		at := now.Add(time.Duration(i) * time.Minute)
		attempt, err := state.Claim(at)
		require.NoError(t, err)
		_, _, err = state.Finish(attempt, DistributionSample{Answer: "valid-answer", Valid: true}, at.Add(time.Second))
		require.NoError(t, err)
	}
	_, err = state.Claim(now.Add(127 * time.Minute))
	require.NoError(t, err)
	summary, err := state.Summary()
	require.NoError(t, err)
	require.Equal(t, 128, summary.Attempted)
	require.Equal(t, 127, summary.ValidSamples)
	require.Equal(t, "collecting", summary.Status)
	require.Equal(t, []string{"probe_pending"}, summary.Reasons)
	require.Empty(t, summary.ClosestModel)
	require.Empty(t, summary.Matches)
	require.Empty(t, summary.Scores)
}

func testDistributionStateIdentity() ConfidenceDistributionIdentity {
	components := make(map[string]string)
	for _, component := range []string{"binding", "protocol", "endpoint", "credential", "model", "proxy", "headers", "contract", "baseline"} {
		components[component] = component + "-digest"
	}
	return ConfidenceDistributionIdentity{Version: ConfidenceDistributionIdentityVersion, Fingerprint: "canonical", Components: components, Protocol: "responses", BaselineVersion: DistributionBaselineVersion("responses"), LegacyFingerprint: "legacy", LegacyCompatible: true, ClientVersion: "0.160.0"}
}

func TestConfidenceDistributionIdentityUpgradeAndReasons(t *testing.T) {
	now := time.Now().UTC()
	state, err := NewConfidenceDistributionState("legacy", "responses")
	require.NoError(t, err)
	_, err = state.Claim(now)
	require.NoError(t, err)
	state.LastDecisive = "mismatch"
	identity := testDistributionStateIdentity()
	reasons, upgrade := state.IdentityChange(identity)
	require.Empty(t, reasons)
	require.True(t, upgrade)
	before, err := json.Marshal(state)
	require.NoError(t, err)
	state.UpgradeIdentity(identity)
	identity.Components["endpoint"] = "mutated-elsewhere"
	require.Equal(t, "endpoint-digest", state.IdentityComponents["endpoint"], "persisted state must own its identity map")
	savedVersion, savedFingerprint, savedComponents := state.IdentityVersion, state.Fingerprint, state.IdentityComponents
	savedClientVersion := state.ClientVersion
	state.IdentityVersion, state.Fingerprint, state.IdentityComponents = 0, "legacy", nil
	state.ClientVersion = ""
	after, err := json.Marshal(state)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "identity upgrade must preserve all sampling and lease fields")
	state.IdentityVersion, state.Fingerprint, state.IdentityComponents = savedVersion, savedFingerprint, savedComponents
	state.ClientVersion = savedClientVersion
	for _, component := range []string{"binding", "protocol", "endpoint", "credential", "model", "proxy", "headers", "contract", "baseline"} {
		t.Run(component, func(t *testing.T) {
			changed := testDistributionStateIdentity()
			changed.Components[component] = "new-digest"
			changed.Fingerprint = "changed"
			reasons, upgrade := state.IdentityChange(changed)
			require.False(t, upgrade)
			require.Equal(t, []string{component + "_changed"}, reasons)
		})
	}
	changed := testDistributionStateIdentity()
	changed.Components["protocol"] = "chat"
	changed.Protocol = "chat_completions"
	changed.Components["baseline"] = "chat-baseline"
	changed.BaselineVersion = DistributionBaselineVersion(changed.Protocol)
	reasons, _ = state.IdentityChange(changed)
	require.Equal(t, []string{"baseline_changed", "protocol_changed"}, reasons, "explicit fields must not duplicate component reasons")
}

func TestConfidenceDistributionLegacyIdentityRequiresEvidence(t *testing.T) {
	for _, scenario := range []string{"proxy-unverifiable", "legacy-changed", "protocol-changed", "baseline-changed", "future-version"} {
		t.Run(scenario, func(t *testing.T) {
			state, err := NewConfidenceDistributionState("legacy", "responses")
			require.NoError(t, err)
			identity := testDistributionStateIdentity()
			switch scenario {
			case "proxy-unverifiable":
				identity.LegacyCompatible = false
			case "legacy-changed":
				identity.LegacyFingerprint = "different"
			case "protocol-changed":
				identity.Protocol = "chat_completions"
			case "baseline-changed":
				identity.BaselineVersion = "new-baseline"
			case "future-version":
				identity.Version = ConfidenceDistributionIdentityVersion + 1
			}
			reasons, upgrade := state.IdentityChange(identity)
			require.False(t, upgrade)
			require.Equal(t, []string{"legacy_identity_unverifiable"}, reasons)
		})
	}
}

func TestConfidenceDistributionSummaryResetMetadataIsIndependent(t *testing.T) {
	state, err := NewConfidenceDistributionStateForIdentity(testDistributionStateIdentity())
	require.NoError(t, err)
	at := time.Now().UTC()
	state.SeriesReset = &ConfidenceDistributionSeriesReset{At: &at, Reasons: []string{"endpoint_changed"}, PreviousAttempted: 18}
	summary, err := state.Summary()
	require.NoError(t, err)
	summary.SeriesReset.Reasons[0] = "client-mutation"
	*summary.SeriesReset.At = at.Add(time.Hour)
	require.Equal(t, []string{"endpoint_changed"}, state.SeriesReset.Reasons)
	require.Equal(t, at, *state.SeriesReset.At)
}
