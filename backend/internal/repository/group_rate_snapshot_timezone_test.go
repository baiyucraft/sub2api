package repository

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestEnsureGroupRateTimezoneSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(groupRateTimezoneLockSQL)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(ensureGroupRateTimezoneSnapshotsSQL)).
		WithArgs("Asia/Shanghai").
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectCommit()

	require.NoError(t, ensureGroupRateTimezoneSnapshots(context.Background(), db, "Asia/Shanghai"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestEnsureGroupRateTimezoneSnapshots_ReportsSQLState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(groupRateTimezoneLockSQL)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(ensureGroupRateTimezoneSnapshotsSQL)).
		WithArgs("Asia/Shanghai").
		WillReturnError(fmt.Errorf("wrapped: %w", &pq.Error{Code: "22023"}))
	mock.ExpectRollback()
	err = ensureGroupRateTimezoneSnapshots(context.Background(), db, "Asia/Shanghai")
	require.ErrorContains(t, err, "sqlstate=22023")
}

func TestEnsureGroupRateTimezoneSnapshots_RejectsMissingConfiguration(t *testing.T) {
	err := ensureGroupRateTimezoneSnapshots(context.Background(), nil, "")
	require.ErrorContains(t, err, "timezone and database are required")
}

func TestEnsureGroupRateTimezoneSnapshots_SerializesConcurrentStartups(t *testing.T) {
	require.Contains(t, groupRateTimezoneLockSQL, "pg_advisory_xact_lock(195, 1)")
	require.Contains(t, ensureGroupRateTimezoneSnapshotsSQL, "latest.timezone IS DISTINCT FROM $1")
	require.Contains(t, ensureGroupRateTimezoneSnapshotsSQL, "$1::varchar(64)")
}
