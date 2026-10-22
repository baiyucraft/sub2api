package upstreamcacheguard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const WarmLifetime = 15 * time.Minute
const ResidenceLifetime = 5 * time.Minute
const RefreshLifetime = 5 * time.Second
const UsableSnapshotLifetime = 30 * time.Second
const FactLifetime = 48 * time.Hour
const MinRecoverySessions = 3
const RecoveryLifetime = 10 * time.Minute
const MaxActiveSessions = 65536

type Engine struct {
	store       Store
	settings    atomic.Pointer[Settings]
	policies    atomic.Pointer[map[int64]Settings]
	refreshMu   sync.Mutex
	mu          sync.RWMutex
	groups      map[int64]Snapshot
	accounts    map[string]AccountSnapshot
	accountGaps map[string]bool
	// Failed completions are account-local. Queued attempts clear when their
	// durable completion succeeds; discarded attempts latch until restart.
	samplingFailures   map[string]map[string]struct{}
	sessions           map[string]SessionState
	sessionAccess      map[string]time.Time
	sessionVersions    map[string]int64
	refreshVersions    map[string]IdentityVersion
	nextBoundaries     map[string]time.Time
	lastReconciled     time.Time
	reconcileCursor    int
	refreshPolicyEpoch uint64
	accountPolicyEpoch map[string]uint64
	refreshCursor      int
	policyEpoch        atomic.Uint64
	activePolicy       atomic.Bool
	loadedAt           time.Time
	now                func() time.Time
	bornAt             time.Time
}

func NewEngine(store Store) *Engine {
	e := &Engine{store: store, groups: map[int64]Snapshot{}, accounts: map[string]AccountSnapshot{}, accountGaps: map[string]bool{}, samplingFailures: map[string]map[string]struct{}{}, sessions: map[string]SessionState{}, sessionAccess: map[string]time.Time{}, sessionVersions: map[string]int64{}, refreshVersions: map[string]IdentityVersion{}, nextBoundaries: map[string]time.Time{}, now: time.Now, bornAt: time.Now().UTC()}
	s := DefaultSettings()
	e.settings.Store(&s)
	e.updateActivePolicy()
	e.policyEpoch.Add(1)
	return e
}
func (e *Engine) ApplySettings(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Priority == "" {
		s.Priority = "cache_first"
	}
	s.ProtectionEvidenceExpiryMinutes = protectionEvidenceExpiryMinutes(s)
	s.GroupIDs = append([]int64(nil), s.GroupIDs...)
	sort.Slice(s.GroupIDs, func(i, j int) bool { return s.GroupIDs[i] < s.GroupIDs[j] })
	ids := s.GroupIDs[:0]
	for _, id := range s.GroupIDs {
		if len(ids) == 0 || ids[len(ids)-1] != id {
			ids = append(ids, id)
		}
	}
	s.GroupIDs = ids
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.settings.Load(); old != nil && sameSettings(*old, s) {
		return nil
	}
	e.settings.Store(&s)
	e.updateActivePolicy()
	e.policyEpoch.Add(1)
	return nil
}

func sameSettings(a, b Settings) bool {
	if len(a.GroupIDs) == 0 {
		a.GroupIDs = nil
	}
	if len(b.GroupIDs) == 0 {
		b.GroupIDs = nil
	}
	return reflect.DeepEqual(a, b)
}
func (e *Engine) Settings() Settings {
	s := *e.settings.Load()
	s.GroupIDs = append([]int64{}, s.GroupIDs...)
	return s
}
func (e *Engine) Close() {}

func (e *Engine) MarkSamplingFailure(r Request, lost bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	if e.samplingFailures[id] == nil {
		e.samplingFailures[id] = map[string]struct{}{}
	}
	if lost {
		// A single sentinel bounds discarded attempts to one entry per identity.
		delete(e.samplingFailures[id], admissionKey(r))
		e.samplingFailures[id][""] = struct{}{}
	} else {
		e.samplingFailures[id][admissionKey(r)] = struct{}{}
	}
}

func (e *Engine) ClearSamplingFailure(r Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := accountIdentity(r.AccountID, r.UpstreamKeyID)
	delete(e.samplingFailures[id], admissionKey(r))
	if len(e.samplingFailures[id]) == 0 {
		delete(e.samplingFailures, id)
	}
}

func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func SessionKey(r Request) string {
	return digest(fmt.Sprintf("%d:%d:%s", r.GroupID, r.APIKeyID, r.LogicalSessionDigest))
}

// AccountKey is retained only for reading legacy group-local rows.
func AccountKey(r Request) string { return fmt.Sprintf("%d", r.AccountID) }

