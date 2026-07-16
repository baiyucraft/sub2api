package repository

import (
	"context"
	"strings"
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

func TestSchedulerMetadataAccountProjectsUpstreamBillingProbe(t *testing.T) {
	lastError := strings.Repeat("upstream diagnostic ", 512)
	probe := map[string]any{
		"status": "ok",
		"data": map[string]any{
			"billing_scope":             "token",
			"resolved_rate_multiplier":  0.03,
			"peak_rate_enabled":         true,
			"peak_start":                "09:00",
			"peak_end":                  "18:00",
			"peak_rate_multiplier":      2.0,
			"timezone":                  "Asia/Shanghai",
			"effective_rate_multiplier": 0.03,
			"remote_diagnostic":         lastError,
		},
		"received_at":   "2026-07-13T10:00:00Z",
		"fresh_until":   "2026-07-13T11:00:00Z",
		"next_probe_at": "2026-07-13T10:30:00Z",
		"http_status":   502,
		"last_error":    lastError,
	}
	account := service.Account{
		ID: 42,
		Extra: map[string]any{
			"upstream_billing_probe": probe,
			"unused_large_field":     "drop-me",
		},
	}

	metadata := buildSchedulerMetadataAccount(account)
	fullPayload, metaPayload, err := marshalSchedulerCacheAccount(account)
	require.NoError(t, err)

	filtered, ok := metadata.Extra["upstream_billing_probe"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "ok", filtered["status"])
	require.Equal(t, "2026-07-13T10:00:00Z", filtered["received_at"])
	require.Equal(t, "2026-07-13T11:00:00Z", filtered["fresh_until"])
	require.Equal(t, "2026-07-13T10:30:00Z", filtered["next_probe_at"])
	require.NotContains(t, filtered, "http_status")
	require.NotContains(t, filtered, "last_error")
	filteredData, ok := filtered["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "token", filteredData["billing_scope"])
	require.Equal(t, 0.03, filteredData["resolved_rate_multiplier"])
	require.Equal(t, true, filteredData["peak_rate_enabled"])
	require.Equal(t, "09:00", filteredData["peak_start"])
	require.Equal(t, "18:00", filteredData["peak_end"])
	require.Equal(t, 2.0, filteredData["peak_rate_multiplier"])
	require.Equal(t, "Asia/Shanghai", filteredData["timezone"])
	require.NotContains(t, filteredData, "effective_rate_multiplier")
	require.NotContains(t, filteredData, "remote_diagnostic")
	require.NotContains(t, metadata.Extra, "unused_large_field")
	require.Contains(t, string(fullPayload), lastError)
	require.NotContains(t, string(metaPayload), "last_error")
	require.Less(t, len(metaPayload)*4, len(fullPayload))
}

func TestSchedulerMetadataAccountDropsInvalidUpstreamBillingProbe(t *testing.T) {
	for _, probe := range []any{
		"invalid",
		map[string]any{},
		map[string]any{"status": ""},
	} {
		metadata := buildSchedulerMetadataAccount(service.Account{
			Extra: map[string]any{service.UpstreamBillingProbeExtraKey: probe},
		})

		require.NotContains(t, metadata.Extra, service.UpstreamBillingProbeExtraKey)
	}
}
