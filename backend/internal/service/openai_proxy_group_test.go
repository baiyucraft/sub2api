package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type proxyGroupBindingCacheStub struct {
	GatewayCache
	mu       sync.Mutex
	bindings map[string]int64
	getErr   error
	missOnce bool
}

func (c *proxyGroupBindingCacheStub) key(accountID int64, sessionHash string) string {
	return fmt.Sprintf("%d:%s", accountID, sessionHash)
}

func (c *proxyGroupBindingCacheStub) GetOpenAIProxyGroupBinding(_ context.Context, accountID int64, sessionHash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return 0, c.getErr
	}
	if c.missOnce {
		c.missOnce = false
		return 0, ErrOpenAIProxyGroupBindingNotFound
	}
	proxyID, ok := c.bindings[c.key(accountID, sessionHash)]
	if !ok {
		return 0, ErrOpenAIProxyGroupBindingNotFound
	}
	return proxyID, nil
}

func (c *proxyGroupBindingCacheStub) ClaimOpenAIProxyGroupBinding(_ context.Context, accountID int64, sessionHash string, proxyID int64, _ time.Duration) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bindings == nil {
		c.bindings = make(map[string]int64)
	}
	if bound := c.bindings[c.key(accountID, sessionHash)]; bound > 0 {
		return bound, nil
	}
	c.bindings[c.key(accountID, sessionHash)] = proxyID
	return proxyID, nil
}

func (c *proxyGroupBindingCacheStub) DeleteOpenAIProxyGroupBindingIfMatch(_ context.Context, accountID int64, sessionHash string, proxyID int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bindings[c.key(accountID, sessionHash)] == proxyID {
		delete(c.bindings, c.key(accountID, sessionHash))
	}
	return nil
}

func (c *proxyGroupBindingCacheStub) boundProxyID(accountID int64, sessionHash string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bindings[c.key(accountID, sessionHash)]
}

type proxyGroupConcurrencyCacheStub struct {
	ConcurrencyCache
	mu           sync.Mutex
	available    map[int64]bool
	acquireCalls []int64
	active       map[int64]map[string]struct{}
	releases     map[int64]int
}

func (c *proxyGroupConcurrencyCacheStub) AcquireAccountProxySlot(_ context.Context, _ int64, proxyID int64, _ int, requestID string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acquireCalls = append(c.acquireCalls, proxyID)
	if !c.available[proxyID] {
		return false, nil
	}
	if c.active == nil {
		c.active = make(map[int64]map[string]struct{})
	}
	if c.active[proxyID] == nil {
		c.active[proxyID] = make(map[string]struct{})
	}
	c.active[proxyID][requestID] = struct{}{}
	return true, nil
}

func (c *proxyGroupConcurrencyCacheStub) ReleaseAccountProxySlot(_ context.Context, _ int64, proxyID int64, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.active[proxyID], requestID)
	if c.releases == nil {
		c.releases = make(map[int64]int)
	}
	c.releases[proxyID]++
	return nil
}

func (c *proxyGroupConcurrencyCacheStub) GetAccountProxyConcurrency(_ context.Context, _ int64, proxyID int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active[proxyID]), nil
}

func (c *proxyGroupConcurrencyCacheStub) calls() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int64(nil), c.acquireCalls...)
}

func (c *proxyGroupConcurrencyCacheStub) activeCount(proxyID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active[proxyID])
}

func (c *proxyGroupConcurrencyCacheStub) releaseCount(proxyID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releases[proxyID]
}

type proxyGroupRepoStub struct {
	ProxyIPGroupRepository
	group *ProxyIPGroup
}

func (r *proxyGroupRepoStub) GetByID(_ context.Context, id int64) (*ProxyIPGroup, error) {
	if r.group == nil || r.group.ID != id {
		return nil, ErrProxyIPGroupNotFound
	}
	copy := *r.group
	copy.ProxyIDs = append([]int64(nil), r.group.ProxyIDs...)
	return &copy, nil
}

type proxyGroupProxyRepoStub struct {
	ProxyRepository
	proxies []Proxy
}

