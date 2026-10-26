package upstreamcacheguard

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"time"
)

// OrdinaryRecoveryState belongs to one account/Key/policy protection episode.
// It never changes the immutable trial classification or trial quota. Counters
// are a projection of bounded facts, not an increment-only recovery ledger.
type OrdinaryRecoveryState struct {
	Version            int       `json:"version"`
	RuleFingerprint    string    `json:"rule_fingerprint"`
	PolicyGeneration   string    `json:"policy_generation"`
	IdentityGeneration string    `json:"identity_generation"`
	BarrierAt          time.Time `json:"barrier_at"`
	QualifiedSince     time.Time `json:"qualified_since"`
	QualifiedUntil     time.Time `json:"qualified_until"`
	LastEvidenceAt     time.Time `json:"last_evidence_at"`
	LastVerifiedAt     time.Time `json:"last_verified_at"`
	ValidRequests      int       `json:"valid_requests"`
	Sessions           int       `json:"sessions"`
	TotalInputTokens   int64     `json:"total_input_tokens"`
	CacheReadTokens    int64     `json:"cache_read_tokens"`
	Reason             string    `json:"reason"`
}

type OrdinaryRecoveryView struct {
	ValidRequests       int        `json:"valid_requests"`
	RequiredRequests    int        `json:"required_requests"`
	Sessions            int        `json:"sessions"`
	RequiredSessions    int        `json:"required_sessions"`
	RatePercent         *float64   `json:"rate_percent"`
	RequiredRatePercent float64    `json:"required_rate_percent"`
	StartedAt           *time.Time `json:"started_at"`
	ReadyAt             *time.Time `json:"ready_at"`
	Reason              string     `json:"reason"`
}

func protectedPhase(phase Phase) bool { return phase == Cooling || phase == Trial || phase == Degraded }

// These generations are derived by the service from existing stable secrets
// and policy records. Installing identical generations is a no-op for progress.
func (e *Engine) SetOrdinaryRecoveryIdentityGeneration(generation string) {
	e.ordinaryConfigMu.Lock()
	defer e.ordinaryConfigMu.Unlock()
	if old := e.ordinaryIdentityGeneration.Load(); old != nil && *old == generation || old == nil && generation == "" {
		return
	}
	e.ordinaryIdentityGeneration.Store(&generation)
	e.policyEpoch.Add(1)
}

func (e *Engine) SetOrdinaryRecoveryPolicyGenerations(generations map[string]string) {
	copy := make(map[string]string, len(generations))
	for signature, generation := range generations {
		copy[signature] = generation
	}
	e.ordinaryConfigMu.Lock()
	defer e.ordinaryConfigMu.Unlock()
	if sameRecoveryGenerations(e.ordinaryPolicyGenerations.Load(), copy) {
		return
	}
	e.ordinaryPolicyGenerations.Store(&copy)
	e.policyEpoch.Add(1)
}

func sameRecoveryGenerations(old *map[string]string, current map[string]string) bool {
	if old == nil {
		return len(current) == 0
	}
	if len(*old) != len(current) {
		return false
	}
	for signature, generation := range current {
		if previous, exists := (*old)[signature]; !exists || previous != generation {
			return false
		}
	}
	return true
}

// ApplyRecoveryConfiguration publishes one complete policy snapshot. A
// completion cannot pair new settings with old recovery generations, and a
// no-op refresh neither invalidates snapshots nor restarts observation clocks.
func (e *Engine) ApplyRecoveryConfiguration(settings Settings, policies map[int64]Settings, generations map[string]string) error {
	settings, err := normalizeEngineSettings(settings)
	if err != nil {
		return err
	}
	policies, err = normalizePolicySettings(policies)
	if err != nil {
		return err
	}
	ownedGenerations := make(map[string]string, len(generations))
	for signature, generation := range generations {
		ownedGenerations[signature] = generation
	}
	e.ordinaryConfigMu.Lock()
	defer e.ordinaryConfigMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	oldPolicies := e.policies.Load()
	policiesUnchanged := oldPolicies == nil && len(policies) == 0 || oldPolicies != nil && reflect.DeepEqual(*oldPolicies, policies)
	if old := e.settings.Load(); old != nil && sameSettings(*old, settings) && policiesUnchanged && sameRecoveryGenerations(e.ordinaryPolicyGenerations.Load(), ownedGenerations) {
		return nil
	}
	e.settings.Store(&settings)
	e.policies.Store(&policies)
	e.ordinaryPolicyGenerations.Store(&ownedGenerations)
	e.updateActivePolicy()
	e.policyEpoch.Add(1)
	return nil
}