// Warmness, unlike account quality, belongs to a concrete cache context.
func WarmRouteKey(r Request) string {
	return digest(fmt.Sprintf("%d:%d:%s:%s", r.AccountID, r.UpstreamKeyID, r.Model, r.Protocol))
}
func sessionCacheKey(group int64, key string) string { return fmt.Sprintf("%d:%s", group, key) }
func admissionKey(r Request) string {
	return digest(fmt.Sprintf("%s:%s:%d", r.ID, AccountKey(r), r.StartedAt.UnixNano()))
}
func admissionMatchesKey(a Admission, r Request) bool {
	if a.Mode == Disabled && !a.Sample {
		return true // Acknowledge old neutral outcomes without creating evidence.
	}
	if a.UpstreamKeyID > 0 {
		return a.UpstreamKeyID == r.UpstreamKeyID
	}
	// Pre-migration admissions already freeze a route digest containing the Key.
	return a.RouteKey == WarmRouteKey(r)
}
func routeWarm(route SessionRoute, at time.Time) bool {
	return !route.LastSuccessfulAt.IsZero() && !at.Before(route.LastSuccessfulAt) && at.Sub(route.LastSuccessfulAt) <= WarmLifetime
}

func planInTx(tx Tx, r Request, session *SessionState, s Settings) (TrialPlan, error) {
	p := TrialPlan{RequestID: r.ID, SessionKey: SessionKey(r), GroupID: r.GroupID}
	if !session.FirstSeenAt.IsZero() {
		p.NaturalNew = session.NaturalRequestID == r.ID
		p.TrialSlot = p.NaturalNew && session.NaturalTrialSlot && !session.NaturalTrialSuppressed
		p.TrialOrdinal = session.NaturalTrialOrdinal
		return p, nil
	}
	var group GroupState
	if _, err := tx.Read(GroupKind, "quota", &group); err != nil {
		return p, err
	}
	old := group.NaturalNewCount
	group.NaturalNewCount++
	p.NaturalNew = true
	p.TrialSlot = group.NaturalNewCount*int64(s.TrialPercent)/100 > old*int64(s.TrialPercent)/100
	if p.TrialSlot {
		p.TrialOrdinal = group.NaturalNewCount * int64(s.TrialPercent) / 100
	}
	session.Key = p.SessionKey
	session.FirstSeenAt = r.StartedAt.UTC()
	session.NaturalRequestID = r.ID
	session.NaturalTrialSlot = p.TrialSlot
	session.NaturalTrialOrdinal = p.TrialOrdinal
	if _, err := tx.Write(GroupKind, "quota", &group); err != nil {
		return p, err
	}
	return p, nil
}

// PlanTrial atomically reserves a group-shared natural-new slot before routing.
// The request must already have its stable gateway ID and HMAC logical identity.
func (e *Engine) PlanTrial(ctx context.Context, r Request) (TrialPlan, error) {
	s := e.ResolvedSettings(r.GroupID)
	p := TrialPlan{RequestID: r.ID, GroupID: r.GroupID}
	if s.Mode == Disabled || !s.ContainsGroup(r.GroupID) {
		return p, nil
	}
	v := r
	if v.AccountID == 0 {
		v.AccountID = 1
	}
	if v.UpstreamKeyID == 0 {
		v.UpstreamKeyID = 1
	}
	if err := validateRequest(v); err != nil {
		return p, err
	}
	if e.store == nil {
		return p, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := e.store.Transact(ctx, r.GroupID, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, SessionKey(r), &session); err != nil {
			return err
		}
		existing := !session.FirstSeenAt.IsZero()
		var err error
		p, err = planInTx(tx, r, &session, s)
		if err != nil || existing {
			// Existing identities only reconstruct their immutable natural plan.
			// Keep the session lock, without advancing its durable revision/age.
			return err
		}
		_, err = tx.Write(SessionKind, SessionKey(r), &session)
		return err
	})
	return p, err
}

func (e *Engine) Admit(ctx context.Context, r Request) (Admission, error) {
	return e.admit(ctx, r, false)
}

// CaptureOnly freezes physical warmness before sending an ordinary selection.
// It reserves a first session's natural ordinal, but cannot create a trial or
// change affinity from unavailable scheduling projections.
func (e *Engine) CaptureOnly(ctx context.Context, r Request) (Admission, error) {
	s := e.ResolvedSettings(r.GroupID)
	a := Admission{RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, StartedAt: r.StartedAt.UTC(), Mode: s.Mode}
	if !e.AnyPolicyActive() {
		return a, nil
	}
	if err := validateRequest(r); err != nil {
		return a, err
	}
	if e.store == nil {
		return a, ErrUnavailable
	}
	a.SessionKey = SessionKey(r)
	var saved SessionState
	var savedVersion int64
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := e.transactAccount(ctx, r, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, a.SessionKey, &session); err != nil {
			return err
		}
		old, exists, err := readAdmission(tx, &session, r)
		if err != nil {
			return err
		}
		if exists {
			if !admissionMatchesKey(old, r) {
				return errors.New("cache guard admission Key changed")
			}
			a = old
			return nil
		}
		if s.Mode != Disabled && s.ContainsGroup(r.GroupID) {
			plan, err := planInTx(tx, r, &session, s)
			if err != nil {
				return err
			}
			a.NaturalNew = plan.NaturalNew
			if plan.NaturalNew {
				session.NaturalTrialSuppressed = true
			}
		} else if session.FirstSeenAt.IsZero() {
			session.Key, session.FirstSeenAt = a.SessionKey, r.StartedAt.UTC()
		}
		a.CaptureOnly, a.Sample = true, true
		a.PolicySignature = PolicySignature(s)
		a.RouteKey = WarmRouteKey(r)
		a.WarmAtStart = routeWarm(session.Routes[a.RouteKey], r.StartedAt)
		if r.StartedAt.After(session.LastStartedAt) {
			session.LastStartedAt = r.StartedAt.UTC()
		}
		if err := checkAdmissionBound(tx, &session, r); err != nil {
			return err
		}
		if err := saveAdmission(tx, &session, r, a); err != nil {
			return err
		}
		saved = session
		savedVersion, err = tx.Write(SessionKind, a.SessionKey, &session)
		return err
	})
	if err != nil {
		return Admission{}, err
	}
	if saved.Key != "" {
		e.mu.Lock()
		e.cacheSessionLocked(sessionCacheKey(r.GroupID, saved.Key), saved, savedVersion, e.now().UTC())
		e.mu.Unlock()
	}
	return a, nil
}

