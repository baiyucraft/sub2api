package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupSchedulingProtectionPutAtomicRollbackAndOutbox(t *testing.T) {
	for _, failCache := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "cache_failure_rolls_back_ttft"}[failCache], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			r := &groupSchedulingProtectionPolicyRepository{db: db}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT id FROM groups .* FOR UPDATE`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
			mock.ExpectExec(`INSERT INTO fork_group_ttft_guard_policies`).WithArgs(int64(7), "enabled", 12, 4).WillReturnResult(sqlmock.NewResult(1, 1))
			write := service.GroupSchedulingProtectionPolicyWrite{TTFTMode: "enabled", TTFTThresholdSeconds: 12, TTFTMinSamples: 4, CacheMode: "enabled", ThresholdPercent: 70, RecoveryThresholdPercent: 80, MinValidRequests: 20, Priority: "ttft_first"}
			cache := mock.ExpectExec(`INSERT INTO fork_group_scheduling_protection_policies`).WithArgs(int64(7), "enabled", float64(70), float64(80), 20, "ttft_first")
			if failCache {
				cache.WillReturnError(errors.New("cache storage failed"))
				mock.ExpectRollback()
			} else {
				cache.WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectExec(`INSERT INTO scheduler_outbox`).WithArgs(service.SchedulerOutboxEventGroupChanged, nil, int64(7), []byte(`{"ttft_guard_policy_changed":true}`), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
				mock.ExpectCommit()
			}
			_, err = r.Put(context.Background(), 7, write)
			if failCache {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
