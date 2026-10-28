package repository

import (
	"context"
	"net/http"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type scopedForkSchedulerCache struct {
	service.SchedulerCache
}

func TestAccountTransactionScopePreservesForkRepositoryAndSuppressesEarlyCache(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	cache := &scopedForkSchedulerCache{}
	repo := newAccountRepositoryWithSQL(client, db, cache)

	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	ctx := dbent.NewTxContext(context.Background(), tx)
	scoped := repo.accountTransactionScope(ctx)
	require.NotSame(t, repo, scoped)
	require.Same(t, tx.Client(), scoped.client)
	require.Same(t, tx.Client(), scoped.sql)
	require.Nil(t, scoped.schedulerCache)
	require.Same(t, client, repo.client)
	require.Same(t, cache, repo.schedulerCache)
	require.Same(t, scoped, scoped.accountTransactionScope(ctx))

	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestScopedAccountPreferredWriteRejectedBeforeDatabaseAccess(t *testing.T) {
	for _, preferred := range [][]int64{nil, {}, {7}} {
		t.Run("rejects exact preferred pool replacement", func(t *testing.T) {
			ctx := adminaccess.WithPrincipal(context.Background(), &adminaccess.Principal{})
			repo := &accountRepository{}
			err := repo.BindGroupsWithPreferred(ctx, 41, []int64{7}, preferred)
			var appErr *infraerrors.ApplicationError
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, int32(http.StatusForbidden), appErr.Code)
			require.Equal(t, "SCOPED_ACCOUNT_FORBIDDEN", appErr.Reason)
		})
	}
}

func TestCreateWithAccountGroupsReusesCallerTransaction(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	tx, err := client.Tx(context.Background())
	require.NoError(t, err)
	ctx := dbent.NewTxContext(context.Background(), tx)

	mock.ExpectQuery(`INSERT INTO "accounts"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(41)))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).
		WillReturnResult(sqlmock.NewResult(1, 1))
	repo := newAccountRepositoryWithSQL(client, db, nil)
	account := &service.Account{
		Name: "outer transaction", Platform: service.PlatformOpenAI,
		Type: service.AccountTypeAPIKey, Status: service.StatusActive,
		Credentials: map[string]any{"api_key": "offline-fixture"}, Schedulable: true,
		Concurrency: 1, Priority: 1,
	}
	require.NoError(t, repo.CreateWithAccountGroups(ctx, account, nil))
	require.Equal(t, int64(41), account.ID)

	// The caller retains the transaction: no nested begin or early commit is allowed.
	mock.ExpectRollback()
	require.NoError(t, tx.Rollback())
	require.NoError(t, mock.ExpectationsWereMet())
}
