package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserGroupRateRepositorySyncGroupRateMultipliersClearsOnlyRateColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userGroupRateRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE user_group_rate_multipliers[\\s\\S]+SET rate_percent = NULL[\\s\\S]+WHERE group_id = \\$1").
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec("DELETE FROM user_group_rate_multipliers[\\s\\S]+rate_percent IS NULL[\\s\\S]+rpm_override IS NULL").
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, repo.SyncGroupRateMultipliers(context.Background(), 42, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserGroupRateRepositorySyncGroupRateMultipliersRollsBackOnUpsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userGroupRateRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE user_group_rate_multipliers[\\s\\S]+user_id <> ALL").
		WithArgs(int64(42), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM user_group_rate_multipliers[\\s\\S]+rpm_override IS NULL").
		WithArgs(int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO user_group_rate_multipliers[\\s\\S]+rate_percent").
		WithArgs(int64(42), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("foreign key violation"))
	mock.ExpectRollback()

	percent := 50.0
	err = repo.SyncGroupRateMultipliers(context.Background(), 42, []service.GroupRateMultiplierInput{
		{UserID: 7, RatePercent: &percent},
	})
	require.EqualError(t, err, "foreign key violation")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserGroupRateRepositorySyncUserGroupRatePercentsRollsBackOnUpsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userGroupRateRepository{sql: db}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE user_group_rate_multipliers[\\s\\S]+group_id = ANY").
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM user_group_rate_multipliers[\\s\\S]+group_id = ANY").
		WithArgs(int64(9), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO user_group_rate_multipliers[\\s\\S]+rate_percent").
		WithArgs(int64(9), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()

	percent := 75.0
	err = repo.SyncUserGroupRatePercents(context.Background(), 9, map[int64]*float64{
		1: nil,
		2: &percent,
	})
	require.EqualError(t, err, "write failed")
	require.NoError(t, mock.ExpectationsWereMet())
}
