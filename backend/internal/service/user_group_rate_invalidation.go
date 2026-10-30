package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// UserGroupRateInvalidationEvent is deliberately separate from auth events:
// older auth workers must never acknowledge an event they cannot understand.
type UserGroupRateInvalidationEvent struct {
	UserID   int64
	Revision int64
}

type UserGroupRateInvalidationOutboxRepository interface {
	ListUserGroupRateInvalidations(context.Context, int) ([]UserGroupRateInvalidationEvent, error)
	DeleteUserGroupRateInvalidation(context.Context, int64, int64) error
}

// This is a narrow optional extension of APIKeyCache. Authentication messages
// and their validation remain unchanged on the original channel.
type UserGroupRateInvalidationCache interface {
	PublishUserGroupRateInvalidation(context.Context, int64) error
	SubscribeUserGroupRateInvalidation(context.Context, func(int64)) error
}

func (w *AuthCacheInvalidationWorker) processUserGroupRateInvalidations(parent context.Context) error {
	repo, ok := w.repo.(UserGroupRateInvalidationOutboxRepository)
	if !ok {
		return nil
	}
	cache, ok := w.cache.(UserGroupRateInvalidationCache)
	if !ok {
		return errors.New("user group rate invalidation publisher unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, authInvalidationRedisTimeout)
	defer cancel()
	events, err := repo.ListUserGroupRateInvalidations(ctx, authInvalidationBatchSize)
	if err != nil {
		return fmt.Errorf("read user group rate invalidations: %w", err)
	}
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		InvalidateUserGroupRateCachesByUser(event.UserID)
		if err := cache.PublishUserGroupRateInvalidation(ctx, event.UserID); err != nil {
			return fmt.Errorf("publish user group rate invalidation: %w", err)
		}
		// A global monotonically unique revision also prevents an old worker
		// acknowledging an event deleted and recreated by another instance.
		if err := repo.DeleteUserGroupRateInvalidation(ctx, event.UserID, event.Revision); err != nil {
			return fmt.Errorf("ack user group rate invalidation: %w", err)
		}
	}
	return nil
}

func (s *APIKeyService) runUserGroupRateInvalidationSubscriber(ctx context.Context, cache UserGroupRateInvalidationCache) {
	defer s.authInvalidationWG.Done()
	backoff := time.Second
	for {
		// The repository calls this only once Redis has acknowledged the
		// subscription, closing the reconnect/message-loss window.
		subCtx := withAuthCacheSubscriptionReady(ctx, InvalidateAllUserGroupRateCaches)
		err := cache.SubscribeUserGroupRateInvalidation(subCtx, InvalidateUserGroupRateCachesByUser)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("user group rate invalidation subscription closed")
		}
		slog.Warn("user group rate invalidation subscriber disconnected", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}
