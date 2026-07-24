package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type qualityStatsUsageRepoStub struct {
	UsageLogRepository
	accountSamples map[int64]AccountQualitySamples
	groupSamples   map[int64]AccountQualitySamples
	accountIDs     []int64
	groupIDs       []int64
	start          time.Time
	realtimeStart  time.Time
	end            time.Time
	deadline       time.Time
}

func (s *qualityStatsUsageRepoStub) GetAccountQualityStatsBatch(ctx context.Context, ids []int64, start, realtimeStart, end time.Time) (map[int64]AccountQualitySamples, error) {
	s.accountIDs = append([]int64(nil), ids...)
	s.start, s.realtimeStart, s.end = start, realtimeStart, end
	s.deadline, _ = ctx.Deadline()
	return s.accountSamples, nil
}

func (s *qualityStatsUsageRepoStub) GetGroupQualityStatsBatch(ctx context.Context, ids []int64, start, realtimeStart, end time.Time) (map[int64]AccountQualitySamples, error) {
	s.groupIDs = append([]int64(nil), ids...)
	s.start, s.realtimeStart, s.end = start, realtimeStart, end
	s.deadline, _ = ctx.Deadline()
	return s.groupSamples, nil
}

func TestApplyAccountQualityScore(t *testing.T) {
	tests := []struct {
		name      string
		window    AccountQualityWindow
		wantScore *int
		wantGrade string
		wantBasis string
	}{
		{
			name: "excellent latency scores 100",
			window: AccountQualityWindow{SampleCount: 10, FirstTokenSampleCount: 10,
				AverageFirstTokenMs: qualityFloat64Ptr(800), AverageDurationMs: qualityFloat64Ptr(5000)},
			wantScore: qualityIntPtr(100), wantGrade: "S+", wantBasis: accountQualityBasisTTFTDuration,
		},
		{
			name: "insufficient samples remain unscored",
			window: AccountQualityWindow{SampleCount: 2, FirstTokenSampleCount: 2,
				AverageFirstTokenMs: qualityFloat64Ptr(500), AverageDurationMs: qualityFloat64Ptr(1000)},
		},
		{
			name:      "duration only is capped",
			window:    AccountQualityWindow{SampleCount: 10, AverageDurationMs: qualityFloat64Ptr(2800)},
			wantScore: qualityIntPtr(69), wantGrade: "B+", wantBasis: accountQualityBasisDurationOnly,
		},
		{
			name: "sparse first token evidence falls back to duration",
			window: AccountQualityWindow{SampleCount: 10, FirstTokenSampleCount: 2,
				AverageFirstTokenMs: qualityFloat64Ptr(1000), AverageDurationMs: qualityFloat64Ptr(20000)},
			wantScore: qualityIntPtr(69), wantGrade: "B+", wantBasis: accountQualityBasisDurationOnly,
		},
		{
			name: "realistic latency stays distinguishable",
			window: AccountQualityWindow{SampleCount: 10, FirstTokenSampleCount: 10,
				AverageFirstTokenMs: qualityFloat64Ptr(7700), AverageDurationMs: qualityFloat64Ptr(20000)},
			wantScore: qualityIntPtr(73), wantGrade: "A-", wantBasis: accountQualityBasisTTFTDuration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyAccountQualityScore(tt.window)
			require.Equal(t, tt.wantScore, got.QualityScore)
			require.Equal(t, tt.wantGrade, got.QualityGrade)
			require.Equal(t, tt.wantBasis, got.ScoreBasis)
		})
	}
}

func TestClassifyAccountQualityActivity(t *testing.T) {
	tests := []struct {
		name      string
		successes int64
		failures  int64
		want      string
	}{
		{name: "idle", want: accountQualityActivityIdle},
		{name: "low sample success", successes: 2, want: accountQualityActivityLowSample},
		{name: "low sample failure", failures: 2, want: accountQualityActivityLowSample},
		{name: "failing", failures: 3, want: accountQualityActivityFailing},
		{name: "active", successes: 8, failures: 1, want: accountQualityActivityActive},
		{name: "degraded", successes: 8, failures: 2, want: accountQualityActivityDegraded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, classifyAccountQualityActivity(tt.successes, tt.failures))
		})
	}
}

