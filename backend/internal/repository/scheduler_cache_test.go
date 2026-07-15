package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestFilterSchedulerCredentialsKeepsSubscriptionPlanType(t *testing.T) {
	filtered := filterSchedulerCredentials(map[string]any{
		"plan_type":     "plus",
		"access_token":  "secret-access-token",
		"refresh_token": "secret-refresh-token",
	})

	require.Equal(t, "plus", filtered["plan_type"])
	require.NotContains(t, filtered, "access_token")
	require.NotContains(t, filtered, "refresh_token")
}

func TestSchedulerMetadataAccountKeepsOpenAISubscriptionIdentity(t *testing.T) {
	account := service.Account{
		ID:       24,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"plan_type":    "plus",
			"access_token": "secret-access-token",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)

	require.True(t, metadata.IsOpenAIChatGPTSubscription())
	require.Empty(t, metadata.GetCredential("access_token"))
}

func TestSchedulerCacheV2NamespaceAndLegacyBucketsIsolation(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := newSchedulerCacheWithChunkSizes(rdb, 8, 8).(*schedulerCache)

	legacyBucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	v2Bucket := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformGemini, Mode: service.SchedulerModeMixed}
	require.NoError(t, rdb.SAdd(ctx, schedulerLegacyBucketSetKey, legacyBucket.String()).Err())
	require.NoError(t, rdb.Set(ctx, "sched:ready:7:openai:single", "1", 0).Err())
	require.NoError(t, rdb.Set(ctx, "sched:active:7:openai:single", "4", 0).Err())
	require.NoError(t, rdb.ZAdd(ctx, "sched:7:openai:single:v4", redis.Z{Member: "77", Score: 0}).Err())
	require.NoError(t, rdb.Set(ctx, "sched:acc:77", `{"id":77,"name":"legacy-full"}`, 0).Err())
	require.NoError(t, rdb.Set(ctx, "sched:meta:77", `{"id":77,"name":"legacy-meta"}`, 0).Err())
	token, err := cache.CaptureBucketWriteToken(ctx, v2Bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, v2Bucket, token, []service.Account{{ID: 91, Platform: service.PlatformGemini}}))
	require.NoError(t, rdb.Set(ctx, "sched:outbox:watermark", "999", 0).Err())
	legacySnapshot, hit, err := cache.GetSnapshot(ctx, legacyBucket)
	require.NoError(t, err)
	require.False(t, hit)
	require.Nil(t, legacySnapshot)
	legacyAccount, err := cache.GetAccount(ctx, 77)
	require.NoError(t, err)
	require.Nil(t, legacyAccount)

	v2Buckets, err := cache.ListBuckets(ctx)
	require.NoError(t, err)
	require.Equal(t, []service.SchedulerBucket{v2Bucket}, v2Buckets)
	legacyBuckets, err := cache.ListLegacyBuckets(ctx)
	require.NoError(t, err)
	require.Equal(t, []service.SchedulerBucket{legacyBucket}, legacyBuckets)
	watermark, err := cache.GetOutboxWatermark(ctx)
	require.NoError(t, err)
	require.Zero(t, watermark)

	for _, key := range mr.Keys() {
		if key == schedulerLegacyBucketSetKey || key == "sched:outbox:watermark" ||
			key == "sched:ready:7:openai:single" || key == "sched:active:7:openai:single" ||
			key == "sched:7:openai:single:v4" || key == "sched:acc:77" || key == "sched:meta:77" {
			continue
		}
		require.Contains(t, key, "sched:v2:")
	}
}

func TestSchedulerMetadataAccountKeepsUpstreamBinding(t *testing.T) {
	configID := int64(31)
	keyID := int64(47)
	metadata := buildSchedulerMetadataAccount(service.Account{
		ID:               24,
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
	})

	require.Equal(t, &configID, metadata.UpstreamConfigID)
	require.Equal(t, &keyID, metadata.UpstreamKeyID)
}
