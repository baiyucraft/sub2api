package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardColdCandidatesJoinFirstOrdinaryBand(t *testing.T) {
	states := map[int64]upstreamcacheguard.AccountSnapshot{
		1: {CachePercent: 96},
		2: {CachePercent: 92},
		3: {CachePercent: 85},
		4: {Phase: upstreamcacheguard.Unknown},
		5: {Phase: upstreamcacheguard.Unknown},
	}
	known, cold := []int64{3, 2, 1}, []int64{5, 4}
	require.Equal(t, [][]int64{{1, 2, 4, 5}, {3}}, cacheGuardOrdinaryRateTiers(known, cold, states, nil))
	require.Equal(t, []int64{3, 2, 1}, known)
	require.Equal(t, []int64{5, 4}, cold)
	require.Equal(t, float64(0), states[4].CachePercent, "neutrality must not synthesize a cache rate")
	require.Equal(t, cacheGuardRateTiers(known, states, nil), cacheGuardOrdinaryRateTiers(known, nil, states, nil))
	require.Equal(t, [][]int64{{4, 5}}, cacheGuardOrdinaryRateTiers(nil, cold, states, nil))
	require.Nil(t, cacheGuardOrdinaryRateTiers(nil, nil, states, nil))
}

func TestJointTTFTColdSchedulingDoesNotQualifyMigration(t *testing.T) {
	quality := map[int64]forkscheduling.TTFTQuality{
		1: {State: "normal", SampleCount: 5, EWMAms: 10},
		3: {State: "insufficient", SampleCount: 1, EWMAms: 1},
		4: {State: "insufficient", SampleCount: 2, EWMAms: 5, NeedsRevalidation: true},
		5: {State: "protected", Degraded: true, SampleCount: 5, EWMAms: 100},
		6: {State: "unavailable"},
		7: {State: "normal", SampleCount: 5, EWMAms: 5, NeedsRevalidation: true},
	}
	for _, id := range []int64{1, 2, 3} {
		require.Equal(t, 0, jointTTFTSchedulingClass(id, quality, 5), "account %d", id)
	}
	for _, id := range []int64{4, 6, 7} {
		require.Equal(t, 1, jointTTFTSchedulingClass(id, quality, 5), "account %d", id)
	}
	require.Equal(t, 2, jointTTFTSchedulingClass(5, quality, 5))
	for _, id := range []int64{2, 3, 4, 7} {
		require.Equal(t, 1, jointTTFTClass(id, quality, 5), "neutral ranking must not qualify migration or recovery account %d", id)
	}
	_, evidence := jointTTFTLatencyEvidence(3, quality, 5)
	require.Equal(t, cacheGuardLatencyCold, evidence, "one sample must not create a measured latency tier")
	_, evidence = jointTTFTLatencyEvidence(6, quality, 5)
	require.Equal(t, cacheGuardLatencyUnavailable, evidence)
	_, evidence = jointTTFTLatencyEvidence(2, nil, 5)
	require.Equal(t, cacheGuardLatencyUnavailable, evidence)
}

func coldOrderAccounts(ids ...int64) []Account {
	accounts := make([]Account, len(ids))
	for i, id := range ids {
		accounts[i] = Account{ID: id}
	}
	return accounts
}