// AdmitExisting records a continuation during a refresh outage. Recheck the
// durable identity inside the transaction; cached affinity cannot create a new
// session or recovery cohort if another worker has since removed/moved it.
func (e *Engine) AdmitExisting(ctx context.Context, r Request) (Admission, error) {
	return e.admit(ctx, r, true)
}

func (e *Engine) admit(ctx context.Context, r Request, existingOnly bool) (Admission, error) {
	s := e.ResolvedSettings(r.GroupID)
	a := Admission{RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID, UpstreamKeyID: r.UpstreamKeyID, StartedAt: r.StartedAt.UTC(), Mode: s.Mode}
	if !e.AnyPolicyActive() {
		return a, nil
	}
	if err := validateRequest(r); err != nil {
		return a, err
	}
	if e.store == nil {
		return a, ErrUnavailable
	}
	a.SessionKey = SessionKey(r)
	var saved SessionState
	var savedVersion int64
	executing := s.Mode != Disabled && s.ContainsGroup(r.GroupID)
	// The caller may already have a deadline; this never extends it.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	err := e.transactAccount(ctx, r, func(tx Tx) error {
		var session SessionState
		if _, err := tx.Read(SessionKind, a.SessionKey, &session); err != nil {
			return err
		}
		if existingOnly {
			route, ok := session.Routes[session.LastRouteKey]
			if session.FirstSeenAt.IsZero() || session.LastAccountID != r.AccountID || !ok || route.UpstreamKeyID != r.UpstreamKeyID {
				return ErrUnavailable
			}
		}
		old, exists, err := readAdmission(tx, &session, r)
		if err != nil {
			return err
		}
		if exists {
			if !admissionMatchesKey(old, r) {
				return errors.New("cache guard admission Key changed")
			}
			a = old
			return nil
		}
		var plan TrialPlan
		if executing && !existingOnly {
			plan, err = planInTx(tx, r, &session, s)
			if err != nil {
				return err
			}
		} else if session.FirstSeenAt.IsZero() {
			session.Key, session.FirstSeenAt = a.SessionKey, r.StartedAt.UTC()
		}
		account, version, err := e.readAccount(tx, r, s, r.StartedAt)
		if err != nil {
			return err
		}
		e.preparePolicies(&account)
		reused, err := e.reuseAccountProjection(tx, r, version, &account)
		if err != nil {
			return err
		}
		if !reused {
			facts, err := tx.(AccountTx).AccountFacts(r.AccountID, r.UpstreamKeyID, r.StartedAt.Add(-time.Duration(s.WindowMinutes)*time.Minute))
			if err != nil {
				return err
			}
			if err = e.recomputePolicies(&account, facts, r.StartedAt, false); err != nil {
				return err
			}
		}
		signature := PolicySignature(s)
		projection := account.Policies[signature]
		selected := &projection.State
		a.PolicySignature = signature
		account.GroupIDs = appendGroup(account.GroupIDs, r.GroupID)
		a.NaturalNew = plan.NaturalNew
		a.Sample = true
		a.RouteKey = WarmRouteKey(r)
		if session.Routes == nil {
			session.Routes = map[string]SessionRoute{}
		}
		a.WarmAtStart = routeWarm(session.Routes[WarmRouteKey(r)], r.StartedAt)
		a.ProtectedAtStart = executing && selected.Phase != "" && selected.Phase != Healthy && selected.Phase != Unknown
		if executing && plan.NaturalNew && session.NaturalAccountID == 0 && !session.NaturalTrialSuppressed {
			session.NaturalAccountID = r.AccountID
			if plan.TrialSlot && selected.Phase != Healthy && selected.Phase != Unknown && selected.Phase != "" && !r.StartedAt.Before(selected.CooldownUntil) {
				if selected.CohortID == "" {
					selected.CohortID = digest(fmt.Sprintf("%s:%s:%d:%s", AccountKey(r), signature, r.StartedAt.UnixNano(), session.Key))
					selected.FirstTrialAt = r.StartedAt.UTC()
					selected.TrialValidRequests = 0
					selected.TrialSessions = 0
					selected.TrialCachePercent = 0
				}
				selected.Phase = Trial
				a.Trial = true
				a.CohortID = selected.CohortID
				var group GroupState
				if _, err := tx.Read(GroupKind, "quota", &group); err != nil {
					return err
				}
				group.TrialCount++
				session.TrialAccountID = r.AccountID
				session.TrialModel = r.Model
				session.TrialProtocol = r.Protocol
				session.CohortID = a.CohortID
				session.PolicySignature = signature

				if _, err := tx.Write(GroupKind, "quota", &group); err != nil {
					return err
				}
			}
		} else if executing && session.TrialAccountID == r.AccountID && session.CohortID != "" && session.CohortID == selected.CohortID && (session.PolicySignature == signature || (session.PolicySignature == "" && signature == PolicySignature(e.Settings()))) {
			a.Trial = true
			a.CohortID = session.CohortID
		}
		if a.Trial {
			selected.TrialGroupIDs = appendGroup(selected.TrialGroupIDs, r.GroupID)
		}
		if executing {
			account.Policies[signature] = projection
		}
		if _, err := tx.(AccountTx).WriteAccount(r.AccountID, r.UpstreamKeyID, &account); err != nil {
			return err
		}
		if r.StartedAt.After(session.LastStartedAt) {
			session.LastStartedAt = r.StartedAt.UTC()
		}
		if err := checkAdmissionBound(tx, &session, r); err != nil {
			return err
		}
		if err := saveAdmission(tx, &session, r, a); err != nil {
			return err
		}
		saved = session
		savedVersion, err = tx.Write(SessionKind, a.SessionKey, &session)
		return err
	})
	if err != nil {
		return Admission{RequestID: r.ID, GroupID: r.GroupID, AccountID: r.AccountID, StartedAt: r.StartedAt.UTC(), Mode: s.Mode}, err
	}
	if saved.Key != "" {
		e.mu.Lock()
		e.cacheSessionLocked(sessionCacheKey(r.GroupID, saved.Key), saved, savedVersion, e.now().UTC())
		e.mu.Unlock()
	}
	return a, nil
}

