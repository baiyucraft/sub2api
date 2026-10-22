package upstreamcacheguard

import (
	"fmt"
	"reflect"
	"time"
)

// PolicySignature deliberately excludes groups and routing priority. Equal
// quality rules share one durable phase and one recovery cohort. Evidence
// expiry is included because it changes when a protected phase becomes
// unknown and therefore changes the durable decision boundary.
func PolicySignature(s Settings) string {
	expiry := protectionEvidenceExpiryMinutes(s)
	// Preserve the released signature for the default expiry so upgrading does
	// not reset durable cohorts, frozen admissions or existing route affinity.
	if expiry == 15 {
		return digest(fmt.Sprintf("cache-policy-v1:%g:%g:%d", s.ThresholdPercent, s.RecoveryThresholdPercent, s.MinValidRequests))
	}
	return digest(fmt.Sprintf("cache-policy-v2:%g:%g:%d:%d", s.ThresholdPercent, s.RecoveryThresholdPercent, s.MinValidRequests, expiry))
}

// PolicyVersion allows one routing evaluation to reject a mixed-generation
// read if a policy changes between its otherwise immutable snapshot reads.
func (e *Engine) PolicyVersion() uint64 { return e.policyEpoch.Load() }

func (e *Engine) ApplyPolicySettings(values map[int64]Settings) error {
	copy := make(map[int64]Settings, len(values))
	for id, s := range values {
		if id <= 0 {
			continue
		}
		if err := s.Validate(); err != nil {
			return err
		}
		if s.Priority == "" {
			s.Priority = "cache_first"
		}
		s.ProtectionEvidenceExpiryMinutes = protectionEvidenceExpiryMinutes(s)
		s.GroupIDs = append([]int64(nil), s.GroupIDs...)
		copy[id] = s
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old := e.policies.Load()
	if old == nil && len(copy) == 0 {
		return nil
	}
	if old != nil && reflect.DeepEqual(*old, copy) {
		return nil
	}
	e.policies.Store(&copy)
	e.updateActivePolicy()
	e.policyEpoch.Add(1)
	return nil
}

func (e *Engine) ResolvedSettings(groupID int64) Settings {
	s := e.Settings()
	if values := e.policies.Load(); values != nil {
		if override, ok := (*values)[groupID]; ok {
			override.GroupIDs = append([]int64(nil), override.GroupIDs...)
			return override
		}
	}
	if !s.ContainsGroup(groupID) {
		s.Mode = Disabled
	}
	return s
}

func (e *Engine) AnyPolicyActive() bool {
	return e.activePolicy.Load()
}
func (e *Engine) updateActivePolicy() {
	if e.Settings().Mode != Disabled {
		e.activePolicy.Store(true)
		return
	}
	if values := e.policies.Load(); values != nil {
		for _, s := range *values {
			if s.Mode != Disabled {
				e.activePolicy.Store(true)
				return
			}
		}
	}
	e.activePolicy.Store(false)
}

func (e *Engine) activePolicies() map[string]Settings {
	values := map[string]Settings{}
	global := e.Settings()
	if global.Mode != Disabled {
		values[PolicySignature(global)] = global
	}
	if overrides := e.policies.Load(); overrides != nil {
		for _, s := range *overrides {
			if s.Mode != Disabled {
				values[PolicySignature(s)] = s
			}
		}
	}
	return values
}

func (e *Engine) preparePolicies(a *AccountState) {
	active := e.activePolicies()
	if a.Policies == nil {
		legacy := *a
		legacy.Policies = nil
		s := e.Settings()
		sig := PolicySignature(s)
		legacy.PolicySignature = sig
		a.Policies = map[string]PolicyProjection{sig: {Settings: s, State: legacy}}
	}
	for sig := range a.Policies {
		if _, ok := active[sig]; !ok {
			delete(a.Policies, sig)
		}
	}
	for sig, s := range active {
		p, exists := a.Policies[sig]
		if !exists {
			p.State = AccountState{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Phase: Healthy, PolicySignature: sig, LegacyQualitySince: a.LegacyQualitySince}
		}
		p.Settings = s
		p.State.Policies = nil
		p.State.PolicySignature = sig
		a.Policies[sig] = p
	}
}

func (e *Engine) recomputePolicies(a *AccountState, facts []Evidence, now time.Time, fresh bool) error {
	return e.recomputePoliciesWithRecovery(a, facts, now, fresh, true)
}

func (e *Engine) recomputePoliciesWithRecovery(a *AccountState, facts []Evidence, now time.Time, fresh, allowRecovery bool) error {
	for sig, p := range a.Policies {
		// A newly configured strategy evaluates the already shared observations.
		initial := p.State.LastEvidenceAt.IsZero()
		if err := recomputeAccountWithRecovery(&p.State, facts, p.Settings, now, fresh || initial, allowRecovery && fresh); err != nil {
			return err
		}
		a.Policies[sig] = p
	}
	// Shared observations never inherit a strategy's recovery reset or cohort.
	raw := *a
	raw.Policies = nil
	raw.PolicySignature = ""
	raw.QualitySince = time.Time{}
	raw.Phase = Healthy
	raw.CohortID = ""
	raw.FirstTrialAt = time.Time{}
	raw.CooldownUntil = time.Time{}
	raw.DegradedAt = time.Time{}
	if err := RecomputeAccount(&raw, facts, e.Settings(), now, false); err != nil {
		return err
	}
	raw.Policies = a.Policies
	*a = raw
	return nil
}

func projectionSettingsCompatible(a, b Settings) bool {
	return PolicySignature(a) == PolicySignature(b) && a.WindowMinutes == b.WindowMinutes && a.CooldownMinutes == b.CooldownMinutes && a.TrialPercent == b.TrialPercent && protectionEvidenceExpiryMinutes(a) == protectionEvidenceExpiryMinutes(b)
}

func accountPoliciesCompatible(a AccountState, active map[string]Settings) bool {
	if a.Policies == nil || len(a.Policies) != len(active) {
		return false
	}
	for signature, settings := range active {
		p, exists := a.Policies[signature]
		if !exists || !projectionSettingsCompatible(p.Settings, settings) {
			return false
		}
	}
	return true
}

func projectAccountSnapshot(a AccountSnapshot, s Settings) AccountSnapshot {
	result := a
	if p, ok := a.Policies[PolicySignature(s)]; ok && projectionSettingsCompatible(p.Settings, s) {
		result.AccountState = p.State
		result.GroupIDs = append([]int64(nil), a.GroupIDs...)
		result.Policies = nil
		return cloneAccountSnapshot(result)
	}
	if a.Policies != nil {
		result.Available, result.Fresh = false, false
		result.Phase = Healthy
		result.CohortID = ""
		result.TrialValidRequests, result.TrialSessions = 0, 0
		result.TrialCachePercent = 0
		result.FirstTrialAt, result.CooldownUntil = time.Time{}, time.Time{}
	}
	result.Policies = nil
	return cloneAccountSnapshot(result)
}

// ProjectAccountSnapshot reads one strategy without borrowing another
// strategy's reset boundary or recovery cohort.
func ProjectAccountSnapshot(a AccountSnapshot, s Settings) AccountSnapshot {
	return projectAccountSnapshot(a, s)
}

func (e *Engine) CachedAccountForGroup(groupID, accountID, keyID int64) (AccountSnapshot, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	a := e.cachedAccountForGroupAtLocked(Request{GroupID: groupID, AccountID: accountID, UpstreamKeyID: keyID}, e.ResolvedSettings(groupID), e.now().UTC())
	return a, a.Available
}
