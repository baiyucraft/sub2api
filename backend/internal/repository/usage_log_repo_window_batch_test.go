package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAccountWindowStatsByStartsOneQueryAndZeroUsage(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	starts := map[int64]time.Time{11: now.Add(-time.Hour), 12: now.Add(-5 * time.Hour)}
	mock.ExpectQuery(`(?s)FROM unnest\(\$1::bigint\[\], \$2::timestamptz\[\]\).*LEFT JOIN usage_logs.*created_at >= w.starts_at`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "requests", "tokens", "cost", "standard_cost", "user_cost"}).AddRow(11, 2, 90, 1.2, 1.5, 0.8).AddRow(12, 0, 0, 0, 0, 0))
	repo := &usageLogRepository{sql: db}
	stats, err := repo.GetAccountWindowStatsByStarts(context.Background(), starts)
	require.NoError(t, err)
	require.Len(t, stats, 2)
	require.Equal(t, int64(2), stats[11].Requests)
	require.Equal(t, 1.5, stats[11].StandardCost)
	require.Zero(t, stats[12].Requests)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountWindowStatsByStartsEmptyDoesNotQuery(t *testing.T) {
	repo := &usageLogRepository{}
	stats, err := repo.GetAccountWindowStatsByStarts(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, stats)
}
