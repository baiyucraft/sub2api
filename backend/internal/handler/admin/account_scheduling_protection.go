package admin

import (
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type accountTTFTProtectionReader interface {
	TTFTProtectionSnapshots(int64, int64, int64, service.OpenAITTFTGuardConfigSnapshot) []service.TTFTProtectionModelView
}

type accountProtectionPolicies interface {
	ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy
}

type AccountUpstreamProtection struct {
	Cache      *service.UpstreamCacheGuardSummary `json:"cache"`
	TTFTModels []service.TTFTProtectionModelView  `json:"ttft_models"`
	Groups     []AccountUpstreamProtectionGroup   `json:"groups"`
}

type AccountUpstreamProtectionGroup struct {
	GroupID        int64                          `json:"group_id"`
	GroupName      string                         `json:"group_name"`
	Priority       string                         `json:"priority"`
	PrioritySource string                         `json:"priority_source"`
	TTFT           AccountUpstreamTTFTProjection  `json:"ttft"`
	Cache          AccountUpstreamCacheProjection `json:"cache"`
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

func accountCurrentKeyTTFTDegradations(account *service.Account, degradations []service.OpenAITTFTGuardDegradation) []service.OpenAITTFTGuardDegradation {
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
	view := &AccountUpstreamProtection{Cache: cache.Summary, TTFTModels: []service.TTFTProtectionModelView{}, Groups: []AccountUpstreamProtectionGroup{}}
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
			view.Groups = append(view.Groups, AccountUpstreamProtectionGroup{GroupID: id, Priority: "cache_first", PrioritySource: "unavailable", TTFT: AccountUpstreamTTFTProjection{State: "unavailable", Source: "unavailable", Models: []service.TTFTProtectionModelView{}}, Cache: AccountUpstreamCacheProjection{UpstreamCacheGuardState: service.UpstreamCacheGuardState{GroupID: id, State: "unavailable", Reason: "snapshot_unavailable"}, Source: "unavailable"}})
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
				severity := map[string]int{"normal": 1, "insufficient": 2, "expired": 3, "recovering": 4, "draining": 5, "unavailable": 6}
				best := 0
				for _, model := range ttft.Models {
					if severity[model.State] > best {
						best = severity[model.State]
						ttft.State = model.State
					}
				}
			}
		}
		view.Groups = append(view.Groups, AccountUpstreamProtectionGroup{GroupID: id, GroupName: policy.TTFT.GroupName, Priority: policy.Priority, PrioritySource: policy.PrioritySource, TTFT: ttft, Cache: AccountUpstreamCacheProjection{UpstreamCacheGuardState: state, Source: policy.CacheSource}})
	}
	return view
}
