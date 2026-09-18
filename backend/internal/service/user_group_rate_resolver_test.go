package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type userGroupRateResolverRepoStub struct {
	UserGroupRateRepository

	percent *float64
	err     error
	calls   int
}

func (s *userGroupRateResolverRepoStub) GetPercentByUserID(context.Context, int64) (map[int64]float64, error) {
	panic("unexpected GetPercentByUserID call")
}

func (s *userGroupRateResolverRepoStub) GetPercentByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.percent, nil
}

func TestNewUserGroupRateResolver_Defaults(t *testing.T) {
	resolver := newUserGroupRateResolver(nil, nil, 0, nil, "")

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.cache)
	require.Equal(t, defaultUserGroupRateCacheTTL, resolver.cacheTTL)
	require.NotNil(t, resolver.sf)
	require.Equal(t, "service.gateway", resolver.logComponent)
}

func TestUserGroupRateResolverResolve_FallbackForNilResolverAndInvalidIDs(t *testing.T) {
	var nilResolver *userGroupRateResolver
	require.Equal(t, 1.4, nilResolver.Resolve(context.Background(), 101, 202, 1.4))

	resolver := newUserGroupRateResolver(nil, nil, time.Second, nil, "service.test")
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 0, 202, 1.4))
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 101, 0, 1.4))
}

func TestUserGroupRateResolverResolve_InvalidCacheEntryLoadsRepoAndCaches(t *testing.T) {
	resetGatewayHotpathStatsForTest()

	percent := 50.0
	repo := &userGroupRateResolverRepoStub{percent: &percent}
	cache := gocache.New(time.Minute, time.Minute)
	cache.Set("101:202", "bad-cache", time.Minute)
	resolver := newUserGroupRateResolver(repo, cache, time.Minute, nil, "service.test")

	got := resolver.Resolve(context.Background(), 101, 202, 1.2)
	require.Equal(t, 0.6, got)
	require.Equal(t, 1, repo.calls)

	cached, ok := cache.Get("101:202")
	require.True(t, ok)
	entry, ok := cached.(cachedUserGroupRate)
	require.True(t, ok)
	require.Equal(t, percent, entry.percent)
	require.True(t, entry.hasOverride)

	hit, miss, load, _, fallback := GatewayUserGroupRateCacheStats()
	require.Equal(t, int64(0), hit)
	require.Equal(t, int64(1), miss)
	require.Equal(t, int64(1), load)
	require.Equal(t, int64(0), fallback)
}

func TestGatewayServiceGetUserGroupRateMultiplier_FallbacksAndUsesExistingResolver(t *testing.T) {
	var nilSvc *GatewayService
	require.Equal(t, 1.3, nilSvc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.3))

	percent := 150.0
	repo := &userGroupRateResolverRepoStub{percent: &percent}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.gateway")
	svc := &GatewayService{userGroupRateResolver: resolver}

	got := svc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.2)
	require.Equal(t, 1.8, got)
	require.Equal(t, 1, repo.calls)
}

func TestUserGroupRateResolver_ExplicitZeroAndInvalidation(t *testing.T) {
	percent := 62.5
	repo := &userGroupRateResolverRepoStub{percent: &percent}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")

	require.False(t, resolver.IsExplicitZero(context.Background(), 101, 202))
	percent = 0
	// The cached positive value must remain until the administrative invalidation.
	require.False(t, resolver.IsExplicitZero(context.Background(), 101, 202))
	InvalidateUserGroupRateCaches(101, 202)
	require.True(t, resolver.IsExplicitZero(context.Background(), 101, 202))

	percent = 75
	InvalidateUserGroupRateCaches(101, 202)
	require.Equal(t, 1.5, resolver.Resolve(context.Background(), 101, 202, 2.0))
}

func TestUserGroupRateResolver_MissingRateFallsBackToGroupDefault(t *testing.T) {
	repo := &userGroupRateResolverRepoStub{percent: nil}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")

	require.False(t, resolver.IsExplicitZero(context.Background(), 101, 202))
	require.Equal(t, 2.0, resolver.Resolve(context.Background(), 101, 202, 2.0))
}

func TestUserGroupRateResolver_CachedPercentTracksCurrentGroupRate(t *testing.T) {
	percent := 50.0
	repo := &userGroupRateResolverRepoStub{percent: &percent}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")

	require.Equal(t, 0.5, resolver.Resolve(context.Background(), 101, 202, 1.0))
	require.Equal(t, 1.0, resolver.Resolve(context.Background(), 101, 202, 2.0))
	require.Equal(t, 1, repo.calls)
}
