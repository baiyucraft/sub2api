package service

import (
	"context"
	"sort"
	"time"
)

type accountProxyRoute struct {
	proxy    *Proxy
	target   ConcurrencyTarget
	position int
	load     *AccountLoadInfo
}

type failedProxyRoutesContextKey struct{}
type preferredProxyRouteContextKey struct{}

func ContextWithPreferredProxyRoute(ctx context.Context, proxyID int64) context.Context {
	if ctx == nil || proxyID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, preferredProxyRouteContextKey{}, proxyID)
}

// ContextWithFailedProxyRoutes supplies request-scoped route exclusions to
// both schedulers without changing the legacy account-exclusion signatures.
func ContextWithFailedProxyRoutes(ctx context.Context, failed map[string]struct{}) context.Context {
	if ctx == nil || len(failed) == 0 {
		return ctx
	}
	merged := make(map[string]struct{}, len(failed))
	if existing, _ := ctx.Value(failedProxyRoutesContextKey{}).(map[string]struct{}); len(existing) > 0 {
		for key := range existing {
			merged[key] = struct{}{}
		}
	}
	for key := range failed {
		merged[key] = struct{}{}
	}
	return context.WithValue(ctx, failedProxyRoutesContextKey{}, merged)
}

func proxyRouteFailed(ctx context.Context, target ConcurrencyTarget) bool {
	if ctx == nil {
		return false
	}
	failed, _ := ctx.Value(failedProxyRoutesContextKey{}).(map[string]struct{})
	_, exists := failed[target.Key()]
	return exists
}

func accountHasConfiguredProxy(account *Account) bool {
	if account == nil {
		return false
	}
	return len(account.ProxyIDs) > 0 || len(account.Proxies) > 0 || (account.ProxyID != nil && *account.ProxyID > 0) || account.Proxy != nil
}

// AvailableAccountProxyRoutes returns configured proxy routes in stable
// binding order and filters disabled/expired proxies. Legacy single-proxy
// accounts are represented by Account.Proxy.
func AvailableAccountProxyRoutes(account *Account, now time.Time) []*Proxy {
	if account == nil {
		return nil
	}
	configured := account.Proxies
	if len(configured) == 0 && account.Proxy != nil {
		configured = []*Proxy{account.Proxy}
	}
	// Scheduler snapshots created before proxy hydration may only carry the
	// legacy proxy_id. Preserve that route instead of treating it as direct or
	// unavailable; the concrete proxy metadata is filled when the account is
	// hydrated for forwarding.
	if len(configured) == 0 && account.ProxyID != nil && *account.ProxyID > 0 {
		configured = []*Proxy{{ID: *account.ProxyID, Status: StatusActive}}
	}
	result := make([]*Proxy, 0, len(configured))
	seen := make(map[int64]struct{}, len(configured))
	for _, proxy := range configured {
		if proxy == nil {
			continue
		}
		// Some legacy snapshots carry proxy metadata without its ID while the
		// account's proxy_id remains authoritative.
		if proxy.ID <= 0 && account.ProxyID != nil && *account.ProxyID > 0 && len(configured) == 1 {
			copyProxy := *proxy
			copyProxy.ID = *account.ProxyID
			if copyProxy.Status == "" {
				copyProxy.Status = StatusActive
			}
			proxy = &copyProxy
		}
		// Lightweight legacy snapshots may omit status along with the ID. The
		// configured proxy_id still identifies that route; treat an empty status
		// as active until a hydrated status says otherwise.
		legacySnapshot := len(configured) == 1 && account.ProxyID != nil && *account.ProxyID > 0 && proxy.ID == *account.ProxyID
		if proxy.ID <= 0 || (!proxy.IsActive() && !(legacySnapshot && proxy.Status == "")) || proxy.IsExpired(now) {
			continue
		}
		if _, exists := seen[proxy.ID]; exists {
			continue
		}
		seen[proxy.ID] = struct{}{}
		result = append(result, proxy)
	}
	return result
}

func orderedAccountProxyRoutes(ctx context.Context, concurrency *ConcurrencyService, account *Account) []accountProxyRoute {
	proxies := AvailableAccountProxyRoutes(account, time.Now())
	if len(proxies) == 0 {
		return nil
	}
	routes := make([]accountProxyRoute, 0, len(proxies))
	targets := make([]ConcurrencyTarget, 0, len(proxies))
	for position, proxy := range proxies {
		target := AccountProxyConcurrencyTarget(account, proxy.ID)
		if proxyRouteFailed(ctx, target) {
			continue
		}
		routes = append(routes, accountProxyRoute{proxy: proxy, target: target, position: position})
		targets = append(targets, target)
	}
	if concurrency == nil {
		return routes
	}
	loads, err := concurrency.GetConcurrencyTargetsLoadBatch(ctx, targets)
	if err != nil {
		return routes
	}
	for i := range routes {
		routes[i].load = loads[routes[i].target.Key()]
	}
	sort.SliceStable(routes, func(i, j int) bool {
		preferredID, _ := ctx.Value(preferredProxyRouteContextKey{}).(int64)
		if preferredID > 0 && routes[i].proxy.ID != routes[j].proxy.ID {
			if routes[i].proxy.ID == preferredID {
				return true
			}
			if routes[j].proxy.ID == preferredID {
				return false
			}
		}
		left, right := routes[i].load, routes[j].load
		if left == nil || right == nil {
			if left == nil && right == nil {
				return routes[i].position < routes[j].position
			}
			return right == nil
		}
		if left.LoadRate != right.LoadRate {
			return left.LoadRate < right.LoadRate
		}
		if left.CurrentConcurrency != right.CurrentConcurrency {
			return left.CurrentConcurrency < right.CurrentConcurrency
		}
		if left.WaitingCount != right.WaitingCount {
			return left.WaitingCount < right.WaitingCount
		}
		return routes[i].position < routes[j].position
	})
	return routes
}

