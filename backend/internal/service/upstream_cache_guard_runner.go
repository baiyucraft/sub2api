package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type cacheGuardSchedulingKey struct{}
type cacheGuardProtectedSelectionKey struct{}
type cacheGuardSchedulingIdentity struct {
	APIKeyID  int64
	RequestID string
	StartedAt time.Time
	budget    *cacheGuardRequestBudget
}

// The deadline begins at the first protection database operation. It is shared
// by selection and admission, without shortening the ordinary request context.
type cacheGuardRequestBudget struct {
	mu        sync.Mutex
	deadline  time.Time
	exhausted atomic.Bool
	ordinary  atomic.Bool
}

func cacheGuardDatabaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if !ok || identity.budget == nil {
		return context.WithTimeout(ctx, 100*time.Millisecond)
	}
	b := identity.budget
	b.mu.Lock()
	if b.deadline.IsZero() {
		b.deadline = time.Now().Add(100 * time.Millisecond)
	}
	deadline := b.deadline
	b.mu.Unlock()
	child, cancel := context.WithDeadline(ctx, deadline)
	return child, func() {
		if child.Err() == context.DeadlineExceeded {
			b.ordinary.Store(true)
		}
		if child.Err() == context.DeadlineExceeded && b.exhausted.CompareAndSwap(false, true) {
			slog.Warn("upstream_cache_guard_budget_exhausted", "budget_ms", 100, "fallback", "ordinary_scheduler")
		}
		cancel()
	}
}

func protectionOrdinaryFallback(ctx context.Context) bool {
	identity, _ := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	return identity.budget != nil && identity.budget.ordinary.Load()
}

func protectionBudgetFallbackError() *UpstreamFailoverError {
	failure := schedulingProtectionScopeChangedError()
	failure.Reason = "scheduling_protection_budget_exhausted"
	failure.ProtectionOrdinaryFallback = true
	return failure
}

// WithUpstreamCacheGuardScheduling is installed only by authenticated streaming
// text handlers. Internal probes and requests without a reliable session stay neutral.
func WithUpstreamCacheGuardScheduling(ctx context.Context, apiKeyID int64) context.Context {
	if apiKeyID <= 0 {
		return ctx
	}
	if old, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity); ok && old.APIKeyID == apiKeyID {
		return ctx
	}
	return context.WithValue(ctx, cacheGuardSchedulingKey{}, cacheGuardSchedulingIdentity{APIKeyID: apiKeyID, RequestID: newCacheGuardID(), StartedAt: time.Now().UTC(), budget: &cacheGuardRequestBudget{}})
}

func newCacheGuardID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

type UpstreamCacheGuardAdmission struct {
	Request         upstreamcacheguard.Request
	Admission       upstreamcacheguard.Admission
	AccountPlatform string
}
type UpstreamCacheGuardRunner struct {
	engine         *upstreamcacheguard.Engine
	store          upstreamcacheguard.Store
	owner          string
	settings       *SettingService
	secret         []byte
	queue          chan upstreamcacheguard.Evidence
	wake           chan struct{}
	stop           chan struct{}
	done           chan struct{}
	once           sync.Once
	available      atomic.Bool
	closed         atomic.Bool
	retrying       atomic.Int64
	lost           atomic.Bool
	pendingFailure atomic.Bool
	// Backlog health is diagnostic only; routing reads account snapshots.
	pendingDelayed atomic.Bool
	pendingUnknown atomic.Bool
	policyMu       sync.RWMutex
	policyResolver GroupSchedulingProtectionPolicyResolver
	appliedPolicy  *GroupSchedulingProtectionPolicySnapshot
}

var installedCacheGuardRunner atomic.Pointer[UpstreamCacheGuardRunner]

func currentUpstreamCacheGuardRunner() *UpstreamCacheGuardRunner {
	return installedCacheGuardRunner.Load()
}

