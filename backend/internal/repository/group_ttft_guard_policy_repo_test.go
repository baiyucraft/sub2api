package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupTTFTGuardPolicyRepositoryGetAndList(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &groupTTFTGuardPolicyRepository{db: db}
	now := time.Now()

	mock.ExpectQuery(`SELECT g.id, g.name, g.platform`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "platform", "mode", "degradation_ttft_seconds", "min_samples", "updated_at"}).
			AddRow(int64(7), "pro", service.PlatformOpenAI, "enabled", 12, 4, now))
	record, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "pro", record.GroupName)
	require.Equal(t, "enabled", *record.Mode)

	mock.ExpectQuery(`SELECT g.id, g.name, g.platform`).WithArgs(service.PlatformOpenAI, service.PlatformComposite).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "platform", "mode", "degradation_ttft_seconds", "min_samples", "updated_at"}).
			AddRow(int64(7), "pro", service.PlatformOpenAI, "enabled", 12, 4, now).
			AddRow(int64(8), "mix", service.PlatformComposite, nil, nil, nil, nil))
	records, err := repo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Nil(t, records[1].Mode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupTTFTGuardPolicyRepositoryPutWritesOutboxOnlyWhenChanged(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		repo := &groupTTFTGuardPolicyRepository{db: db}

		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO fork_group_ttft_guard_policies`).
			WithArgs(int64(7), "enabled", 12, 4).
			WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(7)))
		mock.ExpectExec(`INSERT INTO scheduler_outbox`).
			WithArgs(
				service.SchedulerOutboxEventGroupChanged,
				nil,
				int64(7),
				[]byte(`{"ttft_guard_policy_changed":true}`),
				sqlmock.AnyArg(),
			).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()

		changed, err := repo.Put(context.Background(), 7, "enabled", 12, 4)
		require.NoError(t, err)
		require.True(t, changed)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("idempotent", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		repo := &groupTTFTGuardPolicyRepository{db: db}

		mock.ExpectBegin()
		mock.ExpectQuery(`INSERT INTO fork_group_ttft_guard_policies`).
			WithArgs(int64(7), "enabled", 12, 4).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectCommit()

		changed, err := repo.Put(context.Background(), 7, "enabled", 12, 4)
		require.NoError(t, err)
		require.False(t, changed)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
