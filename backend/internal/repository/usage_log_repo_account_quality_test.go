package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryGetAccountQualityStatsBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	accountIDs := []int64{11, 22}
	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	realtimeStart := start.Add(23 * time.Hour)
	end := start.Add(24 * time.Hour)
	lastSuccess := end.Add(-5 * time.Minute)
	lastError := end.Add(-8 * time.Minute)
	rows := qualityStatsRows("account_id").AddRow(
		int64(11),
		int64(118), int64(108), 760.0, 4100.0,
		int64(240), int64(800),
		int64(172), int64(160), 930.25, 5100.0,
		int64(360), int64(1200),
		int64(18), int64(2), int64(118), int64(4), lastSuccess, lastError,
	).AddRow(
		int64(22),
		int64(4), int64(0), nil, 7000.0,
		int64(0), int64(0),
		int64(4), int64(0), nil, 7000.0,
		int64(0), int64(0),
		int64(0), int64(0), int64(4), int64(1), end.Add(-2*time.Hour), nil,
	)

	mock.ExpectQuery(`(?s)WITH successful AS MATERIALIZED.*ul\.cache_creation_tokens.*total_cost > 0.*ul\.stream = TRUE.*quality AS.*COUNT\(\*\) FILTER \(WHERE created_at >= \$3\).*SUM\(input_tokens \+ cache_creation_tokens \+ cache_read_tokens\) FILTER \(WHERE created_at >= \$3\).*SUM\(input_tokens \+ cache_creation_tokens \+ cache_read_tokens\).*WHERE duration_ms IS NOT NULL.*ops_error_logs.*oe\.stream = TRUE.*oe\.is_count_tokens = FALSE`).
		WithArgs(pq.Array(accountIDs), start, realtimeStart, end).
		WillReturnRows(rows)

	repo := newUsageLogRepositoryWithSQL(nil, db)
	stats, err := repo.GetAccountQualityStatsBatch(context.Background(), accountIDs, start, realtimeStart, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Equal(t, int64(118), stats[11].Recent1h.SampleCount)
	require.Equal(t, int64(108), stats[11].Recent1h.FirstTokenSampleCount)
	require.InDelta(t, 760.0, *stats[11].Recent1h.AverageFirstTokenMs, 0.001)
	require.Equal(t, int64(240), stats[11].Recent1h.CacheRateNumerator)
	require.Equal(t, int64(800), stats[11].Recent1h.CacheRateDenominator)
	require.Equal(t, int64(360), stats[11].Recent24h.CacheRateNumerator)
	require.Equal(t, int64(1200), stats[11].Recent24h.CacheRateDenominator)
	require.Equal(t, int64(172), stats[11].Recent24h.SampleCount)
	require.Equal(t, int64(18), stats[11].SuccessfulRequests1h)
	require.Equal(t, int64(2), stats[11].FailedRequests1h)
	require.Equal(t, &lastSuccess, stats[11].LastSuccessAt)
	require.Equal(t, &lastError, stats[11].LastErrorAt)
	require.Nil(t, stats[22].Recent24h.AverageFirstTokenMs)
	require.NotNil(t, stats[22].Recent24h.AverageDurationMs)
}

func TestUsageLogRepositoryGetGroupQualityStatsBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	realtimeStart := end.Add(-time.Hour)
	groupIDs := []int64{7, 9}
	rows := qualityStatsRows("group_id").AddRow(
		int64(7),
		int64(5), int64(5), 540.0, 5900.0,
		int64(50), int64(160),
		int64(84), int64(70), 920.0, 7300.0,
		int64(500), int64(1400),
		int64(5), int64(0), int64(84), int64(6), end.Add(-time.Minute), nil,
	)

	mock.ExpectQuery(`(?s)WITH successful AS MATERIALIZED.*ul\.group_id.*ul\.cache_creation_tokens.*quality AS.*SUM\(input_tokens \+ cache_creation_tokens \+ cache_read_tokens\).*GROUP BY group_id.*ops_error_logs`).
		WithArgs(pq.Array(groupIDs), start, realtimeStart, end).
		WillReturnRows(rows)

	repo := newUsageLogRepositoryWithSQL(nil, db)
	stats, err := repo.GetGroupQualityStatsBatch(context.Background(), groupIDs, start, realtimeStart, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, int64(5), stats[7].Recent1h.SampleCount)
	require.Equal(t, int64(84), stats[7].Recent24h.SampleCount)
	require.Equal(t, int64(70), stats[7].Recent24h.FirstTokenSampleCount)
	require.InDelta(t, 7300, *stats[7].Recent24h.AverageDurationMs, 0.001)
	require.Equal(t, int64(50), stats[7].Recent1h.CacheRateNumerator)
	require.Equal(t, int64(160), stats[7].Recent1h.CacheRateDenominator)
	require.Equal(t, int64(500), stats[7].Recent24h.CacheRateNumerator)
	require.Equal(t, int64(1400), stats[7].Recent24h.CacheRateDenominator)
}

func TestUsageLogRepositoryGetQualityStatsBatchEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newUsageLogRepositoryWithSQL(nil, db)

	accountStats, err := repo.GetAccountQualityStatsBatch(context.Background(), nil, time.Time{}, time.Time{}, time.Now())
	require.NoError(t, err)
	require.Empty(t, accountStats)
	groupStats, err := repo.GetGroupQualityStatsBatch(context.Background(), nil, time.Time{}, time.Time{}, time.Now())
	require.NoError(t, err)
	require.Empty(t, groupStats)
	require.NoError(t, mock.ExpectationsWereMet())
}

func qualityStatsRows(scope string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		scope,
		"realtime_count", "realtime_first_count", "realtime_first_avg", "realtime_duration_avg",
		"realtime_cache_read_tokens", "realtime_cache_input_tokens",
		"recent_count", "recent_first_count", "recent_first_avg", "recent_duration_avg",
		"recent_cache_read_tokens", "recent_cache_input_tokens",
		"successful_requests_1h", "failed_requests_1h", "successful_requests_24h", "failed_requests_24h",
		"last_success_at", "last_error_at",
	})
}
