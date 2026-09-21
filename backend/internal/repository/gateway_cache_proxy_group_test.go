package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCacheOpenAIProxyGroupBindingClaimAndCompareDelete(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cache, ok := NewGatewayCache(client).(service.OpenAIProxyGroupBindingCache)
	require.True(t, ok)

	ctx := context.Background()
	const (
		accountID   = int64(41)
		sessionHash = "session-claim"
		firstProxy  = int64(101)
		secondProxy = int64(202)
	)

	claimed, err := cache.ClaimOpenAIProxyGroupBinding(ctx, accountID, sessionHash, firstProxy, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, firstProxy, claimed)

	mr.FastForward(30 * time.Second)
	claimed, err = cache.ClaimOpenAIProxyGroupBinding(ctx, accountID, sessionHash, secondProxy, 5*time.Minute)
	require.NoError(t, err)
	require.Equal(t, firstProxy, claimed, "later claims must not replace the first session binding")

	bound, err := cache.GetOpenAIProxyGroupBinding(ctx, accountID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, firstProxy, bound)
	require.Greater(t, mr.TTL(buildOpenAIProxyGroupBindingKey(accountID, sessionHash)), 4*time.Minute)

	require.NoError(t, cache.DeleteOpenAIProxyGroupBindingIfMatch(ctx, accountID, sessionHash, secondProxy))
	bound, err = cache.GetOpenAIProxyGroupBinding(ctx, accountID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, firstProxy, bound, "a stale contender must not delete the winning binding")

	replaced, err := cache.ReplaceOpenAIProxyGroupBindingIfMatch(ctx, accountID, sessionHash, secondProxy, firstProxy, 5*time.Minute)
	require.NoError(t, err)
	require.False(t, replaced, "a stale replacement must not overwrite the winning binding")
	replaced, err = cache.ReplaceOpenAIProxyGroupBindingIfMatch(ctx, accountID, sessionHash, firstProxy, secondProxy, 5*time.Minute)
	require.NoError(t, err)
	require.True(t, replaced)
	bound, err = cache.GetOpenAIProxyGroupBinding(ctx, accountID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, secondProxy, bound)

	require.NoError(t, cache.DeleteOpenAIProxyGroupBindingIfMatch(ctx, accountID, sessionHash, secondProxy))
	_, err = cache.GetOpenAIProxyGroupBinding(ctx, accountID, sessionHash)
	require.ErrorIs(t, err, service.ErrOpenAIProxyGroupBindingNotFound)
}
