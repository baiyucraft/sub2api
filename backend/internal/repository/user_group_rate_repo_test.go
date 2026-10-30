package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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

func TestUserGroupRateRepositoryRuntimeLookupIncludesUserDefault(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "inherited 95 percent", value: 95.0},
		{name: "explicit zero", value: 0.0},
		{name: "unconfigured", value: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			repo := &userGroupRateRepository{sql: db}
			mock.ExpectQuery(`SELECT COALESCE\(ugr.rate_percent,[\s\S]+u.default_group_rate_percent\)[\s\S]+FROM users u[\s\S]+LEFT JOIN user_group_rate_multipliers ugr[\s\S]+WHERE u.id = \$1 AND u.deleted_at IS NULL`).
				WithArgs(int64(135), int64(8)).WillReturnRows(sqlmock.NewRows([]string{"percent"}).AddRow(tc.value))
			got, err := repo.GetPercentByUserAndGroup(context.Background(), 135, 8)
			require.NoError(t, err)
			if tc.value == nil {
				require.Nil(t, got)
			} else {
				require.NotNil(t, got)
				require.Equal(t, tc.value, *got)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserGroupRateRepositoryEffectiveUserMapIncludesInheritedAndExplicitZero(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userGroupRateRepository{sql: db}
	mock.ExpectQuery(`SELECT g.id,[\s\S]+u.default_group_rate_percent / 100.0[\s\S]+FROM users u[\s\S]+JOIN groups g ON TRUE[\s\S]+LEFT JOIN user_group_rate_multipliers ugr[\s\S]+us.expires_at > \$2[\s\S]+NOT g.is_exclusive AND NOT u.restrict_public_groups[\s\S]+FROM user_allowed_groups uag`).
		WithArgs(int64(135), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id", "rate"}).AddRow(8, .19).AddRow(21, 0))
	got, err := repo.GetEffectiveByUserID(context.Background(), 135)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{8: .19, 21: 0}, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

// Execute the production query unchanged: mocked rows alone cannot establish
// that inherited prices respect the public group binding boundary.
func TestUserGroupRateRepositoryEffectiveUserMapPermissionBoundary(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	repo := &userGroupRateRepository{sql: db}
	_, err = db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY, default_group_rate_percent REAL, restrict_public_groups BOOLEAN, deleted_at TIMESTAMP);
		CREATE TABLE groups (id INTEGER PRIMARY KEY, rate_multiplier REAL, status TEXT, subscription_type TEXT, is_exclusive BOOLEAN, deleted_at TIMESTAMP);
		CREATE TABLE user_group_rate_multipliers (user_id INTEGER, group_id INTEGER, rate_percent REAL, rate_multiplier REAL);
		CREATE TABLE user_allowed_groups (user_id INTEGER, group_id INTEGER);
		CREATE TABLE user_subscriptions (user_id INTEGER, group_id INTEGER, status TEXT, starts_at TIMESTAMP, expires_at TIMESTAMP, deleted_at TIMESTAMP);
		INSERT INTO users VALUES (135, 95, FALSE, NULL), (136, NULL, FALSE, NULL);
		INSERT INTO groups VALUES
		 (1, 2, 'active', 'standard', FALSE, NULL),
		 (2, 2, 'active', 'standard', TRUE, NULL),
		 (3, 2, 'active', 'standard', TRUE, NULL),
		 (4, 2, 'active', 'subscription', FALSE, NULL),
		 (5, 2, 'active', 'subscription', FALSE, NULL),
		 (6, 2, 'active', 'subscription', FALSE, NULL),
		 (7, 2, 'active', 'subscription', FALSE, NULL),
		 (8, 2, 'active', 'subscription', FALSE, NULL),
		 (9, 2, 'inactive', 'standard', FALSE, NULL),
		 (10, 2, 'active', 'standard', FALSE, '2020-01-01'),
		 (11, 2, 'inactive', 'standard', TRUE, '2020-01-01'),
		 (12, 2, 'active', 'standard', TRUE, NULL),
		 (13, 2, 'active', 'subscription', FALSE, NULL);
		INSERT INTO user_allowed_groups VALUES (135, 3), (135, 5);
		INSERT INTO user_group_rate_multipliers VALUES
		 (135, 11, 25, NULL), (135, 12, 0, NULL), (135, 13, NULL, 0.7),
		 (136, 12, 0, NULL);`)
	require.NoError(t, err)
	now := time.Now()
	for _, sub := range []struct {
		groupID int64
		status  string
		expires time.Time
		deleted any
	}{
		{4, "active", now.Add(24 * time.Hour), nil},
		{6, "active", now.Add(-24 * time.Hour), nil},
		{7, "inactive", now.Add(24 * time.Hour), nil},
		{8, "active", now.Add(24 * time.Hour), now},
	} {
		// The existing active subscription contract intentionally does not
		// check starts_at; retain that behavior for inherited prices too.
		_, err = db.Exec(`INSERT INTO user_subscriptions VALUES (135, ?, ?, ?, ?, ?)`, sub.groupID, sub.status, now.Add(12*time.Hour), sub.expires, sub.deleted)
		require.NoError(t, err)
	}
	assertRates := func(want map[int64]float64) {
		t.Helper()
		got, err := repo.GetEffectiveByUserID(context.Background(), 135)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	assertRates(map[int64]float64{1: 1.9, 3: 1.9, 4: 1.9, 11: .5, 12: 0, 13: .7})
	_, err = db.Exec(`UPDATE users SET restrict_public_groups=TRUE WHERE id=135`)
	require.NoError(t, err)
	assertRates(map[int64]float64{3: 1.9, 4: 1.9, 11: .5, 12: 0, 13: .7})
	_, err = db.Exec(`INSERT INTO user_allowed_groups VALUES (135, 1); UPDATE users SET default_group_rate_percent=0 WHERE id=135`)
	require.NoError(t, err)
	assertRates(map[int64]float64{1: 0, 3: 0, 4: 0, 11: .5, 12: 0, 13: .7})
	_, err = db.Exec(`UPDATE users SET default_group_rate_percent=NULL WHERE id=135`)
	require.NoError(t, err)
	assertRates(map[int64]float64{11: .5, 12: 0, 13: .7})
	got, err := repo.GetEffectiveByUserID(context.Background(), 136)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{12: 0}, got, "unconfigured user must not acquire inherited entries")
}

func TestUserGroupRateRepositoryEffectiveRateIncludesUserDefault(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userGroupRateRepository{sql: db}
	mock.ExpectQuery(`SELECT COALESCE\(g.rate_multiplier \* ugr.rate_percent / 100.0,[\s\S]+ugr.rate_multiplier,[\s\S]+g.rate_multiplier \* u.default_group_rate_percent / 100.0\)[\s\S]+FROM users u[\s\S]+LEFT JOIN user_group_rate_multipliers ugr`).
		WithArgs(int64(135), int64(8)).WillReturnRows(sqlmock.NewRows([]string{"rate"}).AddRow(0.19))
	got, err := repo.GetByUserAndGroup(context.Background(), 135, 8)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, 0.19, *got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserGroupRateRepositoryResetUsesCallerTransactionAndPreservesRPM(t *testing.T) {
	for _, compatibility := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", compatibility), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			tx, err := client.Tx(context.Background())
			require.NoError(t, err)
			// No base DB is supplied: starting a nested transaction would fail.
			repo := &userGroupRateRepository{}
			prefix := "SET rate_percent = NULL, rate_multiplier = NULL, updated_at = NOW()"
			if compatibility {
				prefix = "SET rate_multiplier = NULL, rate_percent = NULL, updated_at = NOW()"
			}
			mock.ExpectExec(`UPDATE user_group_rate_multipliers[\s\S]+` + regexp.QuoteMeta(prefix) + `[\s\S]+WHERE user_id = \$1`).
				WithArgs(int64(135)).WillReturnResult(sqlmock.NewResult(0, 4))
			mock.ExpectExec(`DELETE FROM user_group_rate_multipliers[\s\S]+rate_percent IS NULL AND rate_multiplier IS NULL AND rpm_override IS NULL`).
				WithArgs(int64(135)).WillReturnResult(sqlmock.NewResult(0, 3))
			ctx := dbent.NewTxContext(context.Background(), tx)
			if compatibility {
				err = repo.SyncUserGroupRates(ctx, 135, map[int64]*float64{})
			} else {
				err = repo.SyncUserGroupRatePercents(ctx, 135, map[int64]*float64{})
			}
			require.NoError(t, err)
			mock.ExpectRollback()
			require.NoError(t, tx.Rollback())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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
