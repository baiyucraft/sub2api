package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

// cacheGuardSkipReason is an aggregate diagnostic label for the fail-open
// exits before an admission is created. Keep these labels free of request
// content, credentials and session identifiers so they are safe to emit in
// ordinary service logs.
type cacheGuardSkipReason uint8

const (
	cacheGuardSkipRunnerUnavailable cacheGuardSkipReason = iota
	cacheGuardSkipAccountUnavailable
	cacheGuardSkipSnapshotUnavailable
	cacheGuardSkipScopeNotSelected
	cacheGuardSkipIdentityUnauthorized
	cacheGuardSkipAccountSnapshotUnavailable
	cacheGuardSkipLogicalRequestInvalid
	cacheGuardSkipStaleSessionUnavailable
	cacheGuardSkipStaleSessionUnbound
	cacheGuardSkipAdmissionError
	cacheGuardSkipAdmissionNotSample
	cacheGuardSkipOrdinaryFallback
	cacheGuardSkipReasonCount
)

func (r cacheGuardSkipReason) String() string {
	switch r {
	case cacheGuardSkipRunnerUnavailable:
		return "runner_unavailable"
	case cacheGuardSkipAccountUnavailable:
		return "account_unavailable"
	case cacheGuardSkipSnapshotUnavailable:
		return "snapshot_unavailable"
	case cacheGuardSkipScopeNotSelected:
		return "scope_not_selected"
	case cacheGuardSkipIdentityUnauthorized:
		return "identity_unauthorized"
	case cacheGuardSkipAccountSnapshotUnavailable:
		return "account_snapshot_unavailable"
	case cacheGuardSkipLogicalRequestInvalid:
		return "logical_request_invalid"
	case cacheGuardSkipStaleSessionUnavailable:
		return "stale_session_unavailable"
	case cacheGuardSkipStaleSessionUnbound:
		return "stale_session_unbound"
	case cacheGuardSkipAdmissionError:
		return "admission_error"
	case cacheGuardSkipAdmissionNotSample:
		return "admission_not_sample"
	case cacheGuardSkipOrdinaryFallback:
		return "ordinary_fallback"
	default:
		return "unknown"
	}
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
	pendingDelayed        atomic.Bool
	pendingUnknown        atomic.Bool
	policyMu              sync.RWMutex
	policyResolver        GroupSchedulingProtectionPolicyResolver
	appliedPolicy         *GroupSchedulingProtectionPolicySnapshot
	appliedGlobal         string
	appliedPolicies       map[int64]UpstreamCacheGuardSettings
	appliedOrdinary       bool
	ordinaryWritePending  bool
	ordinaryBlockedPolicy *GroupSchedulingProtectionPolicySnapshot
	localSettingsRevision atomic.Uint64
	skipCounts            [cacheGuardSkipReasonCount]atomic.Uint64
	diagnostics           cacheGuardDiagnostics
}

func (r *UpstreamCacheGuardRunner) recordAdmissionSkip(reason cacheGuardSkipReason, groupID, accountID int64, keyID ...int64) {
	if r == nil || reason >= cacheGuardSkipReasonCount {
		return
	}
	r.skipCounts[reason].Add(1)
	// Keep the hot path effectively silent while retaining a low-cost, useful
	// counter log per account/Key. A process-global counter can hide an affected
	// account's first failure behind another account's accumulated failures.
	var upstreamKeyID int64
	if len(keyID) > 0 {
		upstreamKeyID = keyID[0]
	}
	r.diagnostics.record("skipped_"+reason.String(), groupID, accountID, upstreamKeyID)
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
	r.engine.SetOrdinaryRecoveryIdentityGeneration(r.ordinaryRecoveryIdentityGeneration())
	if policies != nil {
		r.SetProtectionPolicyResolver(policies)
	}
	installedCacheGuardRunner.Store(r)
	go r.run()
	return r
}
func (r *UpstreamCacheGuardRunner) applySettings(s UpstreamCacheGuardSettings) {
	if r != nil {
		r.policyMu.Lock()
		defer r.policyMu.Unlock()
		r.localSettingsRevision.Add(1)
		// A save advances the durable revision even if its value returns to the
		// previous configuration. Do not re-enable an older matching snapshot.
		r.ordinaryWritePending = true
		r.ordinaryBlockedPolicy = r.appliedPolicy
		_ = r.applyRecoveryConfigurationLocked(s)
	}
}

