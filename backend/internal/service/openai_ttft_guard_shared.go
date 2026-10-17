package service

import (
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
	if g == nil || ms == nil || *ms <= 0 {
		return
	}
	g.reportObservation(legacyTTFTGuardObservation(groupID, accountID, keyID, model, success, *ms, g.now()), cfg, false)
}

// The legacy helper contributes latency only. Recovery requires identified,
// terminal business observations and cannot be synthesized by a trial callback.
func applyTTFTProjectionSample(e *openAITTFTGuardEntry, sample float64, success bool, now time.Time) {
	f := openAITTFTGuardFact{OpenAITTFTGuardObservation: legacyTTFTGuardObservation(1, 1, 0, "legacy", success, int(sample), now)}
	e.addFactAt(f, now, openAITTFTGuardEntryTTL)
	e.evaluateFactsAt(now, openAITTFTGuardEntryTTL)
}

func (g *openAITTFTGuard) projectionLocked(rawKey openAITTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTGuardEntry {
	return g.projectionAtLocked(rawKey, groupID, cfg, g.now())
}

func (g *openAITTFTGuard) projectionAtLocked(rawKey openAITTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot, now time.Time, evaluations ...ttftRecoveryEvaluation) *openAITTFTGuardEntry {
	var evaluation ttftRecoveryEvaluation
	if len(evaluations) > 0 {
		evaluation = evaluations[0]
	}
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	k := rawKey
	k.scopeID = scope
	if e := g.entries[k]; e != nil {
		e.runtimeID = g.runtimeID
		e.setPolicyGeneration(g.policyGenerationLocked())
		e.evaluateFactsAtWithDecision(now, g.ttl, evaluation)
		return e
	}
	raw := g.observations[rawKey]
	if raw == nil || now.Sub(raw.lastSampleAt) >= g.ttl {
		return nil
	}
	g.evictLRULocked()
	e := deriveTTFTProjection(raw, groupID, cfg, evaluation)
	e.runtimeID = g.runtimeID
	e.setPolicyGeneration(g.policyGenerationLocked())
	e.stateAt(now, g.ttl)
	g.nextEntryGeneration++
	e.generation = g.nextEntryGeneration
	g.entries[k] = e
	g.indexProjectionLocked(k)
	g.scheduleExpiryLocked(k, false, e)
	return e
}

func deriveTTFTProjection(raw *openAITTFTGuardEntry, groupID int64, cfg OpenAITTFTGuardConfigSnapshot, evaluations ...ttftRecoveryEvaluation) *openAITTFTGuardEntry {
	capacity := cfg.MinSamples
	if capacity < openAITTFTGuardSampleWindow {
		capacity = openAITTFTGuardSampleWindow
	}
	if capacity > openAITTFTGuardFactWindow {
		capacity = openAITTFTGuardFactWindow
	}
	e := &openAITTFTGuardEntry{config: cfg, factCapacity: capacity, lastGroupID: groupID, lastGroupName: cfg.GroupName, runtimeID: raw.runtimeID, policyGeneration: raw.policyGeneration}
	e.facts = append([]openAITTFTGuardFact(nil), raw.facts...)
	e.completedAttempts = append([]string(nil), raw.completedAttempts...)
	e.nextFactSequence = raw.nextFactSequence
	at := raw.lastSampleAt
	if raw.lastFactAt.After(at) {
		at = raw.lastFactAt
	}
	var evaluation ttftRecoveryEvaluation
	if len(evaluations) > 0 {
		evaluation = evaluations[0]
	}
	e.evaluateFactsAtWithDecision(at, openAITTFTGuardEntryTTL, evaluation)
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
	if exists && (previous.Enabled != next.Enabled || previous.Threshold != next.Threshold || previous.MinSamples != next.MinSamples) {
		g.localPolicyGeneration++
	}
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
	if !g.acceptRequestConfigLocked(groupID, cfg) || !cfg.Enabled {
		return nil
	}
	g.deleteExpiredLocked(now)
	out := map[int64]struct{}{}
	for _, c := range candidates {
		if _, excluded := callerExcluded[c.accountID]; excluded || c.groupID != groupID {
			continue
		}
		k, ok := g.sampleKeyLocked(c.accountID, c.upstreamKeyID, c.model)
		if !ok {
			continue
		}
		if e := g.projectionAtLocked(k, groupID, cfg, now); e != nil && e.stateAt(now, g.ttl) == "protected" {
			out[c.accountID] = struct{}{}
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
			state := e.stateAt(now, g.ttl)
			out[c.accountID] = forkscheduling.TTFTQuality{State: state, Degraded: state == "protected", EWMAms: e.recentEWMA(), SampleCount: e.sampleCount}
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
	changed := false
	for id, old := range g.groupConfigs {
		if old.Source == "" || old.Source == GroupTTFTGuardSourceGlobal {
			next := cfg
			next.GroupName = old.GroupName
			if old.Enabled != next.Enabled || old.Threshold != next.Threshold || old.MinSamples != next.MinSamples {
				changed = true
			}
			g.groupConfigs[id] = normalizeOpenAITTFTGuardConfig(next)
		}
	}
	if changed {
		g.localPolicyGeneration++
	}
	g.inheritedConfig, g.inheritedKnown = cfg, true
	g.recountActivePoliciesLocked()
	g.pruneInactivePoliciesLocked()
}
