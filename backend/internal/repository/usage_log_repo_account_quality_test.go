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
		int64(10), int64(8), 700.0, 3900.0,
		int64(18), int64(15), 760.0, 4100.0,
		int64(10), int64(8), 825.5, 4200.0,
		int64(72), int64(60), 930.25, 5100.0,
		int64(18), int64(2), lastSuccess, lastError,
	).AddRow(
		int64(22),
		int64(0), int64(0), nil, nil,
		int64(0), int64(0), nil, nil,
		int64(4), int64(0), nil, 7000.0,
		int64(4), int64(0), nil, 7000.0,
		int64(0), int64(0), end.Add(-2*time.Hour), nil,
	)

	mock.ExpectQuery(`(?s)WITH successful AS MATERIALIZED.*actual_cost > 0.*ul\.stream = TRUE.*ranked AS.*WHERE duration_ms IS NOT NULL.*ops_error_logs.*oe\.stream = TRUE.*oe\.is_count_tokens = FALSE`).
		WithArgs(pq.Array(accountIDs), start, realtimeStart, end).
		WillReturnRows(rows)

	repo := newUsageLogRepositoryWithSQL(nil, db)
	stats, err := repo.GetAccountQualityStatsBatch(context.Background(), accountIDs, start, realtimeStart, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Equal(t, int64(10), stats[11].Recent1h.Last10.SampleCount)
	require.Equal(t, int64(8), stats[11].Recent1h.Last10.FirstTokenSampleCount)
	require.InDelta(t, 700.0, *stats[11].Recent1h.Last10.AverageFirstTokenMs, 0.001)
	require.Equal(t, int64(72), stats[11].Last24h.Last100.SampleCount)
	require.Equal(t, int64(18), stats[11].SuccessfulRequests1h)
	require.Equal(t, int64(2), stats[11].FailedRequests1h)
	require.Equal(t, &lastSuccess, stats[11].LastSuccessAt)
	require.Equal(t, &lastError, stats[11].LastErrorAt)
	require.Nil(t, stats[22].Last24h.Last10.AverageFirstTokenMs)
	require.NotNil(t, stats[22].Last24h.Last10.AverageDurationMs)
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
		int64(5), int64(5), 540.0, 5900.0,
		int64(10), int64(9), 640.0, 6100.0,
		int64(84), int64(70), 920.0, 7300.0,
		int64(5), int64(0), end.Add(-time.Minute), nil,
	)

	mock.ExpectQuery(`(?s)WITH successful AS MATERIALIZED.*ul\.group_id.*PARTITION BY group_id.*ops_error_logs`).
		WithArgs(pq.Array(groupIDs), start, realtimeStart, end).
		WillReturnRows(rows)

	repo := newUsageLogRepositoryWithSQL(nil, db)
	stats, err := repo.GetGroupQualityStatsBatch(context.Background(), groupIDs, start, realtimeStart, end)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, int64(5), stats[7].Recent1h.Last10.SampleCount)
	require.Equal(t, int64(10), stats[7].Last24h.Last10.SampleCount)
	require.Equal(t, int64(9), stats[7].Last24h.Last10.FirstTokenSampleCount)
	require.InDelta(t, 7300, *stats[7].Last24h.Last100.AverageDurationMs, 0.001)
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
		"realtime_last_10_count", "realtime_last_10_first_count", "realtime_last_10_first_avg", "realtime_last_10_duration_avg",
		"realtime_last_100_count", "realtime_last_100_first_count", "realtime_last_100_first_avg", "realtime_last_100_duration_avg",
		"last_10_count", "last_10_first_count", "last_10_first_avg", "last_10_duration_avg",
		"last_100_count", "last_100_first_count", "last_100_first_avg", "last_100_duration_avg",
		"successful_requests_1h", "failed_requests_1h", "last_success_at", "last_error_at",
	})
}
