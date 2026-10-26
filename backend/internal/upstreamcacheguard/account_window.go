package upstreamcacheguard

import (
	"time"
)

// Apply replaces only the current-window metrics; policy phase and recovery
// cohort are persisted by completion transactions, never by a read refresh.
func (w AccountWindow) Apply(a *AccountState, now time.Time) {
	if a == nil {
		return
	}
	if a.OrdinaryRecovery != nil {
		// Read projections own their metrics but never advance a durable timer.
		ordinary := *a.OrdinaryRecovery
		ordinary.ValidRequests, ordinary.Sessions = w.OrdinaryValidRequests, w.OrdinarySessions
		ordinary.TotalInputTokens, ordinary.CacheReadTokens = w.OrdinaryInputTokens, w.OrdinaryCacheReadTokens
		a.OrdinaryRecovery = &ordinary
	}
	for sig, window := range w.Policies {
		if p, ok := a.Policies[sig]; ok {
			window.Apply(&p.State, now)
			a.Policies[sig] = p
		}
	}
	changed := a.ValidRequests != w.ValidRequests || a.TotalInputTokens != w.TotalInputTokens ||
		a.CacheReadTokens != w.CacheReadTokens || a.TrialValidRequests != w.TrialValidRequests ||
		a.TrialSessions != w.TrialSessions ||
		!w.LastEvidenceAt.IsZero() && w.LastEvidenceAt.After(a.LastEvidenceAt)
	previousRate, previousTrialRate := a.CachePercent, a.TrialCachePercent
	a.ValidRequests = w.ValidRequests
	a.TotalInputTokens = w.TotalInputTokens
	a.CacheReadTokens = w.CacheReadTokens
	a.CachePercent = 0
	if w.TotalInputTokens > 0 {
		a.CachePercent = float64(w.CacheReadTokens) / float64(w.TotalInputTokens) * 100
	}
	a.TrialValidRequests = w.TrialValidRequests
	a.TrialSessions = w.TrialSessions
	a.TrialCachePercent = 0
	if w.TrialInputTokens > 0 {
		a.TrialCachePercent = float64(w.TrialCacheReadTokens) / float64(w.TrialInputTokens) * 100
	}
	changed = changed || previousRate != a.CachePercent || previousTrialRate != a.TrialCachePercent
	if w.LastEvidenceAt.After(a.LastEvidenceAt) {
		a.LastEvidenceAt = w.LastEvidenceAt
	}
	previousGroups := len(a.GroupIDs)
	previousTrialGroups := len(a.TrialGroupIDs)
	for _, groupID := range w.GroupIDs {
		a.GroupIDs = appendGroup(a.GroupIDs, groupID)
	}
	for _, groupID := range w.TrialGroupIDs {
		a.TrialGroupIDs = appendGroup(a.TrialGroupIDs, groupID)
	}
	if (changed || len(a.GroupIDs) != previousGroups || len(a.TrialGroupIDs) != previousTrialGroups) && now.After(a.UpdatedAt) {
		a.UpdatedAt = now
	}
}