func TestJointTTFTColdOrderingPreservesKnownDimension(t *testing.T) {
	tests := []struct {
		name      string
		tiers     [][]int64
		quality   map[int64]forkscheduling.TTFTQuality
		cacheWant [][]int64
		ttftWant  [][]int64
	}{
		{
			name:  "cold_latency_keeps_known_lower_cache_band",
			tiers: [][]int64{{1, 2}, {3, 4}, {5}},
			quality: map[int64]forkscheduling.TTFTQuality{
				1: {State: "normal", SampleCount: 5, EWMAms: 10},
				2: {State: "insufficient", SampleCount: 1, EWMAms: 0.01},
				3: {State: "normal", SampleCount: 5, EWMAms: 20},
				5: {State: "normal", SampleCount: 5, EWMAms: 30},
			},
			cacheWant: [][]int64{{1, 2}, {3, 4}, {5}},
			ttftWant:  [][]int64{{1, 2}, {4}, {3}, {5}},
		},
		{
			name:  "cold_cache_keeps_known_slower_latency",
			tiers: [][]int64{{1, 2}, {3}},
			quality: map[int64]forkscheduling.TTFTQuality{
				1: {State: "normal", SampleCount: 5, EWMAms: 10},
				2: {State: "normal", SampleCount: 5, EWMAms: 30},
				3: {State: "normal", SampleCount: 5, EWMAms: 5},
			},
			cacheWant: [][]int64{{1}, {2}, {3}},
			ttftWant:  [][]int64{{3}, {1}, {2}},
		},
		{
			name:  "low_cache_cold_latency_does_not_join_higher_cache_tier",
			tiers: [][]int64{{1}, {2}},
			quality: map[int64]forkscheduling.TTFTQuality{
				1: {State: "normal", SampleCount: 5, EWMAms: 10},
				2: {State: "insufficient", SampleCount: 1, EWMAms: 0.01},
			},
			cacheWant: [][]int64{{1}, {2}},
			ttftWant:  [][]int64{{1}, {2}},
		},
		{
			name:  "revalidation_and_protection_remain_separate",
			tiers: [][]int64{{5, 4, 3, 2, 1}},
			quality: map[int64]forkscheduling.TTFTQuality{
				1: {State: "normal", SampleCount: 5, EWMAms: 10},
				3: {State: "insufficient", NeedsRevalidation: true},
				4: {State: "protected", Degraded: true, SampleCount: 5, EWMAms: 20},
				5: {State: "unavailable"},
			},
			cacheWant: [][]int64{{1, 2}, {5, 3}, {4}},
			ttftWant:  [][]int64{{2, 1}, {5, 3}, {4}},
		},
	}
	accounts := coldOrderAccounts(1, 2, 3, 4, 5)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cache := jointTTFTOrder(test.tiers, accounts, nil, test.quality, 5, SchedulingPriorityCacheFirst)
			cache = cacheGuardLatencyEvidenceTiers(cache, func(id int64) (float64, cacheGuardLatencyEvidence) {
				return jointTTFTLatencyEvidence(id, test.quality, 5)
			})
			require.Equal(t, test.cacheWant, cache)
			require.Equal(t, test.ttftWant, jointTTFTOrder(test.tiers, accounts, nil, test.quality, 5, SchedulingPriorityTTFTFirst))
		})
	}
}

func TestJointTTFTKnownOnlyTierListsUnchanged(t *testing.T) {
	tiers := [][]int64{{3, 1, 2}, {4, 5}, {6}}
	accounts := coldOrderAccounts(1, 2, 3, 4, 5, 6)
	quality := map[int64]forkscheduling.TTFTQuality{
		1: {State: "normal", SampleCount: 5, EWMAms: 20},
		2: {State: "normal", SampleCount: 5, EWMAms: 10},
		3: {State: "normal", SampleCount: 5, EWMAms: 10},
		4: {State: "normal", SampleCount: 5, EWMAms: 10},
		5: {State: "normal", SampleCount: 5, EWMAms: 30},
		6: {State: "protected", Degraded: true, SampleCount: 5, EWMAms: 5},
	}
	cache := jointTTFTOrder(tiers, accounts, nil, quality, 5, SchedulingPriorityCacheFirst)
	require.Equal(t, tiers, cache)
	cache = cacheGuardLatencyEvidenceTiers(cache, func(id int64) (float64, cacheGuardLatencyEvidence) {
		return jointTTFTLatencyEvidence(id, quality, 5)
	})
	require.Equal(t, [][]int64{{3, 2}, {1}, {4}, {5}, {6}}, cache)
	require.Equal(t, [][]int64{{3, 2}, {4}, {1}, {5}, {6}}, jointTTFTOrder(tiers, accounts, nil, quality, 5, SchedulingPriorityTTFTFirst))
}

func TestCacheGuardLatencyColdAndUnavailableAreDistinct(t *testing.T) {
	tiers := [][]int64{{1, 2, 3}, {4}}
	quality := map[int64]forkscheduling.TTFTQuality{
		1: {State: "normal", SampleCount: 5, EWMAms: 20},
		2: {State: "normal", SampleCount: 5, EWMAms: 10},
		3: {State: "insufficient", SampleCount: 1, EWMAms: 0.01},
	}
	read := func(id int64) (float64, cacheGuardLatencyEvidence) {
		return jointTTFTLatencyEvidence(id, quality, 5)
	}
	require.Equal(t, [][]int64{{2, 3}, {1}, {4}}, cacheGuardLatencyEvidenceTiers(tiers, read))
	quality[3] = forkscheduling.TTFTQuality{State: "unavailable"}
	require.Equal(t, tiers, cacheGuardLatencyEvidenceTiers(tiers, read), "unavailable evidence must retain the conservative old path")
	require.Equal(t, tiers, cacheGuardLatencyTiers(tiers, func(id int64) (float64, bool) {
		return 0, false
	}), "legacy callback behavior remains compatible")
}
