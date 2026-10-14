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
	TrialParticipant      bool                                `json:"trial_participant"`
}

type UpstreamCacheGuardSummary struct {
	State                 string     `json:"state"`
	RatePercent           *float64   `json:"rate_percent"`
	ValidRequests         int        `json:"valid_requests"`
	WindowMinutes         int        `json:"window_minutes"`
	Reason                string     `json:"reason"`
	RecoveryValidRequests int        `json:"recovery_valid_requests"`
	RecoveryRatePercent   *float64   `json:"recovery_rate_percent"`
	RecoverySessions      int        `json:"recovery_sessions"`
	RecoveryStartedAt     *time.Time `json:"recovery_started_at"`
	CooldownUntil         *time.Time `json:"cooldown_until"`
	UpdatedAt             *time.Time `json:"updated_at"`
}

type UpstreamCacheGuardAccountView struct {
	Summary *UpstreamCacheGuardSummary
	States  []UpstreamCacheGuardState
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

func cacheGuardTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// UpstreamCacheGuardAccountStates projects the account decision to its groups.
func UpstreamCacheGuardAccountStates(account *Account) []UpstreamCacheGuardState {
	return GetUpstreamCacheGuardAccountView(account).States
}

func GetUpstreamCacheGuardAccountView(account *Account) UpstreamCacheGuardAccountView {
	if account == nil || !account.IsUpstreamBound() {
		return UpstreamCacheGuardAccountView{}
	}
	return UpstreamCacheGuardAccountViewsBatch([]Account{*account})[account.ID]
}

func UpstreamCacheGuardAccountStatesBatch(accounts []Account) map[int64][]UpstreamCacheGuardState {
	views := UpstreamCacheGuardAccountViewsBatch(accounts)
	result := make(map[int64][]UpstreamCacheGuardState, len(views))
	for id, view := range views {
		result[id] = view.States
	}
	return result
}

// Reads each current account/key snapshot once per row; group membership only
// affects where the shared account decision is executed, not what is sampled.
func UpstreamCacheGuardAccountViewsBatch(accounts []Account) map[int64]UpstreamCacheGuardAccountView {
	r := currentUpstreamCacheGuardRunner()
	settings := DefaultUpstreamCacheGuardSettings()
	if r != nil {
		settings = r.engine.Settings()
	}
	available := r != nil && r.available.Load()
	var readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool)
	if available {
		readAccount = r.engine.CachedAccount
	}
	return upstreamCacheGuardAccountViewsBatch(accounts, settings, available, readAccount)
}

func upstreamCacheGuardAccountViewsBatch(accounts []Account, settings UpstreamCacheGuardSettings, available bool, readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool)) map[int64]UpstreamCacheGuardAccountView {
	result := make(map[int64]UpstreamCacheGuardAccountView, len(accounts))
	if settings.Mode == upstreamcacheguard.Disabled {
		return result
	}
	for i := range accounts {
		account := &accounts[i]
		if !account.IsUpstreamBound() {
			continue
		}
		snapshot, fresh := upstreamcacheguard.AccountSnapshot{}, false
		if available && readAccount != nil {
			snapshot, fresh = readAccount(account.ID, *account.UpstreamKeyID)
		}
		summary := upstreamCacheGuardSummary(snapshot, fresh, settings)
		result[account.ID] = UpstreamCacheGuardAccountView{Summary: &summary, States: upstreamCacheGuardGroupStates(account, settings, summary, snapshot)}
	}
	return result
}

func upstreamCacheGuardSummary(a upstreamcacheguard.AccountSnapshot, fresh bool, settings UpstreamCacheGuardSettings) UpstreamCacheGuardSummary {
	item := UpstreamCacheGuardSummary{State: "insufficient", Reason: "insufficient_samples", WindowMinutes: settings.WindowMinutes}
	if !fresh {
		item.State, item.Reason = "unavailable", "snapshot_unavailable"
		return item
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
		item.State, item.Reason = "normal", "healthy"
	}
	if a.Phase != upstreamcacheguard.Healthy && a.Phase != "" {
		item.State, item.Reason = "protected", "below_threshold"
		if a.Phase == upstreamcacheguard.Trial {
			item.State, item.Reason = "recovering", "waiting_recovery_validation"
		}
	}
	if settings.Mode == upstreamcacheguard.Observe && item.State != "insufficient" {
		item.State = "observing"
	}
	return item
}

func upstreamCacheGuardGroupStates(account *Account, settings UpstreamCacheGuardSettings, summary UpstreamCacheGuardSummary, snapshot upstreamcacheguard.AccountSnapshot) []UpstreamCacheGuardState {
	groups := map[int64]string{}
	currentGroups := map[int64]bool{}
	for _, id := range account.GroupIDs {
		groups[id] = ""
		currentGroups[id] = true
	}
	for _, g := range account.Groups {
		if g != nil {
			groups[g.ID] = g.Name
			currentGroups[g.ID] = true
		}
	}
	for _, g := range account.AccountGroups {
		if _, ok := groups[g.GroupID]; !ok {
			groups[g.GroupID] = ""
		}
		currentGroups[g.GroupID] = true
	}
	for _, id := range snapshot.GroupIDs {
		if _, ok := groups[id]; !ok {
			groups[id] = ""
		}
	}
	trialGroups := make(map[int64]bool, len(snapshot.TrialGroupIDs))
	for _, id := range snapshot.TrialGroupIDs {
		trialGroups[id] = true
	}
	result := make([]UpstreamCacheGuardState, 0, len(groups))
	for id, name := range groups {
		item := UpstreamCacheGuardState{GroupID: id, GroupName: name, State: "disabled", Reason: "disabled"}
		if currentGroups[id] && settings.ContainsGroup(id) {
			item.State, item.Reason = summary.State, summary.Reason
			item.RatePercent, item.ValidRequests = summary.RatePercent, summary.ValidRequests
			item.UpdatedAt, item.CooldownUntil = summary.UpdatedAt, summary.CooldownUntil
			item.RecoveryStartedAt, item.RecoveryValidRequests = summary.RecoveryStartedAt, summary.RecoveryValidRequests
			item.RecoveryRatePercent, item.RecoverySessions = summary.RecoveryRatePercent, summary.RecoverySessions
			item.TrialParticipant = trialGroups[id]
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}