func (r *proxyGroupProxyRepoStub) ListByIDs(_ context.Context, ids []int64) ([]Proxy, error) {
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	out := make([]Proxy, 0, len(ids))
	for _, proxy := range r.proxies {
		if _, ok := wanted[proxy.ID]; ok {
			out = append(out, proxy)
		}
	}
	return out, nil
}

func newProxyGroupTestService(bindingCache *proxyGroupBindingCacheStub, concurrencyCache *proxyGroupConcurrencyCacheStub, proxyIDs []int64, proxies []Proxy) (*OpenAIGatewayService, *Account) {
	groupID := int64(41)
	account := &Account{ID: 101, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ProxyIPGroupID: &groupID}
	svc := &OpenAIGatewayService{
		cache:              bindingCache,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		proxyIPGroupRepo: &proxyGroupRepoStub{group: &ProxyIPGroup{
			ID: groupID, Name: "test-group", PerIPConcurrency: 1, ProxyIDs: append([]int64(nil), proxyIDs...),
		}},
		proxyRepo: &proxyGroupProxyRepoStub{proxies: proxies},
	}
	return svc, account
}

func proxyGroupTestProxies() []Proxy {
	return []Proxy{{ID: 2, Name: "proxy-2", Status: StatusActive}, {ID: 1, Name: "proxy-1", Status: StatusActive}}
}

func requireProxyGroupError(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected error %v, got %v", target, err)
	}
}

func requireResolvedProxy(t *testing.T, account *Account, want int64) {
	t.Helper()
	if account == nil || account.ProxyID == nil || *account.ProxyID != want {
		t.Fatalf("expected resolved proxy %d, got %#v", want, account)
	}
}

func requireProxyCalls(t *testing.T, cache *proxyGroupConcurrencyCacheStub, want ...int64) {
	t.Helper()
	got := cache.calls()
	if len(got) != len(want) {
		t.Fatalf("expected proxy acquisition calls %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected proxy acquisition calls %v, got %v", want, got)
		}
	}
}

func TestAcquireOpenAIProxyGroupEgressKeepsSameSessionBinding(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: true, 2: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{2, 1}, proxyGroupTestProxies())

	first, releaseFirst, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	requireResolvedProxy(t, first, 1)
	releaseFirst()

	second, releaseSecond, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
	if err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	requireResolvedProxy(t, second, 1)
	releaseSecond()

	if got := bindingCache.boundProxyID(account.ID, "session-a"); got != 1 {
		t.Fatalf("expected session binding to proxy 1, got %d", got)
	}
	requireProxyCalls(t, concurrencyCache, 1, 1)
}

func TestAcquireOpenAIProxyGroupEgressDoesNotRebindFullBoundProxy(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: map[string]int64{"101:session-a": 1}}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: false, 2: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1, 2}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
	if resolved != nil || release != nil {
		t.Fatalf("expected no resolved egress on full bound proxy, got account=%#v release=%v", resolved, release != nil)
	}
	requireProxyGroupError(t, err, ErrOpenAIProxyGroupCapacityFull)
	if got := bindingCache.boundProxyID(account.ID, "session-a"); got != 1 {
		t.Fatalf("expected binding to stay on proxy 1, got %d", got)
	}
	requireProxyCalls(t, concurrencyCache, 1)
}

func TestAcquireOpenAIProxyGroupEgressNewSessionSkipsFullMember(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: false, 2: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{2, 1}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-b")
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	requireResolvedProxy(t, resolved, 2)
	if got := bindingCache.boundProxyID(account.ID, "session-b"); got != 2 {
		t.Fatalf("expected session binding to proxy 2, got %d", got)
	}
	requireProxyCalls(t, concurrencyCache, 1, 2)
	release()
}

