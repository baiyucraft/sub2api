//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type schedulerUpgradeCache struct {
	SchedulerCache
	v2Buckets     []SchedulerBucket
	legacyBuckets []SchedulerBucket
	written       map[string][]Account
}

func (c *schedulerUpgradeCache) ListBuckets(context.Context) ([]SchedulerBucket, error) {
	return c.v2Buckets, nil
}

func (c *schedulerUpgradeCache) ListLegacyBuckets(context.Context) ([]SchedulerBucket, error) {
	return c.legacyBuckets, nil
}

func (c *schedulerUpgradeCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *schedulerUpgradeCache) TryLockBucket(context.Context, SchedulerBucket, time.Duration) (bool, error) {
	return true, nil
}

func (c *schedulerUpgradeCache) UnlockBucket(context.Context, SchedulerBucket) error { return nil }

func (c *schedulerUpgradeCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	if c.written == nil {
		c.written = make(map[string][]Account)
	}
	c.written[bucket.String()] = append([]Account(nil), accounts...)
	return nil
}

type schedulerUpgradeAccountRepo struct {
	AccountRepository
	accounts []Account
	calls    []SchedulerBucket
}

type schedulerUpgradeGroupRepo struct {
	GroupRepository
	activeIDs []int64
}

func (r *schedulerUpgradeGroupRepo) ListActiveIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), r.activeIDs...), nil
}

func (r *schedulerUpgradeAccountRepo) record(groupID int64, platform string) ([]Account, error) {
	r.calls = append(r.calls, SchedulerBucket{GroupID: groupID, Platform: platform, Mode: SchedulerModeSingle})
	return append([]Account(nil), r.accounts...), nil
}

func (r *schedulerUpgradeAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, groupID int64, platform string) ([]Account, error) {
	return r.record(groupID, platform)

}

func (r *schedulerUpgradeAccountRepo) ListSchedulableByGroupIDAndPlatforms(_ context.Context, groupID int64, platforms []string) ([]Account, error) {
	return r.record(groupID, platforms[0])
}

func (r *schedulerUpgradeAccountRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]Account, error) {
	return r.record(0, platform)
}

func (r *schedulerUpgradeAccountRepo) ListSchedulableUngroupedByPlatforms(_ context.Context, platforms []string) ([]Account, error) {
	return r.record(0, platforms[0])
}

func TestRunInitialRebuildUsesLegacyBucketsOnlyAsTopology(t *testing.T) {
	legacyBucket := SchedulerBucket{GroupID: 12, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	dbAccount := Account{ID: 501, Platform: PlatformOpenAI, Name: "from-db"}
	cache := &schedulerUpgradeCache{legacyBuckets: []SchedulerBucket{legacyBucket}}
	repo := &schedulerUpgradeAccountRepo{accounts: []Account{dbAccount}}
	groups := &schedulerUpgradeGroupRepo{activeIDs: []int64{legacyBucket.GroupID}}
	service := NewSchedulerSnapshotService(cache, nil, repo, groups, nil)

	service.runInitialRebuild()

	require.Contains(t, repo.calls, legacyBucket)
	require.Equal(t, []Account{dbAccount}, cache.written[legacyBucket.String()])
}

func TestRunInitialRebuildPrefersV2BucketsOverLegacyTopology(t *testing.T) {
	v2Bucket := SchedulerBucket{GroupID: 21, Platform: PlatformOpenAI, Mode: SchedulerModeSingle}
	legacyBucket := SchedulerBucket{GroupID: 22, Platform: PlatformGemini, Mode: SchedulerModeSingle}
	cache := &schedulerUpgradeCache{
		v2Buckets:     []SchedulerBucket{v2Bucket},
		legacyBuckets: []SchedulerBucket{legacyBucket},
	}
	repo := &schedulerUpgradeAccountRepo{accounts: []Account{{ID: 601, Platform: PlatformOpenAI}}}
	groups := &schedulerUpgradeGroupRepo{activeIDs: []int64{v2Bucket.GroupID}}
	service := NewSchedulerSnapshotService(cache, nil, repo, groups, nil)

	service.runInitialRebuild()

	require.Contains(t, repo.calls, v2Bucket)
	require.NotContains(t, repo.calls, legacyBucket)
	require.Contains(t, cache.written, v2Bucket.String())
	require.NotContains(t, cache.written, legacyBucket.String())
}
