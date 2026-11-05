package service

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

func jointTTFTOnlyTiers(accounts []Account, groupID *int64) [][]int64 {
	if len(accounts) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		ids = append(ids, accounts[i].ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return [][]int64{ids}
}

func jointTTFTClass(id int64, quality map[int64]forkscheduling.TTFTQuality, min int) int {
	q, ok := quality[id]
	if ok && q.Degraded {
		return 2
	}
	if ok && q.State == "normal" {
		return 0
	}
	if !ok || q.State == "insufficient" || q.SampleCount < uint64(min) {
		return 1
	}
	return 0
}

func jointRecoveryTiers(tiers [][]int64, accounts []Account, groupID *int64, nominated []int64, first bool) [][]int64 {
	var out [][]int64
	members := map[int64]bool{}
	for i := range accounts {
		members[accounts[i].ID] = true
	}
	var trial []int64
	for _, id := range nominated {
		if members[id] {
			trial = append(trial, id)
		}
	}
	if first && len(trial) > 0 {
		out = append(out, trial)
	}
	for _, tier := range tiers {
		var part []int64
		for _, id := range tier {
			if members[id] {
				part = append(part, id)
			}
		}
		if len(part) > 0 {
			out = append(out, part)
		}
	}
	if !first && len(trial) > 0 {
		out = append(out, trial)
	}
	return out
}

func jointTTFTOrder(tiers [][]int64, accounts []Account, groupID *int64, quality map[int64]forkscheduling.TTFTQuality, min int, priority string) [][]int64 {
	var out [][]int64
	// Quality state precedes account priorities. The original scheduler keeps
	// preferred pools and priority/score ordering within each protection tier.
	members := map[int64]bool{}
	for i := range accounts {
		members[accounts[i].ID] = true
	}
	for class := 0; class < 3; class++ {
		// cache_first retains the cache guard's tier order. For ttft_first,
		// flatten the same-quality candidates and order by measured EWMA;
		// cache tier and source position remain deterministic tie breakers.
		type candidate struct {
			id       int64
			ewma     float64
			known    bool
			tier     int
			position int
		}
		var candidates []candidate
		for tierIndex, tier := range tiers {
			for position, id := range tier {
				if members[id] && jointTTFTClass(id, quality, min) == class {
					q, known := quality[id]
					candidates = append(candidates, candidate{id: id, ewma: q.EWMAms, known: known && q.EWMAms > 0, tier: tierIndex, position: position})
				}
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if priority == SchedulingPriorityTTFTFirst {
			sort.SliceStable(candidates, func(i, j int) bool {
				a, b := candidates[i], candidates[j]
				if a.known != b.known {
					return a.known
				}
				if a.known && a.ewma != b.ewma {
					return a.ewma < b.ewma
				}
				if a.tier != b.tier {
					return a.tier < b.tier
				}
				if a.position != b.position {
					return a.position < b.position
				}
				return a.id < b.id
			})
			for start := 0; start < len(candidates); {
				end := start + 1
				for end < len(candidates) && candidates[end].known == candidates[start].known && candidates[end].tier == candidates[start].tier && (!candidates[start].known || candidates[end].ewma == candidates[start].ewma) {
					end++
				}
				ids := make([]int64, 0, end-start)
				for _, candidate := range candidates[start:end] {
					ids = append(ids, candidate.id)
				}
				out = append(out, ids)
				start = end
			}
			continue
		}
		for _, tier := range tiers {
			var ids []int64
			for _, id := range tier {
				if members[id] && jointTTFTClass(id, quality, min) == class {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				out = append(out, ids)
			}
		}
	}
	return out
}

func (s *OpenAIGatewayService) jointSessionPreference(ctx context.Context, p cacheGuardPreference, groupID int64, model, protocol string, accounts []Account, quality map[int64]forkscheduling.TTFTQuality, cfg OpenAITTFTGuardConfigSnapshot, priority string) cacheGuardPreference {
	if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, groupID) {
		return cacheGuardPreference{}
	}
	if p.KeepAccountID <= 0 {
		return p
	}
	if s.protectionSessionResident(ctx, groupID) {
		p.Migration = false
		p.Tiers = nil
		return p
	}
	var source *Account
	for i := range accounts {
		a := &accounts[i]
		if a.ID == p.KeepAccountID {
			source = a
		}
	}
	if source == nil {
		return p
	}
	if !schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, source) {
		p.Migration, p.Tiers = false, nil
		return p
	}
	cacheTargets := map[int64]bool{}
	cacheTierByID := map[int64]int{}
	if p.Migration {
		for tierIndex, tier := range p.Tiers {
			for _, id := range tier {
				if !quality[id].Degraded {
					cacheTargets[id] = true
					cacheTierByID[id] = tierIndex
				}
			}
		}
	}
	slowTrigger := cfg.Enabled && quality[source.ID].Degraded && s.sessionTTFTCanMigrate(ctx, groupID, source, canonicalOpenAIAccountSchedulingModel(source, model), cacheGuardAccountProtocol(ctx, source, canonicalOpenAIAccountSchedulingModel(source, model), protocol), cfg.Threshold)
	sourceModel := canonicalOpenAIAccountSchedulingModel(source, model)
	var fast []int64
	if slowTrigger {
		for i := range accounts {
			a := &accounts[i]
			q, ok := quality[a.ID]
			if schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, a) && a.ID != source.ID && canonicalOpenAIAccountSchedulingModel(a, model) == sourceModel && ok && jointTTFTClass(a.ID, quality, cfg.MinSamples) == 0 && q.EWMAms > 0 && q.EWMAms < float64(cfg.Threshold.Milliseconds()) {
				fast = append(fast, a.ID)
			}
		}
	}
	sort.SliceStable(fast, func(i, j int) bool {
		a, b := quality[fast[i]], quality[fast[j]]
		if a.EWMAms == b.EWMAms {
			return fast[i] < fast[j]
		}
		return a.EWMAms < b.EWMAms
	})
	var intersection, fastOnly []int64
	for _, id := range fast {
		if cacheTargets[id] {
			intersection = append(intersection, id)
			delete(cacheTargets, id)
		} else {
			fastOnly = append(fastOnly, id)
		}
	}
	// Cache-only evidence keeps its five-point bands so account priority and
	// preference still work inside a band.
	cacheTiersFor := func(targets map[int64]bool) [][]int64 {
		var tiers [][]int64
		for _, tier := range p.Tiers {
			var ids []int64
			for _, id := range tier {
				if targets[id] {
					ids = append(ids, id)
					delete(targets, id)
				}
			}
			if len(ids) > 0 {
				tiers = append(tiers, ids)
			}
		}
		return tiers
	}
	// Intersection candidates retain the existing fast-first order, even for
	// cache_first. Merge only equivalent evidence, preserving each band's first
	// occurrence rather than reordering different cache bands or EWMA values.
	type intersectionBand struct {
		cacheTier int
		ewma      float64
	}
	intersectionIndices := map[intersectionBand]int{}
	var intersectionTiers [][]int64
	for _, id := range intersection {
		band := intersectionBand{cacheTier: cacheTierByID[id], ewma: quality[id].EWMAms}
		index, exists := intersectionIndices[band]
		if !exists {
			index = len(intersectionTiers)
			intersectionIndices[band] = index
			intersectionTiers = append(intersectionTiers, nil)
		}
		intersectionTiers[index] = append(intersectionTiers[index], id)
	}
	cacheOnlyTiers := cacheTiersFor(cacheTargets)
	var fastOnlyTiers [][]int64
	for start := 0; start < len(fastOnly); {
		end := start + 1
		for end < len(fastOnly) && quality[fastOnly[end]].EWMAms == quality[fastOnly[start]].EWMAms {
			end++
		}
		fastOnlyTiers = append(fastOnlyTiers, fastOnly[start:end])
		start = end
	}
	p.Tiers = intersectionTiers
	if priority == SchedulingPriorityTTFTFirst {
		p.Tiers = append(p.Tiers, fastOnlyTiers...)
		p.Tiers = append(p.Tiers, cacheOnlyTiers...)
	} else {
		p.Tiers = append(p.Tiers, cacheOnlyTiers...)
		p.Tiers = append(p.Tiers, fastOnlyTiers...)
	}
	p.Migration = len(p.Tiers) > 0
	p.Tiers = jointTTFTOrder(p.Tiers, accounts, &groupID, quality, cfg.MinSamples, priority)
	return p
}
