package repository

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const groupTTFTGuardPolicyInvalidationChannel = "group_ttft_guard_policy_updated"
const groupTTFTGuardPolicyGlobalInvalidationPayload = "global"

type groupTTFTGuardPolicyInvalidationBus struct {
	rdb *redis.Client
}

func NewGroupTTFTGuardPolicyInvalidationBus(rdb *redis.Client) service.GroupTTFTGuardPolicyInvalidationBus {
	return &groupTTFTGuardPolicyInvalidationBus{rdb: rdb}
}

func (b *groupTTFTGuardPolicyInvalidationBus) NotifyUpdate(ctx context.Context, groupID int64) error {
	if b == nil || b.rdb == nil || groupID <= 0 {
		return nil
	}
	return b.rdb.Publish(ctx, groupTTFTGuardPolicyInvalidationChannel, strconv.FormatInt(groupID, 10)).Err()
}

func (b *groupTTFTGuardPolicyInvalidationBus) NotifyGlobalUpdate(ctx context.Context) error {
	if b == nil || b.rdb == nil {
		return nil
	}
	return b.rdb.Publish(ctx, groupTTFTGuardPolicyInvalidationChannel, groupTTFTGuardPolicyGlobalInvalidationPayload).Err()
}

func (b *groupTTFTGuardPolicyInvalidationBus) SubscribeUpdates(ctx context.Context, handler func(int64)) {
	if b == nil || b.rdb == nil || handler == nil {
		return
	}
	go func() {
		backoff := time.Second
		for ctx.Err() == nil {
			pubsub := b.rdb.Subscribe(ctx, groupTTFTGuardPolicyInvalidationChannel)
			messages := pubsub.Channel()
			connected := false
			for {
				select {
				case <-ctx.Done():
					_ = pubsub.Close()
					return
				case message, ok := <-messages:
					if !ok {
						_ = pubsub.Close()
						goto retry
					}
					connected = true
					if message.Payload == groupTTFTGuardPolicyGlobalInvalidationPayload {
						handler(0)
						continue
					}
					groupID, err := strconv.ParseInt(message.Payload, 10, 64)
					if err == nil && groupID > 0 {
						handler(groupID)
					}
				}
			}
		retry:
			if connected {
				backoff = time.Second
			}
			slog.Warn("group TTFT guard invalidation subscriber stopped; retrying", "retry_in", backoff)
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
	}()
}
