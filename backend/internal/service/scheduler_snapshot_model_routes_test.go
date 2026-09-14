package service

import (
	"context"
	"testing"
	"time"
)

type modelRouteRefreshCache struct {
	SchedulerCache
	accounts []*Account
	buckets  []SchedulerBucket
}

func (c *modelRouteRefreshCache) SetAccount(_ context.Context, account *Account) error {
	copy := *account
	c.accounts = append(c.accounts, &copy)
	return nil
}

func (c *modelRouteRefreshCache) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *modelRouteRefreshCache) TryLockBucket(_ context.Context, _ SchedulerBucket, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *modelRouteRefreshCache) UnlockBucket(_ context.Context, _ SchedulerBucket) error {
	return nil
}

func (c *modelRouteRefreshCache) SetSnapshot(_ context.Context, bucket SchedulerBucket, _ SchedulerBucketWriteToken, _ []Account) error {
	c.buckets = append(c.buckets, bucket)
	return nil
}

type modelRouteRefreshAccountRepo struct {
	AccountRepository
	accounts       []Account
	queryPlatforms []string
}

func (r *modelRouteRefreshAccountRepo) ListByUpstreamKeyID(_ context.Context, _ int64) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *modelRouteRefreshAccountRepo) recordTargetPlatform(platform string) ([]Account, error) {
	r.queryPlatforms = append(r.queryPlatforms, platform)
	return nil, nil
}

func (r *modelRouteRefreshAccountRepo) ListSchedulableByGroupIDAndTargetPlatform(_ context.Context, _ int64, platform string, _ bool) ([]Account, error) {
	return r.recordTargetPlatform(platform)
}

func (r *modelRouteRefreshAccountRepo) ListSchedulableByTargetPlatform(_ context.Context, platform string, _ bool) ([]Account, error) {
	return r.recordTargetPlatform(platform)
}

func (r *modelRouteRefreshAccountRepo) ListSchedulableUngroupedByTargetPlatform(_ context.Context, platform string, _ bool) ([]Account, error) {
	return r.recordTargetPlatform(platform)
}

func TestRefreshUpstreamKeyRoutesRefreshesAccountAndAllConcretePlatformBuckets(t *testing.T) {
	keyID := int64(42)
	repo := &modelRouteRefreshAccountRepo{accounts: []Account{{
		ID: 7, Platform: PlatformOpenAI, UpstreamKeyID: &keyID, GroupIDs: []int64{12, 11, 12},
		UpstreamModelRoutes: []UpstreamKeyModelRoute{{
			PublicModel: "deepseek-chat", TargetPlatform: PlatformDeepseek,
			Enabled: true, Status: UpstreamKeyModelRouteStatusAvailable,
		}},
	}}}
	cache := &modelRouteRefreshCache{}
	service := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	if err := service.RefreshUpstreamKeyRoutes(context.Background(), keyID); err != nil {
		t.Fatalf("RefreshUpstreamKeyRoutes error: %v", err)
	}
	if len(cache.accounts) != 1 || cache.accounts[0].ID != 7 || len(cache.accounts[0].UpstreamModelRoutes) != 1 {
		t.Fatalf("expected refreshed routed account snapshot, got %#v", cache.accounts)
	}
	wantBuckets := schedulerCanonicalBucketCount() * 2
	if len(cache.buckets) != wantBuckets {
		t.Fatalf("unexpected rebuilt bucket count: got %d want %d", len(cache.buckets), wantBuckets)
	}
	seen := make(map[SchedulerBucket]bool, len(cache.buckets))
	for _, bucket := range cache.buckets {
		seen[bucket] = true
	}
	for _, groupID := range []int64{11, 12} {
		for _, platform := range []string{PlatformKimi, PlatformDeepseek} {
			for _, mode := range []string{SchedulerModeSingle, SchedulerModeForced} {
				bucket := SchedulerBucket{GroupID: groupID, Platform: platform, Mode: mode}
				if !seen[bucket] {
					t.Fatalf("missing rebuilt bucket %s", bucket.String())
				}
			}
		}
	}
	queried := make(map[string]bool)
	for _, platform := range repo.queryPlatforms {
		queried[platform] = true
	}
	for _, platform := range schedulerSnapshotPlatforms() {
		if !queried[platform] {
			t.Fatalf("platform %s was not refreshed", platform)
		}
	}
}

func TestRefreshUpstreamKeyRoutesWithoutGroupsRefreshesGlobalBucket(t *testing.T) {
	keyID := int64(43)
	repo := &modelRouteRefreshAccountRepo{accounts: []Account{{ID: 8, Platform: PlatformOpenAI, UpstreamKeyID: &keyID}}}
	cache := &modelRouteRefreshCache{}
	service := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	if err := service.RefreshUpstreamKeyRoutes(context.Background(), keyID); err != nil {
		t.Fatalf("RefreshUpstreamKeyRoutes error: %v", err)
	}
	if len(cache.accounts) != 1 || cache.accounts[0].ID != 8 {
		t.Fatalf("expected refreshed account snapshot, got %#v", cache.accounts)
	}
	if len(cache.buckets) != schedulerCanonicalBucketCount() {
		t.Fatalf("ungrouped account must rebuild the global bucket set: got %d", len(cache.buckets))
	}
	for _, bucket := range cache.buckets {
		if bucket.GroupID != 0 {
			t.Fatalf("ungrouped account must rebuild group 0 buckets: %#v", cache.buckets)
		}
	}
}