func (r *UpstreamCacheGuardRunner) applyRecoverySettings(s UpstreamCacheGuardSettings, readRevision uint64) error {
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	if readRevision != r.localSettingsRevision.Load() {
		return nil // A direct save superseded this earlier database read.
	}
	return r.applyRecoveryConfigurationLocked(s)
}

func (r *UpstreamCacheGuardRunner) SetProtectionPolicyResolver(resolver GroupSchedulingProtectionPolicyResolver) {
	if r == nil {
		return
	}
	r.policyMu.Lock()
	r.policyResolver = resolver
	r.appliedPolicy = nil
	r.appliedGlobal = ""
	r.appliedPolicies = nil
	r.appliedOrdinary = false
	r.ordinaryWritePending = false
	r.ordinaryBlockedPolicy = nil
	r.policyMu.Unlock()
	r.applyPolicySnapshot()
}

func (r *UpstreamCacheGuardRunner) applyPolicySnapshot() {
	if r == nil || r.engine == nil {
		return
	}
	// Resolver replacement and publication are serialized so an older refresh
	// cannot install its policies after a new resolver has been selected.
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	_ = r.applyRecoveryConfigurationLocked(r.engine.Settings())
}

func (r *UpstreamCacheGuardRunner) applyRecoveryConfigurationLocked(global UpstreamCacheGuardSettings) error {
	resolver := r.policyResolver
	identity := cacheGuardConfigurationIdentity(global)
	if provider, ok := resolver.(protectionPolicySnapshotProvider); ok {
		snapshot := provider.ProtectionPoliciesSnapshot()
		if r.ordinaryWritePending && snapshot != nil && snapshot != r.ordinaryBlockedPolicy {
			r.ordinaryWritePending = false
			r.ordinaryBlockedPolicy = nil
		}
		ordinary := !r.ordinaryWritePending && snapshot != nil && snapshot.globalCache != nil && cacheGuardConfigurationIdentity(*snapshot.globalCache) == identity
		if snapshot == r.appliedPolicy && identity == r.appliedGlobal && ordinary == r.appliedOrdinary {
			return nil
		}
		var values map[int64]UpstreamCacheGuardSettings
		var generations map[string]string
		if snapshot != nil {
			values = snapshot.cachePolicies
			if values == nil {
				// Compatibility for lightweight providers with preloaded policies.
				// Construct only once per immutable snapshot, never on every tick.
				if snapshot == r.appliedPolicy {
					values = r.appliedPolicies
				} else {
					values = make(map[int64]UpstreamCacheGuardSettings, len(snapshot.policies))
					for id, policy := range snapshot.policies {
						values[id] = policy.Cache
					}
				}
			}
			if ordinary {
				generations = snapshot.ordinaryRecoveryGenerations
			}
		}
		// A directly updated global setting can arrive before its corresponding
		// resolved-policy snapshot. Publish normal settings promptly, but pause
		// ordinary recovery until all three values belong to the same bundle.
		if err := r.engine.ApplyRecoveryConfiguration(global, values, generations); err != nil {
			return err
		}
		r.appliedPolicy, r.appliedGlobal = snapshot, identity
		r.appliedPolicies, r.appliedOrdinary = values, ordinary
		return nil
	}
	// Legacy providers do not publish a durable generation, so ordinary recovery
	// stays disabled. Preserve manually installed policy maps when unavailable.
	r.engine.SetOrdinaryRecoveryPolicyGenerations(nil)
	if provider, ok := resolver.(interface {
		CachePolicies() map[int64]UpstreamCacheGuardSettings
	}); ok {
		return r.engine.ApplyRecoveryConfiguration(global, provider.CachePolicies(), nil)
	}
	return r.engine.ApplySettings(global)
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
	readRevision := r.localSettingsRevision.Load()
	s, err := r.settings.GetUpstreamCacheGuardSettings(ctx)
	if err != nil {
		r.available.Store(false)
		return
	}
	if err = r.applyRecoverySettings(s, readRevision); err != nil {
		r.available.Store(false)
		return
	}
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
					r.diagnostics.record("completion_queue_full", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
					r.retrying.Add(-1)
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
	r.diagnostics.record("delivery_retry_acknowledged", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
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
					inserted, err := r.engine.CompleteWithResult(ctx, v)
					cancel()
					if err != nil {
						r.diagnostics.record("processing_failed", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
						r.available.Store(false)
						r.pendingFailure.Store(true)
						r.engine.MarkSamplingFailure(v.Request, false)
						break
					}
					if inserted {
						outcome := "processed_excluded"
						if v.ValidSample() {
							outcome = "processed_valid"
						}
						r.diagnostics.record(outcome, v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
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
	stable, reliable := ctx.Value(cacheGuardLogicalSessionKey{}).(string)
	if reliable && stable != "" {
		session = stable
	}
	if !ok || identity.APIKeyID <= 0 || identity.RequestID == "" || groupID == nil || *groupID <= 0 || session == "" || model == "" || len(r.secret) == 0 {
		return upstreamcacheguard.Request{}, false
	}
	h := hmac.New(sha256.New, r.secret)
	_, _ = fmt.Fprintf(h, "upstream-cache-guard:v1:%d:%d:%s", identity.APIKeyID, *groupID, session)
	request := upstreamcacheguard.Request{ID: identity.RequestID, LogicalSessionDigest: hex.EncodeToString(h.Sum(nil)), APIKeyID: identity.APIKeyID, GroupID: *groupID, Model: model, Protocol: protocol, StartedAt: identity.StartedAt}
	if reliable && stable != "" {
		ordinary := hmac.New(sha256.New, r.secret)
		_, _ = fmt.Fprintf(ordinary, "upstream-cache-guard:ordinary-session:v1:%d:%s", identity.APIKeyID, stable)
		request.OrdinaryRecoverySessionID = hex.EncodeToString(ordinary.Sum(nil))
		request.OrdinaryRecoveryIdentityGeneration = r.ordinaryRecoveryIdentityGeneration()
	}
	return request, true
}

func (r *UpstreamCacheGuardRunner) ordinaryRecoveryIdentityGeneration() string {
	if r == nil || len(r.secret) == 0 {
		return ""
	}
	h := hmac.New(sha256.New, r.secret)
	_, _ = h.Write([]byte("upstream-cache-guard:ordinary-identity-generation:v1"))
	return hex.EncodeToString(h.Sum(nil))
}

func BeginUpstreamCacheGuardRequest(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash, model, protocol string, account *Account) *UpstreamCacheGuardAdmission {
	admission, _ := beginUpstreamCacheGuardRequest(ctx, apiKeyID, groupID, sessionHash, model, protocol, account)
	return admission
}

func beginUpstreamCacheGuardRequest(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash, model, protocol string, account *Account) (*UpstreamCacheGuardAdmission, error) {
	if identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity); ok && identity.budget != nil {
		identity.budget.mu.Lock()
		if groupID != nil {
			identity.budget.groupID = *groupID
		}
		if account != nil {
			identity.budget.accountID = account.ID
			identity.budget.upstreamKeyID = accountUpstreamKeyID(account)
		}
		identity.budget.mu.Unlock()
	}
	strict, _ := ctx.Value(cacheGuardProtectedSelectionKey{}).(bool)
	identity, authorized := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	r := currentUpstreamCacheGuardRunner()
	if r == nil {
		return nil, nil
	}
	if r.closed.Load() {
		r.recordAdmissionSkip(cacheGuardSkipRunnerUnavailable, 0, 0)
		return nil, nil
	}
	// Joint TTFT selections also carry strict. With every cache policy disabled,
	// there is no cache admission to require or database budget to consume.
	if !r.engine.AnyPolicyActive() {
		return nil, nil
	}
	if account == nil || !account.IsUpstreamBound() || account.UpstreamKeyID == nil || groupID == nil {
		var aid int64
		if account != nil {
			aid = account.ID
		}
		r.recordAdmissionSkip(cacheGuardSkipAccountUnavailable, 0, aid, accountUpstreamKeyID(account))
		return nil, nil
	}
	if !r.protectionScopeSelected(*groupID, account) {
		r.recordAdmissionSkip(cacheGuardSkipScopeNotSelected, *groupID, account.ID, *account.UpstreamKeyID)
		return nil, nil
	}
	if !authorized || identity.APIKeyID != apiKeyID {
		r.recordAdmissionSkip(cacheGuardSkipIdentityUnauthorized, *groupID, account.ID, *account.UpstreamKeyID)
		return nil, nil
	}
	settings := r.engine.ResolvedSettings(*groupID)
	cacheProtectionRequired := settings.Mode == upstreamcacheguard.Protect && settings.ContainsGroup(*groupID)
	if identity.budget != nil && identity.budget.ordinary.Load() {
		r.recordAdmissionSkip(cacheGuardSkipOrdinaryFallback, *groupID, account.ID, *account.UpstreamKeyID)
		if strict && cacheProtectionRequired {
			return nil, protectionBudgetFallbackError()
		}
		return nil, nil
	}
	req, ok := r.logicalRequest(ctx, groupID, sessionHash, model, protocol)
	if !ok {
		r.recordAdmissionSkip(cacheGuardSkipLogicalRequestInvalid, *groupID, account.ID, *account.UpstreamKeyID)
		return nil, nil
	}
	req.AccountID = account.ID
	req.UpstreamKeyID = *account.UpstreamKeyID
	req.StartedAt = time.Now().UTC()
	view := r.engine.CachedAdmissionReadView(req)
	if view.Consistent {
		cacheProtectionRequired = view.Settings.Mode == upstreamcacheguard.Protect && view.Settings.ContainsGroup(*groupID)
	}
	captureOnly := !cacheProtectionRequired || !view.Consistent || !view.Metadata.Available || !view.Account.Available || (identity.budget != nil && identity.budget.selectionFallback.Load())
	stale := !view.Metadata.Fresh || !view.Account.Fresh
	if !captureOnly && stale {
		loaded, err := r.engine.SessionReadViewWithDatabaseContext(ctx, req, cacheGuardDatabaseContext)
		if err != nil {
			if ctx.Err() != nil || (identity.budget != nil && identity.budget.ordinary.Load()) {
				r.recordAdmissionSkip(cacheGuardSkipStaleSessionUnavailable, *groupID, account.ID, *account.UpstreamKeyID)
				if strict && ctx.Err() == nil {
					return nil, protectionBudgetFallbackError()
				}
				return nil, nil
			}
			captureOnly = true
		} else {
			view = loaded
			captureOnly = !view.Consistent || !view.Metadata.Available || !view.Account.Available || !view.Session.Known || view.Session.PreferredAccountID != account.ID
			stale = !view.Metadata.Fresh || !view.Account.Fresh
		}
	}
	// Missing protection data does not make the actual request unobservable.
	// Capture-only freezes its durable route without allocating a recovery trial.
	if r.engine.PolicyVersion() != view.PolicyVersion {
		captureOnly = true
	}
	if strict && !protectionPolicyEvaluationCurrent(ctx) {
		return nil, schedulingProtectionScopeChangedError()
	}
	selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection)
	newDecision := cacheGuardAdmittedMigration(ctx, account.ID) || (selection != nil && (selection.trial != nil || selection.newAllocation))
	if strict && cacheProtectionRequired && (captureOnly || (stale && newDecision)) {
		r.diagnostics.record("selection_snapshot_fallback", req.GroupID, req.AccountID, req.UpstreamKeyID)
		return nil, protectionSnapshotFallbackError(ctx)
	}
	dbCtx, cancel := cacheGuardDatabaseContextForStage(ctx, "admission")
	var a upstreamcacheguard.Admission
	var err error
	if captureOnly {
		a, err = r.engine.CaptureOnly(dbCtx, req)
	} else if stale {
		a, err = r.engine.AdmitExisting(dbCtx, req)
	} else {
		a, err = r.engine.Admit(dbCtx, req)
	}
	deadlineExceeded := dbCtx.Err() == context.DeadlineExceeded
	cancel()
	if err != nil {
		r.recordAdmissionSkip(cacheGuardSkipAdmissionError, *groupID, account.ID, *account.UpstreamKeyID)
		r.available.Store(false)
		if (deadlineExceeded || errors.Is(err, context.DeadlineExceeded)) && ctx.Err() == nil {
			if identity.budget != nil {
				identity.budget.ordinary.Store(true)
			}
			if strict && cacheProtectionRequired {
				return nil, protectionBudgetFallbackError()
			}
		}
		if strict && cacheProtectionRequired && ctx.Err() == nil {
			return nil, protectionSnapshotFallbackError(ctx)
		}
		return nil, nil
	}
	if !a.Sample {
		r.recordAdmissionSkip(cacheGuardSkipAdmissionNotSample, *groupID, account.ID, *account.UpstreamKeyID)
		return nil, nil
	}
	classification := "admitted_cold"
	if a.WarmAtStart {
		classification = "admitted_warm"
	}
	r.diagnostics.record(classification, req.GroupID, req.AccountID, req.UpstreamKeyID)
	if a.CaptureOnly {
		r.diagnostics.record("admitted_capture_only", req.GroupID, req.AccountID, req.UpstreamKeyID)
	}
	return &UpstreamCacheGuardAdmission{Request: req, Admission: a, AccountPlatform: account.Platform}, nil
}
func ObserveUpstreamCacheGuardResult(admission *UpstreamCacheGuardAdmission, evidence *UpstreamCacheUsageEvidence, success bool, completedAt time.Time) {
	observeUpstreamCacheGuardResult(admission, evidence, success, "", completedAt)
}

func observeUpstreamCacheGuardResult(admission *UpstreamCacheGuardAdmission, evidence *UpstreamCacheUsageEvidence, success bool, reason string, completedAt time.Time) {
	r := currentUpstreamCacheGuardRunner()
	if r == nil || admission == nil || r.closed.Load() {
		return
	}
	v := upstreamcacheguard.Evidence{ID: fmt.Sprintf("%s:%d:%d", admission.Request.ID, admission.Request.AccountID, admission.Request.StartedAt.UnixNano()), Request: admission.Request, Admission: admission.Admission, Success: success, CompletedAt: completedAt}
	v.ProtectionPaused = !r.protectionScopeSelected(admission.Request.GroupID, &Account{Platform: admission.AccountPlatform})
	if evidence != nil {
		v.RawUsage = *evidence
	}
	outcome := "completion_valid_candidate"
	switch {
	case !success:
		outcome = "completion_unsuccessful"
	case !v.Admission.Sample:
		outcome = "completion_not_sample"
	case !v.Admission.WarmAtStart:
		outcome = "completion_cold"
	case !v.RawUsage.Known():
		outcome = "completion_usage_unknown"
	}
	if reason != "" {
		outcome = "completion_" + reason
	}
	v.CompletionReason = outcome
	r.diagnostics.record(outcome, v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
	// Durable delivery happens before returning from the completion observer.
	// The queue only retries database outages; durable admissions make any
	// discarded completion visible after a crash, so incomplete data fails open.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err := r.store.Enqueue(ctx, v)
	cancel()
	if err == nil {
		r.diagnostics.record("delivery_acknowledged", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
		select {
		case r.wake <- struct{}{}:
		default:
		}
		return
	}
	r.diagnostics.record("enqueue_failed", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
	r.available.Store(false)
	r.pendingFailure.Store(true)
	r.engine.MarkSamplingFailure(admission.Request, false)
	r.retrying.Add(1)
	select {
	case r.queue <- v:
	default:
		r.diagnostics.record("completion_queue_full", v.Request.GroupID, v.Request.AccountID, v.Request.UpstreamKeyID)
		r.retrying.Add(-1)
		r.lost.Store(true)
		r.engine.MarkSamplingFailure(admission.Request, true)
	}
}
