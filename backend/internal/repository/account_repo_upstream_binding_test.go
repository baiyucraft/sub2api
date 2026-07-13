package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	dbupstreamconfig "github.com/Wei-Shaw/sub2api/ent/upstreamconfig"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func TestAccountRepositoryCreateBoundLocksConfigThenKeyAndCommitsOutbox(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := boundAccount(0, 11, 22, service.PlatformOpenAI)

	mock.ExpectBegin()
	expectUpstreamConfigLock(mock, 11, true)
	expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, ptrString(service.PlatformOpenAI), true)
	expectAccountCreate(mock, 101)
	expectSchedulerAccountOutbox(mock, nil)
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), account))
	require.Equal(t, int64(101), account.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryUpdateBoundLocksConfigThenKeyAndCommitsOutbox(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := boundAccount(101, 11, 22, service.PlatformOpenAI)

	mock.ExpectBegin()
	expectUpstreamConfigLock(mock, 11, true)
	expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, ptrString(service.PlatformOpenAI), true)
	expectAccountUpdate(mock, account)
	expectSchedulerAccountOutbox(mock, nil)
	mock.ExpectCommit()

	require.NoError(t, repo.Update(context.Background(), account))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCreateOrdinaryAccountSkipsUpstreamLocks(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := baseAccount(0, service.PlatformAnthropic)

	mock.ExpectBegin()
	expectAccountCreate(mock, 102)
	expectSchedulerAccountOutbox(mock, nil)
	mock.ExpectCommit()

	require.NoError(t, repo.Create(context.Background(), account))
	require.Equal(t, int64(102), account.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryUpdateOrdinaryAccountSkipsUpstreamLocks(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := baseAccount(102, service.PlatformAnthropic)

	mock.ExpectBegin()
	expectAccountUpdate(mock, account)
	expectSchedulerAccountOutbox(mock, nil)
	mock.ExpectCommit()

	require.NoError(t, repo.Update(context.Background(), account))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCreateRollsBackAccountWhenOutboxFails(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := baseAccount(0, service.PlatformAnthropic)
	outboxErr := errors.New("outbox unavailable")

	mock.ExpectBegin()
	expectAccountCreate(mock, 103)
	expectSchedulerAccountOutbox(mock, outboxErr)
	mock.ExpectRollback()

	err := repo.Create(context.Background(), account)
	require.ErrorIs(t, err, outboxErr)
	require.Zero(t, account.ID, "rolled-back create must not publish an account ID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryUpdateRollsBackAccountWhenOutboxFails(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := boundAccount(103, 11, 22, service.PlatformOpenAI)
	originalUpdatedAt := time.Unix(123, 0)
	account.UpdatedAt = originalUpdatedAt
	outboxErr := errors.New("outbox unavailable")

	mock.ExpectBegin()
	expectUpstreamConfigLock(mock, 11, true)
	expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, ptrString(service.PlatformOpenAI), true)
	expectAccountUpdate(mock, account)
	expectSchedulerAccountOutbox(mock, outboxErr)
	mock.ExpectRollback()

	err := repo.Update(context.Background(), account)
	require.ErrorIs(t, err, outboxErr)
	require.Equal(t, originalUpdatedAt, account.UpdatedAt, "rolled-back update must not publish a new timestamp")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountRepositoryCreateRejectsInvalidUpstreamBinding(t *testing.T) {
	tests := []struct {
		name       string
		account    *service.Account
		expectLock func(sqlmock.Sqlmock)
		wantReason string
	}{
		{
			name: "missing key id",
			account: func() *service.Account {
				account := baseAccount(0, service.PlatformOpenAI)
				account.UpstreamConfigID = ptrInt64(11)
				return account
			}(),
			wantReason: "UPSTREAM_ACCOUNT_BINDING_REQUIRED",
		},
		{
			name:    "config not found",
			account: boundAccount(0, 11, 22, service.PlatformOpenAI),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, false)
			},
			wantReason: "UPSTREAM_CONFIG_NOT_FOUND",
		},
		{
			name:    "deleted key is not found",
			account: boundAccount(0, 11, 22, service.PlatformOpenAI),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, true)
				expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, ptrString(service.PlatformOpenAI), false)
			},
			wantReason: "UPSTREAM_KEY_NOT_FOUND",
		},
		{
			name:    "key belongs to another config",
			account: boundAccount(0, 11, 22, service.PlatformOpenAI),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, true)
				expectUpstreamKeyLock(mock, 22, 12, service.StatusActive, ptrString(service.PlatformOpenAI), true)
			},
			wantReason: "UPSTREAM_KEY_CONFIG_MISMATCH",
		},
		{
			name:    "inactive key",
			account: boundAccount(0, 11, 22, service.PlatformOpenAI),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, true)
				expectUpstreamKeyLock(mock, 22, 11, service.StatusDisabled, ptrString(service.PlatformOpenAI), true)
			},
			wantReason: "UPSTREAM_KEY_INACTIVE",
		},
		{
			name:    "key platform is null",
			account: boundAccount(0, 11, 22, service.PlatformOpenAI),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, true)
				expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, nil, true)
			},
			wantReason: "UPSTREAM_KEY_PLATFORM_UNASSIGNED",
		},
		{
			name:    "key platform differs from account",
			account: boundAccount(0, 11, 22, service.PlatformAnthropic),
			expectLock: func(mock sqlmock.Sqlmock) {
				expectUpstreamConfigLock(mock, 11, true)
				expectUpstreamKeyLock(mock, 22, 11, service.StatusActive, ptrString(service.PlatformOpenAI), true)
			},
			wantReason: "UPSTREAM_KEY_PLATFORM_MISMATCH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newAccountWriteMock(t)
			mock.ExpectBegin()
			if tt.expectLock != nil {
				tt.expectLock(mock)
			}
			mock.ExpectRollback()

			err := repo.Create(context.Background(), tt.account)
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
			require.Zero(t, tt.account.ID)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAccountRepositoryUpdateRejectsInvalidUpstreamBindingBeforeWrite(t *testing.T) {
	repo, mock := newAccountWriteMock(t)
	account := boundAccount(104, 11, 22, service.PlatformOpenAI)

	mock.ExpectBegin()
	expectUpstreamConfigLock(mock, 11, true)
	expectUpstreamKeyLock(mock, 22, 11, service.StatusDisabled, ptrString(service.PlatformOpenAI), true)
	mock.ExpectRollback()

	err := repo.Update(context.Background(), account)
	require.Equal(t, "UPSTREAM_KEY_INACTIVE", infraerrors.Reason(err))
	require.NoError(t, mock.ExpectationsWereMet())
}

func newAccountWriteMock(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), mock
}

