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
	if ok && q.NeedsRevalidation {
		return 1
	}
	if ok && q.State == "normal" {
		return 0
	}
	if !ok || q.State == "insufficient" || q.SampleCount < uint64(min) {
		return 1
	}
	return 0
}

// Scheduling neutrality is distinct from migration/trial qualification. A new
// account may compete before it has samples, but cannot prove a better target.
func jointTTFTSchedulingClass(id int64, quality map[int64]forkscheduling.TTFTQuality, min int) int {
	q, ok := quality[id]
	if ok && q.Degraded {
		return 2
	}
	if (ok && (q.NeedsRevalidation || q.State == "unavailable")) || quality == nil {
		return 1
	}
	return 0
}

func jointTTFTLatencyEvidence(id int64, quality map[int64]forkscheduling.TTFTQuality, min int) (float64, cacheGuardLatencyEvidence) {
	q, ok := quality[id]
	if quality == nil || (ok && q.State == "unavailable") {
		return 0, cacheGuardLatencyUnavailable
	}
	if !ok || (!q.Degraded && !q.NeedsRevalidation && q.State != "normal" && (q.State == "insufficient" || q.SampleCount < uint64(min))) {
		return 0, cacheGuardLatencyCold
	}
	if q.SampleCount > 0 && q.EWMAms > 0 {
		return q.EWMAms, cacheGuardLatencyKnown
	}
	return 0, cacheGuardLatencyUnavailable
}

// Once both protections admit ordinary traffic, percentage/latency differences
// must not hide cheaper candidates from the native scheduler. Keep recovery
// nominations and non-qualified fallback layers, without changing their quota.
func jointQualifiedProtectionTiers(tiers [][]int64, qualified []int64, quality map[int64]forkscheduling.TTFTQuality, cfg OpenAITTFTGuardConfigSnapshot, nominations ...int64) [][]int64 {
	allowed := make(map[int64]bool, len(qualified))
	for _, id := range qualified {
		if !cfg.Enabled || jointTTFTSchedulingClass(id, quality, cfg.MinSamples) == 0 {
			allowed[id] = true
		}
	}
	members := map[int64]bool{}
	for _, tier := range tiers {
		for _, id := range tier {
			members[id] = true
		}
	}
	seen := map[int64]bool{}
	var out [][]int64
	for _, id := range nominations {
		if id > 0 && members[id] && !seen[id] {
			out = append(out, []int64{id})
			seen[id] = true
		}
	}
	var ordinary []int64
	for id := range members {
		if allowed[id] && !seen[id] {
			ordinary = append(ordinary, id)
			seen[id] = true
		}
	}
	sort.Slice(ordinary, func(i, j int) bool { return ordinary[i] < ordinary[j] })
	if len(ordinary) > 0 {
		out = append(out, ordinary)
	}
	for _, tier := range tiers {
		var fallback []int64
		for _, id := range tier {
			if !seen[id] {
				fallback = append(fallback, id)
				seen[id] = true
			}
		}
		if len(fallback) > 0 {
			out = append(out, fallback)
		}
	}
	return out
}

// A healthy active affinity is soft: a strictly cheaper, lower-priority-value
// ordinary target can replace it. Equal-priority traffic keeps its warm owner.
// This is separate from the cache/TTFT evidence required for quality migration.
func (s *OpenAIGatewayService) preferCheaperQualifiedAccount(ctx context.Context, p cacheGuardPreference, groupID int64, model, protocol string, accounts []Account, quality map[int64]forkscheduling.TTFTQuality, cfg OpenAITTFTGuardConfigSnapshot) cacheGuardPreference {
	if p.KeepAccountID <= 0 || !p.PriorityReallocationAllowed || p.AffinityExpired || protocol == "openai_ws" || s.protectionSessionResident(ctx, groupID) {
		return p
	}
	var source *Account
	for i := range accounts {
		if accounts[i].ID == p.KeepAccountID {
			source = &accounts[i]
			break
		}
	}
	if source == nil || !schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, source) {
		return p
	}
	sourceModel := canonicalOpenAIAccountSchedulingModel(source, model)
	sourceProtocol := cacheGuardAccountProtocol(ctx, source, sourceModel, protocol)
	qualified := map[int64]bool{}
	for _, id := range p.QualifiedIDs {
		qualified[id] = true
	}
	var cheaper []int64
	for i := range accounts {
		a := &accounts[i]
		if !qualified[a.ID] || !cheaperProtectionTarget(source, a) {
			continue
		}
		if cfg.Enabled && jointTTFTSchedulingClass(a.ID, quality, cfg.MinSamples) != 0 {
			continue
		}
		actualModel := canonicalOpenAIAccountSchedulingModel(a, model)
		if actualModel != sourceModel || cacheGuardAccountProtocol(ctx, a, actualModel, protocol) != sourceProtocol {
			continue
		}
		cheaper = append(cheaper, a.ID)
	}
	if len(cheaper) > 0 {
		sort.Slice(cheaper, func(i, j int) bool { return cheaper[i] < cheaper[j] })
		p.Migration, p.PriorityReallocation = true, true
		p.PriorityTargetIDs = make(map[int64]struct{}, len(cheaper))
		for _, id := range cheaper {
			p.PriorityTargetIDs[id] = struct{}{}
		}
		// Preserve proven quality-migration targets if every cheaper target is
		// unavailable, but never retry a candidate in two protection layers.
		tiers := [][]int64{cheaper}
		for _, tier := range p.Tiers {
			var remaining []int64
			for _, id := range tier {
				if _, used := p.PriorityTargetIDs[id]; !used {
					remaining = append(remaining, id)
				}
			}
			if len(remaining) > 0 {
				tiers = append(tiers, remaining)
			}
		}
		p.Tiers = tiers
	}
	return p
}

