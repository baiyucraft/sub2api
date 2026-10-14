package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type cacheGuardSchedulingKey struct{}
type cacheGuardSchedulingIdentity struct {
	APIKeyID  int64
	RequestID string
	StartedAt time.Time
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
	return context.WithValue(ctx, cacheGuardSchedulingKey{}, cacheGuardSchedulingIdentity{apiKeyID, newCacheGuardID(), time.Now().UTC()})
}

func newCacheGuardID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

type UpstreamCacheGuardAdmission struct {
	Request   upstreamcacheguard.Request
	Admission upstreamcacheguard.Admission
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
	policyMu       sync.RWMutex
	policyResolver GroupSchedulingProtectionPolicyResolver
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
	r.policyMu.Unlock()
	r.applyPolicySnapshot()
}

func (r *UpstreamCacheGuardRunner) applyPolicySnapshot() {
	r.policyMu.RLock()
	resolver := r.policyResolver
	r.policyMu.RUnlock()
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
	stalePending := true
	if err == nil {
		stalePending, err = r.store.HasPendingBefore(ctx, time.Now().UTC().Add(-upstreamcacheguard.RefreshLifetime))
	}
	r.available.Store(err == nil && !stalePending && r.retrying.Load() == 0 && !r.lost.Load() && !r.pendingFailure.Load() && !r.closed.Load())
}
func (r *UpstreamCacheGuardRunner) run() {
	defer close(r.done)
	r.refresh()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-r.stop:
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case v := <-r.queue:
					if !r.retryEnqueue(v) {
						r.engine.Close()
						return
					}
				default:
					r.engine.Close()
					return
				}
			}
			r.engine.Close()
			return
		case <-ticker.C:
			r.processPending()
			r.refresh()
		case <-cleanup.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = r.engine.Cleanup(ctx)
			cancel()
		case <-r.wake:
			r.processPending()
		case v := <-r.queue:
			if !r.retryEnqueue(v) {
				select {
				case r.queue <- v:
				case <-r.stop:
					return
				default:
					r.lost.Store(true)
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
	for _, v := range pending {
		ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
		err = r.engine.Complete(ctx, v)
		cancel()
		if err != nil {
			r.available.Store(false)
			r.pendingFailure.Store(true)
			return
		}
	}
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
	r := currentUpstreamCacheGuardRunner()
	if r == nil || !r.available.Load() || r.closed.Load() || account == nil || !account.IsUpstreamBound() || account.UpstreamKeyID == nil || groupID == nil || !r.engine.CachedSnapshot(*groupID).Available {
		return nil
	}
	identity, authorized := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if !authorized || identity.APIKeyID != apiKeyID {
		return nil
	}
	if _, ok := r.engine.CachedAccount(account.ID, *account.UpstreamKeyID); !ok {
		return nil
	}
	req, ok := r.logicalRequest(ctx, groupID, sessionHash, model, protocol)
	if !ok {
		return nil
	}
	req.AccountID = account.ID
	req.UpstreamKeyID = *account.UpstreamKeyID
	req.StartedAt = time.Now().UTC()
	a, err := r.engine.Admit(ctx, req)
	if err != nil {
		r.available.Store(false)
		return nil
	}
	if !a.Sample {
		return nil
	}
	return &UpstreamCacheGuardAdmission{Request: req, Admission: a}
}
func ObserveUpstreamCacheGuardResult(admission *UpstreamCacheGuardAdmission, evidence *UpstreamCacheUsageEvidence, success bool, completedAt time.Time) {
	r := currentUpstreamCacheGuardRunner()
	if r == nil || admission == nil || r.closed.Load() {
		return
	}
	v := upstreamcacheguard.Evidence{ID: fmt.Sprintf("%s:%d:%d", admission.Request.ID, admission.Request.AccountID, admission.Request.StartedAt.UnixNano()), Request: admission.Request, Admission: admission.Admission, Success: success, CompletedAt: completedAt}
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
	r.retrying.Add(1)
	select {
	case r.queue <- v:
	default:
		r.lost.Store(true)
	}
}