func ProvideUpstreamCacheGuardRunner(store upstreamcacheguard.Store, settings *SettingService, cfg *config.Config, policies *GroupSchedulingProtectionPolicyService) *UpstreamCacheGuardRunner {
	r := &UpstreamCacheGuardRunner{engine: NewUpstreamCacheGuardRuntime(store), store: store, owner: newCacheGuardID(), settings: settings, queue: make(chan upstreamcacheguard.Evidence, 16384), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	if cfg != nil {
		r.secret = []byte(cfg.JWT.Secret)
	}
	if policies != nil {
		r.SetProtectionPolicyResolver(policies)
	}
	installedCacheGuardRunner.Store(r)
	go r.run()
	return r
}
func (r *UpstreamCacheGuardRunner) applySettings(s UpstreamCacheGuardSettings) {
	if r != nil {
		_ = r.engine.ApplySettings(s)
	}
}

func (r *UpstreamCacheGuardRunner) SetProtectionPolicyResolver(resolver GroupSchedulingProtectionPolicyResolver) {
	if r == nil {
		return
	}
	r.policyMu.Lock()
	r.policyResolver = resolver
	r.appliedPolicy = nil
	r.policyMu.Unlock()
	r.applyPolicySnapshot()
}

func (r *UpstreamCacheGuardRunner) applyPolicySnapshot() {
	// Resolver replacement and publication are serialized so an older refresh
	// cannot install its policies after a new resolver has been selected.
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	resolver := r.policyResolver
	if provider, ok := resolver.(protectionPolicySnapshotProvider); ok {
		snapshot := provider.ProtectionPoliciesSnapshot()
		if snapshot == nil {
			_ = r.engine.ApplyPolicySettings(nil)
			r.appliedPolicy = nil
			return
		}
		if snapshot == r.appliedPolicy {
			return
		}
		values := make(map[int64]UpstreamCacheGuardSettings, len(snapshot.policies))
		for id, policy := range snapshot.policies {
			values[id] = policy.Cache
		}
		if r.engine.ApplyPolicySettings(values) == nil {
			r.appliedPolicy = snapshot
		}
		return
	}
	if provider, ok := resolver.(interface {
		CachePolicies() map[int64]UpstreamCacheGuardSettings
	}); ok {
		_ = r.engine.ApplyPolicySettings(provider.CachePolicies())
	}
}

func (r *UpstreamCacheGuardRunner) cachePolicyMetadata(groupID int64) (string, string) {
	r.policyMu.RLock()
	resolver := r.policyResolver
	r.policyMu.RUnlock()
	if provider, ok := resolver.(interface{ CachePolicyMetadata(int64) (string, string) }); ok {
		return provider.CachePolicyMetadata(groupID)
	}
	return "inherit", "global"
}
func (r *UpstreamCacheGuardRunner) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	if r.settings == nil || len(r.secret) == 0 {
		r.available.Store(false)
		return
	}
	s, err := r.settings.GetUpstreamCacheGuardSettings(ctx)
	if err != nil {
		r.available.Store(false)
		return
	}
	if err = r.engine.ApplySettings(s); err != nil {
		r.available.Store(false)
		return
	}
	r.applyPolicySnapshot()
	if !r.engine.AnyPolicyActive() {
		r.available.Store(false)
		return
	}
	err = r.engine.Refresh(ctx)
	delayed, pendingErr := r.store.HasPendingBefore(ctx, time.Now().UTC().Add(-upstreamcacheguard.RefreshLifetime))
	r.pendingUnknown.Store(pendingErr != nil)
	if pendingErr == nil {
		r.pendingDelayed.Store(delayed)
	}
	// A delayed pending row is an outbox health signal, not a global cache
	// protection circuit breaker. The engine keeps the last successful snapshot
	// available for a short bounded window when a refresh is temporarily late.
	r.available.Store(err == nil && r.retrying.Load() == 0 && !r.lost.Load() && !r.pendingFailure.Load() && !r.closed.Load())
}
func (r *UpstreamCacheGuardRunner) run() {
	defer close(r.done)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		r.refresh()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.refresh()
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				r.processPending()
			case <-r.wake:
				r.processPending()
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-r.stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = r.engine.Cleanup(ctx)
				cancel()
			}
		}
	}()
	defer func() { workers.Wait(); r.engine.Close() }()
	for {
		select {
		case <-r.stop:
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case v := <-r.queue:
					if !r.retryEnqueue(v) {
						return
					}
				default:
					return
				}
			}
			return
		case v := <-r.queue:
			if !r.retryEnqueue(v) {
				select {
				case r.queue <- v:
				case <-r.stop:
					return
				default:
					r.lost.Store(true)
					r.engine.MarkSamplingFailure(v.Request, true)
				}
				select {
				case <-time.After(time.Second):
				case <-r.stop:
					return
				}
			}
		}
	}
}
func (r *UpstreamCacheGuardRunner) retryEnqueue(v upstreamcacheguard.Evidence) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := r.store.Enqueue(ctx, v); err != nil {
		r.available.Store(false)
		r.pendingFailure.Store(true)
		r.engine.MarkSamplingFailure(v.Request, false)
		return false
	}
	r.retrying.Add(-1)
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return true
}
func (r *UpstreamCacheGuardRunner) processPending() {
	if r.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	pending, err := r.store.ClaimPending(ctx, r.owner, 32, 10*time.Second)
	cancel()
	if err != nil {
		r.available.Store(false)
		r.pendingFailure.Store(true)
		return
	}
	// Claim order is preserved within each account, including across Key changes.
	// Only this consumer dispatches batches; the next batch waits for all workers.
	byAccount := make(map[int64][]upstreamcacheguard.Evidence)
	for _, v := range pending {
		byAccount[v.Request.AccountID] = append(byAccount[v.Request.AccountID], v)
	}
	jobs := make(chan []upstreamcacheguard.Evidence)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(byAccount)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for account := range jobs {
				for _, v := range account {
					ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
					err := r.engine.Complete(ctx, v)
					cancel()
					if err != nil {
						r.available.Store(false)
						r.pendingFailure.Store(true)
						r.engine.MarkSamplingFailure(v.Request, false)
						break
					}
					r.engine.ClearSamplingFailure(v.Request)
				}
			}
		}()
	}
	for _, account := range byAccount {
		jobs <- account
	}
	close(jobs)
	workers.Wait()
	if len(pending) == 32 {
		// Coalesced completion notifications cannot strand the rest of a burst
		// behind one batch per timer tick. Keep draining the durable outbox.
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	if r.pendingFailure.Load() {
		ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
		pending, err := r.store.HasPending(ctx)
		cancel()
		if err == nil && !pending && r.retrying.Load() == 0 {
			r.pendingFailure.Store(false)
		}
	}
}
func (r *UpstreamCacheGuardRunner) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.closed.Store(true)
		r.available.Store(false)
		installedCacheGuardRunner.CompareAndSwap(r, nil)
		close(r.stop)
		<-r.done
	})
}