func (e *Engine) Complete(ctx context.Context, v Evidence) error {
	_, err := e.CompleteWithResult(ctx, v)
	return err
}

// CompleteWithResult reports only newly consumed facts whose transaction
// committed. Durable retries and rolled-back insertions are not new samples.
func (e *Engine) CompleteWithResult(ctx context.Context, v Evidence) (bool, error) {
	inserted, err := e.complete(ctx, v)
	return err == nil && inserted, err
}

func (e *Engine) complete(ctx context.Context, v Evidence) (bool, error) {
	s := e.ResolvedSettings(v.Request.GroupID)
	if e.store == nil {
		return false, ErrUnavailable
	}
	if err := validateRequest(v.Request); err != nil {
		return false, err
	}
	if v.ID == "" || len(v.ID) > 200 || v.CompletedAt.IsZero() || v.CompletedAt.Before(v.Request.StartedAt) || v.Admission.SessionKey != SessionKey(v.Request) || v.Admission.RequestID != v.Request.ID || v.Admission.AccountID != v.Request.AccountID || v.Admission.GroupID != v.Request.GroupID || !admissionMatchesKey(v.Admission, v.Request) {
		return false, errors.New("cache guard evidence/admission mismatch")
	}
	now := e.now().UTC()
	if v.CompletedAt.After(now.Add(time.Minute)) {
		return false, errors.New("cache guard completion in future")
	}
	// Very late delivery is outside the deduplication/recomputation horizon.
	applyPolicy := !v.ProtectionPaused && v.Admission.Sample && e.AnyPolicyActive() && !v.CompletedAt.Before(now.Add(-FactLifetime))
	v.CompletedAt = v.CompletedAt.UTC()
	v.RawUsage.CompletedAt = v.CompletedAt
	v.RawUsage.Success = v.Success
	var inserted bool
	err := e.transactAccount(ctx, v.Request, func(tx Tx) error {
		var err error
		inserted, err = tx.InsertFact(v)
		if err != nil || !inserted {
			return err
		}
		var session SessionState
		if _, err = tx.Read(SessionKind, v.Admission.SessionKey, &session); err != nil {
			return err
		}
		// Rebuild from the immutable admission when recovering a missing typed row.
		if session.Routes == nil {
			session.Routes = map[string]SessionRoute{}
		}
		if session.FirstSeenAt.IsZero() {
			session.Key = v.Admission.SessionKey
			session.FirstSeenAt = v.Request.StartedAt
		}
		completed := v.Admission
		completed.Completed = true
		if err := saveAdmission(tx, &session, v.Request, completed); err != nil {
			return err
		}
		if !applyPolicy {
			_, err = tx.Write(SessionKind, v.Admission.SessionKey, &session)
			return err
		}
		routeKey := WarmRouteKey(v.Request)
		route := session.Routes[routeKey]
		signature := PolicySignature(s)
		if !v.Admission.CaptureOnly && route.PolicySignature != signature {
			// Warmness is a physical fact. Affinity and pending migration are
			// interpretations of that fact under one effective quality rule.
			route.HighCacheSeen, route.PendingLow, route.LowStreak = false, false, 0
			route.PolicySignature = signature
		}
		if v.Success && v.RawUsage.Known() && v.CompletedAt.After(route.LastSuccessfulAt) {
			if !v.Admission.CaptureOnly && session.LastAccountID > 0 && session.LastAccountID != v.Request.AccountID && !v.CompletedAt.Before(session.LastCompletedAt) {
				session.ResidenceUntil = v.CompletedAt.Add(ResidenceLifetime)
			}
			rate, _ := v.RawUsage.Rate()
			route.AccountID = v.Request.AccountID
			route.UpstreamKeyID = v.Request.UpstreamKeyID
			route.LastSuccessfulAt = v.CompletedAt
			route.LastCachePercent = rate
			if !v.Admission.CaptureOnly && rate >= s.ThresholdPercent {
				route.HighCacheSeen = true
			}
			if !v.Admission.CaptureOnly && !v.CompletedAt.Before(session.LastCompletedAt) {
				session.LastAccountID = v.Request.AccountID
				session.LastModel = v.Request.Model
				session.LastProtocol = v.Request.Protocol
				session.LastRouteKey = routeKey
				session.LastCompletedAt = v.CompletedAt
			}
		}
		account, _, err := e.readAccount(tx, v.Request, s, now)
		if err != nil {
			return err
		}
		e.preparePolicies(&account)
		account.GroupIDs = appendGroup(account.GroupIDs, v.Request.GroupID)
		reference := now
		if v.CompletedAt.After(reference) {
			reference = v.CompletedAt
		}
		historyWindow := time.Duration(s.WindowMinutes) * time.Minute
		if historyWindow < WarmLifetime {
			historyWindow = WarmLifetime
		}
		facts, err := tx.(AccountTx).AccountFacts(v.Request.AccountID, v.Request.UpstreamKeyID, reference.Add(-historyWindow))
		if err != nil {
			return err
		}
		if len(facts) > MaxFacts {
			return errors.New("cache guard bounded recomputation exceeded")
		}
		fresh := v.ValidSample() && !v.CompletedAt.Before(reference.Add(-time.Duration(s.WindowMinutes)*time.Minute))
		if err = e.recomputePoliciesWithRecovery(&account, facts, reference, fresh, !v.Admission.CaptureOnly); err != nil {
			return err
		}
		if v.Admission.CaptureOnly {
			session.Routes[routeKey] = route
			if _, err = tx.(AccountTx).WriteAccount(v.Request.AccountID, v.Request.UpstreamKeyID, &account); err != nil {
				return err
			}
			_, err = tx.Write(SessionKind, v.Admission.SessionKey, &session)
			return err
		}
		// Recompute the last two valid warm outcomes, including out-of-order facts.
		var own []Evidence
		for _, f := range facts {
			if f.Admission.SessionKey == v.Admission.SessionKey && WarmRouteKey(f.Request) == routeKey && !f.CompletedAt.Before(now.Add(-historyWindow)) {
				own = append(own, f)
				if f.ValidSample() && !f.Admission.CaptureOnly {
					if rate, ok := f.RawUsage.Rate(); ok && rate >= s.ThresholdPercent {
						route.HighCacheSeen = true
					}
				}
			}
		}
		// An unfinished intervening attempt is also a barrier. Otherwise two
		// quickly completed lows could be joined across a slower in-flight call.
		pending, err := pendingRouteAdmissions(tx, &session, v.Request)
		if err != nil {
			return err
		}
		for _, a := range pending {
			own = append(own, Evidence{ID: a.RequestID, Request: Request{StartedAt: a.StartedAt}})
		}
		sort.Slice(own, func(i, j int) bool {
			if own[i].Request.StartedAt.Equal(own[j].Request.StartedAt) {
				return own[i].ID < own[j].ID
			}
			return own[i].Request.StartedAt.Before(own[j].Request.StartedAt)
		})
		route.LowStreak = 0
		for i := len(own) - 1; i >= 0 && route.LowStreak < 2; i-- {
			if !own[i].ValidSample() || own[i].Admission.CaptureOnly || !own[i].Admission.ProtectedAtStart || (own[i].Admission.PolicySignature != "" && own[i].Admission.PolicySignature != PolicySignature(s)) {
				break
			}
			rate, _ := own[i].RawUsage.Rate()
			if rate >= s.ThresholdPercent {
				break
			}
			route.LowStreak++
		}
		route.PendingLow = route.LowStreak >= 2
		session.Routes[routeKey] = route
		if _, err = tx.(AccountTx).WriteAccount(v.Request.AccountID, v.Request.UpstreamKeyID, &account); err != nil {
			return err
		}
		_, err = tx.Write(SessionKind, v.Admission.SessionKey, &session)
		return err
	})
	return inserted, err
}

