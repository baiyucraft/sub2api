package service

import (
	"context"
	"sort"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

func jointTTFTOnlyTiers(accounts []Account, groupID *int64) [][]int64 {
	sorted := append([]Account(nil), accounts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if cacheGuardPoolBetter(&sorted[i], &sorted[j], groupID) {
			return true
		}
		if cacheGuardPoolBetter(&sorted[j], &sorted[i], groupID) {
			return false
		}
		return sorted[i].ID < sorted[j].ID
	})
	var out [][]int64
	var previous *Account
	for i := range sorted {
		a := &sorted[i]
		if previous == nil || !cacheGuardSamePool(a, previous, groupID) {
			out = append(out, []int64{})
		}
		out[len(out)-1] = append(out[len(out)-1], a.ID)
		previous = a
	}
	return out
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
	for _, pool := range jointTTFTOnlyTiers(accounts, groupID) {
		members := map[int64]bool{}
		for _, id := range pool {
			members[id] = true
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
	}
	return out
}

func jointTTFTOrder(tiers [][]int64, accounts []Account, groupID *int64, quality map[int64]forkscheduling.TTFTQuality, min int, priority string) [][]int64 {
	var out [][]int64
	// Keep preferred/priority pools outside either soft policy. Quality state is
	// always the first ordering key; cache/TTFT preference is applied only among
	// candidates in the same state.
	for _, pool := range jointTTFTOnlyTiers(accounts, groupID) {
		members := map[int64]bool{}
		for _, id := range pool {
			members[id] = true
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
	var first *Account
	for i := range accounts {
		a := &accounts[i]
		if a.ID == p.KeepAccountID {
			source = a
		}
		if first == nil || cacheGuardPoolBetter(a, first, &groupID) {
			first = a
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
	if p.Migration {
		for _, tier := range p.Tiers {
			for _, id := range tier {
				if !quality[id].Degraded {
					cacheTargets[id] = true
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
			if schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, a) && a.ID != source.ID && canonicalOpenAIAccountSchedulingModel(a, model) == sourceModel && cacheGuardSamePool(a, first, &groupID) && ok && jointTTFTClass(a.ID, quality, cfg.MinSamples) == 0 && q.EWMAms > 0 && q.EWMAms < float64(cfg.Threshold.Milliseconds()) {
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
	var intersection, cacheOnly, fastOnly []int64
	for _, id := range fast {
		if cacheTargets[id] {
			intersection = append(intersection, id)
			delete(cacheTargets, id)
		} else {
			fastOnly = append(fastOnly, id)
		}
	}
	for _, tier := range p.Tiers {
		for _, id := range tier {
			if cacheTargets[id] {
				cacheOnly = append(cacheOnly, id)
				delete(cacheTargets, id)
			}
		}
	}
	p.Tiers = nil
	add := func(ids []int64) {
		for _, id := range ids {
			p.Tiers = append(p.Tiers, []int64{id})
		}
	}
	add(intersection)
	if priority == SchedulingPriorityTTFTFirst {
		add(fastOnly)
		add(cacheOnly)
	} else {
		add(cacheOnly)
		add(fastOnly)
	}
	p.Migration = len(p.Tiers) > 0
	p.Tiers = jointTTFTOrder(p.Tiers, accounts, &groupID, quality, cfg.MinSamples, priority)
	return p
}
