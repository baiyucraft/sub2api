//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountProxyRouteCacheForTest struct {
	stubConcurrencyCacheForTest
	loads          map[string]*AccountLoadInfo
	acquireResults map[string]bool
	acquired       []ConcurrencyTarget
	released       []ConcurrencyTarget
}

func (c *accountProxyRouteCacheForTest) GetConcurrencyTargetsLoadBatch(_ context.Context, targets []ConcurrencyTarget) (map[string]*AccountLoadInfo, error) {
	result := make(map[string]*AccountLoadInfo, len(targets))
	for _, target := range targets {
		if load := c.loads[target.Key()]; load != nil {
			copyLoad := *load
			result[target.Key()] = &copyLoad
		}
	}
	return result, nil
}

func (c *accountProxyRouteCacheForTest) AcquireConcurrencyTargetSlot(_ context.Context, target ConcurrencyTarget, _ string) (bool, error) {
	c.acquired = append(c.acquired, target)
	acquired, configured := c.acquireResults[target.Key()]
	if !configured {
		acquired = true
	}
	return acquired, nil
}

func (c *accountProxyRouteCacheForTest) ReleaseConcurrencyTargetSlot(_ context.Context, target ConcurrencyTarget, _ string) error {
	c.released = append(c.released, target)
	return nil
}

func (c *accountProxyRouteCacheForTest) IncrementConcurrencyTargetWaitCount(context.Context, ConcurrencyTarget, int) (bool, error) {
	return true, nil
}

func (c *accountProxyRouteCacheForTest) DecrementConcurrencyTargetWaitCount(context.Context, ConcurrencyTarget) error {
	return nil
}

func (c *accountProxyRouteCacheForTest) GetConcurrencyTargetWaitingCount(context.Context, ConcurrencyTarget) (int, error) {
	return 0, nil
}

func activeAccountProxy(id int64, name string) *Proxy {
	return &Proxy{ID: id, Name: name, Status: StatusActive}
}

func TestAcquireAccountProxyRouteUsesLeastLoadedIndependentCapacity(t *testing.T) {
	first := activeAccountProxy(11, "first")
	second := activeAccountProxy(12, "second")
	account := &Account{ID: 41, Concurrency: 5, ProxyIDs: []int64{11, 12}, Proxies: []*Proxy{first, second}, Proxy: first}
	cache := &accountProxyRouteCacheForTest{loads: map[string]*AccountLoadInfo{
		"account_proxy:41:11": {CurrentConcurrency: 4, WaitingCount: 1, LoadRate: 80},
		"account_proxy:41:12": {CurrentConcurrency: 1, WaitingCount: 0, LoadRate: 20},
	}}

	proxy, target, result, err := AcquireAccountProxyRoute(context.Background(), NewConcurrencyService(cache), account)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Same(t, second, proxy)
	require.Equal(t, ConcurrencyTarget{Kind: ConcurrencyTargetAccountProxy, ID: 41, ProxyID: 12, Limit: 5}, target)
	require.Equal(t, []ConcurrencyTarget{target}, cache.acquired)

	result.ReleaseFunc()
	require.Equal(t, []ConcurrencyTarget{target}, cache.released)
}

func TestPreferredAccountProxyRouteHonorsStickyRouteBeforeLoad(t *testing.T) {
	first := activeAccountProxy(11, "first")
	second := activeAccountProxy(12, "second")
	account := &Account{ID: 41, Concurrency: 5, ProxyIDs: []int64{11, 12}, Proxies: []*Proxy{first, second}, Proxy: first}
	cache := &accountProxyRouteCacheForTest{loads: map[string]*AccountLoadInfo{
		"account_proxy:41:11": {CurrentConcurrency: 0, LoadRate: 0},
		"account_proxy:41:12": {CurrentConcurrency: 4, LoadRate: 80},
	}}
	ctx := ContextWithPreferredProxyRoute(context.Background(), second.ID)

	require.Same(t, second, PreferredAccountProxyRoute(ctx, NewConcurrencyService(cache), account))
}

func TestAvailableAccountProxyRoutesPreservesBindingsAndFiltersUnavailable(t *testing.T) {
	expiredAt := time.Now().Add(-time.Minute)
	active := activeAccountProxy(11, "active")
	disabled := &Proxy{ID: 12, Status: StatusDisabled}
	expired := &Proxy{ID: 13, Status: StatusActive, ExpiresAt: &expiredAt}
	account := &Account{Proxies: []*Proxy{disabled, active, expired, active}}

	require.Equal(t, []*Proxy{active}, AvailableAccountProxyRoutes(account, time.Now()))
}

func TestGatewayTryAcquireAccountSlotSynchronizesSelectedProxyID(t *testing.T) {
	first := activeAccountProxy(11, "first")
	second := activeAccountProxy(12, "second")
	firstID := first.ID
	account := &Account{ID: 41, Concurrency: 5, ProxyID: &firstID, ProxyIDs: []int64{11, 12}, Proxies: []*Proxy{first, second}, Proxy: first}
	cache := &accountProxyRouteCacheForTest{loads: map[string]*AccountLoadInfo{
		"account_proxy:41:11": {CurrentConcurrency: 5, LoadRate: 100},
		"account_proxy:41:12": {CurrentConcurrency: 0, LoadRate: 0},
	}}
	svc := &GatewayService{concurrencyService: NewConcurrencyService(cache)}

	result, err := svc.tryAcquireAccountSlot(context.Background(), account)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.NotNil(t, account.ProxyID)
	require.Equal(t, second.ID, *account.ProxyID)
	require.Same(t, second, account.Proxy)
}

func TestReplaceAccountPreservingRouteSynchronizesProxyID(t *testing.T) {
	selectedProxy := activeAccountProxy(12, "second")
	selection := &AccountSelectionResult{ProxyID: selectedProxy.ID, Proxy: selectedProxy}
	firstID := int64(11)
	fresh := &Account{ID: 41, Concurrency: 5, ProxyID: &firstID, Proxy: activeAccountProxy(firstID, "first")}

	selection.ReplaceAccountPreservingRoute(fresh)

	require.Same(t, fresh, selection.Account)
	require.Same(t, selectedProxy, fresh.Proxy)
	require.NotNil(t, fresh.ProxyID)
	require.Equal(t, selectedProxy.ID, *fresh.ProxyID)
	require.Equal(t, "account_proxy:41:12", selection.ConcurrencyTarget.Key())
}

func TestContextWithFailedProxyRoutesMergesExistingRoutes(t *testing.T) {
	account := &Account{ID: 41, Concurrency: 5}
	first := AccountProxyConcurrencyTarget(account, 7).Key()
	second := AccountProxyConcurrencyTarget(account, 9).Key()
	ctx := ContextWithFailedProxyRoutes(context.Background(), map[string]struct{}{first: {}})
	ctx = ContextWithFailedProxyRoutes(ctx, map[string]struct{}{second: {}})

	require.True(t, proxyRouteFailed(ctx, AccountProxyConcurrencyTarget(account, 7)))
	require.True(t, proxyRouteFailed(ctx, AccountProxyConcurrencyTarget(account, 9)))
}

func TestOpenAIWSRequestCompatibilityIncludesProxyRoute(t *testing.T) {
	account := &Account{ID: 41}
	first := normalizeOpenAIWSRequestCompatibility(account, nil, "http://proxy-a.example:8080")
	second := normalizeOpenAIWSRequestCompatibility(account, nil, "http://proxy-b.example:8080")

	require.NotEqual(t, first, second)
	require.Equal(t, first, normalizeOpenAIWSRequestCompatibility(account, nil, " http://proxy-a.example:8080 "))
}