func TestQualityStatsServicesAreReadOnlyBoundedAndNormalizeIDs(t *testing.T) {
	now := time.Date(2026, 7, 24, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	firstToken, duration := 900.0, 7000.0
	samples := map[int64]AccountQualitySamples{
		7: {
			Recent1h: AccountQualityPeriodSamples{Last10: AccountQualityWindow{
				SampleCount: 10, FirstTokenSampleCount: 10,
				AverageFirstTokenMs: &firstToken, AverageDurationMs: &duration,
			}},
			Last24h: AccountQualityPeriodSamples{Last10: AccountQualityWindow{
				SampleCount: 10, FirstTokenSampleCount: 10,
				AverageFirstTokenMs: &firstToken, AverageDurationMs: &duration,
			}},
			SuccessfulRequests1h: 10,
		},
	}

	t.Run("account reader only", func(t *testing.T) {
		repo := &qualityStatsUsageRepoStub{accountSamples: samples}
		svc := &AccountUsageService{usageLogRepo: repo}
		before := time.Now()
		stats, err := svc.GetAccountQualityStatsBatch(context.Background(), []int64{9, 7, 7, -1}, now)
		require.NoError(t, err)
		require.Equal(t, []int64{7, 9}, repo.accountIDs)
		require.Equal(t, now.UTC().Add(-24*time.Hour), repo.start)
		require.Equal(t, now.UTC().Add(-time.Hour), repo.realtimeStart)
		require.Equal(t, now.UTC(), repo.end)
		require.WithinDuration(t, before.Add(accountQualityQueryTimeout), repo.deadline, time.Second)
		require.Equal(t, AccountQualityScoreVersion, stats[7].ScoreVersion)
		require.Equal(t, accountQualityActivityActive, stats[7].Activity.State)
		require.Equal(t, accountQualityActivityIdle, stats[9].Activity.State)
	})

	t.Run("group reader only", func(t *testing.T) {
		repo := &qualityStatsUsageRepoStub{groupSamples: samples}
		svc := &DashboardService{usageRepo: repo}
		stats, err := svc.GetGroupQualityStatsBatch(context.Background(), []int64{9, 7, 9, 0}, now)
		require.NoError(t, err)
		require.Equal(t, []int64{7, 9}, repo.groupIDs)
		require.Equal(t, AccountQualityScoreVersion, stats[7].ScoreVersion)
		require.False(t, repo.deadline.IsZero())
	})
}

func TestAccountQualityGradeBoundaries(t *testing.T) {
	tests := []struct {
		score int
		grade string
	}{
		{100, "S+"}, {95, "S+"}, {94, "S"}, {90, "S"}, {89, "S-"}, {85, "S-"},
		{84, "A+"}, {80, "A+"}, {79, "A"}, {75, "A"}, {74, "A-"}, {70, "A-"},
		{69, "B+"}, {65, "B+"}, {64, "B"}, {60, "B"}, {59, "B-"}, {50, "B-"},
		{49, "C"}, {0, "C"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.grade, accountQualityGrade(tt.score), "score=%d", tt.score)
	}
}

func TestAccountQualityCurvesInterpolateAndRejectMissingValues(t *testing.T) {
	score, ok := qualityCurveScore(qualityFloat64Ptr(10000), accountQualityTTFTCurve)
	require.True(t, ok)
	require.InDelta(t, 67.0, score, 0.001)

	score, ok = qualityCurveScore(qualityFloat64Ptr(62500), accountQualityDurationCurve)
	require.True(t, ok)
	require.InDelta(t, 38.75, score, 0.001)

	_, ok = qualityCurveScore(nil, accountQualityTTFTCurve)
	require.False(t, ok)
}

func qualityFloat64Ptr(value float64) *float64 { return &value }
func qualityIntPtr(value int) *int             { return &value }
