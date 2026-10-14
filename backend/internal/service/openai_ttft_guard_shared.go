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
	g.syncConfigLocked(groupID, cfg)
	active := cfg.Enabled
	for _, c := range g.groupConfigs {
		active = active || c.Enabled
	}
	if !active {
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
			delete(g.observations, oldest)
		}
		raw = &openAITTFTGuardEntry{}
		g.observations[k] = raw
	}
	raw.successes[raw.nextSample] = success
	raw.addSample(float64(*ms))
	raw.lastSampleAt, raw.lastTouchedAt = now, now
	raw.lastGroupID, raw.lastGroupName = groupID, cfg.GroupName
	// Each existing projection consumes the observation once. Recovery resets
	// only that projection's effective window, leaving raw and other policies.
	for key, e := range g.entries {
		if key.accountID == k.accountID && key.upstreamKeyID == k.upstreamKeyID && key.model == k.model {
			applyTTFTProjectionSample(e, float64(*ms), success, now)
		}
	}
	if cfg.Enabled {
		g.projectionLocked(k, groupID, cfg)
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
	if raw == nil || g.now().Sub(raw.lastSampleAt) >= g.ttl {
		return nil
	}
	g.evictLRULocked()
	e := deriveTTFTProjection(raw, groupID, cfg)
	g.nextEntryGeneration++
	e.generation = g.nextEntryGeneration
	g.entries[k] = e
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

func (g *openAITTFTGuard) syncConfigLocked(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
	if groupID <= 0 {
		return
	}
	if len(g.groupConfigs) >= g.maxEntries {
		for id := range g.groupConfigs {
			if id != groupID {
				delete(g.groupConfigs, id)
				break
			}
		}
	}
	next := normalizeOpenAITTFTGuardConfig(cfg)
	previous, exists := g.groupConfigs[groupID]
	g.groupConfigs[groupID] = next
	if !exists || previous.Enabled != next.Enabled || previous.Threshold != next.Threshold || previous.MinSamples != next.MinSamples {
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
			delete(g.entries, k)
		}
	}
	for k := range g.groupProbes {
		if !active[k.scopeID] {
			delete(g.groupProbes, k)
		}
	}
}

func (g *openAITTFTGuard) exclusions(candidates []openAITTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot, tickets ...map[int64]*openAITTFTTrial) map[int64]struct{} {
	if g == nil || len(candidates) == 0 {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	groupID := candidates[0].groupID
	g.syncConfigLocked(groupID, cfg)
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	g.deleteExpiredLocked(g.now())
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
		if e := g.projectionLocked(k, groupID, cfg); e != nil && e.degraded {
			e.lastTouchedAt = g.now()
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
	if g == nil || !cfg.Enabled {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[int64]forkscheduling.TTFTQuality{}
	for _, c := range candidates {
		g.syncConfigLocked(c.groupID, cfg)
		k, ok := g.sampleKeyLocked(c.accountID, c.upstreamKeyID, c.model)
		if !ok {
			continue
		}
		raw := g.observations[k]
		if raw == nil || g.now().Sub(raw.lastSampleAt) >= g.ttl {
			continue
		}
		if e := g.projectionLocked(k, c.groupID, cfg); e != nil {
			out[c.accountID] = forkscheduling.TTFTQuality{Degraded: e.degraded, EWMAms: e.recentEWMA(), SampleCount: e.sampleCount}
		}
	}
	return out
}

func (g *openAITTFTGuard) degradations(ids []int64) map[int64][]OpenAITTFTGuardDegradation {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteExpiredLocked(g.now())
	wanted := map[int64]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	out := map[int64][]OpenAITTFTGuardDegradation{}
	for groupID, cfg := range g.groupConfigs {
		if !cfg.Enabled {
			continue
		}
		for key, raw := range g.observations {
			if !wanted[key.accountID] || (g.accountKeys[key.accountID] > 0 && g.accountKeys[key.accountID] != key.upstreamKeyID) {
				continue
			}
			e := g.readProjectionLocked(key, groupID, cfg)
			if e == nil || !e.degraded {
				continue
			}
			last := (raw.nextSample - 1 + openAITTFTGuardSampleWindow) % openAITTFTGuardSampleWindow
			out[key.accountID] = append(out[key.accountID], OpenAITTFTGuardDegradation{UpstreamKeyID: key.upstreamKeyID, GroupID: groupID, GroupName: cfg.GroupName, PolicySource: cfg.Source, Model: key.model, Reason: e.degradedReason, ThresholdMs: cfg.Threshold.Milliseconds(), LastTTFTMs: int64(raw.samples[last]), EWMAms: e.recentEWMA(), SampleCount: e.sampleCount, DegradedAt: e.degradedAt, LastSampleAt: raw.lastSampleAt, ExpiresAt: raw.lastSampleAt.Add(g.ttl), RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: openAITTFTGuardRecoverySamples})
		}
	}
	for id := range out {
		sort.Slice(out[id], func(i, j int) bool {
			a, b := out[id][i], out[id][j]
			if a.GroupID != b.GroupID {
				return a.GroupID < b.GroupID
			}
			return a.Model < b.Model
		})
	}
	return out
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
func (g *openAITTFTGuard) deleteExpiredLocked(now time.Time) {
	for k, e := range g.entries {
		if e == nil || e.lastSampleAt.IsZero() || now.Sub(e.lastSampleAt) >= g.ttl {
			delete(g.entries, k)
		}
	}
	accounts := map[int64]bool{}
	for k, e := range g.observations {
		if e == nil || e.lastSampleAt.IsZero() || now.Sub(e.lastSampleAt) >= 2*g.ttl {
			delete(g.observations, k)
		} else {
			accounts[k.accountID] = true
		}
	}
	for id := range g.accountKeys {
		if !accounts[id] {
			delete(g.accountKeys, id)
		}
	}
}
func (g *openAITTFTGuard) clearGroupLocked(groupID int64) { delete(g.groupConfigs, groupID) }
func (g *openAITTFTGuard) clearScopeLocked(scopeID int64) { delete(g.groupConfigs, scopeID) }
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
	for id, old := range g.groupConfigs {
		if old.Source == "" || old.Source == GroupTTFTGuardSourceGlobal {
			next := cfg
			next.GroupName = old.GroupName
			g.groupConfigs[id] = normalizeOpenAITTFTGuardConfig(next)
		}
	}
	g.pruneInactivePoliciesLocked()
}
