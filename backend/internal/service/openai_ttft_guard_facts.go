package service

import (
	"context"
	"crypto/rand"
	"sort"
	"time"
)

const openAITTFTGuardAlgorithmVersion = "ttft-window-v2"

// OpenAITTFTGuardObservation is terminal evidence from one physical upstream
// send. SessionID and LogicalRequestID are opaque, trusted hashes, never headers
// or request bodies. A failed/cancelled attempt may establish latency, but can
// never establish successful recovery.
type OpenAITTFTGuardObservation struct {
	GroupID, AccountID, UpstreamKeyID             int64
	Model, AttemptID, LogicalRequestID, SessionID string
	RuntimeID                                     string
	AlgorithmVersion                              string
	PolicyGeneration                              uint64
	SentAt, FirstTokenAt, CompletedAt             time.Time
	TTFTMs                                        int
	Success, Canceled, Trial                      bool
	FirstTokenOnly                                bool
}

type openAITTFTGuardFact struct {
	OpenAITTFTGuardObservation
	identified bool
	sequence   uint64
}

func (f openAITTFTGuardFact) eventAt() time.Time {
	if !f.FirstTokenAt.IsZero() {
		return f.FirstTokenAt
	}
	return f.CompletedAt
}

// retainedAt separates terminal liveness from the first-token latency clock.
// A long stream may finish now even after its latency evidence has expired.
func (f openAITTFTGuardFact) retainedAt() time.Time {
	if f.CompletedAt.After(f.eventAt()) {
		return f.CompletedAt
	}
	return f.eventAt()
}

func (g *openAITTFTGuard) policyGenerationLocked() uint64 {
	if g.policySnapshot != nil {
		return g.policySnapshot.Revision()
	}
	return g.localPolicyGeneration
}

// A policy epoch may reuse process storage, but never old evidence or recovery
// state. The fact window can be empty while its revalidation tombstone survives.
func (e *openAITTFTGuardEntry) setPolicyGeneration(generation uint64) {
	if e.policyGeneration == generation {
		return
	}
	e.policyGeneration = generation
	e.facts, e.completedAttempts = nil, nil
	e.samples = [openAITTFTGuardSampleWindow]float64{}
	e.successes = [openAITTFTGuardSampleWindow]bool{}
	e.sampleLen, e.nextSample, e.sampleCount = 0, 0, 0
	e.nextFactSequence, e.consecutiveElevated = 0, 0
	e.lastFactAt, e.lastSampleAt = time.Time{}, time.Time{}
	e.lastRecoveryBlockReason, e.lastRecoveryBlockAt = "", time.Time{}
	e.needsRevalidation, e.degraded = false, false
	e.latestBadBarrier, e.badExpiresAt, e.recoveredThrough, e.degradedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	e.triggerTTFTMs, e.triggerThresholdMs = 0, 0
	e.degradedReason, e.recoveryReason = "", "fresh_samples_insufficient"
	e.consecutiveRecovered, e.recoverySessions, e.recoveryOrdinarySamples, e.recoveryTrialSamples = 0, 0, 0, 0
	e.trialCycle, e.highestCompletedCycle = 0, 0
	e.completedTrialCycles, e.completedTrialCount = [64]uint64{}, 0
	e.recoveryEpoch++
}

// OpenAITTFTGuardMarkCurrentKey records account key identity at physical send.
// Terminal callbacks cannot roll it back merely by completing out of order.
func (s *OpenAIGatewayService) OpenAITTFTGuardMarkCurrentKey(accountID, keyID int64, sentAt time.Time) {
	if s == nil {
		return
	}
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markCurrentKeyLocked(accountID, keyID, sentAt)
}
func (g *openAITTFTGuard) markCurrentKeyLocked(accountID, keyID int64, sentAt time.Time) {
	if accountID <= 0 || keyID <= 0 || sentAt.IsZero() || sentAt.After(g.now()) {
		return
	}
	if previous, ok := g.accountKeySentAt[accountID]; ok && !sentAt.After(previous) {
		return
	}
	if _, exists := g.accountKeySentAt[accountID]; !exists && len(g.accountKeySentAt) >= g.maxEntries {
		var oldest int64
		var at time.Time
		for id, ts := range g.accountKeySentAt {
			if at.IsZero() || ts.Before(at) {
				oldest, at = id, ts
			}
		}
		delete(g.accountKeySentAt, oldest)
		delete(g.accountKeys, oldest)
	}
	g.accountKeys[accountID], g.accountKeySentAt[accountID] = keyID, sentAt
}