func protectionEvidenceAnchor(a *AccountState) time.Time {
	anchor := a.LastEvidenceAt
	if a.LastDegradedEvidenceAt != nil && !a.LastDegradedEvidenceAt.IsZero() {
		if a.LastDegradedEvidenceAt.After(anchor) {
			anchor = *a.LastDegradedEvidenceAt
		}
	}
	if a.DegradedAt.After(anchor) {
		anchor = a.DegradedAt
	}
	return anchor
}

// expireProtectionState turns an old protected decision into an explicitly
// unknown decision. Unknown is deliberately schedulable as a normal account:
// the cache guard has no current evidence and must not keep protecting forever.
func expireProtectionState(a *AccountState, s Settings, now time.Time) bool {
	if a == nil || (a.Phase != Degraded && a.Phase != Cooling && a.Phase != Trial) {
		return false
	}
	anchor := protectionEvidenceAnchor(a)
	if anchor.IsZero() || !now.After(anchor.Add(time.Duration(protectionEvidenceExpiryMinutes(s))*time.Minute)) {
		return false
	}
	a.Phase = Unknown
	a.CohortID = ""
	a.FirstTrialAt = time.Time{}
	a.CooldownUntil = time.Time{}
	a.DegradedAt = time.Time{}
	a.TrialValidRequests = 0
	a.TrialSessions = 0
	a.TrialCachePercent = 0
	a.TrialGroupIDs = nil
	// Freeze the latest valid evidence as the revalidation boundary. Old facts
	// cannot recreate protection, even if they arrived after initial degradation.
	boundary := anchor
	a.LastDegradedEvidenceAt = &boundary
	return true
}