func cheaperProtectionTarget(source, target *Account) bool {
	if source == nil || target == nil || source.ID == target.ID || target.Priority >= source.Priority {
		return false
	}
	sourceRate, sourceKnown := upstreamSourceSchedulingRate(source)
	targetRate, targetKnown := upstreamSourceSchedulingRate(target)
	return sourceKnown && targetKnown && targetRate < sourceRate
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
	return jointTTFTOrderWithColdNeutrality(tiers, accounts, groupID, quality, min, priority, true)
}

func jointTTFTOrderWithColdNeutrality(tiers [][]int64, accounts []Account, groupID *int64, quality map[int64]forkscheduling.TTFTQuality, min int, priority string, coldNeutral bool) [][]int64 {
	var out [][]int64
	classify := jointTTFTSchedulingClass
	if !coldNeutral {
		classify = jointTTFTClass
	}
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
			cold     bool
			latency  int
			tier     int
			position int
		}
		var candidates []candidate
		for tierIndex, tier := range tiers {
			for position, id := range tier {
				if members[id] && classify(id, quality, min) == class {
					ewma, evidence := jointTTFTLatencyEvidence(id, quality, min)
					if !coldNeutral {
						q, ok := quality[id]
						ewma, evidence = q.EWMAms, cacheGuardLatencyUnavailable
						if ok && ewma > 0 {
							evidence = cacheGuardLatencyKnown
						}
					}
					candidates = append(candidates, candidate{id: id, ewma: ewma, known: evidence == cacheGuardLatencyKnown, cold: evidence == cacheGuardLatencyCold, tier: tierIndex, position: position})
				}
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if priority == SchedulingPriorityTTFTFirst {
			// Form latency bands from known evidence before merging neutral TTFT
			// into the first band. Retain each candidate's cache band: neutrality
			// on latency must not erase a known lower cache rate.
			var measured []float64
			for _, candidate := range candidates {
				if candidate.known {
					measured = append(measured, candidate.ewma)
				}
			}
			sort.Float64s(measured)
			ranks := map[float64]int{}
			for _, value := range measured {
				if _, exists := ranks[value]; !exists {
					ranks[value] = len(ranks)
				}
			}
			for i := range candidates {
				if candidates[i].known {
					candidates[i].latency = ranks[candidates[i].ewma]
				} else if !candidates[i].cold {
					candidates[i].latency = len(ranks) + 1
				}
			}
			sort.SliceStable(candidates, func(i, j int) bool {
				a, b := candidates[i], candidates[j]
				if a.latency != b.latency {
					return a.latency < b.latency
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
				for end < len(candidates) && candidates[end].latency == candidates[start].latency && candidates[end].tier == candidates[start].tier {
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
				if members[id] && classify(id, quality, min) == class {
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
	// A cache-proven migration target may still have unknown latency. Preserve
	// the original preference for proven fast targets; cold neutrality grants
	// natural new traffic, not a new reason to move an existing conversation.
	p.Tiers = jointTTFTOrderWithColdNeutrality(p.Tiers, accounts, &groupID, quality, cfg.MinSamples, priority, false)
	return p
}
