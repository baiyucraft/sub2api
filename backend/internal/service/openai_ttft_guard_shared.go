package service

import (
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

// A policy is a projection of shared observations, never a group-owned sample
// store. Identical thresholds share recovery progress regardless of source.
func openAITTFTGuardScopeFor(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) (int64, bool) {
	if groupID <= 0 || !validOpenAITTFTGuardConfig(cfg) {
		return 0, false
	}
	return cfg.Threshold.Milliseconds()*100 + int64(cfg.MinSamples), true
}

func (g *openAITTFTGuard) sampleKeyLocked(accountID, keyID int64, model string) (openAITTFTGuardKey, bool) {
	k, ok := openAITTFTGuardKeyFor(0, accountID, model)
	if keyID == 0 {
		keyID = g.accountKeys[accountID]
	}
	k.upstreamKeyID = keyID
	return k, ok
}

func (g *openAITTFTGuard) report(groupID, accountID int64, model string, success bool, ms *int, cfg OpenAITTFTGuardConfigSnapshot) {
	g.reportForKey(groupID, accountID, 0, model, success, ms, cfg)
}

func (g *openAITTFTGuard) reportForKey(groupID, accountID, keyID int64, model string, success bool, ms *int, cfg OpenAITTFTGuardConfigSnapshot) {
	if g == nil || groupID <= 0 || ms == nil || *ms <= 0 {
		return
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policiesManaged {
		if current, ok := g.groupConfigs[groupID]; ok {
			cfg = current
		} else {
			cfg.Enabled = false
		}
	} else {
		g.syncConfigLocked(groupID, cfg)
	}
	if g.activePolicies == 0 {
		return
	}
	now := g.now()
	g.deleteExpiredLocked(now)
	if keyID > 0 {
		g.accountKeys[accountID] = keyID
	}
	k, ok := g.sampleKeyLocked(accountID, keyID, model)
	if !ok {
		return
	}
	raw := g.observations[k]
	if raw != nil && now.Sub(raw.lastSampleAt) >= g.ttl {
		raw = nil
	}
	if raw == nil {
		if len(g.observations) >= g.maxEntries {
			var oldest openAITTFTGuardKey
			var at time.Time
			for id, e := range g.observations {
				if at.IsZero() || e.lastSampleAt.Before(at) {
					oldest, at = id, e.lastSampleAt
				}
			}
			g.deleteObservationLocked(oldest)
		}
		raw = &openAITTFTGuardEntry{}
		if g.observations[k] != nil {
			g.deleteObservationLocked(k)
		}
		g.observations[k] = raw
		g.indexObservationLocked(k)
	}
	if keyID > 0 {
		g.accountKeys[accountID] = keyID
	}
	raw.successes[raw.nextSample] = success
	raw.addSample(float64(*ms))
	raw.lastSampleAt, raw.lastTouchedAt = now, now
	raw.lastGroupID, raw.lastGroupName = groupID, cfg.GroupName
	// Each existing projection consumes the observation once. Recovery resets
	// only that projection's effective window, leaving raw and other policies.
	g.scheduleExpiryLocked(k, true, raw)
	for key := range g.projectionKeys[k] {
		e := g.entries[key]
		applyTTFTProjectionSample(e, float64(*ms), success, now)
		g.scheduleExpiryLocked(key, false, e)
	}
	if cfg.Enabled {
		g.projectionAtLocked(k, groupID, cfg, now)
	}
	g.recalculateNextExpiryLocked()
}

func applyTTFTProjectionSample(e *openAITTFTGuardEntry, sample float64, success bool, now time.Time) {
	e.addSample(sample)
	e.lastSampleAt, e.lastTouchedAt = now, now
	threshold := float64(e.config.Threshold.Milliseconds())
	if e.degraded {
		// Only an admitted recovery ticket may advance or reset this counter.
		return
	}
	if sample >= 1.5*threshold {
		e.consecutiveElevated++
	} else {
		e.consecutiveElevated = 0
	}
	switch {
	case sample >= 3*threshold:
		e.degraded, e.degradedReason = true, "critical_sample"
	case e.consecutiveElevated >= 2:
		e.degraded, e.degradedReason = true, "consecutive_elevated"
	case e.sampleCount >= uint64(e.config.MinSamples) && e.recentEWMA() >= threshold:
		e.degraded, e.degradedReason = true, "ewma"
	}
	if e.degraded {
		e.degradedAt = now
	}
}

func (g *openAITTFTGuard) projectionLocked(rawKey openAITTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTGuardEntry {
	return g.projectionAtLocked(rawKey, groupID, cfg, g.now())
}

func (g *openAITTFTGuard) projectionAtLocked(rawKey openAITTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot, now time.Time) *openAITTFTGuardEntry {
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	k := rawKey
	k.scopeID = scope
	if e := g.entries[k]; e != nil {
		return e
	}
	raw := g.observations[rawKey]
	if raw == nil || now.Sub(raw.lastSampleAt) >= g.ttl {
		return nil
	}
	g.evictLRULocked()
	e := deriveTTFTProjection(raw, groupID, cfg)
	g.nextEntryGeneration++
	e.generation = g.nextEntryGeneration
	g.entries[k] = e
	g.indexProjectionLocked(k)
	g.scheduleExpiryLocked(k, false, e)
	return e
}

func deriveTTFTProjection(raw *openAITTFTGuardEntry, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTGuardEntry {
	e := &openAITTFTGuardEntry{config: cfg, lastGroupID: groupID, lastGroupName: cfg.GroupName}
	// The cumulative count preserves min_samples > ring size; only bounded
	// recent evidence is replayed, never invented historical recovery samples.
	e.sampleCount = raw.sampleCount - uint64(raw.sampleLen)
	start := 0
	if raw.sampleLen == openAITTFTGuardSampleWindow {
		start = raw.nextSample
	}
	for i := 0; i < raw.sampleLen; i++ {
		j := (start + i) % openAITTFTGuardSampleWindow
		applyTTFTProjectionSample(e, raw.samples[j], raw.successes[j], raw.lastSampleAt)
	}
	return e
}

func (g *openAITTFTGuard) readProjectionLocked(key openAITTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTGuardEntry {
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	raw := g.observations[key]
	if raw == nil || g.now().Sub(raw.lastSampleAt) >= g.ttl {
		return nil
	}
	key.scopeID = scope
	if e := g.entries[key]; e != nil {
		return e
	}
	return deriveTTFTProjection(raw, groupID, cfg)
}

// Once policy snapshots own runtime configuration, a request captured before a
// policy change may read only the matching projection. It must never publish its
// older configuration back into the shared runtime or consume a recovery probe.
func (g *openAITTFTGuard) acceptRequestConfigLocked(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) bool {
	if !g.policiesManaged {
		g.syncConfigLocked(groupID, cfg)
		return true
	}
	current, exists := g.groupConfigs[groupID]
	return exists && sameOpenAITTFTGuardRuntimeConfig(current, normalizeOpenAITTFTGuardConfig(cfg))
}

func (g *openAITTFTGuard) syncConfigLocked(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
	if g.policiesManaged || groupID <= 0 {
		return
	}
	if _, exists := g.groupConfigs[groupID]; !exists && len(g.groupConfigs) >= g.maxEntries {
		for id := range g.groupConfigs {
			if id != groupID {
				if g.groupConfigs[id].Enabled {
					g.activePolicies--
				}
				delete(g.groupConfigs, id)
				break
			}
		}
	}
	next := normalizeOpenAITTFTGuardConfig(cfg)
	previous, exists := g.groupConfigs[groupID]
	g.groupConfigs[groupID] = next
	if exists && previous.Enabled {
		g.activePolicies--
	}
	if next.Enabled {
		g.activePolicies++
	}
	if !exists || previous.Enabled != next.Enabled || previous.Threshold != next.Threshold || previous.MinSamples != next.MinSamples || previous.Source != next.Source {
		g.inheritedKnown = false
		g.pruneInactivePoliciesLocked()
	}
}

func (g *openAITTFTGuard) pruneInactivePoliciesLocked() {
	active := map[int64]bool{}
	for id, cfg := range g.groupConfigs {
		if scope, ok := openAITTFTGuardScopeFor(id, cfg); ok && cfg.Enabled {
			active[scope] = true
		}
	}
	for k := range g.entries {
		if !active[k.scopeID] {
			g.deleteProjectionLocked(k)
		}
	}
	for k := range g.groupProbes {
		if !active[k.scopeID] {
			delete(g.groupProbes, k)
		}
	}
}

func (g *openAITTFTGuard) exclusions(candidates []openAITTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot, tickets ...map[int64]*openAITTFTTrial) map[int64]struct{} {
	if g == nil {
		return nil
	}
	return g.exclusionsAt(candidates, callerExcluded, cfg, g.now(), tickets...)
}

func (g *openAITTFTGuard) exclusionsAt(candidates []openAITTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot, now time.Time, tickets ...map[int64]*openAITTFTTrial) map[int64]struct{} {
	if g == nil || len(candidates) == 0 {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	groupID := candidates[0].groupID
	if !g.acceptRequestConfigLocked(groupID, cfg) {
		return nil
	}
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	g.deleteExpiredLocked(now)
	byModel := map[string][]int64{}
	seen := map[openAITTFTGuardKey]bool{}
	for _, c := range candidates {
		if _, excluded := callerExcluded[c.accountID]; excluded || c.groupID != groupID {
			continue
		}
		k, ok := g.sampleKeyLocked(c.accountID, c.upstreamKeyID, c.model)
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		if e := g.projectionAtLocked(k, groupID, cfg, now); e != nil && e.degraded {
			e.lastTouchedAt = now
			byModel[k.model] = append(byModel[k.model], c.accountID)
		}
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	out := map[int64]struct{}{}
	for _, m := range models {
		ids := byModel[m]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		k := openAITTFTGuardProbeKey{scopeID: scope, model: m}
		probe := g.groupProbes[k]
		if probe == nil {
			if len(g.groupProbes) >= g.maxEntries {
				for old := range g.groupProbes {
					delete(g.groupProbes, old)
					break
				}
			}
			probe = &openAITTFTGuardProbeState{}
			g.groupProbes[k] = probe
		}
		probe.sequence++
		admit := int64(0)
		probe.nominated = 0
		if probe.sequence%openAITTFTGuardProbeEvery == 0 {
			admit = ids[probe.cursor%uint64(len(ids))]
			probe.cursor++
			probe.cycle++
			probe.nominated = admit
			if len(tickets) > 0 {
				for _, c := range candidates {
					if c.accountID != admit || normalizeOpenAIAccountModelTransientModel(c.model) != m {
						continue
					}
					key, ok := g.sampleKeyLocked(c.accountID, c.upstreamKeyID, m)
					if !ok {
						continue
					}
					key.scopeID = scope
					if e := g.entries[key]; e != nil {
						e.trialCycle = probe.cycle
						tickets[0][admit] = &openAITTFTTrial{guard: g, key: key, degradedAt: e.degradedAt, cycle: probe.cycle, generation: e.generation, recoveryEpoch: e.recoveryEpoch}
					}
				}
			}
		}
		for _, id := range ids {
			if id != admit {
				out[id] = struct{}{}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (g *openAITTFTGuard) quality(candidates []openAITTFTGuardCandidate, cfg OpenAITTFTGuardConfigSnapshot) map[int64]forkscheduling.TTFTQuality {
	if g == nil {
		return nil
	}
	return g.qualityAt(candidates, cfg, g.now())
}

func (g *openAITTFTGuard) qualityAt(candidates []openAITTFTGuardCandidate, cfg OpenAITTFTGuardConfigSnapshot, now time.Time) map[int64]forkscheduling.TTFTQuality {
	if g == nil || !cfg.Enabled {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[int64]forkscheduling.TTFTQuality{}
	for _, c := range candidates {
		if !g.acceptRequestConfigLocked(c.groupID, cfg) {
			continue
		}
		k, ok := g.sampleKeyLocked(c.accountID, c.upstreamKeyID, c.model)
		if !ok {
			continue
		}
		raw := g.observations[k]
		if raw == nil || now.Sub(raw.lastSampleAt) >= g.ttl {
			continue
		}
		if e := g.projectionAtLocked(k, c.groupID, cfg, now); e != nil {
			out[c.accountID] = forkscheduling.TTFTQuality{Degraded: e.degraded, EWMAms: e.recentEWMA(), SampleCount: e.sampleCount}
		}
	}
	return out
}

func (g *openAITTFTGuard) degradations(ids []int64) map[int64][]OpenAITTFTGuardDegradation {
	return g.indexedDegradations(ids)
}

func (g *openAITTFTGuard) isDegraded(groupID, accountID int64, model string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteExpiredLocked(g.now())
	cfg, ok := g.groupConfigs[groupID]
	if !ok || !cfg.Enabled {
		return false
	}
	k, ok := g.sampleKeyLocked(accountID, 0, model)
	if !ok {
		return false
	}
	e := g.projectionLocked(k, groupID, cfg)
	return e != nil && e.degraded
}
func (g *openAITTFTGuard) clearGroupLocked(groupID int64) {
	// The policy publisher invalidates projections on a real content change.
	// A legacy invalidation callback may arrive for an unchanged PUT.
	if g.policiesManaged {
		return
	}
	if g.groupConfigs[groupID].Enabled {
		g.activePolicies--
	}
	delete(g.groupConfigs, groupID)
	g.inheritedKnown = false
}
func (g *openAITTFTGuard) clearScopeLocked(scopeID int64) { g.clearGroupLocked(scopeID) }
func (g *openAITTFTGuard) clearForConfig(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
	if g != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.syncConfigLocked(groupID, cfg)
	}
}
func (g *openAITTFTGuard) clearInheritedConfigMismatch(cfg OpenAITTFTGuardConfigSnapshot) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policiesManaged || (g.inheritedKnown && sameOpenAITTFTGuardRuntimeConfig(g.inheritedConfig, cfg)) {
		return
	}
	for id, old := range g.groupConfigs {
		if old.Source == "" || old.Source == GroupTTFTGuardSourceGlobal {
			next := cfg
			next.GroupName = old.GroupName
			g.groupConfigs[id] = normalizeOpenAITTFTGuardConfig(next)
		}
	}
	g.inheritedConfig, g.inheritedKnown = cfg, true
	g.recountActivePoliciesLocked()
	g.pruneInactivePoliciesLocked()
}