func (e *Engine) ordinaryGenerations(signature string) (string, string) {
	identity, policy := "", ""
	if v := e.ordinaryIdentityGeneration.Load(); v != nil {
		identity = *v
	}
	if v := e.ordinaryPolicyGenerations.Load(); v != nil {
		policy = (*v)[signature]
	}
	return identity, policy
}

func ordinaryRuleFingerprint(a AccountState, s Settings, policyGeneration string) string {
	return digest(fmt.Sprintf("ordinary-cache-recovery-v1:%d:%d:%s:%d:%d:%d:%s:%s:%s", a.AccountID, a.UpstreamKeyID,
		PolicySignature(s), s.WindowMinutes, s.CooldownMinutes, RecoveryLifetime,
		a.DegradedAt.UTC().Format(time.RFC3339Nano), a.CooldownUntil.UTC().Format(time.RFC3339Nano), policyGeneration))
}

func opaqueRecoveryDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func recoveryIdentityMatches(f Evidence) bool {
	return opaqueRecoveryDigest(f.Request.OrdinaryRecoverySessionID) &&
		opaqueRecoveryDigest(f.Request.OrdinaryRecoveryIdentityGeneration) &&
		f.Request.OrdinaryRecoverySessionID == f.Admission.OrdinaryRecoverySessionID &&
		f.Request.OrdinaryRecoveryIdentityGeneration == f.Admission.OrdinaryRecoveryIdentityGeneration
}

func ordinaryFact(f Evidence, p *OrdinaryRecoveryState) bool {
	return p != nil && f.ValidSample() && !f.ProtectionPaused && !f.Admission.CaptureOnly && !f.Admission.Trial &&
		recoveryIdentityMatches(f) && f.Request.OrdinaryRecoveryIdentityGeneration == p.IdentityGeneration &&
		!f.Request.StartedAt.Before(p.BarrierAt) && !f.CompletedAt.Before(p.BarrierAt)
}

// OrdinaryRecoveryFactEligible exposes the pure frozen-identity predicate to
// database projection implementations and differential tests. Account/Key,
// policy reset boundaries and the current window are filtered by the caller.
func OrdinaryRecoveryFactEligible(f Evidence, p *OrdinaryRecoveryState) bool {
	return ordinaryFact(f, p)
}

func resetOrdinaryProgress(p *OrdinaryRecoveryState, at time.Time, reason string) {
	p.QualifiedSince, p.QualifiedUntil = time.Time{}, time.Time{}
	if at.After(p.BarrierAt) {
		p.BarrierAt = at
	}
	p.ValidRequests, p.Sessions = 0, 0
	p.TotalInputTokens, p.CacheReadTokens = 0, 0
	p.Reason = reason
}

