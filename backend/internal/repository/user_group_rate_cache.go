package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const userGroupRateInvalidateChannel = "user:group-rate:invalidate:v1"

func (c *apiKeyCache) PublishUserGroupRateInvalidation(ctx context.Context, userID int64) error {
	if userID <= 0 {
		return errors.New("invalid user group rate invalidation user")
	}
	return c.rdb.Publish(ctx, userGroupRateInvalidateChannel, strconv.FormatInt(userID, 10)).Err()
}

func (c *apiKeyCache) SubscribeUserGroupRateInvalidation(ctx context.Context, handler func(int64)) error {
	pubsub := c.rdb.Subscribe(ctx, userGroupRateInvalidateChannel)
	defer func() { _ = pubsub.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = pubsub.Close() })
	defer stopClose()
	if _, err := pubsub.Receive(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("subscribe to user group rate invalidation: %w", err)
	}
	service.NotifyAuthCacheSubscriptionReady(ctx)
	for {
		message, err := pubsub.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch msg := message.(type) {
		case *redis.Subscription:
			// Receive exposes automatic resubscription acknowledgements too,
			// unlike Channel's message-only delivery.
			service.NotifyAuthCacheSubscriptionReady(ctx)
		case *redis.Message:
			userID, err := strconv.ParseInt(msg.Payload, 10, 64)
			// Canonical positive decimal only: no whitespace, signs, JSON,
			// leading zeros, or extension of the auth-cache message grammar.
			if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != msg.Payload {
				continue
			}
			handler(userID)
		}
	}
}
