package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

const (
	AccountQualityRealtimeWindowHours  = 1
	AccountQualityWindowHours          = 24
	AccountQualityScoreVersion         = 2
	accountQualityQueryTimeout         = 10 * time.Second
	accountQualityMinSamples           = 3
	accountQualityMinTTFTSamples       = 3
	accountQualityTTFTWeight           = 0.85
	accountQualityDurationWeight       = 0.15
	accountQualityDurationOnlyMax      = 69
	accountQualityFailingMinErrors     = 3
	accountQualityDegradedMinAttempts  = 5
	accountQualityDegradedFailureRatio = 0.20

	accountQualityBasisTTFTDuration = "ttft_duration"
	accountQualityBasisTTFTOnly     = "ttft_only"
	accountQualityBasisDurationOnly = "duration_only"

	accountQualityActivityActive    = "active"
	accountQualityActivityLowSample = "low_sample"
	accountQualityActivityDegraded  = "degraded"
	accountQualityActivityFailing   = "failing"
	accountQualityActivityIdle      = "idle"
)

type accountQualityCurvePoint struct {
	LatencyMs float64
	Score     float64
}

var accountQualityTTFTCurve = []accountQualityCurvePoint{
	{LatencyMs: 800, Score: 100},
	{LatencyMs: 2000, Score: 95},
	{LatencyMs: 4000, Score: 85},
	{LatencyMs: 8000, Score: 72},
	{LatencyMs: 12000, Score: 62},
	{LatencyMs: 20000, Score: 48},
	{LatencyMs: 30000, Score: 35},
	{LatencyMs: 45000, Score: 20},
	{LatencyMs: 60000, Score: 10},
	{LatencyMs: 90000, Score: 0},
}

var accountQualityDurationCurve = []accountQualityCurvePoint{
	{LatencyMs: 5000, Score: 100},
	{LatencyMs: 10000, Score: 90},
	{LatencyMs: 20000, Score: 75},
	{LatencyMs: 40000, Score: 55},
	{LatencyMs: 60000, Score: 40},
	{LatencyMs: 90000, Score: 25},
	{LatencyMs: 120000, Score: 12},
	{LatencyMs: 180000, Score: 0},
}

// AccountQualityWindow contains the latency summary for one recent-request window.
// A nil score means there is not enough evidence to make a useful judgement.
type AccountQualityWindow struct {
	SampleCount           int64    `json:"sample_count"`
	FirstTokenSampleCount int64    `json:"first_token_sample_count"`
	AverageFirstTokenMs   *float64 `json:"average_first_token_ms"`
	AverageDurationMs     *float64 `json:"average_duration_ms"`
	QualityScore          *int     `json:"quality_score"`
	QualityGrade          string   `json:"quality_grade,omitempty"`
	ScoreBasis            string   `json:"score_basis,omitempty"`
}

type AccountQualityPeriod struct {
	Last10      AccountQualityWindow `json:"last_10"`
	Last100     AccountQualityWindow `json:"last_100"`
	WindowHours int                  `json:"window_hours"`
}

type AccountQualityActivity struct {
	State                  string     `json:"state"`
	SuccessfulRequestCount int64      `json:"successful_request_count"`
	FailedRequestCount     int64      `json:"failed_request_count"`
	LastSuccessAt          *time.Time `json:"last_success_at"`
	LastErrorAt            *time.Time `json:"last_error_at"`
}

type AccountQualityStats struct {
	Last10       AccountQualityWindow   `json:"last_10"`
	Last100      AccountQualityWindow   `json:"last_100"`
	WindowHours  int                    `json:"window_hours"`
	Recent1h     AccountQualityPeriod   `json:"recent_1h"`
	Activity     AccountQualityActivity `json:"activity"`
	ScoreVersion int                    `json:"score_version"`
}

type AccountQualityPeriodSamples struct {
	Last10  AccountQualityWindow
	Last100 AccountQualityWindow
}