// RecomputeAccount is deliberately a bounded replay of immutable raw facts;
// retries and late arrival cannot double count or reverse the evidence clock.
func RecomputeAccount(a *AccountState, facts []Evidence, s Settings, now time.Time, fresh bool) error {
	return recomputeAccountWithRecovery(a, facts, s, now, fresh, fresh)
}

func recomputeAccountWithRecovery(a *AccountState, facts []Evidence, s Settings, now time.Time, fresh, allowRecovery bool) error {
	var total, read, trialTotal, trialRead int64
	valid, trialValid := 0, 0
	trialSessions := map[string]struct{}{}
	cutoff := now.Add(-time.Duration(s.WindowMinutes) * time.Minute)
	var newest time.Time
	for _, f := range facts {
		if (a.AccountID > 0 && f.Request.AccountID > 0 && a.AccountID != f.Request.AccountID) || (a.UpstreamKeyID > 0 && f.Request.UpstreamKeyID > 0 && a.UpstreamKeyID != f.Request.UpstreamKeyID) || !f.ValidSample() || f.CompletedAt.Before(cutoff) || f.CompletedAt.After(now) || (!a.QualitySince.IsZero() && !f.CompletedAt.After(a.QualitySince)) {
			continue
		}
		if since := a.LegacyQualitySince[f.Request.GroupID]; !since.IsZero() && !f.CompletedAt.After(since) {
			continue
		}
		if f.CompletedAt.After(newest) {
			newest = f.CompletedAt
		}
		if math.MaxInt64-total < f.RawUsage.TotalInputTokens || math.MaxInt64-read < f.RawUsage.CacheReadTokens {
			return errors.New("cache guard token sum overflow")
		}
		total += f.RawUsage.TotalInputTokens
		read += f.RawUsage.CacheReadTokens
		valid++
		if !f.ProtectionPaused && !f.Admission.CaptureOnly && a.CohortID != "" && f.Admission.Trial && f.Admission.CohortID == a.CohortID && (f.Admission.PolicySignature == a.PolicySignature || f.Admission.PolicySignature == "") {
			trialTotal += f.RawUsage.TotalInputTokens
			trialRead += f.RawUsage.CacheReadTokens
			trialValid++
			trialSessions[sessionCacheKey(f.Request.GroupID, f.Admission.SessionKey)] = struct{}{}
		}
	}
	a.ValidRequests = valid
	a.TotalInputTokens = total
	a.CacheReadTokens = read
	a.CachePercent = 0
	if total > 0 {
		a.CachePercent = float64(read) / float64(total) * 100
	}
	a.TrialValidRequests = trialValid
	a.TrialSessions = len(trialSessions)
	a.TrialCachePercent = 0
	if trialTotal > 0 {
		a.TrialCachePercent = float64(trialRead) / float64(trialTotal) * 100
	}
	if now.After(a.UpdatedAt) {
		a.UpdatedAt = now
	}
	if a.Phase == "" {
		a.Phase = Healthy
	}
	// New evidence is a transaction fact, not a net count or completion-clock
	// change. Expiry can offset a late insertion without making it any less new.
	if newest.After(a.LastEvidenceAt) {
		a.LastEvidenceAt = newest
	}
	// Expiration is evaluated even on a non-fresh replay so a stale durable
	// phase cannot be resurrected merely because the refresh had no new facts.
	expireProtectionState(a, s, now)
	if !fresh {
		return nil
	}
	if a.Phase == Healthy && valid > s.MinValidRequests && a.CachePercent < s.ThresholdPercent {
		a.Phase = Cooling
		a.DegradedAt = now
		if !newest.IsZero() {
			x := newest
			a.LastDegradedEvidenceAt = &x
		}
		a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		a.CohortID = ""
		a.TrialGroupIDs = nil
		a.FirstTrialAt = time.Time{}
		return nil
	}
	if a.Phase == Unknown && valid > s.MinValidRequests && (a.LastDegradedEvidenceAt == nil || newest.After(*a.LastDegradedEvidenceAt)) {
		// A new, sufficiently sized window is enough to leave unknown. Only a
		// below-threshold window enters protection again.
		if a.CachePercent < s.ThresholdPercent {
			a.Phase = Cooling
			a.DegradedAt = now
			if !newest.IsZero() {
				x := newest
				a.LastDegradedEvidenceAt = &x
			}
			a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		} else if allowRecovery {
			a.Phase = Healthy
			a.LastDegradedEvidenceAt = nil
			a.QualitySince = now
		}
		return nil
	}
	// No-data and wall-clock passage alone never restore an account.
	if a.Phase != Trial || a.FirstTrialAt.IsZero() || now.Before(a.FirstTrialAt.Add(RecoveryLifetime)) || trialValid <= s.MinValidRequests || len(trialSessions) < MinRecoverySessions {
		return nil
	}
	if allowRecovery && a.TrialCachePercent >= s.RecoveryThresholdPercent {
		a.Phase = Healthy
		a.CohortID = ""
		a.TrialGroupIDs = nil
		a.FirstTrialAt = time.Time{}
		a.CooldownUntil = time.Time{}
		a.LastDegradedEvidenceAt = nil
		a.QualitySince = now
		a.ValidRequests = 0
		a.TotalInputTokens = 0
		a.CacheReadTokens = 0
		a.CachePercent = 0
		return nil
	}
	if a.TrialCachePercent < s.ThresholdPercent {
		a.Phase = Cooling
		if !newest.IsZero() {
			x := newest
			a.LastDegradedEvidenceAt = &x
		}
		a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		a.CohortID = ""
		a.TrialGroupIDs = nil
		a.FirstTrialAt = time.Time{}
	}
	return nil
}

