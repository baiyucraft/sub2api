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
