package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestScheduledTestPlanRepositoryListDueExcludesSoftDeletedAccounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery(`(?s)FROM scheduled_test_plans AS stp\s+JOIN accounts AS a ON a\.id = stp\.account_id AND a\.deleted_at IS NULL\s+WHERE stp\.enabled = true`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account_id", "model_id", "cron_expression", "enabled", "max_results", "auto_recover",
			"last_run_at", "next_run_at", "created_at", "updated_at",
		}))

	repo := &scheduledTestPlanRepository{db: db}
	plans, err := repo.ListDue(context.Background(), time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC))

	require.NoError(t, err)
	require.Empty(t, plans)
	require.NoError(t, mock.ExpectationsWereMet())
}