// OpenAITTFTGuardObservationIdentity freezes the process and policy generation
// at the actual send boundary, rather than at selection or completion.
func (s *OpenAIGatewayService) OpenAITTFTGuardObservationIdentity(groupID int64) (string, uint64) {
	if s == nil || groupID <= 0 {
		return "", 0
	}
	cfg, valid := s.resolveOpenAITTFTGuardConfig(context.Background(), groupID)
	s.syncTTFTProtectionPolicies()
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	defer g.mu.Unlock()
	if valid && !g.policiesManaged {
		g.syncConfigLocked(groupID, cfg)
	}
	return g.runtimeID, g.policyGenerationLocked()
}

func (s *OpenAIGatewayService) ReportOpenAITTFTGuardObservation(ctx context.Context, observation OpenAITTFTGuardObservation) bool {
	return s.reportOpenAITTFTGuardObservation(ctx, observation, nil)
}

func (s *OpenAIGatewayService) reportOpenAITTFTGuardObservation(ctx context.Context, observation OpenAITTFTGuardObservation, decision *ttftRecoveryDecision) bool {
	if s == nil || !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, observation.GroupID) || !schedulingProtectionPlatformSelected(s.groupProtectionPolicyResolver, PlatformOpenAI) {
		return false
	}
	cfg, valid := s.resolveOpenAITTFTGuardConfig(ctx, observation.GroupID)
	if !valid {
		return false
	}
	s.syncTTFTProtectionPolicies()
	return s.getOpenAITTFTGuard().reportObservationOutcome(observation, cfg, true, decision)
}

func (g *openAITTFTGuard) reportObservation(o OpenAITTFTGuardObservation, cfg OpenAITTFTGuardConfigSnapshot, identified bool) bool {
	return g.reportObservationOutcome(o, cfg, identified, nil)
}