// Status is a pure projection. The read clock may invalidate qualification but
// can never start its observation period or turn the account healthy.
func OrdinaryRecoveryStatusAt(a AccountState, s Settings, at time.Time) *OrdinaryRecoveryView {
	if !protectedPhase(a.Phase) || s.Mode != Protect {
		return nil
	}
	v := &OrdinaryRecoveryView{RequiredRequests: s.MinValidRequests + 1, RequiredSessions: MinRecoverySessions,
		RequiredRatePercent: s.RecoveryThresholdPercent, Reason: "waiting_samples"}
	if at.Before(a.CooldownUntil) {
		v.Reason = "cooldown"
		return v
	}
	p := a.OrdinaryRecovery
	if p == nil {
		return v
	}
	v.ValidRequests, v.Sessions, v.Reason = p.ValidRequests, p.Sessions, p.Reason
	if p.TotalInputTokens > 0 {
		rate := float64(p.CacheReadTokens) / float64(p.TotalInputTokens) * 100
		v.RatePercent = &rate
	}
	// Current-window projections may expire samples without a completion
	// transaction. Reclassify missing metrics even before a timer has started,
	// while retaining explicit control barriers ahead of statistical reasons.
	switch p.Reason {
	case "sampling_gap", "paused", "waiting_identity", "identity_changed", "rule_changed":
		return v
	}
	if reason := ordinaryMetricsReason(p.ValidRequests, p.Sessions, p.TotalInputTokens, p.CacheReadTokens, a.TotalInputTokens, a.CacheReadTokens, s); reason != "" {
		v.Reason = reason
		return v
	}
	if !p.QualifiedSince.IsZero() {
		if at.Before(p.LastVerifiedAt) || p.QualifiedUntil.IsZero() || at.After(p.QualifiedUntil) {
			v.Reason = "window_interrupted"
			return v
		}
		start, ready := p.QualifiedSince, p.QualifiedSince.Add(RecoveryLifetime)
		v.StartedAt, v.ReadyAt = &start, &ready
		v.Reason = "waiting_validation_time"
		if !at.Before(ready) {
			v.Reason = "awaiting_fresh_evidence"
		}
	} else {
		v.Reason = "awaiting_fresh_evidence"
	}
	return v
}

type ordinaryWindowFact struct {
	at       time.Time
	input    int64
	read     int64
	session  string
	ordinary bool
}

type ordinaryWindowTotals struct {
	input, read, ordinaryInput, ordinaryRead int64
	count                                    int
	sessions                                 map[string]int
}

func (w ordinaryWindowTotals) reason(s Settings) string {
	return ordinaryMetricsReason(w.count, len(w.sessions), w.ordinaryInput, w.ordinaryRead, w.input, w.read, s)
}

func ordinaryMetricsReason(count, sessions int, input, read, sharedInput, sharedRead int64, s Settings) string {
	if count <= s.MinValidRequests {
		return "waiting_samples"
	}
	if sessions < MinRecoverySessions {
		return "waiting_sessions"
	}
	if input <= 0 || float64(read)/float64(input)*100 < s.RecoveryThresholdPercent {
		return "below_recovery_threshold"
	}
	if sharedInput <= 0 || float64(sharedRead)/float64(sharedInput)*100 < s.RecoveryThresholdPercent {
		return "shared_below_recovery_threshold"
	}
	return ""
}