func (r *UpstreamCacheGuardRunner) logicalRequest(ctx context.Context, groupID *int64, session, model, protocol string) (upstreamcacheguard.Request, bool) {
	identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if stable, exists := ctx.Value(cacheGuardLogicalSessionKey{}).(string); exists && stable != "" {
		session = stable
	}
	if !ok || identity.APIKeyID <= 0 || identity.RequestID == "" || groupID == nil || *groupID <= 0 || session == "" || model == "" || len(r.secret) == 0 {
		return upstreamcacheguard.Request{}, false
	}
	h := hmac.New(sha256.New, r.secret)
	_, _ = fmt.Fprintf(h, "upstream-cache-guard:v1:%d:%d:%s", identity.APIKeyID, *groupID, session)
	return upstreamcacheguard.Request{ID: identity.RequestID, LogicalSessionDigest: hex.EncodeToString(h.Sum(nil)), APIKeyID: identity.APIKeyID, GroupID: *groupID, Model: model, Protocol: protocol, StartedAt: identity.StartedAt}, true
}

func BeginUpstreamCacheGuardRequest(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash, model, protocol string, account *Account) *UpstreamCacheGuardAdmission {
	admission, _ := beginUpstreamCacheGuardRequest(ctx, apiKeyID, groupID, sessionHash, model, protocol, account)
	return admission
}

