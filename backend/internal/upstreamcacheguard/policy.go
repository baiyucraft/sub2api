package upstreamcacheguard

import (
	"fmt"
	"time"
)

// PolicySignature deliberately excludes groups and routing priority. Equal
// quality rules share one durable phase and one recovery cohort.
func PolicySignature(s Settings) string {
	return digest(fmt.Sprintf("cache-policy-v1:%g:%g:%d", s.ThresholdPercent, s.RecoveryThresholdPercent, s.MinValidRequests))
}

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
		s.GroupIDs = append([]int64(nil), s.GroupIDs...)
		copy[id] = s
	}
	e.policies.Store(&copy)
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
	if e.Settings().Mode != Disabled {
		return true
	}
	if values := e.policies.Load(); values != nil {
		for _, s := range *values {
			if s.Mode != Disabled {
				return true
			}
		}
	}
	return false
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
	for sig, p := range a.Policies {
		// A newly configured strategy evaluates the already shared observations.
		initial := p.State.LastEvidenceAt.IsZero()
		if err := RecomputeAccount(&p.State, facts, p.Settings, now, fresh || initial); err != nil {
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

func projectAccountSnapshot(a AccountSnapshot, s Settings) AccountSnapshot {
	result := cloneAccountSnapshot(a)
	if p, ok := a.Policies[PolicySignature(s)]; ok {
		result.AccountState = p.State
		result.GroupIDs = append([]int64(nil), a.GroupIDs...)
		result.Policies = nil
		return cloneAccountSnapshot(result)
	}
	if a.Policies != nil {
		result.Phase = Healthy
		result.CohortID = ""
		result.TrialValidRequests, result.TrialSessions = 0, 0
		result.TrialCachePercent = 0
		result.FirstTrialAt, result.CooldownUntil = time.Time{}, time.Time{}
	}
	result.Policies = nil
	return result
}

// ProjectAccountSnapshot reads one strategy without borrowing another
// strategy's reset boundary or recovery cohort.
func ProjectAccountSnapshot(a AccountSnapshot, s Settings) AccountSnapshot {
	return projectAccountSnapshot(a, s)
}

func (e *Engine) CachedAccountForGroup(groupID, accountID, keyID int64) (AccountSnapshot, bool) {
	a, fresh := e.CachedSharedAccount(accountID, keyID)
	return projectAccountSnapshot(a, e.ResolvedSettings(groupID)), fresh
}
