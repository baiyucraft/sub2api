//go:build upstreamtablepg

package repository

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestUpstreamTableProxyPageBatchRedis(t *testing.T) {
	url := os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_REDIS")
	if url == "" {
		t.Skip("dedicated Redis test instance required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_ISOLATED"))
	options, err := redis.ParseURL(url)
	require.NoError(t, err)
	rdb := redis.NewClient(options)
	defer rdb.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cache := NewConcurrencyCache(rdb, 15, 900).(*concurrencyCache)
	account := time.Now().UnixNano()
	a := service.AccountProxyConcurrencyTarget{AccountID: account, ProxyID: 11}
	b := service.AccountProxyConcurrencyTarget{AccountID: account + 1, ProxyID: 11}
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	var keys []string
	for target, count := range map[service.AccountProxyConcurrencyTarget]int{a: 2, b: 4} {
		regular := accountProxySlotKey(target.AccountID, target.ProxyID)
		live := liveAccountProxySlotKey(target.AccountID, target.ProxyID)
		keys = append(keys, regular, live)
		for i := 0; i < count; i++ {
			require.NoError(t, rdb.ZAdd(ctx, regular, redis.Z{Score: float64(now.Unix()), Member: fmt.Sprintf("regular-%d", i)}).Err())
		}
		require.NoError(t, rdb.ZAdd(ctx, regular, redis.Z{Score: float64(now.Unix() - int64(cache.slotTTLSeconds)), Member: "expired"}).Err())
		require.NoError(t, rdb.ZAdd(ctx, live, redis.Z{Score: float64(now.Unix()), Member: "live"}, redis.Z{Score: float64(now.Unix() - liveLeaseTTLSeconds), Member: "expired"}).Err())
	}
	defer rdb.Del(context.Background(), keys...)
	loads, err := cache.GetAccountProxyConcurrencyTargetsBatch(ctx, []service.AccountProxyConcurrencyTarget{a, b, a, {AccountID: 0, ProxyID: 11}})
	require.NoError(t, err)
	require.Equal(t, map[service.AccountProxyConcurrencyTarget]int{a: 3, b: 5}, loads)
	for _, target := range []service.AccountProxyConcurrencyTarget{a, b} {
		point, err := cache.GetAccountProxyConcurrency(ctx, target.AccountID, target.ProxyID)
		require.NoError(t, err)
		require.Equal(t, loads[target], point)
	}

	t.Run("one invalid key preserves other page targets", func(t *testing.T) {
		c := service.AccountProxyConcurrencyTarget{AccountID: account + 2, ProxyID: 11}
		regularC := accountProxySlotKey(c.AccountID, c.ProxyID)
		liveC := liveAccountProxySlotKey(c.AccountID, c.ProxyID)
		defer rdb.Del(context.Background(), regularC, liveC)
		require.NoError(t, rdb.Set(ctx, accountProxySlotKey(b.AccountID, b.ProxyID), "invalid-type", 0).Err())
		require.NoError(t, rdb.ZAdd(ctx, regularC, redis.Z{Score: float64(now.Unix()), Member: "regular"}).Err())
		require.NoError(t, rdb.Set(ctx, liveC, "invalid-type", 0).Err())
		partial, err := cache.GetAccountProxyConcurrencyTargetsBatch(ctx, []service.AccountProxyConcurrencyTarget{a, b, c})
		require.ErrorContains(t, err, "WRONGTYPE")
		require.Equal(t, map[service.AccountProxyConcurrencyTarget]int{a: 3, b: 0, c: 0}, partial)
		for _, target := range []service.AccountProxyConcurrencyTarget{b, c} {
			point, pointErr := cache.GetAccountProxyConcurrency(ctx, target.AccountID, target.ProxyID)
			require.Error(t, pointErr)
			require.Zero(t, point)
		}
		// Existing per-account callers still receive their original error contract.
		legacy, err := cache.GetAccountProxyConcurrencyBatch(ctx, b.AccountID, []int64{b.ProxyID})
		require.Error(t, err)
		require.Nil(t, legacy)
	})
}