func beginUpstreamCacheGuardRequest(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash, model, protocol string, account *Account) (*UpstreamCacheGuardAdmission, error) {
	strict, _ := ctx.Value(cacheGuardProtectedSelectionKey{}).(bool)
	if protectionOrdinaryFallback(ctx) {
		if strict {
			return nil, protectionBudgetFallbackError()
		}
		return nil, nil
	}
	r := currentUpstreamCacheGuardRunner()
	if r == nil || r.closed.Load() || account == nil || !account.IsUpstreamBound() || account.UpstreamKeyID == nil || groupID == nil {
		return nil, nil
	}
	at := time.Now().UTC()
	snapshot := r.engine.CachedMetadataAt(*groupID, at)
	if !snapshot.Available {
		return nil, nil
	}
	if !r.protectionScopeSelected(*groupID, account) {
		return nil, nil
	}
	identity, authorized := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if !authorized || identity.APIKeyID != apiKeyID {
		return nil, nil
	}
	accountSnapshot, ok := r.engine.CachedAccountForGroupAt(*groupID, account.ID, *account.UpstreamKeyID, at)
	if !ok {
		return nil, nil
	}
	req, ok := r.logicalRequest(ctx, groupID, sessionHash, model, protocol)
	if !ok {
		return nil, nil
	}
	req.AccountID = account.ID
	req.UpstreamKeyID = *account.UpstreamKeyID
	req.StartedAt = time.Now().UTC()
	// During the bounded stale window only continue an already classified
	// session. This prevents a refresh outage from creating new trial evidence
	// or making a migration decision from old observations.
	stale := !snapshot.Fresh || !accountSnapshot.Fresh
	if stale {
		session, err := r.engine.SessionReadAtWithDatabaseContext(ctx, req, at, cacheGuardDatabaseContext)
		if err != nil {
			if strict && protectionOrdinaryFallback(ctx) && ctx.Err() == nil {
				return nil, protectionBudgetFallbackError()
			}
			return nil, nil
		}
		if !session.Known || session.PreferredAccountID != account.ID {
			return nil, nil
		}
	}
	dbCtx, cancel := cacheGuardDatabaseContext(ctx)
	defer cancel()
	var a upstreamcacheguard.Admission
	var err error
	if stale {
		a, err = r.engine.AdmitExisting(dbCtx, req)
	} else {
		a, err = r.engine.Admit(dbCtx, req)
	}
	if err != nil {
		r.available.Store(false)
		if (dbCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
			if identity.budget != nil {
				identity.budget.ordinary.Store(true)
			}
			if strict {
				return nil, protectionBudgetFallbackError()
			}
		}
		return nil, nil
	}
	if !a.Sample {
		return nil, nil
	}
	return &UpstreamCacheGuardAdmission{Request: req, Admission: a, AccountPlatform: account.Platform}, nil
}
func ObserveUpstreamCacheGuardResult(admission *UpstreamCacheGuardAdmission, evidence *UpstreamCacheUsageEvidence, success bool, completedAt time.Time) {
	r := currentUpstreamCacheGuardRunner()
	if r == nil || admission == nil || r.closed.Load() {
		return
	}
	v := upstreamcacheguard.Evidence{ID: fmt.Sprintf("%s:%d:%d", admission.Request.ID, admission.Request.AccountID, admission.Request.StartedAt.UnixNano()), Request: admission.Request, Admission: admission.Admission, Success: success, CompletedAt: completedAt}
	v.ProtectionPaused = !r.protectionScopeSelected(admission.Request.GroupID, &Account{Platform: admission.AccountPlatform})
	if evidence != nil {
		v.RawUsage = *evidence
	}
	// Durable delivery happens before returning from the completion observer.
	// The queue only retries database outages; durable admissions make any
	// discarded completion visible after a crash, so incomplete data fails open.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := r.store.Enqueue(ctx, v)
	cancel()
	if err == nil {
		select {
		case r.wake <- struct{}{}:
		default:
		}
		return
	}
	r.available.Store(false)
	r.pendingFailure.Store(true)
	r.engine.MarkSamplingFailure(admission.Request, false)
	r.retrying.Add(1)
	select {
	case r.queue <- v:
	default:
		r.lost.Store(true)
		r.engine.MarkSamplingFailure(admission.Request, true)
	}
}