// AccountQualitySamples is the repository result before the service applies the
// display-only scoring policy.
type AccountQualitySamples struct {
	Recent1h             AccountQualityPeriodSamples
	Last24h              AccountQualityPeriodSamples
	SuccessfulRequests1h int64
	FailedRequests1h     int64
	LastSuccessAt        *time.Time
	LastErrorAt          *time.Time
}

type accountQualityStatsReader interface {
	GetAccountQualityStatsBatch(ctx context.Context, accountIDs []int64, startTime, realtimeStartTime, endTime time.Time) (map[int64]AccountQualitySamples, error)
}

type groupQualityStatsReader interface {
	GetGroupQualityStatsBatch(ctx context.Context, groupIDs []int64, startTime, realtimeStartTime, endTime time.Time) (map[int64]AccountQualitySamples, error)
}

func normalizeQualityIDs(ids []int64) []int64 {
	uniqueIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		uniqueIDs = append(uniqueIDs, id)
	}
	sort.Slice(uniqueIDs, func(i, j int) bool { return uniqueIDs[i] < uniqueIDs[j] })
	return uniqueIDs
}

func buildAccountQualityStats(ids []int64, samples map[int64]AccountQualitySamples) map[int64]AccountQualityStats {
	result := make(map[int64]AccountQualityStats, len(ids))
	for _, id := range ids {
		sample := samples[id]
		recent := AccountQualityPeriod{
			Last10:      applyAccountQualityScore(sample.Recent1h.Last10),
			Last100:     applyAccountQualityScore(sample.Recent1h.Last100),
			WindowHours: AccountQualityRealtimeWindowHours,
		}
		result[id] = AccountQualityStats{
			Last10:      applyAccountQualityScore(sample.Last24h.Last10),
			Last100:     applyAccountQualityScore(sample.Last24h.Last100),
			WindowHours: AccountQualityWindowHours,
			Recent1h:    recent,
			Activity: AccountQualityActivity{
				State:                  classifyAccountQualityActivity(sample.SuccessfulRequests1h, sample.FailedRequests1h),
				SuccessfulRequestCount: sample.SuccessfulRequests1h,
				FailedRequestCount:     sample.FailedRequests1h,
				LastSuccessAt:          sample.LastSuccessAt,
				LastErrorAt:            sample.LastErrorAt,
			},
			ScoreVersion: AccountQualityScoreVersion,
		}
	}
	return result
}

// GetAccountQualityStatsBatch returns display-only latency summaries for accounts.
// The optional reader interface keeps this read-only feature out of scheduling contracts.
func (s *AccountUsageService) GetAccountQualityStatsBatch(ctx context.Context, accountIDs []int64, now time.Time) (map[int64]AccountQualityStats, error) {
	uniqueIDs := normalizeQualityIDs(accountIDs)
	result := make(map[int64]AccountQualityStats, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return result, nil
	}
	reader, ok := s.usageLogRepo.(accountQualityStatsReader)
	if !ok {
		return nil, fmt.Errorf("account quality statistics are not supported by the usage repository")
	}

	queryCtx, cancel := context.WithTimeout(ctx, accountQualityQueryTimeout)
	defer cancel()
	endTime := now.UTC()
	samples, err := reader.GetAccountQualityStatsBatch(
		queryCtx,
		uniqueIDs,
		endTime.Add(-AccountQualityWindowHours*time.Hour),
		endTime.Add(-AccountQualityRealtimeWindowHours*time.Hour),
		endTime,
	)
	if err != nil {
		return nil, fmt.Errorf("get account quality stats failed: %w", err)
	}
	return buildAccountQualityStats(uniqueIDs, samples), nil
}