func (g *openAITTFTGuard) reportObservationOutcome(o OpenAITTFTGuardObservation, cfg OpenAITTFTGuardConfigSnapshot, identified bool, decision *ttftRecoveryDecision) bool {
	if g == nil || o.GroupID <= 0 || o.AccountID <= 0 || (!identified && (o.TTFTMs <= 0 || o.FirstTokenAt.IsZero())) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	eventAt := (openAITTFTGuardFact{OpenAITTFTGuardObservation: o}).retainedAt()
	if !eventAt.After(now.Add(-g.ttl)) || eventAt.After(now) {
		return false
	}
	if identified && (o.AlgorithmVersion != openAITTFTGuardAlgorithmVersion || o.AttemptID == "" || o.RuntimeID != g.runtimeID || o.SentAt.IsZero() || (!o.CompletedAt.IsZero() && o.CompletedAt.Before(o.SentAt)) || (!o.FirstTokenAt.IsZero() && (o.FirstTokenAt.Before(o.SentAt) || (!o.CompletedAt.IsZero() && o.CompletedAt.Before(o.FirstTokenAt)))) || o.CompletedAt.After(now) || (o.FirstTokenOnly && (!o.CompletedAt.IsZero() || o.FirstTokenAt.IsZero() || o.TTFTMs <= 0)) || (!o.FirstTokenOnly && o.CompletedAt.IsZero())) {
		return false
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	if g.policiesManaged {
		if current, ok := g.groupConfigs[o.GroupID]; ok {
			cfg = current
		} else {
			cfg.Enabled = false
		}
	} else {
		g.syncConfigLocked(o.GroupID, cfg)
	}
	if identified && o.PolicyGeneration != g.policyGenerationLocked() {
		return false
	}
	if g.activePolicies == 0 {
		return false
	}
	g.deleteExpiredLocked(now)
	k, ok := g.sampleKeyLocked(o.AccountID, o.UpstreamKeyID, o.Model)
	if !ok {
		return false
	}
	raw := g.observations[k]
	if raw == nil {
		if len(g.observations) >= g.maxEntries {
			var oldest openAITTFTGuardKey
			var at time.Time
			for candidate, entry := range g.observations {
				candidateAt := entry.lastSampleAt
				if entry.lastFactAt.After(candidateAt) {
					candidateAt = entry.lastFactAt
				}
				if at.IsZero() || candidateAt.Before(at) {
					oldest, at = candidate, candidateAt
				}
			}
			g.deleteObservationAndProjectionsLocked(oldest)
		}
		raw = &openAITTFTGuardEntry{runtimeID: g.runtimeID}
		g.observations[k] = raw
		g.indexObservationLocked(k)
	}
	raw.factCapacity = openAITTFTGuardSampleWindow
	for _, policy := range g.groupConfigs {
		if policy.Enabled && policy.MinSamples > raw.factCapacity {
			raw.factCapacity = policy.MinSamples
		}
	}
	raw.setPolicyGeneration(g.policyGenerationLocked())
	fact := openAITTFTGuardFact{OpenAITTFTGuardObservation: o, identified: identified}
	if !raw.mergeFactAt(fact, now, g.ttl) {
		return false
	}
	sentAt := o.SentAt
	if !identified {
		sentAt = eventAt
	}
	g.markCurrentKeyLocked(o.AccountID, o.UpstreamKeyID, sentAt)
	raw.lastGroupID, raw.lastGroupName, raw.lastTouchedAt = o.GroupID, cfg.GroupName, now
	raw.policyGeneration = g.policyGenerationLocked()
	g.scheduleExpiryLocked(k, true, raw)
	originScope, _ := openAITTFTGuardScopeFor(o.GroupID, cfg)
	evaluation := ttftRecoveryEvaluation{attemptID: o.AttemptID, decision: decision}
	for projectionKey := range g.projectionKeys[k] {
		e := g.entries[projectionKey]
		e.runtimeID = g.runtimeID
		e.setPolicyGeneration(g.policyGenerationLocked())
		e.mergeFactAt(fact, now, g.ttl)
		if projectionKey.scopeID == originScope {
			e.evaluateFactsAtWithDecision(now, g.ttl, evaluation)
		} else {
			e.evaluateFactsAt(now, g.ttl)
		}
		g.scheduleExpiryLocked(projectionKey, false, e)
	}
	if cfg.Enabled {
		g.projectionAtLocked(k, o.GroupID, cfg, now, evaluation)
	}
	// A policy may have been registered without ever being read. Materialize
	// every potentially affected policy while the raw window contains this fact;
	// otherwise a later first read could derive from a bounded raw window after
	// the original bad sample was evicted and lose the active protection epoch.
	// Each trigger requires the newly observed latency to reach its threshold.
	// Fast traffic therefore does not expand the policy/account cross product.
	groupIDs := make([]int64, 0)
	for groupID, policy := range g.groupConfigs {
		if !policy.Enabled || groupID == o.GroupID || int64(o.TTFTMs) < policy.Threshold.Milliseconds() {
			continue
		}
		groupIDs = append(groupIDs, groupID)
	}
	sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
	for _, groupID := range groupIDs {
		policy := g.groupConfigs[groupID]
		scope, ok := openAITTFTGuardScopeFor(groupID, policy)
		if !ok {
			continue
		}
		projectionKey := k
		projectionKey.scopeID = scope
		if _, exists := g.entries[projectionKey]; !exists {
			g.projectionAtLocked(k, groupID, policy, now)
		}
	}
	return true
}

// mergeFactAt coalesces first-token and terminal phases of one physical attempt.
func (e *openAITTFTGuardEntry) mergeFactAt(f openAITTFTGuardFact, now time.Time, ttl time.Duration) bool {
	for i := range e.facts {
		if e.facts[i].AttemptID != f.AttemptID {
			continue
		}
		if f.FirstTokenOnly || !e.facts[i].CompletedAt.IsZero() {
			return false
		}
		merged := e.facts[i]
		first := merged.FirstTokenAt
		oldRuntimeID, oldGeneration := merged.RuntimeID, merged.PolicyGeneration
		merged.OpenAITTFTGuardObservation = f.OpenAITTFTGuardObservation
		merged.FirstTokenAt, merged.TTFTMs = first, e.facts[i].TTFTMs
		merged.FirstTokenOnly, merged.identified = false, f.identified
		// The send boundary owns runtime/policy identity. A terminal callback
		// arriving after a policy refresh cannot become current-generation proof.
		merged.RuntimeID, merged.PolicyGeneration = oldRuntimeID, oldGeneration
		merged.sequence = e.facts[i].sequence
		e.facts[i] = merged
		e.rememberTerminal(f.AttemptID)
		sort.SliceStable(e.facts, func(i, j int) bool {
			if e.facts[i].retainedAt().Equal(e.facts[j].retainedAt()) {
				return e.facts[i].sequence < e.facts[j].sequence
			}
			return e.facts[i].retainedAt().Before(e.facts[j].retainedAt())
		})
		e.rebuildSamples(now, ttl)
		if merged.retainedAt().After(e.lastFactAt) {
			e.lastFactAt = merged.retainedAt()
		}
		return true
	}
	return e.addFactAt(f, now, ttl)
}

func (e *openAITTFTGuardEntry) rememberTerminal(id string) {
	e.completedAttempts = append(e.completedAttempts, id)
	if len(e.completedAttempts) > openAITTFTGuardFactWindow*4 {
		e.completedAttempts = e.completedAttempts[len(e.completedAttempts)-openAITTFTGuardFactWindow*4:]
	}
}

// Facts are bounded independently of EWMA's ten-sample presentation ring. Every
// fact has its own expiry, so an arbitrarily fresh success cannot renew an old
// slow observation. Duplicate physical attempts are ignored even after their
// sample aged out of the active window (the bounded terminal-ID cache below).
func (e *openAITTFTGuardEntry) addFactAt(f openAITTFTGuardFact, now time.Time, ttl time.Duration) bool {
	for _, id := range e.completedAttempts {
		if id == f.AttemptID {
			return false
		}
	}
	if !f.FirstTokenOnly {
		e.rememberTerminal(f.AttemptID)
	}
	e.pruneFactsAt(now, ttl)
	e.nextFactSequence++
	f.sequence = e.nextFactSequence
	e.facts = append(e.facts, f)
	sort.SliceStable(e.facts, func(i, j int) bool {
		if e.facts[i].retainedAt().Equal(e.facts[j].retainedAt()) {
			return e.facts[i].sequence < e.facts[j].sequence
		}
		return e.facts[i].retainedAt().Before(e.facts[j].retainedAt())
	})
	capacity := e.factCapacity
	if capacity == 0 {
		capacity = e.config.MinSamples
	}
	if capacity < openAITTFTGuardSampleWindow {
		capacity = openAITTFTGuardSampleWindow
	}
	if capacity > openAITTFTGuardFactWindow {
		capacity = openAITTFTGuardFactWindow
	}
	if len(e.facts) > capacity {
		e.facts = e.facts[len(e.facts)-capacity:]
	}
	e.rebuildSamples(now, ttl)
	if f.retainedAt().After(e.lastFactAt) {
		e.lastFactAt = f.retainedAt()
	}
	return true
}

func (e *openAITTFTGuardEntry) pruneFactsAt(now time.Time, ttl time.Duration) {
	kept := e.facts[:0]
	for _, f := range e.facts {
		if f.retainedAt().After(now.Add(-ttl)) && !f.retainedAt().After(now) {
			kept = append(kept, f)
		}
	}
	e.facts = kept
	capacity := e.factCapacity
	if capacity == 0 {
		capacity = e.config.MinSamples
	}
	if capacity < openAITTFTGuardSampleWindow {
		capacity = openAITTFTGuardSampleWindow
	}
	if capacity > openAITTFTGuardFactWindow {
		capacity = openAITTFTGuardFactWindow
	}
	if len(e.facts) > capacity {
		e.facts = e.facts[len(e.facts)-capacity:]
	}
}

func (e *openAITTFTGuardEntry) latencyFacts() []openAITTFTGuardFact {
	facts := append([]openAITTFTGuardFact(nil), e.facts...)
	sort.SliceStable(facts, func(i, j int) bool {
		if facts[i].FirstTokenAt.Equal(facts[j].FirstTokenAt) {
			return facts[i].sequence < facts[j].sequence
		}
		return facts[i].FirstTokenAt.Before(facts[j].FirstTokenAt)
	})
	return facts
}

func (e *openAITTFTGuardEntry) rebuildSamples(now time.Time, ttl time.Duration) {
	e.samples = [openAITTFTGuardSampleWindow]float64{}
	e.successes = [openAITTFTGuardSampleWindow]bool{}
	e.sampleLen, e.nextSample, e.sampleCount = 0, 0, 0
	e.lastSampleAt = time.Time{}
	for _, f := range e.latencyFacts() {
		if (f.identified && f.PolicyGeneration != e.policyGeneration) || f.TTFTMs <= 0 || !f.FirstTokenAt.After(now.Add(-ttl)) || f.FirstTokenAt.After(now) {
			continue
		}
		e.samples[e.nextSample] = float64(f.TTFTMs)
		e.successes[e.nextSample] = f.Success && !f.Canceled
		e.nextSample = (e.nextSample + 1) % openAITTFTGuardSampleWindow
		if e.sampleLen < openAITTFTGuardSampleWindow {
			e.sampleLen++
		}
		e.sampleCount++
		e.lastSampleAt = f.FirstTokenAt
	}
}

func (e *openAITTFTGuardEntry) evaluateFactsAt(now time.Time, ttl time.Duration) {
	e.evaluateFactsAtWithDecision(now, ttl, ttftRecoveryEvaluation{})
}

func (e *openAITTFTGuardEntry) evaluateFactsAtWithDecision(now time.Time, ttl time.Duration, evaluation ttftRecoveryEvaluation) {
	e.pruneFactsAt(now, ttl)
	e.rebuildSamples(now, ttl)
	threshold := float64(e.config.Threshold.Milliseconds())
	var prefix openAITTFTGuardEntry
	elevated := 0
	for _, f := range e.latencyFacts() {
		if (f.identified && f.PolicyGeneration != e.policyGeneration) || f.TTFTMs <= 0 || !f.FirstTokenAt.After(now.Add(-ttl)) || f.FirstTokenAt.After(now) {
			continue
		}
		before := prefix.recentEWMA()
		prefix.addSample(float64(f.TTFTMs))
		if float64(f.TTFTMs) >= 1.5*threshold {
			elevated++
		} else {
			elevated = 0
		}
		reason := ""
		switch {
		case float64(f.TTFTMs) >= 3*threshold:
			reason = "critical_sample"
		case elevated >= 2:
			reason = "consecutive_elevated"
		case prefix.sampleCount >= uint64(e.config.MinSamples) && prefix.recentEWMA() >= threshold && (before < threshold || float64(f.TTFTMs) >= threshold):
			reason = "ewma"
		}
		if reason == "" || !f.FirstTokenAt.After(e.latestBadBarrier) || !f.FirstTokenAt.After(e.recoveredThrough) {
			continue
		}
		e.latestBadBarrier, e.degradedAt = f.FirstTokenAt, f.FirstTokenAt
		e.badExpiresAt = f.FirstTokenAt.Add(ttl)
		e.degradedReason = reason
		e.triggerTTFTMs, e.triggerThresholdMs = f.TTFTMs, e.config.Threshold.Milliseconds()
		e.needsRevalidation = true
		e.recoveryEpoch++
	}
	e.degraded = e.needsRevalidation && now.Before(e.badExpiresAt)
	e.consecutiveRecovered, e.recoverySessions = 0, 0
	e.recoveryOrdinarySamples, e.recoveryTrialSamples = 0, 0
	if !e.needsRevalidation {
		evaluation.record(evaluation.attemptID, false, "not_recovering")
		e.recoveryReason = ""
		if e.sampleCount == 0 {
			e.recoveryReason = "no_fresh_evidence"
		} else if e.recoveredThrough.IsZero() && e.sampleCount < uint64(e.config.MinSamples) {
			e.recoveryReason = "fresh_samples_insufficient"
		}
		return
	}
	completed := append([]openAITTFTGuardFact(nil), e.facts...)
	sort.SliceStable(completed, func(i, j int) bool {
		if completed[i].CompletedAt.Equal(completed[j].CompletedAt) {
			return completed[i].sequence < completed[j].sequence
		}
		return completed[i].CompletedAt.Before(completed[j].CompletedAt)
	})
	requests, sessions := map[string]bool{}, map[string]bool{}
	var lastCompleted time.Time
	for _, f := range completed {
		// A first-token event establishes latency only. Recovery diagnostics,
		// just like recovery credit, require an actual terminal completion.
		if f.FirstTokenOnly || f.CompletedAt.IsZero() || !f.identified || f.PolicyGeneration != e.policyGeneration || f.RuntimeID != e.runtimeID || !f.CompletedAt.After(now.Add(-ttl)) || f.CompletedAt.After(now) {
			continue
		}
		if f.LogicalRequestID == "" {
			evaluation.record(f.AttemptID, false, "missing_logical_request")
			e.recordRecoveryBlock("missing_logical_request", f.CompletedAt)
			continue
		}
		if !f.SentAt.After(e.latestBadBarrier) {
			evaluation.record(f.AttemptID, false, "old_send")
			e.recordRecoveryBlock("old_send", f.CompletedAt)
			continue
		}
		if !f.Success || f.Canceled || f.TTFTMs <= 0 || !f.FirstTokenAt.After(now.Add(-ttl)) || float64(f.TTFTMs) > .6*threshold {
			reason := ""
			switch {
			case f.Canceled:
				reason = "canceled"
			case !f.Success:
				reason = "failure"
			case f.TTFTMs <= 0 || f.FirstTokenAt.IsZero():
				reason = "no_ttft"
			case !f.FirstTokenAt.After(f.CompletedAt.Add(-ttl)):
				reason = "expired_ttft"
			case float64(f.TTFTMs) > .6*threshold:
				reason = "slow_ttft"
			}
			// Expiry caused solely by a later snapshot read is not a newly
			// observed terminal block. Keep its original completion evidence.
			e.recordRecoveryBlock(reason, f.CompletedAt)
			evaluation.record(f.AttemptID, false, reason)
			e.consecutiveRecovered, e.recoveryOrdinarySamples, e.recoveryTrialSamples = 0, 0, 0
			sessions = map[string]bool{}
			continue
		}
		if requests[f.LogicalRequestID] {
			evaluation.record(f.AttemptID, false, "duplicate_logical_request")
			e.recordRecoveryBlock("duplicate_logical_request", f.CompletedAt)
			continue
		}
		requests[f.LogicalRequestID] = true
		evaluation.record(f.AttemptID, true, "counted")
		e.consecutiveRecovered++
		if f.SessionID != "" {
			sessions[f.SessionID] = true
		}
		if f.Trial {
			e.recoveryTrialSamples++
		} else {
			e.recoveryOrdinarySamples++
		}
		lastCompleted = f.CompletedAt
	}
	e.recoverySessions = len(sessions)
	switch {
	case e.consecutiveRecovered >= openAITTFTGuardRecoverySamples && e.recoverySessions >= 2:
		e.needsRevalidation, e.degraded = false, false
		e.recoveredThrough, e.recoveryReason = lastCompleted, ""
		e.lastRecoveryBlockReason, e.lastRecoveryBlockAt = "", time.Time{}
	case e.recoverySessions < 2 && e.consecutiveRecovered >= openAITTFTGuardRecoverySamples:
		e.recoveryReason = "recovery_sessions_insufficient"
	case !e.degraded:
		e.recoveryReason = "bad_evidence_expired"
	default:
		e.recoveryReason = "recovery_samples_insufficient"
	}
}

func (e *openAITTFTGuardEntry) recordRecoveryBlock(reason string, completedAt time.Time) {
	if reason != "" && !completedAt.IsZero() && completedAt.After(e.lastRecoveryBlockAt) {
		e.lastRecoveryBlockReason, e.lastRecoveryBlockAt = reason, completedAt
	}
}

func (e *openAITTFTGuardEntry) stateAt(now time.Time, ttl time.Duration) string {
	if e == nil {
		return "insufficient"
	}
	e.evaluateFactsAt(now, ttl)
	if e.degraded {
		return "protected"
	}
	if e.needsRevalidation || e.sampleCount == 0 || (e.recoveredThrough.IsZero() && e.sampleCount < uint64(e.config.MinSamples)) {
		return "insufficient"
	}
	return "normal"
}

func legacyTTFTGuardObservation(groupID, accountID, keyID int64, model string, success bool, ms int, at time.Time) OpenAITTFTGuardObservation {
	return OpenAITTFTGuardObservation{GroupID: groupID, AccountID: accountID, UpstreamKeyID: keyID, Model: model, AttemptID: "legacy-" + rand.Text(), AlgorithmVersion: openAITTFTGuardAlgorithmVersion, TTFTMs: ms, FirstTokenAt: at, CompletedAt: at, Success: success}
}
