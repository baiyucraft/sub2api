package admin

import (
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accountTTFTProtectionReader interface {
	TTFTProtectionSnapshots(int64, int64, int64, service.OpenAITTFTGuardConfigSnapshot) []service.TTFTProtectionModelView
}

type accountProtectionPolicies interface {
	ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy
}

type accountProtectionPagePolicies struct {
	policies map[int64]service.GroupSchedulingProtectionResolvedPolicy
	selected map[string]bool
}

func (p accountProtectionPagePolicies) ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy {
	return p.policies
}
func (p accountProtectionPagePolicies) PlatformSelected(platform string) bool {
	return p.selected[platform]
}
func (p accountProtectionPagePolicies) ProtectionGroupPlatform(id int64) (string, bool) {
	policy, ok := p.policies[id]
	platform := policy.GroupPlatform
	if platform == "" {
		platform = service.PlatformOpenAI
	}
	return platform, ok
}

type accountProtectionModelKey struct{ accountID, keyID, groupID int64 }
type accountProtectionPageTTFT map[accountProtectionModelKey][]service.TTFTProtectionModelView

func (r accountProtectionPageTTFT) OpenAITTFTGuardDegradations([]int64) map[int64][]service.OpenAITTFTGuardDegradation {
	return nil
}
func (r accountProtectionPageTTFT) TTFTProtectionSnapshots(accountID, keyID, groupID int64, _ service.OpenAITTFTGuardConfigSnapshot) []service.TTFTProtectionModelView {
	// Platform gating mutates projected model views, so give each consumer ownership.
	return append([]service.TTFTProtectionModelView{}, r[accountProtectionModelKey{accountID, keyID, groupID}]...)
}

// upstreamProtectionsBatch fixes policy contents once and snapshots only the
// current page's account/key/model projections in one short runtime read.
func (h *AccountHandler) upstreamProtectionsBatch(accounts []service.Account, caches map[int64]service.UpstreamCacheGuardAccountView) map[int64]*AccountUpstreamProtection {
	return h.upstreamProtectionsBatchAt(accounts, caches, time.Now())
}

func (h *AccountHandler) upstreamProtectionsBatchAt(accounts []service.Account, caches map[int64]service.UpstreamCacheGuardAccountView, evaluatedAt time.Time) map[int64]*AccountUpstreamProtection {
	if h.isSimpleMode() || len(accounts) == 0 {
		return nil
	}
	bound := false
	for i := range accounts {
		if accounts[i].IsUpstreamBound() {
			bound = true
			break
		}
	}
	if !bound {
		return nil
	}
	page := *h
	if h.protectionPolicyReader != nil {
		var policies map[int64]service.GroupSchedulingProtectionResolvedPolicy
		if snapshots, ok := h.protectionPolicyReader.(interface {
			ProtectionPoliciesSnapshot() *service.GroupSchedulingProtectionPolicySnapshot
		}); ok {
			policies = snapshots.ProtectionPoliciesSnapshot().Policies()
		} else {
			policies = h.protectionPolicyReader.ProtectionPolicies()
		}
		selected := make(map[string]bool)
		platforms, _ := h.protectionPolicyReader.(interface{ PlatformSelected(string) bool })
		remember := func(platform string) {
			if _, ok := selected[platform]; !ok {
				selected[platform] = service.SchedulingProtectionPlatformSelected(platforms, platform)
			}
		}
		remember(service.PlatformOpenAI)
		for _, policy := range policies {
			remember(policy.GroupPlatform)
		}
		for i := range accounts {
			remember(accounts[i].Platform)
		}
		page.protectionPolicyReader = accountProtectionPagePolicies{policies: policies, selected: selected}
	}
	if batch, ok := h.ttftGuardReader.(interface {
		TTFTProtectionSnapshotsBatch([]service.TTFTProtectionSnapshotRequest) [][]service.TTFTProtectionModelView
	}); ok {
		requests := make([]service.TTFTProtectionSnapshotRequest, 0, len(accounts)*2)
		policies := map[int64]service.GroupSchedulingProtectionResolvedPolicy{}
		if page.protectionPolicyReader != nil {
			policies = page.protectionPolicyReader.ProtectionPolicies()
		}
		for i := range accounts {
			account := &accounts[i]
			if !account.IsUpstreamBound() || account.Platform != service.PlatformOpenAI {
				continue
			}
			requests = append(requests, service.TTFTProtectionSnapshotRequest{AccountID: account.ID, KeyID: *account.UpstreamKeyID})
			ids := append([]int64{}, account.GroupIDs...)
			for _, group := range account.Groups {
				if group != nil {
					ids = append(ids, group.ID)
				}
			}
			seen := make(map[int64]bool, len(ids))
			for _, id := range ids {
				if seen[id] {
					continue
				}
				seen[id] = true
				policy, exists := policies[id]
				if !exists {
					continue
				}
				requests = append(requests, service.TTFTProtectionSnapshotRequest{AccountID: account.ID, KeyID: *account.UpstreamKeyID, GroupID: id, Config: service.OpenAITTFTGuardConfigSnapshot{Enabled: policy.TTFT.Enabled, Threshold: policy.TTFT.Threshold, MinSamples: policy.TTFT.MinSamples, Source: policy.TTFT.Source, GroupName: policy.TTFT.GroupName}})
			}
		}
		var models [][]service.TTFTProtectionModelView
		if fixed, ok := h.ttftGuardReader.(interface {
			TTFTProtectionSnapshotsBatchAt([]service.TTFTProtectionSnapshotRequest, time.Time) [][]service.TTFTProtectionModelView
		}); ok {
			models = fixed.TTFTProtectionSnapshotsBatchAt(requests, evaluatedAt)
		} else {
			models = batch.TTFTProtectionSnapshotsBatch(requests)
		}
		reader := make(accountProtectionPageTTFT, len(requests))
		for i, request := range requests {
			if i < len(models) {
				reader[accountProtectionModelKey{request.AccountID, request.KeyID, request.GroupID}] = models[i]
			}
		}
		page.ttftGuardReader = reader
	}
	result := make(map[int64]*AccountUpstreamProtection, len(accounts))
	for i := range accounts {
		result[accounts[i].ID] = page.upstreamProtection(&accounts[i], caches[accounts[i].ID])
	}
	return result
}

type AccountUpstreamProtection struct {
	PlatformSelected bool                               `json:"platform_selected"`
	Cache            *service.UpstreamCacheGuardSummary `json:"cache"`
	TTFTModels       []service.TTFTProtectionModelView  `json:"ttft_models"`
	Groups           []AccountUpstreamProtectionGroup   `json:"groups"`
}

type AccountUpstreamProtectionGroup struct {
	PlatformSelected bool                           `json:"platform_selected"`
	GroupID          int64                          `json:"group_id"`
	GroupName        string                         `json:"group_name"`
	Priority         string                         `json:"priority"`
	PrioritySource   string                         `json:"priority_source"`
	TTFT             AccountUpstreamTTFTProjection  `json:"ttft"`
	Cache            AccountUpstreamCacheProjection `json:"cache"`
}

type AccountUpstreamTTFTProjection struct {
	State       string                            `json:"state"`
	ThresholdMs int64                             `json:"threshold_ms"`
	MinSamples  int                               `json:"min_samples"`
	Source      string                            `json:"source"`
	Models      []service.TTFTProtectionModelView `json:"models"`
}

type AccountUpstreamCacheProjection struct {
	service.UpstreamCacheGuardState
	Source string `json:"source"`
}

func (h *AccountHandler) SetSchedulingProtectionPolicyService(policies *service.GroupSchedulingProtectionPolicyService) {
	h.protectionPolicyReader = policies
}

func accountCurrentKeyTTFTDegradations(account *service.Account, degradations []service.OpenAITTFTGuardDegradation, readers ...any) []service.OpenAITTFTGuardDegradation {
	if len(readers) > 0 && account != nil {
		platforms, _ := readers[0].(interface{ PlatformSelected(string) bool })
		if !service.SchedulingProtectionPlatformSelected(platforms, account.Platform) {
			return nil
		}
		selected := make([]service.OpenAITTFTGuardDegradation, 0, len(degradations))
		for _, degradation := range degradations {
			if service.SchedulingProtectionGroupSelected(readers[0], degradation.GroupID) {
				selected = append(selected, degradation)
			}
		}
		degradations = selected
	}
	if account == nil || !account.IsUpstreamBound() {
		return degradations
	}
	current := make([]service.OpenAITTFTGuardDegradation, 0, len(degradations))
	for _, degradation := range degradations {
		if degradation.UpstreamKeyID == *account.UpstreamKeyID {
			current = append(current, degradation)
		}
	}
	return current
}

func (h *AccountHandler) upstreamProtection(account *service.Account, cache service.UpstreamCacheGuardAccountView) *AccountUpstreamProtection {
	if account == nil || !account.IsUpstreamBound() || h.isSimpleMode() {
		return nil
	}
	reader, _ := h.ttftGuardReader.(accountTTFTProtectionReader)
	if reader == nil && h.protectionPolicyReader == nil {
		return nil
	}
	platforms, _ := h.protectionPolicyReader.(interface{ PlatformSelected(string) bool })
	selected := service.SchedulingProtectionPlatformSelected(platforms, account.Platform)
	view := &AccountUpstreamProtection{PlatformSelected: selected, Cache: cache.Summary, TTFTModels: []service.TTFTProtectionModelView{}, Groups: []AccountUpstreamProtectionGroup{}}
	if !selected && view.Cache != nil {
		summary := *view.Cache
		summary.State, summary.Reason = "platform_not_selected", "platform_not_selected"
		summary.RecoveryValidRequests, summary.RecoverySessions = 0, 0
		summary.RecoveryRatePercent, summary.RecoveryStartedAt, summary.CooldownUntil = nil, nil, nil
		view.Cache = &summary
	}
	keyID := int64(0)
	if account.UpstreamKeyID != nil {
		keyID = *account.UpstreamKeyID
	}
	if reader != nil && account.Platform == service.PlatformOpenAI {
		view.TTFTModels = reader.TTFTProtectionSnapshots(account.ID, keyID, 0, service.OpenAITTFTGuardConfigSnapshot{})
	}
	policies := map[int64]service.GroupSchedulingProtectionResolvedPolicy{}
	if h.protectionPolicyReader != nil {
		policies = h.protectionPolicyReader.ProtectionPolicies()
	}
	cacheGroups := map[int64]service.UpstreamCacheGuardState{}
	for _, state := range cache.States {
		cacheGroups[state.GroupID] = state
	}
	ids := append([]int64{}, account.GroupIDs...)
	seen := map[int64]bool{}
	for _, group := range account.Groups {
		if group != nil {
			ids = append(ids, group.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		policy, ok := policies[id]
		if !ok {
			state, reason := "unavailable", "snapshot_unavailable"
			if !selected {
				state, reason = "platform_not_selected", "platform_not_selected"
			}
			view.Groups = append(view.Groups, AccountUpstreamProtectionGroup{PlatformSelected: selected, GroupID: id, Priority: "cache_first", PrioritySource: "unavailable", TTFT: AccountUpstreamTTFTProjection{State: state, Source: "unavailable", Models: []service.TTFTProtectionModelView{}}, Cache: AccountUpstreamCacheProjection{UpstreamCacheGuardState: service.UpstreamCacheGuardState{GroupID: id, State: state, Reason: reason}, Source: "unavailable"}})
			continue
		}
		state, cacheKnown := cacheGroups[id]
		if !cacheKnown {
			state = service.UpstreamCacheGuardState{GroupID: id, State: "disabled", Reason: "disabled"}
			if policy.Cache.Mode != "disabled" {
				state.State, state.Reason = "unavailable", "snapshot_unavailable"
			}
		}
		state.ThresholdPercent, state.RecoveryThresholdPercent, state.MinValidRequests = policy.Cache.ThresholdPercent, policy.Cache.RecoveryThresholdPercent, policy.Cache.MinValidRequests
		state.PolicyMode, state.PolicySource, state.SchedulingPriority = policy.CacheMode, policy.CacheSource, policy.Priority
		ttft := AccountUpstreamTTFTProjection{State: "disabled", ThresholdMs: policy.TTFT.Threshold.Milliseconds(), MinSamples: policy.TTFT.MinSamples, Source: policy.TTFT.Source, Models: []service.TTFTProtectionModelView{}}
		if policy.TTFT.Enabled && account.Platform == service.PlatformOpenAI {
			ttft.State = "unavailable"
			if reader != nil {
				ttft.State = "insufficient"
			}
		}
		if reader != nil && account.Platform == service.PlatformOpenAI {
			ttft.Models = reader.TTFTProtectionSnapshots(account.ID, keyID, id, service.OpenAITTFTGuardConfigSnapshot{Enabled: policy.TTFT.Enabled, Threshold: policy.TTFT.Threshold, MinSamples: policy.TTFT.MinSamples, Source: policy.TTFT.Source, GroupName: policy.TTFT.GroupName})
			if policy.TTFT.Enabled {
				severity := map[string]int{"normal": 1, "insufficient": 2, "recovering": 3, "draining": 4, "unavailable": 5}
				best := 0
				for _, model := range ttft.Models {
					if severity[model.State] > best {
						best = severity[model.State]
						ttft.State = model.State
					}
				}
			}
		}
		groupSelected := selected && service.SchedulingProtectionGroupSelected(h.protectionPolicyReader, id)
		if !groupSelected {
			service.MarkUpstreamCacheGuardPlatformNotSelected(&state)
			ttft.State = "platform_not_selected"
			for i := range ttft.Models {
				ttft.Models[i].State, ttft.Models[i].RecoverySamples = "platform_not_selected", 0
			}
		}
		view.Groups = append(view.Groups, AccountUpstreamProtectionGroup{PlatformSelected: groupSelected, GroupID: id, GroupName: policy.TTFT.GroupName, Priority: policy.Priority, PrioritySource: policy.PrioritySource, TTFT: ttft, Cache: AccountUpstreamCacheProjection{UpstreamCacheGuardState: state, Source: policy.CacheSource}})
	}
	return view
}
