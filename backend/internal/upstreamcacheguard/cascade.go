package upstreamcacheguard

import "time"

// Legacy wire types remain decodable; account-scoped decisions do not derive
// protection from another group.
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
