package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserGroupRateInvalidationOutboxBoundedReadAndRevisionAck(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &authCacheInvalidationOutboxRepository{db: db}
	mock.ExpectQuery(`SELECT user_id, revision[\s\S]+FROM user_group_rate_invalidation_outbox[\s\S]+ORDER BY updated_at, user_id[\s\S]+LIMIT \$1`).
		WithArgs(100).WillReturnRows(sqlmock.NewRows([]string{"user_id", "revision"}).AddRow(135, 200))
	events, err := repo.ListUserGroupRateInvalidations(context.Background(), 999)
	require.NoError(t, err)
	require.Equal(t, []service.UserGroupRateInvalidationEvent{{UserID: 135, Revision: 200}}, events)
	// Zero affected rows is an expected concurrent update/other-worker ack,
	// not a reason to remove the newer revision or retry an old destructive ack.
	mock.ExpectExec(`DELETE FROM user_group_rate_invalidation_outbox[\s\S]+WHERE user_id = \$1 AND revision = \$2`).
		WithArgs(int64(135), int64(200)).WillReturnResult(sqlmock.NewResult(0, 0))
	require.NoError(t, repo.DeleteUserGroupRateInvalidation(context.Background(), 135, 200))
	require.NoError(t, mock.ExpectationsWereMet())
}
