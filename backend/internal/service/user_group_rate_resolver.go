package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

type userGroupRateResolver struct {
	repo         UserGroupRateRepository
	cache        *gocache.Cache
	cacheTTL     time.Duration
	sf           *singleflight.Group
	logComponent string
}

// cachedUserGroupRate retains whether a user override actually exists. A bare
// effective float is insufficient because an explicit 0 and a missing entry
// must remain distinguishable, and a missing entry must not cache a caller's
// group default multiplier.
type cachedUserGroupRate struct {
	multiplier  float64
	hasOverride bool
}

// All request paths keep their own resolver instance (and therefore their own
// local cache).  Keep a small process-local registry so an administrative rate
// update can invalidate every instance immediately instead of waiting for the
// resolver TTL.  This is intentionally an in-process mechanism: each process
// receives the same admin operation through its normal cache invalidation
// path, while the resolver itself remains independent of the admin service.
var userGroupRateResolverRegistry sync.Map // map[*userGroupRateResolver]struct{}

func newUserGroupRateResolver(repo UserGroupRateRepository, cache *gocache.Cache, cacheTTL time.Duration, sf *singleflight.Group, logComponent string) *userGroupRateResolver {
	if cacheTTL <= 0 {
		cacheTTL = defaultUserGroupRateCacheTTL
	}
	if cache == nil {
		cache = gocache.New(cacheTTL, time.Minute)
	}
	if logComponent == "" {
		logComponent = "service.gateway"
	}
	if sf == nil {
		sf = &singleflight.Group{}
	}

	resolver := &userGroupRateResolver{
		repo:         repo,
		cache:        cache,
		cacheTTL:     cacheTTL,
		sf:           sf,
		logComponent: logComponent,
	}
	userGroupRateResolverRegistry.Store(resolver, struct{}{})
	return resolver
}

func (r *userGroupRateResolver) Resolve(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	multiplier, _ := r.ResolveWithExplicitZero(ctx, userID, groupID, groupDefaultMultiplier)
	return multiplier
}

// ResolveWithExplicitZero resolves a user's effective group multiplier and
// reports whether the stored value was explicitly zero. Both values come from
// the same cache/repository lookup so callers do not perform a second query.
func (r *userGroupRateResolver) ResolveWithExplicitZero(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) (float64, bool) {
	if r == nil || userID <= 0 || groupID <= 0 {
		return groupDefaultMultiplier, false
	}

	key := fmt.Sprintf("%d:%d", userID, groupID)
	if r.cache != nil {
		if cached, ok := r.cache.Get(key); ok {
			if entry, castOK := cached.(cachedUserGroupRate); castOK {
				userGroupRateCacheHitTotal.Add(1)
				if entry.hasOverride {
					return entry.multiplier, entry.multiplier == 0
				}
				return groupDefaultMultiplier, false
			}
			// Compatibility with entries written by older in-process code before
			// the cache started preserving override presence.
			if multiplier, castOK := cached.(float64); castOK {
				userGroupRateCacheHitTotal.Add(1)
				return multiplier, multiplier == 0
			}
		}
	}
	if r.repo == nil {
		return groupDefaultMultiplier, false
	}
	userGroupRateCacheMissTotal.Add(1)

	value, err, shared := r.sf.Do(key, func() (any, error) {
		if r.cache != nil {
			if cached, ok := r.cache.Get(key); ok {
				if entry, castOK := cached.(cachedUserGroupRate); castOK {
					userGroupRateCacheHitTotal.Add(1)
					return entry, nil
				}
				if multiplier, castOK := cached.(float64); castOK {
					userGroupRateCacheHitTotal.Add(1)
					return cachedUserGroupRate{multiplier: multiplier, hasOverride: true}, nil
				}
			}
		}

		userGroupRateCacheLoadTotal.Add(1)
		userRate, repoErr := r.repo.GetByUserAndGroup(ctx, userID, groupID)
		if repoErr != nil {
			return nil, repoErr
		}

		entry := cachedUserGroupRate{}
		if userRate != nil {
			entry.multiplier = *userRate
			entry.hasOverride = true
		}
		if r.cache != nil {
			r.cache.Set(key, entry, r.cacheTTL)
		}
		return entry, nil
	})
	if shared {
		userGroupRateCacheSFSharedTotal.Add(1)
	}
	if err != nil {
		userGroupRateCacheFallbackTotal.Add(1)
		logger.LegacyPrintf(r.logComponent, "get user group rate failed, fallback to group default: user=%d group=%d err=%v", userID, groupID, err)
		return groupDefaultMultiplier, false
	}

	entry, ok := value.(cachedUserGroupRate)
	if !ok {
		userGroupRateCacheFallbackTotal.Add(1)
		return groupDefaultMultiplier, false
	}
	if !entry.hasOverride {
		return groupDefaultMultiplier, false
	}
	return entry.multiplier, entry.multiplier == 0
}

// IsExplicitZero reports whether a user has explicitly configured a zero rate
// for the group. A missing entry is deliberately different from a stored 0.
func (r *userGroupRateResolver) IsExplicitZero(ctx context.Context, userID, groupID int64) bool {
	_, zero := r.ResolveWithExplicitZero(ctx, userID, groupID, 1)
	return zero
}

// Invalidate removes one user/group entry after an administrative update.
func (r *userGroupRateResolver) Invalidate(userID, groupID int64) {
	if r == nil || r.cache == nil || userID <= 0 || groupID <= 0 {
		return
	}
	r.cache.Delete(fmt.Sprintf("%d:%d", userID, groupID))
}

func (r *userGroupRateResolver) InvalidateUser(userID int64) {
	if r == nil || r.cache == nil || userID <= 0 {
		return
	}
	for key := range r.cache.Items() {
		if strings.HasPrefix(key, fmt.Sprintf("%d:", userID)) {
			r.cache.Delete(key)
		}
	}
}

// InvalidateGroup removes all locally cached entries for one group.
func (r *userGroupRateResolver) InvalidateGroup(groupID int64) {
	if r == nil || r.cache == nil || groupID <= 0 {
		return
	}
	suffix := fmt.Sprintf(":%d", groupID)
	for key := range r.cache.Items() {
		if strings.HasSuffix(key, suffix) {
			r.cache.Delete(key)
		}
	}
}

// InvalidateUserGroupRateCaches invalidates one user/group pair in every
// resolver created by the process.
func InvalidateUserGroupRateCaches(userID, groupID int64) {
	userGroupRateResolverRegistry.Range(func(key, _ any) bool {
		if resolver, ok := key.(*userGroupRateResolver); ok {
			resolver.Invalidate(userID, groupID)
		}
		return true
	})
}

// InvalidateUserGroupRateCachesByUser invalidates all group entries for a user
// in every request-path resolver.
func InvalidateUserGroupRateCachesByUser(userID int64) {
	userGroupRateResolverRegistry.Range(func(key, _ any) bool {
		if resolver, ok := key.(*userGroupRateResolver); ok {
			resolver.InvalidateUser(userID)
		}
		return true
	})
}

// InvalidateUserGroupRateCachesByGroup invalidates all user entries for a group
// in every request-path resolver.
func InvalidateUserGroupRateCachesByGroup(groupID int64) {
	userGroupRateResolverRegistry.Range(func(key, _ any) bool {
		if resolver, ok := key.(*userGroupRateResolver); ok {
			resolver.InvalidateGroup(groupID)
		}
		return true
	})
}
