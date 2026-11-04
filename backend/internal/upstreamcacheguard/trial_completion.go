package upstreamcacheguard

import "time"

// Only the transaction which inserted this cohort's real completion can decide
// its outcome. Recomputing old metrics or reading an expired window cannot.
func advanceTrialCompletion(a *AccountState, s Settings, current Evidence, now time.Time) {
	if s.Mode != Protect || a.Phase != Trial || a.CohortID == "" || a.FirstTrialAt.IsZero() ||
		!current.ValidSample() || current.ProtectionPaused || current.Admission.CaptureOnly || !current.Admission.Trial ||
		current.Admission.CohortID != a.CohortID || current.Request.AccountID != a.AccountID || current.Request.UpstreamKeyID != a.UpstreamKeyID ||
		(current.Admission.PolicySignature != "" && current.Admission.PolicySignature != PolicySignature(s)) ||
		current.Request.StartedAt.Before(a.FirstTrialAt) || current.CompletedAt.Before(a.FirstTrialAt) || current.CompletedAt.After(now) ||
		current.CompletedAt.Before(now.Add(-time.Duration(s.WindowMinutes)*time.Minute)) || a.TrialValidRequests <= s.MinValidRequests {
		return
	}
	// Failure needs sufficient actual warm samples, not the independent
	// success requirements of session diversity and a ten-minute observation.
	if a.TrialCachePercent < s.ThresholdPercent {
		a.Phase = Cooling
		if a.LastDegradedEvidenceAt == nil || current.CompletedAt.After(*a.LastDegradedEvidenceAt) {
			anchor := current.CompletedAt
			a.LastDegradedEvidenceAt = &anchor
		}
		a.CooldownUntil = now.Add(time.Duration(s.CooldownMinutes) * time.Minute)
		a.CohortID, a.FirstTrialAt = "", time.Time{}
		a.TrialGroupIDs = nil
		a.TrialValidRequests, a.TrialSessions, a.TrialCachePercent = 0, 0, 0
		return
	}
	ready := a.FirstTrialAt.Add(RecoveryLifetime)
	if a.TrialSessions >= EffectiveRecoveryMinSessions(s) && a.TrialCachePercent >= s.RecoveryThresholdPercent &&
		!now.Before(ready) && !current.CompletedAt.Before(ready) {
		recoverAccount(a, now)
	}
}

func (e *Engine) advanceTrialPolicies(a *AccountState, current Evidence, now time.Time) {
	for signature, projection := range a.Policies {
		advanceTrialCompletion(&projection.State, projection.Settings, current, now)
		a.Policies[signature] = projection
	}
}
