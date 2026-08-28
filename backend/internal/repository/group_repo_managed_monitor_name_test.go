//go:build unit

package repository

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestSyncManagedChannelMonitorNamesUpdatesOnlyManagedActiveKeys(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer db.Close()

	mock.ExpectExec(`WITH updated_monitors`).
		WithArgs("新分组", int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 2))

	repo := &groupRepository{sql: db}
	require.NoError(t, repo.SyncManagedChannelMonitorNames(context.Background(), 7, " 新分组 "))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSyncManagedChannelMonitorNamesSkipsInvalidInput(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	repo := &groupRepository{sql: db}
	require.NoError(t, repo.SyncManagedChannelMonitorNames(context.Background(), 0, "新分组"))
	require.NoError(t, repo.SyncManagedChannelMonitorNames(context.Background(), 7, " "))
	require.NoError(t, mock.ExpectationsWereMet())
}