func baseAccount(id int64, platform string) *service.Account {
	return &service.Account{
		ID:          id,
		Name:        "account-write-test",
		Platform:    platform,
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{},
		Extra:       map[string]any{},
		Concurrency: 1,
		Priority:    10,
		Status:      service.StatusActive,
		Schedulable: true,
	}
}

func boundAccount(id, configID, keyID int64, platform string) *service.Account {
	account := baseAccount(id, platform)
	account.UpstreamConfigID = ptrInt64(configID)
	account.UpstreamKeyID = ptrInt64(keyID)
	return account
}

func expectUpstreamConfigLock(mock sqlmock.Sqlmock, id int64, found bool) {
	rows := sqlmock.NewRows(dbupstreamconfig.Columns)
	if found {
		now := time.Now()
		rows.AddRow(id, now, now, nil, "config", service.UpstreamProviderNewAPI, "https://upstream.invalid", nil, service.UpstreamAuthModeCookie, []byte("{}"), []byte("{}"), nil, 1.0, nil, service.StatusActive, nil, nil, nil)
	}
	mock.ExpectQuery(`SELECT .* FROM "upstream_configs".*"deleted_at" IS NULL.*FOR UPDATE`).WillReturnRows(rows)
}

func expectUpstreamKeyLock(mock sqlmock.Sqlmock, id, configID int64, status string, platform *string, found bool) {
	rows := sqlmock.NewRows(dbupstreamkey.Columns)
	if found {
		now := time.Now()
		var platformValue any
		if platform != nil {
			platformValue = *platform
		}
		rows.AddRow(id, now, now, nil, configID, "key", "test-secret", "test-hash", nil, nil, "", platformValue, service.UpstreamKeyPlatformSourceManual, nil, service.UpstreamKeyPlatformDetectionDetected, nil, nil, status, nil, 0, nil, []byte("{}"))
	}
	mock.ExpectQuery(`SELECT .* FROM "upstream_keys".*"deleted_at" IS NULL.*FOR UPDATE`).WillReturnRows(rows)
}

func expectAccountCreate(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectQuery(`INSERT INTO "accounts".*RETURNING "id"`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
}

func expectAccountUpdate(mock sqlmock.Sqlmock, account *service.Account) {
	mock.ExpectExec(`UPDATE "accounts".*WHERE "id" =`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	now := time.Now()
	var configID, keyID any
	if account.UpstreamConfigID != nil {
		configID = *account.UpstreamConfigID
	}
	if account.UpstreamKeyID != nil {
		keyID = *account.UpstreamKeyID
	}
	mock.ExpectQuery(`SELECT .* FROM "accounts" WHERE "id" =`).WillReturnRows(
		sqlmock.NewRows(dbaccount.Columns).AddRow(
			account.ID, now, now, nil, account.Name, nil, account.Platform, account.Type,
			[]byte("{}"), []byte("{}"), nil, nil, configID, keyID,
			nil, nil, account.Concurrency, nil, account.Priority, 1.0, account.Status, "", nil, nil,
			false, account.Schedulable, nil, nil, nil, nil, nil, nil, nil, nil, nil, dbaccount.QuotaDimensionGlobal,
		),
	)
}

func expectSchedulerAccountOutbox(mock sqlmock.Sqlmock, err error) {
	expectation := mock.ExpectExec(`INSERT INTO scheduler_outbox`)
	if err != nil {
		expectation.WillReturnError(err)
		return
	}
	expectation.WillReturnResult(sqlmock.NewResult(1, 1))
}

func ptrInt64(value int64) *int64 {
	return &value
}

func ptrString(value string) *string {
	return &value
}