// Compute the first actual loss of qualification under time-only expiry. The
// oldest fact alone is not such a boundary: a five-minute window can sustain
// ten minutes of qualification while new evidence extends this deadline.
func ordinaryWindow(a AccountState, s Settings, p *OrdinaryRecoveryState, facts []Evidence, now time.Time) (ordinaryWindowTotals, time.Time, time.Time, error) {
	w := ordinaryWindowTotals{sessions: map[string]int{}}
	rows := make([]ordinaryWindowFact, 0, len(facts))
	var newest time.Time
	cutoff := now.Add(-time.Duration(s.WindowMinutes) * time.Minute)
	for _, f := range facts {
		if (a.AccountID > 0 && f.Request.AccountID > 0 && a.AccountID != f.Request.AccountID) ||
			(a.UpstreamKeyID > 0 && f.Request.UpstreamKeyID > 0 && a.UpstreamKeyID != f.Request.UpstreamKeyID) ||
			!f.ValidSample() || f.CompletedAt.Before(cutoff) || f.CompletedAt.After(now) ||
			(!a.QualitySince.IsZero() && !f.CompletedAt.After(a.QualitySince)) {
			continue
		}
		if since := a.LegacyQualitySince[f.Request.GroupID]; !since.IsZero() && !f.CompletedAt.After(since) {
			continue
		}
		if math.MaxInt64-w.input < f.RawUsage.TotalInputTokens || math.MaxInt64-w.read < f.RawUsage.CacheReadTokens {
			return w, time.Time{}, time.Time{}, errors.New("ordinary cache recovery token sum overflow")
		}
		row := ordinaryWindowFact{at: f.CompletedAt, input: f.RawUsage.TotalInputTokens, read: f.RawUsage.CacheReadTokens,
			ordinary: ordinaryFact(f, p), session: f.Admission.OrdinaryRecoverySessionID}
		w.input += row.input
		w.read += row.read
		if row.ordinary {
			w.count++
			w.ordinaryInput += row.input
			w.ordinaryRead += row.read
			w.sessions[row.session]++
		}
		if f.CompletedAt.After(newest) {
			newest = f.CompletedAt
		}
		rows = append(rows, row)
	}
	if w.reason(s) != "" {
		return w, time.Time{}, newest, nil
	}
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) }) {
		sort.Slice(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
	}
	future := w
	future.sessions = make(map[string]int, len(w.sessions))
	for id, count := range w.sessions {
		future.sessions[id] = count
	}
	for i := 0; i < len(rows); {
		at := rows[i].at
		for i < len(rows) && rows[i].at.Equal(at) {
			r := rows[i]
			future.input -= r.input
			future.read -= r.read
			if r.ordinary {
				future.count--
				future.ordinaryInput -= r.input
				future.ordinaryRead -= r.read
				future.sessions[r.session]--
				if future.sessions[r.session] == 0 {
					delete(future.sessions, r.session)
				}
			}
			i++
		}
		if future.reason(s) != "" {
			return w, at.Add(time.Duration(s.WindowMinutes) * time.Minute), newest, nil
		}
	}
	return w, time.Time{}, newest, nil
}

func advanceOrdinaryRecovery(a *AccountState, s Settings, facts []Evidence, current Evidence, now time.Time, identityGeneration, policyGeneration string, gap bool) error {
	if !protectedPhase(a.Phase) {
		a.OrdinaryRecovery = nil
		return nil
	}
	if s.Mode != Protect {
		if a.OrdinaryRecovery != nil {
			resetOrdinaryProgress(a.OrdinaryRecovery, now, "paused")
		}
		return nil
	}
	if identityGeneration == "" || policyGeneration == "" {
		if a.OrdinaryRecovery != nil {
			resetOrdinaryProgress(a.OrdinaryRecovery, now, "waiting_identity")
		}
		return nil
	}
	p := a.OrdinaryRecovery
	fingerprint := ordinaryRuleFingerprint(*a, s, policyGeneration)
	if p == nil {
		p = &OrdinaryRecoveryState{Version: 1, RuleFingerprint: fingerprint, PolicyGeneration: policyGeneration,
			IdentityGeneration: identityGeneration, BarrierAt: a.CooldownUntil, Reason: "waiting_samples"}
		a.OrdinaryRecovery = p
	} else if p.Version != 1 || p.RuleFingerprint != fingerprint || p.IdentityGeneration != identityGeneration {
		reason := "rule_changed"
		if p.IdentityGeneration != identityGeneration {
			reason = "identity_changed"
		}
		p = &OrdinaryRecoveryState{Version: 1, RuleFingerprint: fingerprint, PolicyGeneration: policyGeneration,
			IdentityGeneration: identityGeneration, BarrierAt: now, Reason: reason, LastVerifiedAt: now}
		a.OrdinaryRecovery = p
		return nil
	}
	if now.Before(p.LastVerifiedAt) {
		resetOrdinaryProgress(p, p.LastVerifiedAt, "window_interrupted")
		return nil
	}
	p.LastVerifiedAt = now
	if gap || current.ProtectionPaused || current.Admission.CaptureOnly {
		reason := "sampling_gap"
		if current.ProtectionPaused || current.Admission.CaptureOnly {
			reason = "paused"
		}
		resetOrdinaryProgress(p, now, reason)
		return nil
	}
	if now.Before(a.CooldownUntil) {
		resetOrdinaryProgress(p, a.CooldownUntil, "cooldown")
		return nil
	}
	if !p.QualifiedSince.IsZero() && (p.QualifiedUntil.IsZero() || now.After(p.QualifiedUntil)) {
		// Do not carry a ten-minute clock across an unobserved empty/low window.
		p.QualifiedSince, p.QualifiedUntil = time.Time{}, time.Time{}
		p.Reason = "window_interrupted"
	}
	priorEvidenceAt := p.LastEvidenceAt
	w, until, newest, err := ordinaryWindow(*a, s, p, facts, now)
	if err != nil {
		return err
	}
	// Evidence freshness is another time-only invalidation boundary. Without
	// this cap, a long window could bridge an already expired protection phase.
	if !newest.IsZero() {
		evidenceUntil := newest.Add(time.Duration(protectionEvidenceExpiryMinutes(s)) * time.Minute)
		if until.IsZero() || evidenceUntil.Before(until) {
			until = evidenceUntil
		}
	}
	if newest.After(p.LastEvidenceAt) {
		p.LastEvidenceAt = newest
	}
	p.ValidRequests, p.Sessions = w.count, len(w.sessions)
	p.TotalInputTokens, p.CacheReadTokens = w.ordinaryInput, w.ordinaryRead
	if reason := w.reason(s); reason != "" {
		p.QualifiedSince, p.QualifiedUntil, p.Reason = time.Time{}, time.Time{}, reason
		return nil
	}
	// A historical, duplicate-clock, trial, or untrusted completion may alter
	// the exact window, but cannot start or finish an observation period.
	fresh := ordinaryFact(current, p) && current.CompletedAt.After(priorEvidenceAt) && current.CompletedAt.Equal(newest) &&
		!current.CompletedAt.After(now) && !current.CompletedAt.Before(now.Add(-time.Duration(s.WindowMinutes)*time.Minute))
	if !fresh {
		if !p.QualifiedSince.IsZero() {
			p.QualifiedUntil = until
		}
		return nil
	}
	if p.QualifiedSince.IsZero() {
		p.QualifiedSince = now
	}
	p.QualifiedUntil = until
	p.Reason = "waiting_validation_time"
	if now.Before(p.QualifiedSince.Add(RecoveryLifetime)) || current.CompletedAt.Before(p.QualifiedSince.Add(RecoveryLifetime)) {
		return nil
	}
	// Original trial recovery remains independently sufficient. This is the
	// same successful state reset, without manufacturing any trial admission.
	recoverAccount(a, now)
	return nil
}

