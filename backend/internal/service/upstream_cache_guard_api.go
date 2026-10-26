package service

import (
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

type UpstreamCacheGuardState struct {
	PolicySource             string                                   `json:"policy_source"`
	PolicyMode               string                                   `json:"policy_mode"`
	ThresholdPercent         float64                                  `json:"threshold_percent"`
	RecoveryThresholdPercent float64                                  `json:"recovery_threshold_percent"`
	MinValidRequests         int                                      `json:"min_valid_requests"`
	SchedulingPriority       string                                   `json:"scheduling_priority"`
	GroupID                  int64                                    `json:"group_id"`
	GroupName                string                                   `json:"group_name,omitempty"`
	State                    string                                   `json:"state"`
	RatePercent              *float64                                 `json:"rate_percent"`
	ValidRequests            int                                      `json:"valid_requests"`
	Reason                   string                                   `json:"reason"`
	RecoveryValidRequests    int                                      `json:"recovery_valid_requests"`
	RecoveryRatePercent      *float64                                 `json:"recovery_rate_percent"`
	RecoverySessions         int                                      `json:"recovery_sessions"`
	RecoveryStartedAt        *time.Time                               `json:"recovery_started_at"`
	CooldownUntil            *time.Time                               `json:"cooldown_until"`
	UpdatedAt                *time.Time                               `json:"updated_at"`
	NativeState              string                                   `json:"native_state,omitempty"`
	NativeReason             string                                   `json:"native_reason,omitempty"`
	LinkedProtection         *UpstreamCacheGuardLinkedProtection      `json:"linked_protection,omitempty"`
	TrialParticipant         bool                                     `json:"trial_participant"`
	OrdinaryRecovery         *upstreamcacheguard.OrdinaryRecoveryView `json:"ordinary_recovery,omitempty"`
}

type UpstreamCacheGuardSummary struct {
	ordinaryRecovery      *upstreamcacheguard.OrdinaryRecoveryView
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
	return UpstreamCacheGuardAccountViewsBatchAt(accounts, time.Now().UTC())
}

func UpstreamCacheGuardAccountViewsBatchAt(accounts []Account, at time.Time) map[int64]UpstreamCacheGuardAccountView {
	r := currentUpstreamCacheGuardRunner()
	settings := DefaultUpstreamCacheGuardSettings()
	if r != nil {
		settings = r.engine.Settings()
	}
	// Availability is evaluated per cached account. A runner-wide pending or
	// refresh error must not blank unrelated account observations.
	available := r != nil && !r.closed.Load()
	var readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool)
	if available {
		candidates := make([]upstreamcacheguard.Candidate, 0, len(accounts))
		for i := range accounts {
			if accounts[i].IsUpstreamBound() {
				candidates = append(candidates, upstreamcacheguard.Candidate{AccountID: accounts[i].ID, UpstreamKeyID: accountUpstreamKeyID(&accounts[i])})
			}
		}
		snapshots := r.engine.CachedSharedAccountsAt(candidates, at)
		readAccount = func(accountID, keyID int64) (upstreamcacheguard.AccountSnapshot, bool) {
			snapshot, exists := snapshots[accountID]
			return snapshot, exists && snapshot.UpstreamKeyID == keyID && snapshot.Available
		}
	}
	if r == nil || !r.engine.AnyPolicyActive() {
		return map[int64]UpstreamCacheGuardAccountView{}
	}
	views := upstreamCacheGuardPolicyAccountViewsBatchAt(accounts, settings, available, readAccount, r.engine.ResolvedSettings, r.cachePolicyMetadata, at)
	for i := range accounts {
		account := &accounts[i]
		view, ok := views[account.ID]
		if !ok {
			continue
		}
		for j := range view.States {
			if !r.protectionScopeSelected(view.States[j].GroupID, account) {
				MarkUpstreamCacheGuardPlatformNotSelected(&view.States[j])
			}
		}
		selected := mostSevereUpstreamCacheGuardState(view.States)
		if !schedulingProtectionAccountSelected(r.protectionPolicyReader(), account) || selected == nil {
			view.Summary.State, view.Summary.Reason = "platform_not_selected", "platform_not_selected"
			view.Summary.RecoveryValidRequests, view.Summary.RecoverySessions = 0, 0
			view.Summary.RecoveryRatePercent, view.Summary.RecoveryStartedAt, view.Summary.CooldownUntil = nil, nil, nil
		} else {
			view.Summary.State, view.Summary.Reason = selected.State, selected.Reason
		}
		views[account.ID] = view
	}
	return views
}