func TestAcquireOpenAIProxyGroupEgressConcurrentClaimKeepsWinner(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: map[string]int64{"101:session-a": 2}, missOnce: true}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: true, 2: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1, 2}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
	if err != nil {
		t.Fatalf("acquire after concurrent claim failed: %v", err)
	}
	requireResolvedProxy(t, resolved, 2)
	requireProxyCalls(t, concurrencyCache, 1, 2)
	if got := concurrencyCache.releaseCount(1); got != 1 {
		t.Fatalf("losing proxy slot must be released, got %d releases", got)
	}
	if got := bindingCache.boundProxyID(account.ID, "session-a"); got != 2 {
		t.Fatalf("winning proxy binding changed to %d", got)
	}
	release()
}

func TestAcquireOpenAIProxyGroupEgressFailsClosed(t *testing.T) {
	t.Run("no members", func(t *testing.T) {
		bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
		concurrencyCache := &proxyGroupConcurrencyCacheStub{available: make(map[int64]bool)}
		svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, nil, nil)

		resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
		if resolved != nil || release != nil {
			t.Fatalf("expected no egress for empty group, got account=%#v release=%v", resolved, release != nil)
		}
		requireProxyGroupError(t, err, ErrOpenAIProxyGroupNoEgress)
	})

	t.Run("binding cache failure", func(t *testing.T) {
		bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64), getErr: errors.New("redis unavailable")}
		concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: true}}
		svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1}, proxyGroupTestProxies())

		resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "session-a")
		if resolved != nil || release != nil {
			t.Fatalf("expected binding failure to fail closed, got account=%#v release=%v", resolved, release != nil)
		}
		requireProxyGroupError(t, err, ErrOpenAIProxyGroupBindingUnavailable)
		requireProxyCalls(t, concurrencyCache)
	})
}

func TestAcquireOpenAIProxyGroupEgressAllMembersFull(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: false, 2: false}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1, 2}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "")
	if resolved != nil || release != nil {
		t.Fatalf("expected no egress when all members are full, got account=%#v release=%v", resolved, release != nil)
	}
	requireProxyGroupError(t, err, ErrOpenAIProxyGroupCapacityFull)
	requireProxyCalls(t, concurrencyCache, 1, 2)
}

func TestAcquireOpenAIProxyGroupEgressReleaseFreesSlot(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIProxyGroupEgress(context.Background(), account, "")
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	requireResolvedProxy(t, resolved, 1)
	if got := concurrencyCache.activeCount(1); got != 1 {
		t.Fatalf("expected one active slot before release, got %d", got)
	}

	release()
	if got := concurrencyCache.activeCount(1); got != 0 {
		t.Fatalf("expected slot to be released, got %d active", got)
	}
	if got := concurrencyCache.releaseCount(1); got != 1 {
		t.Fatalf("expected one release call, got %d", got)
	}
}

func TestAcquireOpenAIManagementEgressSkipsFullMemberWithoutBinding(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: false, 2: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1, 2}, proxyGroupTestProxies())

	resolved, release, err := svc.AcquireOpenAIManagementEgress(context.Background(), account)
	if err != nil {
		t.Fatalf("acquire management egress failed: %v", err)
	}
	requireResolvedProxy(t, resolved, 2)
	requireProxyCalls(t, concurrencyCache, 1, 2)
	if got := bindingCache.boundProxyID(account.ID, ""); got != 0 {
		t.Fatalf("management egress must not create session binding, got proxy %d", got)
	}

	release()
	if got := concurrencyCache.releaseCount(2); got != 1 {
		t.Fatalf("expected management proxy slot release, got %d", got)
	}
}

func TestAcquireOpenAIManagementEgressFailsClosedWithoutConcurrency(t *testing.T) {
	bindingCache := &proxyGroupBindingCacheStub{bindings: make(map[string]int64)}
	concurrencyCache := &proxyGroupConcurrencyCacheStub{available: map[int64]bool{1: true}}
	svc, account := newProxyGroupTestService(bindingCache, concurrencyCache, []int64{1}, proxyGroupTestProxies())
	svc.concurrencyService = nil

	resolved, release, err := svc.AcquireOpenAIManagementEgress(context.Background(), account)
	if resolved != nil || release != nil {
		t.Fatalf("expected no management egress when concurrency is unavailable")
	}
	requireProxyGroupError(t, err, ErrOpenAIProxyGroupBindingUnavailable)
}
