package repository

import (
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAggregateUpstreamHealthConfidenceUsesOnlyValidCompleted(t *testing.T) {
	now := time.Now().UTC()
	score100, score0 := 100, 0
	high, version := service.UpstreamConfidenceDefaultEffort, service.UpstreamConfidencePromptVersion
	rows := []*dbent.UpstreamHealthObservation{
		{ConfidenceScore: nil, ConfidenceChecks: map[string]int{"attempted": 1, "network_error": 1}, ObservedAt: now.Add(-time.Hour), RequestedEffort: &high, ConfidencePromptVersion: &version, ConfidenceStatus: stringPtr("network_error")},
		{ConfidenceScore: &score0, ConfidenceChecks: map[string]int{"valid_completed": 1, "mixed": 1}, ObservedAt: now.Add(-2 * time.Hour), RequestedEffort: &high, ConfidencePromptVersion: &version, ConfidenceStatus: stringPtr("mixed")},
		{ConfidenceScore: &score100, ConfidenceChecks: map[string]int{"valid_completed": 1, "current_success": 1}, ObservedAt: now.Add(-3 * time.Hour), RequestedEffort: &high, ConfidencePromptVersion: &version, ConfidenceStatus: stringPtr("current_success")},
		{ConfidenceScore: &score100, ConfidenceChecks: map[string]int{"valid_completed": 1, "current_success": 1}, ObservedAt: now.Add(-48 * time.Hour), RequestedEffort: &high, ConfidencePromptVersion: &version, ConfidenceStatus: stringPtr("current_success")},
	}
	summary := aggregateUpstreamHealthConfidence(now, rows)
	require.Equal(t, 2, summary.SampleCount24h)
	require.Equal(t, 3, summary.SampleCount7d)
	require.InDelta(t, 50, *summary.Score24h, 0.001)
	require.InDelta(t, 66.666, *summary.Score7d, 0.01)
	require.Equal(t, "mixed", summary.Status)
	require.Equal(t, 0, *summary.LastScore)
}

func TestAggregateUpstreamHealthConfidenceKeepsMixedStickyWithinWindow(t *testing.T) {
	now := time.Now().UTC()
	score100, score0 := 100, 0
	summary := aggregateUpstreamHealthConfidence(now, []*dbent.UpstreamHealthObservation{
		{ConfidenceScore: &score100, ConfidenceChecks: map[string]int{"valid_completed": 1, "current_success": 1}, ConfidenceStatus: stringPtr("current_success"), ObservedAt: now.Add(-time.Hour)},
		{ConfidenceScore: &score0, ConfidenceChecks: map[string]int{"valid_completed": 1, "mixed": 1}, ConfidenceStatus: stringPtr("mixed"), ObservedAt: now.Add(-2 * time.Hour)},
	})
	require.Equal(t, "mixed", summary.Status)
	require.Equal(t, 100, *summary.LastScore)
}

func TestAggregateUpstreamHealthConfidenceReportsCompletedStates(t *testing.T) {
	now := time.Now().UTC()
	score100, score0 := 100, 0

	success := aggregateUpstreamHealthConfidence(now, []*dbent.UpstreamHealthObservation{
		{ConfidenceScore: &score100, ConfidenceChecks: map[string]int{"valid_completed": 1, "current_success": 1}, ObservedAt: now.Add(-time.Hour)},
		{ConfidenceScore: &score0, ConfidenceChecks: map[string]int{"valid_completed": 1, "unsuccessful": 1}, ObservedAt: now.Add(-2 * time.Hour)},
	})
	require.Equal(t, "current_success", success.Status)
	require.InDelta(t, 50, *success.Score7d, 0.001)

	unsuccessful := aggregateUpstreamHealthConfidence(now, []*dbent.UpstreamHealthObservation{
		{ConfidenceScore: &score0, ConfidenceChecks: map[string]int{"valid_completed": 1, "unsuccessful": 1}, ObservedAt: now.Add(-time.Hour)},
	})
	require.Equal(t, "unsuccessful", unsuccessful.Status)
	require.InDelta(t, 0, *unsuccessful.Score7d, 0.001)
}

func TestAggregateUpstreamHealthConfidenceMarksInsufficientWithoutValidSamples(t *testing.T) {
	summary := aggregateUpstreamHealthConfidence(time.Now().UTC(), []*dbent.UpstreamHealthObservation{
		{ConfidenceChecks: map[string]int{"network_error": 1}, ObservedAt: time.Now().UTC()},
	})
	require.Nil(t, summary.Score24h)
	require.Nil(t, summary.Score7d)
	require.Zero(t, summary.SampleCount24h)
	require.Equal(t, "data_insufficient", summary.Status)
}

func TestAggregateUpstreamHealthConfidenceSeparatesHardAnomaliesFromJuiceScore(t *testing.T) {
	now := time.Now().UTC()
	score100 := 100
	summary := aggregateUpstreamHealthConfidence(now, []*dbent.UpstreamHealthObservation{
		{ConfidenceChecks: map[string]int{}, ConfidenceEvidence: map[string]any{"kind": "coverage", "classification": "explicit_hidden_override", "hard_anomaly": true}, ConfidenceStatus: stringPtr("explicit_hidden_override"), ObservedAt: now.Add(-time.Hour)},
		{ConfidenceChecks: map[string]int{}, ConfidenceEvidence: map[string]any{"kind": "output_integrity", "classification": "output_rewrite_40_prefix", "hard_anomaly": true}, ConfidenceStatus: stringPtr("output_rewrite_40_prefix"), ObservedAt: now.Add(-2 * time.Hour)},
		{ConfidenceScore: &score100, ConfidenceChecks: map[string]int{"attempted": 1, "valid_completed": 1, "current_success": 1}, ConfidenceEvidence: map[string]any{"kind": "juice", "classification": "current_success"}, ConfidenceStatus: stringPtr("current_success"), ObservedAt: now.Add(-3 * time.Hour)},
	})
	require.Equal(t, 3, summary.Attempted24h)
	require.Equal(t, 1, summary.ValidCompleted24h)
	require.Equal(t, 1, summary.CurrentSuccess24h)
	require.InDelta(t, 100, *summary.Score24h, 0.001)
	require.Equal(t, 1, summary.CoverageHardAnomaly24h)
	require.Equal(t, 1, summary.OutputRewrite24h)
	require.Equal(t, "coverage", summary.LastEvidence["kind"])
}

func stringPtr(value string) *string { return &value }
