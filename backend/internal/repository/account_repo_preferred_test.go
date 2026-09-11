package repository

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositorySetPreferredAccountUpdatesRelationAndOutbox(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const groupID int64 = 7
	const accountID int64 = 12
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT scheduler_preferred FROM account_groups WHERE group_id = $1 AND account_id = $2`)).
		WithArgs(groupID, accountID).
		WillReturnRows(sqlmock.NewRows([]string{"scheduler_preferred"}).AddRow(false))
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE account_groups SET scheduler_preferred = $1 WHERE group_id = $2 AND account_id = $3`)).
		WithArgs(true, groupID, accountID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).
		WithArgs(service.SchedulerOutboxEventGroupChanged, nil, groupID, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).
		WithArgs(service.SchedulerOutboxEventAccountBulkChanged, nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(2, 1))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	require.NoError(t, repo.SetPreferredAccount(context.Background(), groupID, accountID, true))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositorySetPreferredAccountIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const groupID int64 = 7
	const accountID int64 = 12
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT scheduler_preferred FROM account_groups WHERE group_id = $1 AND account_id = $2`)).
		WithArgs(groupID, accountID).
		WillReturnRows(sqlmock.NewRows([]string{"scheduler_preferred"}).AddRow(true))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	require.NoError(t, repo.SetPreferredAccount(context.Background(), groupID, accountID, true))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositorySetPreferredAccountRejectsUnboundRelation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	const groupID int64 = 7
	const accountID int64 = 12
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT scheduler_preferred FROM account_groups WHERE group_id = $1 AND account_id = $2`)).
		WithArgs(groupID, accountID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	err = repo.SetPreferredAccount(context.Background(), groupID, accountID, true)
	require.Error(t, err)
	require.Equal(t, "ACCOUNT_NOT_IN_GROUP", infraerrors.Reason(err))
	require.NoError(t, mock.ExpectationsWereMet())
}