func MarkUpstreamCacheGuardPlatformNotSelected(state *UpstreamCacheGuardState) {
	state.State, state.Reason = "platform_not_selected", "platform_not_selected"
	state.RecoveryValidRequests, state.RecoverySessions = 0, 0
	state.RecoveryRatePercent, state.RecoveryStartedAt, state.CooldownUntil = nil, nil, nil
	state.TrialParticipant = false
	state.OrdinaryRecovery = nil
	state.LinkedProtection = nil
	state.NativeState, state.NativeReason = "", ""
}

func upstreamCacheGuardAccountViewsBatch(accounts []Account, settings UpstreamCacheGuardSettings, available bool, readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool)) map[int64]UpstreamCacheGuardAccountView {
	return upstreamCacheGuardPolicyAccountViewsBatch(accounts, settings, available, readAccount, nil, nil)
}

func upstreamCacheGuardPolicyAccountViewsBatch(accounts []Account, settings UpstreamCacheGuardSettings, available bool, readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool), resolve func(int64) UpstreamCacheGuardSettings, metadata func(int64) (string, string)) map[int64]UpstreamCacheGuardAccountView {
	return upstreamCacheGuardPolicyAccountViewsBatchAt(accounts, settings, available, readAccount, resolve, metadata, time.Now().UTC())
}

func upstreamCacheGuardPolicyAccountViewsBatchAt(accounts []Account, settings UpstreamCacheGuardSettings, available bool, readAccount func(int64, int64) (upstreamcacheguard.AccountSnapshot, bool), resolve func(int64) UpstreamCacheGuardSettings, metadata func(int64) (string, string), at time.Time) map[int64]UpstreamCacheGuardAccountView {
	result := make(map[int64]UpstreamCacheGuardAccountView, len(accounts))
	if settings.Mode == upstreamcacheguard.Disabled && resolve == nil {
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
			// The reader's boolean is the compatibility contract for raw
			// snapshot usability, including readers predating Available.
			snapshot.Available = fresh
		}
		// The header reports shared observations. Policy-local recovery resets
		// only affect the per-group details below.
		summary := upstreamCacheGuardSummaryAt(snapshot, fresh, settings, at)
		states := upstreamCacheGuardGroupStates(account, settings, summary, snapshot)
		for i := range states {
			groupSettings := settings
			if resolve != nil {
				groupSettings = resolve(states[i].GroupID)
			}
			states[i].ThresholdPercent = groupSettings.ThresholdPercent
			states[i].RecoveryThresholdPercent = groupSettings.RecoveryThresholdPercent
			states[i].MinValidRequests = groupSettings.MinValidRequests
			states[i].SchedulingPriority = groupSettings.Priority
			states[i].PolicyMode, states[i].PolicySource = "inherit", "global"
			if metadata != nil {
				states[i].PolicyMode, states[i].PolicySource = metadata(states[i].GroupID)
			}
			if resolve != nil && accountMatchesCacheGuardGroup(account, states[i].GroupID) && groupSettings.Mode != upstreamcacheguard.Disabled && groupSettings.ContainsGroup(states[i].GroupID) {
				projected := upstreamcacheguard.ProjectAccountSnapshot(snapshot, groupSettings)
				groupSummary := upstreamCacheGuardSummaryAt(projected, fresh && projected.Available, groupSettings, at)
				applyCacheGuardGroupSummary(&states[i], groupSummary)
				states[i].TrialParticipant = false
				for _, id := range projected.TrialGroupIDs {
					states[i].TrialParticipant = states[i].TrialParticipant || id == states[i].GroupID
				}
			} else if resolve != nil {
				states[i].State, states[i].Reason = "disabled", "disabled"
				states[i].RatePercent, states[i].ValidRequests = nil, 0
				states[i].RecoveryRatePercent, states[i].RecoveryValidRequests, states[i].RecoverySessions = nil, 0, 0
				states[i].RecoveryStartedAt, states[i].CooldownUntil = nil, nil
				states[i].TrialParticipant = false
				states[i].OrdinaryRecovery = nil
			}
		}
		if resolve != nil {
			if selected := mostSevereUpstreamCacheGuardState(states); selected != nil {
				summary.State, summary.Reason = selected.State, selected.Reason
				if selected.Reason == "evidence_expired" {
					// The shared raw window can outlive a policy's evidence. Do
					// not present its old rate as a currently usable decision.
					summary.RatePercent, summary.ValidRequests = nil, 0
				}
			}
			// Recovery cohorts can differ across strategies; their progress is
			// intentionally shown only in the corresponding group detail.
			summary.RecoveryValidRequests, summary.RecoverySessions = 0, 0
			summary.RecoveryRatePercent, summary.RecoveryStartedAt, summary.CooldownUntil = nil, nil, nil
		}
		result[account.ID] = UpstreamCacheGuardAccountView{Summary: &summary, States: states}
	}
	return result
}

