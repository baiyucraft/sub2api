package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGroupTTFTGuardPolicyInvalidationBusPublishesGroupID(t *testing.T) {
	redisServer := miniredis.RunT(t)
	publisherRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	subscriberRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = publisherRedis.Close()
		_ = subscriberRedis.Close()
	})

	publisher := NewGroupTTFTGuardPolicyInvalidationBus(publisherRedis)
	subscriber := NewGroupTTFTGuardPolicyInvalidationBus(subscriberRedis)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan int64, 1)
	subscriber.SubscribeUpdates(ctx, func(groupID int64) {
		select {
		case received <- groupID:
		default:
		}
	})

	require.Eventually(t, func() bool {
		return redisServer.PubSubNumSub(groupTTFTGuardPolicyInvalidationChannel)[groupTTFTGuardPolicyInvalidationChannel] == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, publisher.NotifyUpdate(context.Background(), 42))

	select {
	case groupID := <-received:
		require.Equal(t, int64(42), groupID)
	case <-time.After(time.Second):
		t.Fatal("group TTFT guard invalidation notification was not received")
	}
}

func TestGroupTTFTGuardPolicyInvalidationBusPublishesGlobalUpdate(t *testing.T) {
	redisServer := miniredis.RunT(t)
	publisherRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	subscriberRedis := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() {
		_ = publisherRedis.Close()
		_ = subscriberRedis.Close()
	})

	publisher := NewGroupTTFTGuardPolicyInvalidationBus(publisherRedis)
	subscriber := NewGroupTTFTGuardPolicyInvalidationBus(subscriberRedis)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan int64, 1)
	subscriber.SubscribeUpdates(ctx, func(groupID int64) {
		select {
		case received <- groupID:
		default:
		}
	})

	require.Eventually(t, func() bool {
		return redisServer.PubSubNumSub(groupTTFTGuardPolicyInvalidationChannel)[groupTTFTGuardPolicyInvalidationChannel] == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, publisher.NotifyGlobalUpdate(context.Background()))

	select {
	case groupID := <-received:
		require.Zero(t, groupID)
	case <-time.After(time.Second):
		t.Fatal("global TTFT guard invalidation notification was not received")
	}
}

func TestGroupTTFTGuardPolicyInvalidationBusIgnoresInvalidPayload(t *testing.T) {
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	bus := NewGroupTTFTGuardPolicyInvalidationBus(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	received := make(chan int64, 1)
	bus.SubscribeUpdates(ctx, func(groupID int64) { received <- groupID })

	require.Eventually(t, func() bool {
		return redisServer.PubSubNumSub(groupTTFTGuardPolicyInvalidationChannel)[groupTTFTGuardPolicyInvalidationChannel] == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, rdb.Publish(context.Background(), groupTTFTGuardPolicyInvalidationChannel, "not-a-group-id").Err())
	require.NoError(t, rdb.Publish(context.Background(), groupTTFTGuardPolicyInvalidationChannel, "0").Err())

	select {
	case groupID := <-received:
		t.Fatalf("unexpected invalidation for invalid payload: %d", groupID)
	case <-time.After(100 * time.Millisecond):
	}
}
