package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGetBatchUserUsageStatsReadsAggregateWindowsAndKeepsLegacyPlatformSpend(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newUsageLogRepositoryWithSQL(nil, db)

	hourlyColumns := []string{
		"user_id",
		"today_input", "today_output", "today_cache_creation", "today_cache_read", "today_spend", "today_account_cost",
		"last_30d_input", "last_30d_output", "last_30d_cache_creation", "last_30d_cache_read", "last_30d_spend", "last_30d_account_cost",
	}
	mock.ExpectQuery(regexp.QuoteMeta("FILTER (WHERE bucket_date >= $2::date AND bucket_date < $3::date)")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(hourlyColumns).
			AddRow(int64(7), int64(100), int64(20), int64(5), int64(8), 0.0, 0.0, int64(1000), int64(200), int64(50), int64(80), 12.5, 7.25))

	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_dashboard_user_daily")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "user_spend", "account_cost",
		}).AddRow(int64(7), int64(9000), int64(2000), int64(500), int64(800), 99.5, 61.25))

	coverageStart := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	observedAt := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_dashboard_user_backfill_state")).
		WillReturnRows(sqlmock.NewRows([]string{"earliest_covered_date", "coverage_start", "status", "updated_at"}).
			AddRow("2026-05-01", coverageStart, usagestats.UserUsageAggregationAvailable, observedAt))

	mock.ExpectQuery(regexp.QuoteMeta("FROM users")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).
			AddRow(int64(7), time.Date(2026, 5, 2, 8, 0, 0, 0, time.UTC)).
			AddRow(int64(8), time.Date(2026, 4, 30, 8, 0, 0, 0, time.UTC)).
			AddRow(int64(9), time.Date(2026, 5, 1, 4, 0, 0, 0, time.UTC)))

	mock.ExpectQuery("FROM usage_logs ul").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "platform", "total_cost", "today_cost"}).
			AddRow(int64(7), "openai", 12.25, 1.5).
			AddRow(int64(7), nil, 0.75, 0.25))

	stats, err := repo.GetBatchUserUsageStats(
		context.Background(),
		[]int64{9, 8, 7, 7, 0},
		time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	user := stats[7]
	require.NotNil(t, user)
	require.EqualValues(t, 133, user.Today.TotalTokens)
	require.Zero(t, user.Today.UserSpend, "zero-price token usage must remain visible")
	require.EqualValues(t, 1330, user.Last30Days.TotalTokens)
	require.EqualValues(t, 12300, user.Lifetime.TotalTokens)
	require.Equal(t, 12.5, user.Last30Days.UserSpend)
	require.Equal(t, 7.25, user.Last30Days.AccountCost)
	require.Equal(t, 99.5, user.Lifetime.UserSpend)
	require.Equal(t, 61.25, user.Lifetime.AccountCost)
	require.Equal(t, 1.75, user.TodayActualCost)
	require.Equal(t, 13.0, user.TotalActualCost)
	require.Len(t, user.ByPlatform, 1)
	require.Equal(t, "openai", user.ByPlatform[0].Platform)
	require.Equal(t, "2026-05-01", *user.LifetimeSince)
	require.Equal(t, observedAt.Format(time.RFC3339), *user.ObservedAt)
	require.True(t, user.LifetimeComplete)
	require.False(t, stats[8].LifetimeComplete)
	require.False(t, stats[9].LifetimeComplete, "same-day users created before the exact coverage start are partial")
	require.Equal(t, usagestats.UserUsageAggregationAvailable, stats[8].AggregationStatus)
}

func TestGetBatchUserUsageStatsEmptyDoesNotQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := newUsageLogRepositoryWithSQL(nil, db)

	stats, err := repo.GetBatchUserUsageStats(context.Background(), []int64{0, -1}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Empty(t, stats)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeUserUsageAggregationStatus(t *testing.T) {
	for _, status := range []string{
		usagestats.UserUsageAggregationAvailable,
		usagestats.UserUsageAggregationBuilding,
		usagestats.UserUsageAggregationPartial,
		usagestats.UserUsageAggregationUnavailable,
	} {
		require.Equal(t, status, normalizeUserUsageAggregationStatus(status))
	}
	require.Equal(t, usagestats.UserUsageAggregationUnavailable, normalizeUserUsageAggregationStatus(""))
	require.Equal(t, usagestats.UserUsageAggregationUnavailable, normalizeUserUsageAggregationStatus("failed"))
}