func mostSevereUpstreamCacheGuardState(states []UpstreamCacheGuardState) *UpstreamCacheGuardState {
	priority := map[string]int{"unavailable": 7, "protected": 6, "recovering": 5, "observing": 4, "normal": 3, "insufficient": 2, "disabled": 1}
	var selected *UpstreamCacheGuardState
	for i := range states {
		if states[i].State == "platform_not_selected" {
			continue
		}
		if selected == nil || priority[states[i].State] > priority[selected.State] {
			selected = &states[i]
		}
	}
	return selected
}

func accountMatchesCacheGuardGroup(account *Account, groupID int64) bool {
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	for _, group := range account.Groups {
		if group != nil && group.ID == groupID {
			return true
		}
	}
	for _, group := range account.AccountGroups {
		if group.GroupID == groupID {
			return true
		}
	}
	return false
}

func applyCacheGuardGroupSummary(item *UpstreamCacheGuardState, summary UpstreamCacheGuardSummary) {
	item.State, item.Reason = summary.State, summary.Reason
	item.RatePercent, item.ValidRequests = summary.RatePercent, summary.ValidRequests
	item.UpdatedAt, item.CooldownUntil = summary.UpdatedAt, summary.CooldownUntil
	item.RecoveryStartedAt, item.RecoveryValidRequests = summary.RecoveryStartedAt, summary.RecoveryValidRequests
	item.RecoveryRatePercent, item.RecoverySessions = summary.RecoveryRatePercent, summary.RecoverySessions
	item.OrdinaryRecovery = summary.ordinaryRecovery
}

func upstreamCacheGuardSummary(a upstreamcacheguard.AccountSnapshot, fresh bool, settings UpstreamCacheGuardSettings) UpstreamCacheGuardSummary {
	return upstreamCacheGuardSummaryAt(a, fresh, settings, time.Now().UTC())
}

func upstreamCacheGuardSummaryAt(a upstreamcacheguard.AccountSnapshot, fresh bool, settings UpstreamCacheGuardSettings, at time.Time) UpstreamCacheGuardSummary {
	item := UpstreamCacheGuardSummary{State: "insufficient", Reason: "insufficient_samples", WindowMinutes: settings.WindowMinutes}
	if !fresh {
		item.State, item.Reason = "unavailable", "snapshot_unavailable"
		return item
	}
	if a.Phase == upstreamcacheguard.Unknown {
		item.UpdatedAt = cacheGuardTime(a.UpdatedAt)
		item.State, item.Reason = "unavailable", "evidence_expired"
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
		item.ordinaryRecovery = upstreamcacheguard.OrdinaryRecoveryStatusAt(a.AccountState, settings, at)
		if a.ValidRequests > settings.MinValidRequests && a.CachePercent >= settings.ThresholdPercent {
			item.Reason = "waiting_recovery_validation"
		}
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
			item.OrdinaryRecovery = summary.ordinaryRecovery
			item.TrialParticipant = trialGroups[id]
		}
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].GroupID < result[j].GroupID })
	return result
}
