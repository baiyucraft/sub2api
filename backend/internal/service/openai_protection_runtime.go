package service

import (
	"sort"
)

func accountUpstreamKeyID(a *Account) int64 {
	if a != nil && a.UpstreamKeyID != nil {
		return *a.UpstreamKeyID
	}
	return 0
}

func (s *OpenAIGatewayService) syncTTFTProtectionPolicies() {
	if s == nil {
		return
	}
	var snapshot *GroupSchedulingProtectionPolicySnapshot
	var policies map[int64]GroupSchedulingProtectionResolvedPolicy
	if provider, ok := s.groupProtectionPolicyResolver.(interface {
		ProtectionPoliciesSnapshot() *GroupSchedulingProtectionPolicySnapshot
	}); ok {
		snapshot = provider.ProtectionPoliciesSnapshot()
		if snapshot == nil {
			return
		}
		policies = snapshot.policies
	} else if provider, ok := s.groupProtectionPolicyResolver.(interface {
		ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy
	}); ok {
		policies = provider.ProtectionPolicies()
	} else {
		return
	}
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	defer g.mu.Unlock()
	if snapshot != nil && (g.policySnapshot == snapshot || (g.policySnapshot != nil && snapshot.revision < g.policySnapshot.revision)) {
		return
	}
	ids := make([]int64, 0, len(policies))
	for id := range policies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if policies[ids[i]].TTFT.Enabled != policies[ids[j]].TTFT.Enabled {
			return policies[ids[i]].TTFT.Enabled
		}
		return ids[i] < ids[j]
	})
	next := make(map[int64]OpenAITTFTGuardConfigSnapshot)
	for _, id := range ids {
		if len(next) >= g.maxEntries {
			break
		}
		p := policies[id]
		next[id] = OpenAITTFTGuardConfigSnapshot{Enabled: p.TTFT.Enabled, Threshold: p.TTFT.Threshold, MinSamples: p.TTFT.MinSamples, Source: p.TTFT.Source, GroupName: p.TTFT.GroupName}
	}
	g.groupConfigs = next
	g.policiesManaged = true
	g.policySnapshot = snapshot
	g.inheritedKnown = false
	g.recountActivePoliciesLocked()
	g.pruneInactivePoliciesLocked()
}

// claimTrial cannot mint a ticket from a diagnostic/selection read. Tickets
// are reserved only by reserveTrialOpportunity after real allocations funded it.
func (g *openAITTFTGuard) claimTrial(groupID int64, a *Account, model string, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTTrial {
	return nil
}
