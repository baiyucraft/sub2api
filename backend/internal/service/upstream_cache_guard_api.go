package service

import (
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type UpstreamCacheGuardState struct {
	GroupID               int64                               `json:"group_id"`
	GroupName             string                              `json:"group_name,omitempty"`
	State                 string                              `json:"state"`
	RatePercent           *float64                            `json:"rate_percent"`
	ValidRequests         int                                 `json:"valid_requests"`
	Reason                string                              `json:"reason"`
	RecoveryValidRequests int                                 `json:"recovery_valid_requests"`
	RecoveryRatePercent   *float64                            `json:"recovery_rate_percent"`
	RecoverySessions      int                                 `json:"recovery_sessions"`
	RecoveryStartedAt     *time.Time                          `json:"recovery_started_at"`
	CooldownUntil         *time.Time                          `json:"cooldown_until"`
	UpdatedAt             *time.Time                          `json:"updated_at"`
	NativeState           string                              `json:"native_state,omitempty"`
	NativeReason          string                              `json:"native_reason,omitempty"`
	LinkedProtection      *UpstreamCacheGuardLinkedProtection `json:"linked_protection,omitempty"`
}

type UpstreamCacheGuardLinkedProtection struct {
	Protected            bool                                 `json:"protected"`
	Reason               string                               `json:"reason"`
	PrimarySourceGroupID int64                                `json:"primary_source_group_id"`
	Sources              []UpstreamCacheGuardProtectionSource `json:"sources"`
}

type UpstreamCacheGuardProtectionSource struct {
	GroupID                  int64      `json:"group_id"`
	GroupName                string     `json:"group_name,omitempty"`
	ThresholdPercent         float64    `json:"threshold_percent"`
	RecoveryThresholdPercent float64    `json:"recovery_threshold_percent"`
	Phase                    string     `json:"phase"`
	RatePercent              *float64   `json:"rate_percent"`
	ValidRequests            int        `json:"valid_requests"`
	MinValidRequests         int        `json:"min_valid_requests"`
	RecoveryValidRequests    int        `json:"recovery_valid_requests"`
	RecoveryRatePercent      *float64   `json:"recovery_rate_percent"`
	RecoverySessions         int        `json:"recovery_sessions"`
	RecoveryStartedAt        *time.Time `json:"recovery_started_at"`
	CooldownUntil            *time.Time `json:"cooldown_until"`
	UpdatedAt                *time.Time `json:"updated_at"`
}

// A linked source affects the displayed decision, never this group's samples
// or recovery progress. Sources are separately labelled, bounded API values.
func applyCacheGuardLinkedProtection(item *UpstreamCacheGuardState, derived *upstreamcacheguard.DerivedProtection, mode upstreamcacheguard.Mode, groups map[int64]string) {
	if derived == nil || !derived.Protected || len(derived.Sources) == 0 {
		return
	}
	item.NativeState, item.NativeReason = item.State, item.Reason
	linked := &UpstreamCacheGuardLinkedProtection{Protected: true, Reason: derived.Reason, Sources: make([]UpstreamCacheGuardProtectionSource, 0, len(derived.Sources))}
	if derived.PrimarySource != nil {
		linked.PrimarySourceGroupID = derived.PrimarySource.GroupID
	}
	for _, source := range derived.Sources {
		value := UpstreamCacheGuardProtectionSource{
			GroupID: source.GroupID, GroupName: groups[source.GroupID], ThresholdPercent: source.ThresholdPercent,
			RecoveryThresholdPercent: source.RecoveryThresholdPercent, Phase: string(source.Phase),
			ValidRequests: source.ValidRequests, MinValidRequests: source.MinValidRequests,
			RecoveryValidRequests: source.TrialValidRequests, RecoverySessions: source.TrialSessions,
			RecoveryStartedAt: cacheGuardTime(source.FirstTrialAt), CooldownUntil: cacheGuardTime(source.CooldownUntil), UpdatedAt: cacheGuardTime(source.UpdatedAt),
		}
		if source.ValidRequests > 0 {
			rate := source.CachePercent
			value.RatePercent = &rate
		}
		if source.TrialValidRequests > 0 {
			rate := source.TrialCachePercent
			value.RecoveryRatePercent = &rate
		}
		linked.Sources = append(linked.Sources, value)
	}
	item.LinkedProtection = linked
	item.State, item.Reason = "protected", "linked_group_protection"
	if mode == upstreamcacheguard.Observe {
		item.State = "observing"
	}
}

func cacheGuardTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// UpstreamCacheGuardAccountStates reads only the shared runtime snapshot. It
// never issues an account-by-account query or exposes logical session digests.
func UpstreamCacheGuardAccountStates(account *Account) []UpstreamCacheGuardState {
	if account == nil || !account.IsUpstreamBound() {
		return nil
	}
	r := currentUpstreamCacheGuardRunner()
	settings := DefaultUpstreamCacheGuardSettings()
	if r != nil {
		settings = r.engine.Settings()
	}
	groups := map[int64]string{}
	for _, id := range account.GroupIDs {
		groups[id] = ""
	}
	for _, g := range account.Groups {
		if g != nil {
			groups[g.ID] = g.Name
		}
	}
	for _, g := range account.AccountGroups {
		if _, ok := groups[g.GroupID]; !ok {
			groups[g.GroupID] = ""
		}
	}
	result := make([]UpstreamCacheGuardState, 0, len(groups))
	for id, name := range groups {
		item := UpstreamCacheGuardState{GroupID: id, GroupName: name, State: "disabled", Reason: "disabled"}
		if settings.Mode == upstreamcacheguard.Disabled || !settings.ContainsGroup(id) {
			result = append(result, item)
			continue
		}
		item.State = "insufficient"
		item.Reason = "insufficient_samples"
		if r == nil || !r.available.Load() {
			item.Reason = "snapshot_unavailable"
			result = append(result, item)
			continue
		}
		snapshot := r.engine.CachedSnapshot(id)
		if !snapshot.Available {
			item.Reason = "snapshot_unavailable"
			result = append(result, item)
			continue
		}
		for _, a := range snapshot.Accounts {
			if a.AccountID != account.ID || (a.UpstreamKeyID > 0 && a.UpstreamKeyID != *account.UpstreamKeyID) {
				continue
			}
			item.ValidRequests = a.ValidRequests
			if a.ValidRequests > 0 {
				rate := a.CachePercent
				item.RatePercent = &rate
			}
			item.UpdatedAt = cacheGuardTime(a.UpdatedAt)
			item.CooldownUntil = cacheGuardTime(a.CooldownUntil)
			item.RecoveryStartedAt = cacheGuardTime(a.FirstTrialAt)
			item.RecoveryValidRequests = a.TrialValidRequests
			item.RecoverySessions = a.TrialSessions
			if a.TrialValidRequests > 0 {
				rate := a.TrialCachePercent
				item.RecoveryRatePercent = &rate
			}
			if a.Phase == upstreamcacheguard.Healthy && a.ValidRequests > settings.MinValidRequests {
				item.State = "normal"
				item.Reason = "healthy"
			}
			if a.Phase != upstreamcacheguard.Healthy && a.Phase != "" {
				item.State = "protected"
				item.Reason = "below_threshold"
				if a.Phase == upstreamcacheguard.Trial {
					item.State = "recovering"
					item.Reason = "waiting_recovery_validation"
				}
			}
			applyCacheGuardLinkedProtection(&item, a.DerivedProtection, settings.Mode, groups)
			if settings.Mode == upstreamcacheguard.Observe {
				item.State = "observing"
			}
			break
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}