// GetGroupQualityStatsBatch returns display-only latency summaries aggregated by group.
func (s *DashboardService) GetGroupQualityStatsBatch(ctx context.Context, groupIDs []int64, now time.Time) (map[int64]AccountQualityStats, error) {
	uniqueIDs := normalizeQualityIDs(groupIDs)
	result := make(map[int64]AccountQualityStats, len(uniqueIDs))
	if len(uniqueIDs) == 0 {
		return result, nil
	}
	reader, ok := s.usageRepo.(groupQualityStatsReader)
	if !ok {
		return nil, fmt.Errorf("group quality statistics are not supported by the usage repository")
	}

	queryCtx, cancel := context.WithTimeout(ctx, accountQualityQueryTimeout)
	defer cancel()
	endTime := now.UTC()
	samples, err := reader.GetGroupQualityStatsBatch(
		queryCtx,
		uniqueIDs,
		endTime.Add(-AccountQualityWindowHours*time.Hour),
		endTime.Add(-AccountQualityRealtimeWindowHours*time.Hour),
		endTime,
	)
	if err != nil {
		return nil, fmt.Errorf("get group quality stats failed: %w", err)
	}
	return buildAccountQualityStats(uniqueIDs, samples), nil
}

func classifyAccountQualityActivity(successfulRequests, failedRequests int64) string {
	if successfulRequests == 0 && failedRequests >= accountQualityFailingMinErrors {
		return accountQualityActivityFailing
	}
	attempts := successfulRequests + failedRequests
	if attempts >= accountQualityDegradedMinAttempts && float64(failedRequests)/float64(attempts) >= accountQualityDegradedFailureRatio {
		return accountQualityActivityDegraded
	}
	if successfulRequests >= accountQualityMinSamples {
		return accountQualityActivityActive
	}
	if attempts > 0 {
		return accountQualityActivityLowSample
	}
	return accountQualityActivityIdle
}

func applyAccountQualityScore(window AccountQualityWindow) AccountQualityWindow {
	if window.SampleCount < accountQualityMinSamples {
		return window
	}

	ttftScore, hasTTFT := qualityCurveScore(window.AverageFirstTokenMs, accountQualityTTFTCurve)
	if window.FirstTokenSampleCount < accountQualityMinTTFTSamples {
		hasTTFT = false
	}
	durationScore, hasDuration := qualityCurveScore(window.AverageDurationMs, accountQualityDurationCurve)

	var score float64
	var basis string
	if hasTTFT && hasDuration {
		score = ttftScore*accountQualityTTFTWeight + durationScore*accountQualityDurationWeight
		basis = accountQualityBasisTTFTDuration
	} else if hasTTFT {
		score = ttftScore
		basis = accountQualityBasisTTFTOnly
	} else if hasDuration {
		score = math.Min(durationScore, accountQualityDurationOnlyMax)
		basis = accountQualityBasisDurationOnly
	} else {
		return window
	}

	rounded := int(math.Round(math.Max(0, math.Min(100, score))))
	window.QualityScore = &rounded
	window.QualityGrade = accountQualityGrade(rounded)
	window.ScoreBasis = basis
	return window
}

func qualityCurveScore(value *float64, curve []accountQualityCurvePoint) (float64, bool) {
	if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) || len(curve) == 0 {
		return 0, false
	}
	if *value <= curve[0].LatencyMs {
		return curve[0].Score, true
	}
	for i := 1; i < len(curve); i++ {
		current := curve[i]
		if *value > current.LatencyMs {
			continue
		}
		previous := curve[i-1]
		ratio := (*value - previous.LatencyMs) / (current.LatencyMs - previous.LatencyMs)
		return previous.Score + ratio*(current.Score-previous.Score), true
	}
	return curve[len(curve)-1].Score, true
}

func accountQualityGrade(score int) string {
	switch {
	case score >= 95:
		return "S+"
	case score >= 90:
		return "S"
	case score >= 85:
		return "S-"
	case score >= 80:
		return "A+"
	case score >= 75:
		return "A"
	case score >= 70:
		return "A-"
	case score >= 65:
		return "B+"
	case score >= 60:
		return "B"
	case score >= 50:
		return "B-"
	default:
		return "C"
	}
}
