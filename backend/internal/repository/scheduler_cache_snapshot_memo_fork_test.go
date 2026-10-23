//go:build unit

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCacheSnapshotMemoForkV2RevisionIsolation(t *testing.T) {
	cache, mr, recorder := newSchedulerCacheMemoUnit(t, 2)
	ctx := context.Background()
	bucket := memoTestBucket(81)
	require.Equal(t, "sched:v2:meta:rev", schedulerAccountMetaRevisionKey)
	require.Equal(t, "sched:v2:meta:rev:anthropic", schedulerAccountMetaRevisionKeyForPlatform(bucket.Platform))
	for _, key := range schedulerSnapshotTagKeys(bucket) {
		require.Contains(t, key, "sched:v2:")
	}

	mr.Set("sched:meta:rev", "legacy-global")
	mr.Set("sched:meta:rev:anthropic", "legacy-platform")
	account := memoTestAccount(801, "before")
	memoPublish(t, cache, bucket, account)
	require.Equal(t, "before", memoRead(t, cache, bucket)[0].Name)
	before := memoRevision(t, cache, schedulerAccountMetaRevisionKeyForPlatform(bucket.Platform))
	require.NotEmpty(t, before)

	// Old processes must neither invalidate v2 entries nor receive v2 revisions.
	mr.Set("sched:meta:rev", "legacy-global-updated")
	mr.Set("sched:meta:rev:anthropic", "legacy-platform-updated")
	recorder.reset()
	require.Equal(t, "before", memoRead(t, cache, bucket)[0].Name)
	require.Zero(t, recorder.countPrefix("zrange "), "legacy revisions must not invalidate v2 memo entries")

	account.Name = "after"
	require.NoError(t, cache.SetAccount(ctx, &account))
	require.NotEqual(t, before, memoRevision(t, cache, schedulerAccountMetaRevisionKeyForPlatform(bucket.Platform)))
	require.Equal(t, "after", memoRead(t, cache, bucket)[0].Name)
	require.Equal(t, "legacy-global-updated", memoRevision(t, cache, "sched:meta:rev"))
	require.Equal(t, "legacy-platform-updated", memoRevision(t, cache, "sched:meta:rev:anthropic"))
}

func TestSchedulerCacheSnapshotMemoForkPreservesUpstreamAndPreferredMetadata(t *testing.T) {
	cache, _, _ := newSchedulerCacheMemoUnit(t, 2)
	bucket := memoTestBucket(82)
	configID, keyID := int64(91), int64(92)
	gate, sourceRate, override := false, 0.125, 7
	account := memoTestAccount(802, "bound")
	account.UpstreamConfigID = &configID
	account.UpstreamKeyID = &keyID
	account.UpstreamSchedulingEnabled = &gate
	account.UpstreamSourceRateMultiplier = &sourceRate
	account.UpstreamConcurrencyLimit = 7
	account.UpstreamConcurrencySource = "override"
	account.UpstreamConcurrencyOverride = &override
	account.UpstreamConcurrencyUsesDefault = true
	account.UpstreamConcurrencyUnlimited = true
	account.UpstreamImagePricing = &service.UpstreamKeyImagePricing{}
	account.UpstreamVideoPricing = &service.UpstreamKeyVideoPricing{}
	account.UpstreamLongContext = &service.UpstreamLongContextState{}
	account.AccountGroups = []service.AccountGroup{{AccountID: account.ID, GroupID: bucket.GroupID, SchedulerPreferred: true}}

	memoPublish(t, cache, bucket, account)
	for _, label := range []string{"decoded", "memoized"} {
		t.Run(label, func(t *testing.T) {
			got := memoRead(t, cache, bucket)[0]
			require.Equal(t, account.UpstreamConfigID, got.UpstreamConfigID)
			require.Equal(t, account.UpstreamKeyID, got.UpstreamKeyID)
			require.Equal(t, account.UpstreamSchedulingEnabled, got.UpstreamSchedulingEnabled)
			require.False(t, got.IsSchedulable(), "parent upstream gate must survive snapshot projection")
			require.Equal(t, account.UpstreamSourceRateMultiplier, got.UpstreamSourceRateMultiplier)
			require.Equal(t, account.UpstreamConcurrencyLimit, got.UpstreamConcurrencyLimit)
			require.Equal(t, account.UpstreamConcurrencySource, got.UpstreamConcurrencySource)
			require.Equal(t, account.UpstreamConcurrencyOverride, got.UpstreamConcurrencyOverride)
			require.Equal(t, account.UpstreamConcurrencyUsesDefault, got.UpstreamConcurrencyUsesDefault)
			require.Equal(t, account.UpstreamConcurrencyUnlimited, got.UpstreamConcurrencyUnlimited)
			require.Equal(t, account.UpstreamImagePricing, got.UpstreamImagePricing)
			require.Equal(t, account.UpstreamVideoPricing, got.UpstreamVideoPricing)
			require.Equal(t, account.UpstreamLongContext, got.UpstreamLongContext)
			require.Equal(t, account.AccountGroups, got.AccountGroups)
			require.Equal(t, []int64{bucket.GroupID}, got.GroupIDs)
			require.NotContains(t, got.Credentials, "access_token", "memoization must retain metadata redaction")
		})
	}
}
