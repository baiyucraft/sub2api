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
	if !ok || q.SampleCount < uint64(min) {
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
	if priority != SchedulingPriorityTTFTFirst {
		for _, tier := range tiers {
			for class := 0; class < 3; class++ {
				var ids []int64
				for _, id := range tier {
					if jointTTFTClass(id, quality, min) == class {
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
	// Keep preferred/priority pools outside either soft policy. Within each pool
	// preserve cache ordering independently for normal, unknown and slow states.
	for _, pool := range jointTTFTOnlyTiers(accounts, groupID) {
		members := map[int64]bool{}
		for _, id := range pool {
			members[id] = true
		}
		for class := 0; class < 3; class++ {
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
	cacheTargets := map[int64]bool{}
	if p.Migration {
		for _, tier := range p.Tiers {
			for _, id := range tier {
				if priority != SchedulingPriorityTTFTFirst || !quality[id].Degraded {
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
			if a.ID != source.ID && canonicalOpenAIAccountSchedulingModel(a, model) == sourceModel && cacheGuardSamePool(a, first, &groupID) && ok && !q.Degraded && q.SampleCount >= uint64(cfg.MinSamples) && q.EWMAms > 0 && q.EWMAms < float64(cfg.Threshold.Milliseconds()) {
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
	return p
}