func (e *Engine) Refresh(ctx context.Context) error {
	return e.refreshAccounts(ctx)
}

// Rebuild rehydrates typed durable runtime rows after process restart. Every
// completed fact and its versioned state were committed in the same transaction.
func (e *Engine) Rebuild(ctx context.Context) error { return e.Refresh(ctx) }
func (e *Engine) Cleanup(ctx context.Context) error {
	if e.store == nil {
		return ErrUnavailable
	}
	before := e.now().UTC().Add(-FactLifetime)
	if store, ok := e.store.(BatchCleanupStore); ok {
		for {
			n, err := store.CleanupBatch(ctx, before, 1000)
			if err != nil || n < 1000 {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
	return e.store.Cleanup(ctx, before)
}
func (e *Engine) CachedSnapshot(groupID int64) Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	at := e.now().UTC()
	s := e.ResolvedSettings(groupID)
	g := e.cachedMetadataAtLocked(groupID, s, at)
	for id, account := range e.accounts {
		account = e.accountSnapshotAtLocked(id, account, at)
		account = projectAccountSnapshot(account, s)
		if account.Available {
			g.Accounts = append(g.Accounts, account)
		}
	}
	sort.Slice(g.Accounts, func(i, j int) bool { return g.Accounts[i].AccountID < g.Accounts[j].AccountID })
	return g
}
func (e *Engine) Snapshot(_ context.Context, groupID int64) (Snapshot, error) {
	s := e.CachedSnapshot(groupID)
	if !s.Available {
		return s, ErrUnavailable
	}
	return s, nil
}
func (e *Engine) CachedSessionRead(r Request) SessionRead {
	e.mu.RLock()
	defer e.mu.RUnlock()
	at := e.now().UTC()
	s := e.ResolvedSettings(r.GroupID)
	return e.cachedSessionReadAtLocked(r, s, at, e.cachedMetadataAtLocked(r.GroupID, s, at))
}
func (e *Engine) CachedSessionReadAt(r Request, at time.Time) SessionRead {
	e.mu.RLock()
	defer e.mu.RUnlock()
	s := e.ResolvedSettings(r.GroupID)
	return e.cachedSessionReadAtLocked(r, s, at, e.cachedMetadataAtLocked(r.GroupID, s, at))
}

func (e *Engine) cachedSessionReadAtLocked(r Request, s Settings, at time.Time, snapshot Snapshot) SessionRead {
	if !snapshot.Available || !s.ContainsGroup(r.GroupID) {
		return SessionRead{}
	}
	x, ok := e.sessions[sessionCacheKey(r.GroupID, SessionKey(r))]
	if !ok {
		return SessionRead{}
	}
	read := SessionRead{Known: true, Fresh: snapshot.Fresh, PreferredAccountID: x.LastAccountID, ResidenceUntil: x.ResidenceUntil}
	accountUnknown := false
	if r.AccountID == x.LastAccountID && r.UpstreamKeyID > 0 {
		if route, ok := x.Routes[x.LastRouteKey]; !ok || route.UpstreamKeyID != r.UpstreamKeyID {
			return SessionRead{}
		}
	}
	if route, ok := x.Routes[x.LastRouteKey]; ok && x.LastAccountID > 0 {
		id := accountIdentity(x.LastAccountID, route.UpstreamKeyID)
		account := projectAccountSnapshot(e.accountSnapshotAtLocked(id, e.accounts[id], at), s)
		if !account.Available {
			// Retain the known identity so scheduling cannot mistake an expired
			// existing session for a natural new trial opportunity.
			read.Fresh = false
			return read
		}
		read.Fresh = read.Fresh && account.Fresh
		accountUnknown = account.Phase == Unknown
	}
	if x.LastModel != r.Model || x.LastProtocol != r.Protocol ||
		(r.AccountID > 0 && r.UpstreamKeyID > 0 && x.LastRouteKey != WarmRouteKey(r)) {
		return read
	}
	if route, ok := x.Routes[x.LastRouteKey]; ok {
		read.LastSuccessfulAt = route.LastSuccessfulAt
		read.WarmAtStart = routeWarm(route, r.StartedAt)
		samePolicy := route.PolicySignature == PolicySignature(s) || (route.PolicySignature == "" && PolicySignature(s) == PolicySignature(e.Settings()))
		read.PendingLow = samePolicy && route.PendingLow
		read.Protected = read.WarmAtStart && ((samePolicy && route.HighCacheSeen && !route.PendingLow) || r.StartedAt.Before(x.ResidenceUntil))
	}
	read.Trial = x.CohortID != "" && x.TrialAccountID == x.LastAccountID && x.TrialModel == r.Model && x.TrialProtocol == r.Protocol && (x.PolicySignature == PolicySignature(s) || (x.PolicySignature == "" && PolicySignature(s) == PolicySignature(e.Settings())))
	read.CohortID = x.CohortID
	if accountUnknown {
		// Keep physical warmness and durable identity, but never expose an old
		// recovery cohort or migration preference after account evidence expires.
		read.Trial, read.Protected, read.PendingLow = false, false, false
		read.CohortID = ""
	}
	if s.Mode != Protect {
		read.Protected = false
	}
	return read
}
func (e *Engine) SessionRead(ctx context.Context, r Request) (SessionRead, error) {
	view, err := e.SessionReadViewWithDatabaseContext(ctx, r, nil)
	return view.Session, err
}
func (e *Engine) SessionReadAt(ctx context.Context, r Request, at time.Time) (SessionRead, error) {
	return e.SessionReadAtWithDatabaseContext(ctx, r, at, nil)
}

// The factory runs only when a cache miss actually needs a durable read.
func (e *Engine) SessionReadAtWithDatabaseContext(ctx context.Context, r Request, at time.Time, databaseContext func(context.Context) (context.Context, context.CancelFunc)) (SessionRead, error) {
	if !e.CachedMetadataAt(r.GroupID, at).Available {
		return SessionRead{}, ErrUnavailable
	}
	key := sessionCacheKey(r.GroupID, SessionKey(r))
	e.mu.Lock()
	_, exists := e.sessions[key]
	if exists {
		e.sessionAccess[key] = e.now().UTC()
	}
	e.mu.Unlock()
	if !exists {
		if loader, ok := e.store.(SessionLoader); ok {
			if databaseContext != nil {
				var cancel context.CancelFunc
				ctx, cancel = databaseContext(ctx)
				defer cancel()
			}
			x, version, err := loader.LoadSession(ctx, r.GroupID, SessionKey(r))
			if err != nil {
				return SessionRead{}, err
			}
			if x != nil {
				e.mu.Lock()
				// A concurrent admission is newer than this cold read.
				if _, present := e.sessions[key]; !present {
					e.cacheSessionLocked(key, *x, version, e.now().UTC())
				}
				e.mu.Unlock()
			}
		}
	}
	return e.CachedSessionReadAt(r, at), nil
}

func (e *Engine) cacheSessionLocked(key string, session SessionState, version int64, at time.Time) {
	if current, exists := e.sessionVersions[key]; exists && current > version {
		return
	}
	if _, exists := e.sessions[key]; !exists && len(e.sessions) >= MaxActiveSessions {
		// Capacity eviction changes only residency; durable identity is retained
		// and must be cold loaded before any new-session decision.
		for evicted := range e.sessions {
			delete(e.sessions, evicted)
			delete(e.sessionVersions, evicted)
			delete(e.sessionAccess, evicted)
			break
		}
	}
	e.sessions[key] = session
	e.sessionVersions[key] = version
	e.sessionAccess[key] = at
}
func CandidatePreference(snapshot Snapshot, session SessionRead, c Candidate) int {
	if !snapshot.Available || snapshot.Mode != Protect {
		return 0
	}
	if session.Protected && session.PreferredAccountID == c.AccountID {
		return 2
	}
	for _, a := range snapshot.Accounts {
		if a.AccountID == c.AccountID && (a.Phase == Degraded || a.Phase == Cooling || a.Phase == Trial) {
			return -1
		}
	}
	return 0
}
func PreferCandidates(snapshot Snapshot, session SessionRead, candidates []Candidate) []Candidate {
	out := append([]Candidate(nil), candidates...)
	for i := range out {
		out[i].OriginalIndex = i
		out[i].Preference = CandidatePreference(snapshot, session, out[i])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Preference > out[j].Preference })
	return out
}
