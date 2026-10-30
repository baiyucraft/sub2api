package repository

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *authCacheInvalidationOutboxRepository) ListUserGroupRateInvalidations(ctx context.Context, limit int) ([]service.UserGroupRateInvalidationEvent, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil user group rate invalidation outbox database")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, revision
		FROM user_group_rate_invalidation_outbox
		ORDER BY updated_at, user_id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]service.UserGroupRateInvalidationEvent, 0, limit)
	for rows.Next() {
		var event service.UserGroupRateInvalidationEvent
		if err := rows.Scan(&event.UserID, &event.Revision); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *authCacheInvalidationOutboxRepository) DeleteUserGroupRateInvalidation(ctx context.Context, userID, revision int64) error {
	if r == nil || r.db == nil {
		return errors.New("nil user group rate invalidation outbox database")
	}
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_group_rate_invalidation_outbox
		WHERE user_id = $1 AND revision = $2
	`, userID, revision)
	return err
}
