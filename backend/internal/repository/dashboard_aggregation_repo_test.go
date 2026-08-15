package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newDashboardAggregationSQLMock(t *testing.T) (*dashboardAggregationRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return newDashboardAggregationRepositoryWithSQL(db), mock
}

func TestDashboardAggregationRepositoryCleanupUsageLogsRequiresUserAggregateCoverage(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	earliest := cutoff.AddDate(0, 0, -30)
	latest := cutoff.Add(-time.Second)

	mock.ExpectQuery("SELECT EXISTS").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillPartial, earliest, latest.Add(time.Second), nil))
	mock.ExpectRollback()

	err := repo.CleanupUsageLogs(context.Background(), cutoff)
	require.ErrorContains(t, err, "do not cover")
}

func TestDashboardAggregationRepositoryCleanupUsageLogsAllowsCoveredRange(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	fixedNow := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	repo.clock = func() time.Time { return fixedNow }
	earliest := cutoff.AddDate(0, 0, -30)
	latest := cutoff.Add(-time.Second)

	mock.ExpectQuery("SELECT EXISTS").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillAvailable, earliest, cutoff, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MIN(created_at), MAX(created_at)")).
		WithArgs(cutoff).
		WillReturnRows(sqlmock.NewRows([]string{"min", "max"}).AddRow(earliest, latest))
	mock.ExpectQuery("SELECT id FROM usage_group_rollup_state.*FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	mock.ExpectQuery("WITH victims AS").
		WithArgs(cutoff, usageLogsCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(latest).AddRow(earliest))
	mock.ExpectExec("UPDATE usage_group_rollup_state").
		WithArgs(earliest, service.GroupUsageTimezoneName()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT closed_before::text, retained_from.*FOR UPDATE").
		WillReturnRows(sqlmock.NewRows([]string{"closed_before", "retained_from", "timezone_name"}).
			AddRow("2026-08-14", time.Unix(0, 0).UTC(), service.GroupUsageTimezoneName()))
	mock.ExpectCommit()

	require.NoError(t, repo.CleanupUsageLogs(context.Background(), cutoff))
}

func TestDashboardAggregationRepositoryEnsureUserUsageBackfillNoLogsIsAvailable(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillUnavailable, nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MIN(created_at), statement_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"min", "statement_timestamp"}).AddRow(nil, now))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_backfill_state").
		WithArgs(nil, now, now, userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillBuilding, now, now, now))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT statement_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"statement_timestamp"}).AddRow(now))
	mock.ExpectExec("UPDATE usage_dashboard_user_backfill_state").
		WithArgs(userUsageBackfillAvailable, now, configuredDate(now.Add(-time.Nanosecond)), userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.EnsureUserUsageBackfill(context.Background()))
}

func TestDashboardAggregationRepositoryEnsureUserUsageBackfillResumesOneDayChunk(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	start := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	cursor := start.Add(24 * time.Hour)
	targetEnd := cursor.Add(24 * time.Hour)

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillPartial, start, cursor, targetEnd))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MIN(created_at), statement_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"min", "statement_timestamp"}).AddRow(start, targetEnd.Add(time.Hour)))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_backfill_state").
		WithArgs(configuredDate(start), start, targetEnd, userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(cursor, targetEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_dashboard_user_backfill_state").
		WithArgs(targetEnd, configuredDate(targetEnd.Add(-time.Nanosecond)), userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	finalNow := targetEnd.Add(time.Hour)
	hourStart, hourEnd, _, _ := userUsageBucketRange(targetEnd, finalNow)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillBuilding, start, targetEnd, targetEnd))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT statement_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"statement_timestamp"}).AddRow(finalNow))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(targetEnd, finalNow, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE usage_dashboard_user_backfill_state").
		WithArgs(userUsageBackfillAvailable, finalNow, configuredDate(finalNow.Add(-time.Nanosecond)), userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.EnsureUserUsageBackfill(context.Background()))
}

