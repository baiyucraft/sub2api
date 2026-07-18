package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseMonitorRateRange(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  string
	}{
		{"", MonitorRateRange24Hours},
		{"24h", MonitorRateRange24Hours},
		{"7d", MonitorRateRange7Days},
		{"30d", MonitorRateRange30Days},
	} {
		got, err := ParseMonitorRateRange(tt.input)
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
	_, err := ParseMonitorRateRange("60d")
	require.ErrorIs(t, err, ErrChannelMonitorInvalidRateRange)
}

func TestBuildPublicRateTrend_PeakBoundaries(t *testing.T) {
	from := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	until := time.Date(2026, 7, 18, 13, 0, 0, 0, time.UTC)
	observed := from.Add(-time.Hour)
	trend := buildPublicRateTrend(GroupRateSnapshotSeries{
		ObservedSince: observed,
		Snapshots: []GroupRateSnapshot{{
			GroupID:            1,
			RateMultiplier:     2,
			PeakRateEnabled:    true,
			PeakStart:          "10:00",
			PeakEnd:            "12:00",
			PeakRateMultiplier: 1.5,
			Timezone:           "UTC",
			EffectiveAt:        observed,
		}},
	}, from, until)

	require.Equal(t, &observed, trend.ObservedSince)
	require.NotNil(t, trend.CurrentRate)
	require.Equal(t, 2.0, *trend.CurrentRate)
	require.Equal(t, []PublicRateTrendPoint{
		{ObservedAt: from, Rate: 2},
		{ObservedAt: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC), Rate: 3},
		{ObservedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC), Rate: 2},
	}, trend.Points)
}

func TestBuildPublicRateTrend_ConfigChangeReplacesSameTimestampPoint(t *testing.T) {
	from := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	changeAt := time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC)
	until := time.Date(2026, 7, 18, 13, 0, 0, 0, time.UTC)
	trend := buildPublicRateTrend(GroupRateSnapshotSeries{Snapshots: []GroupRateSnapshot{
		{
			RateMultiplier: 2,
			Timezone:       "UTC",
			EffectiveAt:    from,
		},
		{
			RateMultiplier:     4,
			PeakRateEnabled:    true,
			PeakStart:          "10:00",
			PeakEnd:            "12:00",
			PeakRateMultiplier: 1.5,
			Timezone:           "UTC",
			EffectiveAt:        changeAt,
		},
	}}, from, until)

	require.Equal(t, []PublicRateTrendPoint{
		{ObservedAt: from, Rate: 2},
		{ObservedAt: changeAt, Rate: 6},
		{ObservedAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC), Rate: 4},
	}, trend.Points)
}

func TestBuildPublicRateTrend_SupportsCrossDayHistoricalConfig(t *testing.T) {
	from := time.Date(2026, 7, 18, 21, 0, 0, 0, time.UTC)
	until := time.Date(2026, 7, 19, 3, 0, 0, 0, time.UTC)
	trend := buildPublicRateTrend(GroupRateSnapshotSeries{Snapshots: []GroupRateSnapshot{{
		RateMultiplier:     1,
		PeakRateEnabled:    true,
		PeakStart:          "22:00",
		PeakEnd:            "02:00",
		PeakRateMultiplier: 2,
		Timezone:           "UTC",
		EffectiveAt:        from,
	}}}, from, until)

	require.Equal(t, []PublicRateTrendPoint{
		{ObservedAt: from, Rate: 1},
		{ObservedAt: time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC), Rate: 2},
		{ObservedAt: time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC), Rate: 1},
	}, trend.Points)
}

func TestBuildPublicRateTrend_DoesNotFabricateBeforeFirstSnapshot(t *testing.T) {
	from := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	observed := from.Add(2 * time.Hour)
	until := from.Add(4 * time.Hour)
	trend := buildPublicRateTrend(GroupRateSnapshotSeries{Snapshots: []GroupRateSnapshot{{
		RateMultiplier: 1.25,
		Timezone:       "UTC",
		EffectiveAt:    observed,
	}}}, from, until)

	require.Len(t, trend.Points, 1)
	require.Equal(t, observed, trend.Points[0].ObservedAt)
	require.Equal(t, observed, *trend.ObservedSince)
}