func (e *Engine) advanceOrdinaryPolicies(a *AccountState, facts []Evidence, current Evidence, now time.Time, supported, pendingGap bool) error {
	e.mu.RLock()
	id := accountIdentity(current.Request.AccountID, current.Request.UpstreamKeyID)
	gap := pendingGap || e.accountGaps[id] || len(e.samplingFailures[id]) > 0
	e.mu.RUnlock()
	for signature, projection := range a.Policies {
		identity, policy := e.ordinaryGenerations(signature)
		if !supported {
			identity, policy = "", ""
		}
		if err := advanceOrdinaryRecovery(&projection.State, projection.Settings, facts, current, now, identity, policy, gap); err != nil {
			return err
		}
		a.Policies[signature] = projection
	}
	return nil
}

// Invalidate a copy when a newly published policy/secret generation makes a
// previously qualified progress record inapplicable. Readers never mutate it.
func (e *Engine) ordinaryRecoveryReadState(a *AccountState, s Settings, at time.Time, gap bool) bool {
	p := a.OrdinaryRecovery
	if p == nil {
		return false
	}
	identity, policy := e.ordinaryGenerations(PolicySignature(s))
	reason := ""
	if gap {
		reason = "sampling_gap"
	} else if identity == "" || policy == "" {
		reason = "waiting_identity"
	} else if identity != p.IdentityGeneration {
		reason = "identity_changed"
	} else if s.Mode != Protect {
		reason = "paused"
	} else if p.Version != 1 || p.RuleFingerprint != ordinaryRuleFingerprint(*a, s, policy) {
		reason = "rule_changed"
	}
	if reason == "" {
		return false
	}
	copy := *p
	resetOrdinaryProgress(&copy, at, reason)
	a.OrdinaryRecovery = &copy
	return true
}
