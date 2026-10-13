package upstreamcacheguard

import (
	"math"
	"sort"
	"time"
)

// GroupPolicy is the effective policy for one group. A resolver can overlay
// per-group settings in the future without changing native evidence ownership.
type GroupPolicy struct {
	GroupID  int64
	Settings Settings
}

type GroupPolicyResolver interface {
	ResolveGroupPolicy(groupID int64) (GroupPolicy, bool)
}

// UniformGroupPolicyResolver preserves the current single settings contract.
// Resolving a policy does not persist configuration or allocate trial slots.
type UniformGroupPolicyResolver struct {
	Settings Settings
}

func (r UniformGroupPolicyResolver) ResolveGroupPolicy(groupID int64) (GroupPolicy, bool) {
	if !r.Settings.ContainsGroup(groupID) {
		return GroupPolicy{}, false
	}
	settings := r.Settings
	settings.GroupIDs = append([]int64(nil), settings.GroupIDs...)
	return GroupPolicy{GroupID: groupID, Settings: settings}, true
}

const ProtectionReasonThresholdCascade = "cache_threshold_cascade"

// ProtectionSource describes native protection owned by another group. Its
// measurements and recovery progress never replace the target's local data.
type ProtectionSource struct {
	GroupID                  int64     `json:"group_id"`
	ThresholdPercent         float64   `json:"threshold_percent"`
	RecoveryThresholdPercent float64   `json:"recovery_threshold_percent"`
	Phase                    Phase     `json:"phase"`
	CohortID                 string    `json:"cohort_id,omitempty"`
	DegradedAt               time.Time `json:"degraded_at,omitempty"`
	CooldownUntil            time.Time `json:"cooldown_until,omitempty"`
	FirstTrialAt             time.Time `json:"first_trial_at,omitempty"`
	TrialValidRequests       int       `json:"trial_valid_requests"`
	TrialSessions            int       `json:"trial_sessions"`
	TrialCachePercent        float64   `json:"trial_cache_percent"`
	MinValidRequests         int       `json:"min_valid_requests"`
	ValidRequests            int       `json:"valid_requests"`
	CachePercent             float64   `json:"cache_percent"`
	LastEvidenceAt           time.Time `json:"last_evidence_at,omitempty"`
	UpdatedAt                time.Time `json:"updated_at,omitempty"`
	Version                  int64     `json:"version"`
}

// DerivedProtection is an opinion for routing, not a native account phase.
// In Observe mode it describes a proposed action only; routing must still
// require Protect mode independently of this administrative view.
// It has no independent cohort or recovery transition. Each reason disappears
// only when that native source ceases to apply; other sources remain intact.
type DerivedProtection struct {
	Protected     bool               `json:"protected"`
	Reason        string             `json:"reason,omitempty"`
	PrimarySource *ProtectionSource  `json:"primary_source,omitempty"`
	Sources       []ProtectionSource `json:"sources"`
}

func activeGroupPolicy(resolver GroupPolicyResolver, groupID int64) (GroupPolicy, bool) {
	if resolver == nil || groupID <= 0 {
		return GroupPolicy{}, false
	}
	p, ok := resolver.ResolveGroupPolicy(groupID)
	if !ok || p.GroupID != groupID || (p.Settings.Mode != Protect && p.Settings.Mode != Observe) || !p.Settings.ContainsGroup(groupID) ||
		math.IsNaN(p.Settings.ThresholdPercent) || math.IsInf(p.Settings.ThresholdPercent, 0) ||
		p.Settings.ThresholdPercent < 0 || p.Settings.ThresholdPercent > 100 {
		return GroupPolicy{}, false
	}
	return p, true
}

// DeriveAccountProtection is a bounded, side-effect-free read of native
// snapshots. A higher cache threshold is stricter, so a target inherits a
// native source only at an equal or higher threshold. Derived state is never
// used as a source, and current upstream key identity must match both sides.
//
// Native protection deliberately survives an expired measurement window: only
// fresh native recovery evidence can restore it. The snapshot itself must be
// fresh and available. This function cannot start a trial or clear a cohort.
func DeriveAccountProtection(targetGroupID, accountID, upstreamKeyID int64, snapshots []Snapshot, resolver GroupPolicyResolver, now time.Time) DerivedProtection {
	result := DerivedProtection{}
	target, ok := activeGroupPolicy(resolver, targetGroupID)
	if !ok || accountID <= 0 || upstreamKeyID <= 0 || now.IsZero() {
		return result
	}
	sources := map[int64]ProtectionSource{}
	for _, snapshot := range snapshots {
		if snapshot.GroupID == targetGroupID || !snapshot.Available || snapshot.SamplingGap || snapshot.Mode != target.Settings.Mode ||
			snapshot.LoadedAt.IsZero() || now.Before(snapshot.LoadedAt) || now.Sub(snapshot.LoadedAt) > RefreshLifetime {
			continue
		}
		policy, applies := activeGroupPolicy(resolver, snapshot.GroupID)
		if !applies || policy.Settings.Mode != target.Settings.Mode || target.Settings.ThresholdPercent < policy.Settings.ThresholdPercent {
			continue
		}
		for _, account := range snapshot.Accounts {
			if account.AccountID != accountID || account.UpstreamKeyID != upstreamKeyID ||
				(account.Phase != Degraded && account.Phase != Cooling && account.Phase != Trial) {
				continue
			}
			source := ProtectionSource{
				GroupID: snapshot.GroupID, ThresholdPercent: policy.Settings.ThresholdPercent,
				RecoveryThresholdPercent: policy.Settings.RecoveryThresholdPercent,
				Phase:                    account.Phase, CohortID: account.CohortID,
				DegradedAt: account.DegradedAt, CooldownUntil: account.CooldownUntil,
				FirstTrialAt: account.FirstTrialAt, TrialValidRequests: account.TrialValidRequests,
				TrialSessions: account.TrialSessions, TrialCachePercent: account.TrialCachePercent,
				MinValidRequests: policy.Settings.MinValidRequests, ValidRequests: account.ValidRequests,
				CachePercent: account.CachePercent, LastEvidenceAt: account.LastEvidenceAt,
				UpdatedAt: account.UpdatedAt, Version: account.Version,
			}
			if previous, exists := sources[snapshot.GroupID]; !exists || source.Version > previous.Version {
				sources[snapshot.GroupID] = source
			}
		}
	}
	for _, source := range sources {
		result.Sources = append(result.Sources, source)
	}
	sort.Slice(result.Sources, func(i, j int) bool {
		if result.Sources[i].ThresholdPercent != result.Sources[j].ThresholdPercent {
			return result.Sources[i].ThresholdPercent < result.Sources[j].ThresholdPercent
		}
		return result.Sources[i].GroupID < result.Sources[j].GroupID
	})
	if len(result.Sources) > 0 {
		result.Protected = true
		result.Reason = ProtectionReasonThresholdCascade
		primary := result.Sources[0]
		result.PrimarySource = &primary
	}
	return result
}
