package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ttftQualityRevalidationCandidate() []openAITTFTGuardCandidate {
	return []openAITTFTGuardCandidate{{groupID: 100, accountID: 7, upstreamKeyID: 99, model: "gpt-test"}}
}

func TestTTFTQualityPreservesRevalidationAfterRawExpiry(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "trigger")
	e := f.entry(t)
	candidates := ttftQualityRevalidationCandidate()
	q := f.g.qualityAt(candidates, f.cfg, f.now)[7]
	require.Equal(t, "protected", q.State)
	require.True(t, q.Degraded)
	require.True(t, q.NeedsRevalidation)

	// The bad latency leaves the sample window before raw storage is cleaned.
	f.now = f.now.Add(f.g.ttl + time.Millisecond)
	q = f.g.qualityAt(candidates, f.cfg, f.now)[7]
	require.Equal(t, "insufficient", q.State)
	require.False(t, q.Degraded)
	require.True(t, q.NeedsRevalidation)
	require.Zero(t, q.SampleCount)

	// Raw observations have a longer storage TTL. Their deletion must not hide
	// the surviving recovery projection from scheduling quality reads.
	f.now = f.now.Add(f.g.ttl + time.Millisecond)
	f.g.deleteExpiredLocked(f.now)
	require.Empty(t, f.g.observations)
	require.Len(t, f.g.entries, 1)
	q = f.g.qualityAt(candidates, f.cfg, f.now)[7]
	require.Equal(t, "insufficient", q.State)
	require.True(t, q.NeedsRevalidation)
	require.False(t, q.Degraded)
	require.Zero(t, q.SampleCount)

	touched, epoch, completed := e.lastTouchedAt, e.recoveryEpoch, e.completedTrialCount
	for i := 0; i < 3; i++ {
		require.Equal(t, q, f.g.qualityAt(candidates, f.cfg, f.now)[7])
	}
	require.Equal(t, touched, e.lastTouchedAt)
	require.Equal(t, epoch, e.recoveryEpoch)
	require.Equal(t, completed, e.completedTrialCount)
	require.True(t, e.needsRevalidation)
	require.Empty(t, f.g.groupProbes)
	require.Empty(t, f.g.trialBudgets)

	// Equivalent policies share the same tombstone across groups.
	candidates[0].groupID = 200
	require.Equal(t, q, f.g.qualityAt(candidates, f.cfg, f.now)[7])
	require.Len(t, f.g.entries, 1)
}

func TestTTFTQualityColdAndExpiredHealthyRemainUnknown(t *testing.T) {
	f := newTTFTFactFixture()
	candidates := ttftQualityRevalidationCandidate()
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now))
	require.Empty(t, f.g.entries, "reading a cold candidate must not create a projection")

	for i := 0; i < f.cfg.MinSamples; i++ {
		f.report(t, 1000, "normal")
	}
	q := f.g.qualityAt(candidates, f.cfg, f.now)[7]
	require.Equal(t, "normal", q.State)
	require.False(t, q.NeedsRevalidation)
	f.now = f.now.Add(f.g.ttl)
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now))
	f.g.deleteExpiredLocked(f.now)
	require.Empty(t, f.g.entries)
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now))
}

func TestTTFTQualityRevalidationIsolatedByKeyModelAndPolicyGeneration(t *testing.T) {
	f := newTTFTFactFixture()
	f.report(t, 225000, "trigger")
	e := f.entry(t)
	candidates := ttftQualityRevalidationCandidate()
	f.now = f.now.Add(2*f.g.ttl + time.Millisecond)
	f.g.deleteExpiredLocked(f.now)

	candidates[0].upstreamKeyID = 100
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now))
	candidates[0].upstreamKeyID = 99
	candidates[0].model = "other-model"
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now))
	require.Len(t, f.g.entries, 1)
	candidates[0].model = "gpt-test"
	require.True(t, f.g.qualityAt(candidates, f.cfg, f.now)[7].NeedsRevalidation)

	f.g.localPolicyGeneration++
	require.Empty(t, f.g.qualityAt(candidates, f.cfg, f.now), "old-policy tombstones are not current recovery evidence")
	require.False(t, e.needsRevalidation)
	require.Equal(t, f.g.localPolicyGeneration, e.policyGeneration)
}

func TestTTFTQualityUnavailableIsNotCold(t *testing.T) {
	t.Run("managed policy mismatch", func(t *testing.T) {
		f := newTTFTFactFixture()
		f.report(t, 225000, "trigger")
		e := f.entry(t)
		current := f.cfg
		current.MinSamples++
		f.g.groupConfigs[100] = current
		f.g.policiesManaged = true
		epoch, samples := e.recoveryEpoch, e.sampleCount
		q := f.g.qualityAt(ttftQualityRevalidationCandidate(), f.cfg, f.now)[7]
		require.Equal(t, "unavailable", q.State)
		require.Zero(t, q.SampleCount)
		require.Equal(t, current, f.g.groupConfigs[100])
		require.Equal(t, epoch, e.recoveryEpoch)
		require.Equal(t, samples, e.sampleCount)
		require.Empty(t, f.g.groupProbes)
	})

	t.Run("invalid candidate identity", func(t *testing.T) {
		f := newTTFTFactFixture()
		candidates := ttftQualityRevalidationCandidate()
		candidates[0].model = ""
		require.Equal(t, "unavailable", f.g.qualityAt(candidates, f.cfg, f.now)[7].State)
		candidates[0].model = "gpt-test"
		candidates[0].groupID = 0
		require.Equal(t, "unavailable", f.g.qualityAt(candidates, f.cfg, f.now)[7].State)
		require.Empty(t, f.g.entries)
	})

	t.Run("invalid enabled configuration", func(t *testing.T) {
		f := newTTFTFactFixture()
		cfg := f.cfg
		cfg.Threshold = time.Second
		require.Equal(t, "unavailable", f.g.qualityAt(ttftQualityRevalidationCandidate(), cfg, f.now)[7].State)
		require.Empty(t, f.g.entries)
		cfg.Enabled = false
		require.Empty(t, f.g.qualityAt(ttftQualityRevalidationCandidate(), cfg, f.now))
	})
}
