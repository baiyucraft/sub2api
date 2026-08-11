package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthPercentileUsesLinearInterpolation(t *testing.T) {
	p50 := percentile([]float64{400, 100, 300, 200}, 0.50)
	p95 := percentile([]float64{400, 100, 300, 200}, 0.95)
	require.NotNil(t, p50)
	require.NotNil(t, p95)
	require.InDelta(t, 250, *p50, 0.0001)
	require.InDelta(t, 385, *p95, 0.0001)
	require.Nil(t, percentile(nil, 0.95))
}

func TestUpstreamHealthTrendWorstStateOrdering(t *testing.T) {
	states := []service.UpstreamHealthStatus{
		service.UpstreamHealthHealthy,
		service.UpstreamHealthDisabled,
		service.UpstreamHealthObserving,
		service.UpstreamHealthRecovering,
		service.UpstreamHealthDegraded,
		service.UpstreamHealthSuspended,
	}
	for i := 1; i < len(states); i++ {
		require.Greater(t, upstreamHealthStateSeverity(states[i]), upstreamHealthStateSeverity(states[i-1]))
	}
}

func TestMostFrequentSourceIsStableOnTies(t *testing.T) {
	require.Equal(t, "business", mostFrequentSource(map[string]int{"probe": 2, "business": 2}))
	require.Equal(t, "probe", mostFrequentSource(map[string]int{"probe": 3, "business": 2}))
}

func TestUpstreamHealthTrendExcludesTTFTFromFailedObservations(t *testing.T) {
	valid := upstreamHealthInt64Ptr(120)
	failed := upstreamHealthInt64Ptr(900)
	require.True(t, upstreamHealthObservationHasValidTTFT(service.UpstreamHealthObservation{Result: "success", TTFTMs: valid}))
	require.False(t, upstreamHealthObservationHasValidTTFT(service.UpstreamHealthObservation{Result: "429", TTFTMs: failed}))
	require.False(t, upstreamHealthObservationHasValidTTFT(service.UpstreamHealthObservation{Result: "probe_response_mismatch", TTFTMs: failed}))
}

func TestAggregateUpstreamHealthTrendBucketsWorstStateAndMetrics(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	items := []service.UpstreamHealthObservation{
		{ObservedAt: now.Add(-9*time.Minute - 10*time.Second), State: service.UpstreamHealthHealthy, Source: "probe", Result: "success", TTFTMs: upstreamHealthInt64Ptr(100), DurationMs: upstreamHealthInt64Ptr(300)},
		{ObservedAt: now.Add(-8*time.Minute - 20*time.Second), State: service.UpstreamHealthDegraded, Source: "business", Result: "429", Reason: "capacity_limited", TTFTMs: upstreamHealthInt64Ptr(300), DurationMs: upstreamHealthInt64Ptr(500)},
		{ObservedAt: now.Add(-3 * time.Minute), State: service.UpstreamHealthHealthy, Source: "probe", Result: "success", TTFTMs: upstreamHealthInt64Ptr(200), DurationMs: upstreamHealthInt64Ptr(400)},
		{ObservedAt: now.Add(-7 * time.Hour), State: service.UpstreamHealthSuspended, Source: "legacy", Result: "401"},
	}

	trend, err := aggregateUpstreamHealthTrend(42, "6h", now, items)
	require.NoError(t, err)
	require.Equal(t, int64(42), trend.KeyID)
	require.Equal(t, int64(300), trend.BucketSeconds)
	require.Len(t, trend.Points, 2)

	first := trend.Points[0]
	require.Equal(t, service.UpstreamHealthDegraded, first.State)
	require.Equal(t, 2, first.SampleCount)
	require.Equal(t, 1, first.TTFTSampleCount)
	require.InDelta(t, 100, *first.TTFTP50Ms, 0.0001)
	require.InDelta(t, 100, *first.TTFTP95Ms, 0.0001)
	require.InDelta(t, 400, *first.DurationAvgMs, 0.0001)
	require.Equal(t, "business", first.PrimarySource)
	require.Equal(t, "capacity_limited", first.LatestReason)
	require.Equal(t, "429", first.LatestResult)
}