func TestDashboardAggregationRepositoryEnsureUserUsageBackfillFailureIsRetryablePartial(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	start := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	targetEnd := start.Add(6 * time.Hour)
	backfillErr := errors.New("temporary aggregate failure")

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillUnavailable, nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MIN(created_at), statement_timestamp()")).
		WillReturnRows(sqlmock.NewRows([]string{"min", "statement_timestamp"}).AddRow(start, targetEnd))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_backfill_state").
		WithArgs(configuredDate(start), start, targetEnd, userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(start, targetEnd, sqlmock.AnyArg()).
		WillReturnError(backfillErr)
	mock.ExpectRollback()
	mock.ExpectExec("UPDATE usage_dashboard_user_backfill_state").
		WithArgs(userUsageBackfillPartial, backfillErr.Error(), userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.EnsureUserUsageBackfill(context.Background())
	require.ErrorIs(t, err, backfillErr)
}

func TestDashboardAggregationRepositoryPartialMarkerCannotDowngradeAvailableState(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	cause := errors.New("commit result uncertain")

	mock.ExpectExec("WHERE id = 1 AND status = \\$3").
		WithArgs(userUsageBackfillPartial, cause.Error(), userUsageBackfillBuilding).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, repo.markUserUsageBackfillPartial(context.Background(), cause))
}

func TestDashboardAggregationRepositoryCleanupAggregatesRetainsUserDaily(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	hourlyCutoff := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	dailyCutoff := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)

	mock.ExpectExec("DELETE FROM usage_dashboard_hourly WHERE").WithArgs(hourlyCutoff).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_hourly_users WHERE").WithArgs(hourlyCutoff).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_user_hourly WHERE").WithArgs(hourlyCutoff).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily WHERE").WithArgs(dailyCutoff).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily_users WHERE").WithArgs(dailyCutoff).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.CleanupAggregates(context.Background(), hourlyCutoff, dailyCutoff))
}

func TestUserUsageHourlyAggregationIncludesFreeTokenUsageAndAccountCostFallback(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectExec("date_trunc\\('hour', created_at AT TIME ZONE 'UTC'\\) AT TIME ZONE 'UTC'").
		WithArgs(start, end).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.upsertUserHourlyAggregates(context.Background(), start, end))
}

func TestUserUsageDailyAggregationReadsRawLogsAtConfiguredDayBoundaries(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	start := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectExec("FROM usage_logs").
		WithArgs(start, end, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.upsertUserDailyAggregates(context.Background(), start, end))
}

func TestUserUsageDailySyncAddsPartialBoundaryAndReplacesLaterDays(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	coverageStart := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	coverageEnd := time.Date(2026, 8, 1, 2, 0, 0, 0, time.UTC)
	dayBoundary := nextConfiguredDayBoundary(coverageStart).UTC()

	mock.ExpectExec("usage_dashboard_user_daily.input_tokens").
		WithArgs(coverageStart, dayBoundary, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(dayBoundary, coverageEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.syncUserDailyAggregatesFromCoverage(context.Background(), coverageStart, coverageEnd))
}

func TestUserUsageBucketRangeUsesUTCForHourlyIdentity(t *testing.T) {
	offset := time.FixedZone("UTC+05:45", 5*60*60+45*60)
	start := time.Date(2026, 7, 30, 0, 10, 0, 0, offset)
	end := start.Add(75 * time.Minute)

	hourStart, hourEnd, _, _ := userUsageBucketRange(start, end)

	require.Equal(t, 0, hourStart.Minute())
	require.Equal(t, time.UTC, hourStart.Location())
	require.Equal(t, 0, hourEnd.Minute())
	require.Equal(t, time.UTC, hourEnd.Location())
	require.True(t, hourEnd.After(hourStart))
}

func TestDashboardAggregationRepositoryAvailableCoverageDoesNotBridgeGaps(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectExec(regexp.QuoteMeta("AND $1 <= coverage_end")).
		WithArgs(
			start,
			end,
			configuredDate(start),
			configuredDate(end.Add(-time.Nanosecond)),
			userUsageBackfillAvailable,
		).
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.ErrorContains(t, repo.advanceAvailableUserUsageCoverage(context.Background(), start, end), "affected 0 rows")
}

func TestDashboardAggregationRepositoryRecomputePreservesPermanentUserAggregates(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	hourStart := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	hourEnd := hourStart.Add(24 * time.Hour)
	dayStart := hourStart
	dayEnd := hourEnd

	mock.ExpectExec("DELETE FROM usage_dashboard_hourly WHERE").WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_hourly_users WHERE").WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily WHERE").WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM usage_dashboard_daily_users WHERE").WithArgs(dayStart, dayEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_hourly_users").
		WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily_users").WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_hourly").WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily").WithArgs(dayStart, dayEnd, dayStart, dayEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.recomputeRangeInTx(context.Background(), hourStart, hourEnd, dayStart, dayEnd))
}