// PreferredAccountProxyRoute returns the least-loaded available proxy without
// reserving a slot. It is used to keep a WaitPlan on the same route.
func PreferredAccountProxyRoute(ctx context.Context, concurrency *ConcurrencyService, account *Account) *Proxy {
	routes := orderedAccountProxyRoutes(ctx, concurrency, account)
	if len(routes) == 0 {
		return nil
	}
	return routes[0].proxy
}

func preferredAccountProxyConcurrencyTarget(ctx context.Context, concurrency *ConcurrencyService, account *Account) (ConcurrencyTarget, bool) {
	if account == nil {
		return ConcurrencyTarget{}, false
	}
	if proxy := PreferredAccountProxyRoute(ctx, concurrency, account); proxy != nil {
		return AccountProxyConcurrencyTarget(account, proxy.ID), true
	}
	if accountHasConfiguredProxy(account) {
		return ConcurrencyTarget{}, false
	}
	return account.SchedulingConcurrencyTarget(), true
}

func accountProxyRouteWaitingCount(ctx context.Context, concurrency *ConcurrencyService, account *Account) (int, bool) {
	target, ok := preferredAccountProxyConcurrencyTarget(ctx, concurrency, account)
	if !ok || concurrency == nil {
		return 0, false
	}
	waiting, err := concurrency.GetTargetWaitingCount(ctx, target)
	return waiting, err == nil
}

func selectionProxyForAccountRoute(ctx context.Context, concurrency *ConcurrencyService, account *Account, acquired bool) *Proxy {
	if account == nil {
		return nil
	}
	if acquired {
		return account.Proxy
	}
	if preferred := PreferredAccountProxyRoute(ctx, concurrency, account); preferred != nil {
		return preferred
	}
	if accountHasConfiguredProxy(account) {
		return nil
	}
	return account.Proxy
}

func accountSelectionResultForProxyRoute(account *Account, proxy *Proxy, acquired bool, release func(), waitPlan *AccountWaitPlan) *AccountSelectionResult {
	if account == nil {
		return &AccountSelectionResult{Acquired: acquired, ReleaseFunc: release, WaitPlan: waitPlan}
	}
	if proxy == nil && accountHasConfiguredProxy(account) {
		account.ProxyID = nil
		account.Proxy = nil
		return &AccountSelectionResult{Account: account, Acquired: acquired, ReleaseFunc: release}
	}
	proxyID := int64(0)
	target := account.SchedulingConcurrencyTarget()
	if proxy != nil {
		proxyID = proxy.ID
		account.ProxyID = &proxyID
		account.Proxy = proxy
		target = AccountProxyConcurrencyTarget(account, proxyID)
	}
	if waitPlan != nil {
		waitPlan.Target = target
	}
	return &AccountSelectionResult{
		Account:           account,
		ProxyID:           proxyID,
		Proxy:             proxy,
		ConcurrencyTarget: target,
		Acquired:          acquired,
		ReleaseFunc:       release,
		WaitPlan:          waitPlan,
	}
}

// AcquireAccountProxyRoute chooses the least-loaded available proxy and
// reserves its independent capacity. It returns nil proxy for direct accounts.
func AcquireAccountProxyRoute(ctx context.Context, concurrency *ConcurrencyService, account *Account) (*Proxy, ConcurrencyTarget, *AcquireResult, error) {
	if account == nil {
		return nil, ConcurrencyTarget{Kind: ConcurrencyTargetAccount}, &AcquireResult{Acquired: false}, nil
	}
	routes := orderedAccountProxyRoutes(ctx, concurrency, account)
	if len(routes) == 0 {
		if accountHasConfiguredProxy(account) {
			return nil, ConcurrencyTarget{}, &AcquireResult{Acquired: false}, nil
		}
		target := account.SchedulingConcurrencyTarget()
		if concurrency == nil {
			return nil, target, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
		}
		result, err := concurrency.AcquireTargetSlot(ctx, target)
		return nil, target, result, err
	}
	for _, route := range routes {
		if route.load != nil && route.load.LoadRate >= 100 {
			continue
		}
		if concurrency == nil {
			return route.proxy, route.target, &AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
		}
		result, err := concurrency.AcquireTargetSlot(ctx, route.target)
		if err != nil {
			return nil, route.target, nil, err
		}
		if result.Acquired {
			return route.proxy, route.target, result, nil
		}
	}
	return nil, ConcurrencyTarget{}, &AcquireResult{Acquired: false}, nil
}
