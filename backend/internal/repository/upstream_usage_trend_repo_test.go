package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func newUpstreamTrendSQLMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func TestUpstreamUsageTrendSQLPreservesCostAndAttributionSemantics(t *testing.T) {
	compactSQL := strings.Join(strings.Fields(upstreamUsageTrendSQL), " ")
	require.Contains(t, compactSQL, "COALESCE(ul.upstream_config_id, a.upstream_config_id)")
	require.Contains(t, compactSQL, "ul.group_id = ANY(p.group_ids)")
	require.Contains(t, compactSQL, "ul.upstream_config_id IS NULL AND a.upstream_config_id IS NOT NULL")
	require.Contains(t, compactSQL, "COALESCE(ul.account_stats_cost, ul.total_cost) AS upstream_base_cost")
	require.Contains(t, compactSQL, "COALESCE(ul.account_rate_multiplier, 1) AS account_rate_multiplier")
	require.Contains(t, compactSQL, "ul.upstream_cost_to_cny_rate > 0")
	require.Contains(t, compactSQL, "upstream_base_cost * account_rate_multiplier * cny_rate")
	require.Contains(t, compactSQL, "billed_cost * cny_rate")
	require.Contains(t, compactSQL, "cny_rate IS NULL THEN upstream_base_cost * account_rate_multiplier")
	require.Contains(t, compactSQL, "AT TIME ZONE 'UTC'")
	require.NotContains(t, compactSQL, "COALESCE(ul.upstream_cost_to_cny_rate, 1)")
}

func TestQueryUpstreamUsageTrendFiltersConfigAndScansCosts(t *testing.T) {
	db, mock := newUpstreamTrendSQLMock(t)
	now := time.Date(2026, 7, 10, 6, 35, 0, 0, time.UTC)
	configID := int64(42)
	groupIDs := []int64{2, 5}
	bucket := time.Date(2026, 7, 9, 7, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`WITH params AS`).
		WithArgs(
			time.Date(2026, 7, 9, 7, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 10, 7, 0, 0, 0, time.UTC),
			"1 hour",
			"hour",
			&configID,
			pq.Array(groupIDs),
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"bucket_start", "requests", "upstream_base_cost", "upstream_cost",
			"billed_cost", "gross_profit", "unconverted_cost", "legacy_attributed_requests",
		}).AddRow(bucket, int64(3), 14.0, 21.0, 35.0, 14.0, 5.5, int64(2)))

	trend, err := QueryUpstreamUsageTrend(context.Background(), db, usagestats.UpstreamUsageTrendQuery{
		UpstreamConfigID: &configID,
		GroupIDs:         groupIDs,
		Range:            usagestats.UpstreamUsageTrendRange24H,
		Now:              now,
	})
	require.NoError(t, err)
	require.Equal(t, usagestats.UpstreamUsageTrendRange24H, trend.Range)
	require.Equal(t, usagestats.UpstreamUsageTrendCurrency, trend.Currency)
	require.Equal(t, int64(2), trend.LegacyAttributedRequests)
	require.Equal(t, []usagestats.UpstreamUsageTrendPoint{{
		Bucket:           "2026-07-09T07:00:00Z",
		Requests:         3,
		UpstreamBaseCost: 14,
		UpstreamCost:     21,
		BilledCost:       35,
		GrossProfit:      14,
		UnconvertedCost:  5.5,
	}}, trend.Points)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryUpstreamUsageTrendSupportsUnfilteredDailyRange(t *testing.T) {
	db, mock := newUpstreamTrendSQLMock(t)
	now := time.Date(2026, 7, 10, 23, 59, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)generate_series.*p\.upstream_config_id IS NULL.*ORDER BY b\.bucket_start ASC`).
		WithArgs(
			time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
			"1 day",
			"day",
			nil,
			nil,
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"bucket_start", "requests", "upstream_base_cost", "upstream_cost",
			"billed_cost", "gross_profit", "unconverted_cost", "legacy_attributed_requests",
		}))

	trend, err := QueryUpstreamUsageTrend(context.Background(), db, usagestats.UpstreamUsageTrendQuery{
		Range: usagestats.UpstreamUsageTrendRange7D,
		Now:   now,
	})
	require.NoError(t, err)
	require.NotNil(t, trend.Points)
	require.Empty(t, trend.Points)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryUpstreamUsageTrendRejectsInvalidRangeBeforeQuery(t *testing.T) {
	db, mock := newUpstreamTrendSQLMock(t)

	trend, err := QueryUpstreamUsageTrend(context.Background(), db, usagestats.UpstreamUsageTrendQuery{
		Range: "90d",
		Now:   time.Now(),
	})
	require.Nil(t, trend)
	require.ErrorContains(t, err, "unsupported upstream usage trend range")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryUpstreamUsageTrendReturnsQueryError(t *testing.T) {
	db, mock := newUpstreamTrendSQLMock(t)
	wantErr := errors.New("query failed")
	mock.ExpectQuery(`WITH params AS`).WillReturnError(wantErr)

	trend, err := QueryUpstreamUsageTrend(context.Background(), db, usagestats.UpstreamUsageTrendQuery{
		Range: usagestats.UpstreamUsageTrendRange30D,
		Now:   time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
	})
	require.Nil(t, trend)
	require.ErrorIs(t, err, wantErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestQueryUpstreamUsageTrendReturnsScanError(t *testing.T) {
	db, mock := newUpstreamTrendSQLMock(t)
	mock.ExpectQuery(`WITH params AS`).WillReturnRows(sqlmock.NewRows([]string{
		"bucket_start", "requests", "upstream_base_cost", "upstream_cost",
		"billed_cost", "gross_profit", "unconverted_cost", "legacy_attributed_requests",
	}).AddRow("not-a-time", int64(1), 1.0, 1.0, 1.0, 0.0, 0.0, int64(0)))

	trend, err := QueryUpstreamUsageTrend(context.Background(), db, usagestats.UpstreamUsageTrendQuery{
		Range: usagestats.UpstreamUsageTrendRange24H,
		Now:   time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
	})
	require.Nil(t, trend)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
