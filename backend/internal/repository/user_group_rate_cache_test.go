package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestUserGroupRateCacheSeparateTopicValidatesMessagesAndStops(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := &apiKeyCache{rdb: client}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan int64, 20)
	stopped := make(chan error, 1)
	go func() { stopped <- cache.SubscribeUserGroupRateInvalidation(ctx, func(id int64) { received <- id }) }()
	require.Eventually(t, func() bool {
		subs, err := client.PubSubNumSub(context.Background(), userGroupRateInvalidateChannel).Result()
		return err == nil && subs[userGroupRateInvalidateChannel] == 1
	}, time.Second, time.Millisecond)
	for _, payload := range []string{"", "0", "-1", "+135", "0135", " 135", "135 ", "{\"user_id\":135}", "9223372036854775808", "hash"} {
		require.NoError(t, client.Publish(context.Background(), userGroupRateInvalidateChannel, payload).Err())
	}
	// A valid looking user ID on the auth channel must never reach this handler.
	require.NoError(t, client.Publish(context.Background(), authCacheInvalidateChannel, "135").Err())
	require.NoError(t, cache.PublishUserGroupRateInvalidation(context.Background(), 135))
	select {
	case got := <-received:
		require.Equal(t, int64(135), got)
	case <-time.After(time.Second):
		t.Fatal("valid rate invalidation was not delivered")
	}
	select {
	case got := <-received:
		t.Fatalf("invalid or cross-topic message delivered: %d", got)
	default:
	}
	cancel()
	select {
	case err := <-stopped:
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("rate subscriber did not stop")
	}
	require.Error(t, cache.PublishUserGroupRateInvalidation(context.Background(), 0))
}