func TestDashboardAggregationRepositoryAggregateRangePreservesPermanentUserAggregates(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	hourStart := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	hourEnd := hourStart.Add(24 * time.Hour)
	dayStart := hourStart
	dayEnd := hourEnd

	mock.ExpectExec("INSERT INTO usage_dashboard_hourly_users").
		WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily_users").
		WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_hourly").
		WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_daily").
		WithArgs(dayStart, dayEnd, dayStart, dayEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.aggregateRangeInTx(context.Background(), hourStart, hourEnd, dayStart, dayEnd))
}

func TestDashboardAggregationRepositoryAggregateUserUsageRangeOwnsPermanentAggregates(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	hourStart := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	hourEnd := hourStart.Add(time.Hour)

	mock.ExpectExec("INSERT INTO usage_dashboard_user_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(hourStart, hourEnd, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("AND $1 <= coverage_end")).
		WithArgs(
			hourStart,
			hourEnd,
			configuredDate(hourStart),
			configuredDate(hourEnd.Add(-time.Nanosecond)),
			userUsageBackfillAvailable,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.aggregateUserUsageRangeInTx(
		context.Background(),
		hourStart,
		hourEnd,
		hourStart,
		hourEnd,
	))
}

func TestDashboardAggregationRepositoryAggregateUserUsageRangeCatchesUpFromCoverageEnd(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	coverageEnd := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	requestedStart := coverageEnd.Add(28 * time.Minute)
	end := coverageEnd.Add(30 * time.Minute)
	hourStart, hourEnd, _, _ := userUsageBucketRange(requestedStart, end)

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillAvailable, coverageEnd.AddDate(0, 0, -30), coverageEnd, nil))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(coverageEnd, end, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("AND $1 <= coverage_end")).
		WithArgs(
			coverageEnd,
			end,
			configuredDate(coverageEnd),
			configuredDate(end.Add(-time.Nanosecond)),
			userUsageBackfillAvailable,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.aggregateAvailableUserUsageRangeInTx(context.Background(), requestedStart, end))
}

func TestDashboardAggregationRepositoryAggregateUserUsageRangeHandlesZeroLookback(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	end := time.Date(2026, 7, 30, 10, 30, 0, 0, time.UTC)
	coverageEnd := end.Add(-time.Minute)
	hourStart, hourEnd, _, _ := userUsageBucketRange(end.Add(-time.Hour), end)

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillAvailable, coverageEnd.AddDate(0, 0, -30), coverageEnd, nil))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(coverageEnd, end, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("AND $1 <= coverage_end")).
		WithArgs(
			coverageEnd,
			end,
			configuredDate(coverageEnd),
			configuredDate(end.Add(-time.Nanosecond)),
			userUsageBackfillAvailable,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.aggregateAvailableUserUsageRangeInTx(context.Background(), end, end))
}

func TestDashboardAggregationRepositoryAggregateUserUsageRangeDoesNotRecountLookbackOverlap(t *testing.T) {
	repo, mock := newDashboardAggregationSQLMock(t)
	coverageEnd := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	requestedStart := coverageEnd.Add(-time.Minute)
	end := coverageEnd.Add(time.Minute)
	hourStart, hourEnd, _, _ := userUsageBucketRange(requestedStart, end)

	mock.ExpectQuery("SELECT status, coverage_start, coverage_end, target_end").
		WillReturnRows(sqlmock.NewRows([]string{"status", "coverage_start", "coverage_end", "target_end"}).
			AddRow(userUsageBackfillAvailable, coverageEnd.AddDate(0, 0, -30), coverageEnd, nil))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_hourly").
		WithArgs(hourStart, hourEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO usage_dashboard_user_daily").
		WithArgs(coverageEnd, end, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("AND $1 <= coverage_end")).
		WithArgs(
			coverageEnd,
			end,
			configuredDate(coverageEnd),
			configuredDate(end.Add(-time.Nanosecond)),
			userUsageBackfillAvailable,
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.aggregateAvailableUserUsageRangeInTx(context.Background(), requestedStart, end))
}
