package service

import (
	"sort"
	"time"
)

// TTFTProtectionSnapshotRequest identifies one read-only policy projection.
// Batch output has exactly one element per request, in the same order.
type TTFTProtectionSnapshotRequest struct {
	AccountID, KeyID, GroupID int64
	Config                    OpenAITTFTGuardConfigSnapshot
}

type ttftProtectionRead struct {
	key           openAITTFTGuardKey
	raw           ttftProtectionEntryCopy
	hasRaw        bool
	projection    ttftProtectionEntryCopy
	hasProjection bool
}

// Snapshot copies own their bounded fact slices. Evaluation after unlocking
// cannot mutate live recovery state, sample age, trial credit or LRU metadata.
type ttftProtectionEntryCopy struct{ openAITTFTGuardEntry }

func copyTTFTProtectionEntry(e *openAITTFTGuardEntry) ttftProtectionEntryCopy {
	c := *e
	c.facts = append([]openAITTFTGuardFact(nil), e.facts...)
	c.completedAttempts = nil
	return ttftProtectionEntryCopy{c}
}

func (e *ttftProtectionEntryCopy) materialize() openAITTFTGuardEntry {
	c := e.openAITTFTGuardEntry
	c.facts = append([]openAITTFTGuardFact(nil), e.facts...)
	return c
}

func (e *ttftProtectionEntryCopy) recentEWMA() float64 {
	entry := e.materialize()
	return entry.recentEWMA()
}

// Only bounded value copies happen under the runtime lock. Policy replay,
// EWMA computation, presentation and sorting run after the lock is released.
func (g *openAITTFTGuard) copyProtectionReadsLocked(r TTFTProtectionSnapshotRequest) []ttftProtectionRead {
	identity := openAITTFTGuardAccountKey{r.AccountID, r.KeyID}
	models := g.observationModels[identity]
	scope, _ := openAITTFTGuardScopeFor(r.GroupID, r.Config)
	keys := make(map[openAITTFTGuardKey]struct{}, len(models))
	for key := range models {
		keys[key] = struct{}{}
	}
	// Raw observations expire independently from projections. Keep an active
	// projection tombstone visible until recovery or bounded LRU eviction.
	if r.GroupID > 0 && r.Config.Enabled {
		for rawKey := range g.projectionModels[identity] {
			projectionKey := rawKey
			projectionKey.scopeID = scope
			if g.entries[projectionKey] != nil {
				keys[rawKey] = struct{}{}
			}
		}
	}
	reads := make([]ttftProtectionRead, 0, len(keys))
	for key := range keys {
		raw := g.observations[key]
		read := ttftProtectionRead{key: key}
		if raw != nil {
			read.raw = copyTTFTProtectionEntry(raw)
			read.raw.setPolicyGeneration(g.policyGenerationLocked())
			read.hasRaw = true
		}
		if r.GroupID > 0 && r.Config.Enabled {
			pk := key
			pk.scopeID = scope
			if p := g.entries[pk]; p != nil {
				read.projection = copyTTFTProtectionEntry(p)
				read.projection.setPolicyGeneration(g.policyGenerationLocked())
				read.hasProjection = true
			}
		}
		if raw == nil && !read.hasProjection {
			continue
		}
		reads = append(reads, read)
	}
	return reads
}

func (r *ttftProtectionRead) entry(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) (ttftProtectionEntryCopy, bool) {
	if r.hasProjection {
		return r.projection, true
	}
	if _, ok := openAITTFTGuardScopeFor(groupID, cfg); !ok || !cfg.Enabled {
		return ttftProtectionEntryCopy{}, false
	}
	raw := r.raw.materialize()
	return copyTTFTProtectionEntry(deriveTTFTProjection(&raw, groupID, cfg)), true
}

func (s *OpenAIGatewayService) TTFTProtectionSnapshots(accountID, keyID, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) []TTFTProtectionModelView {
	if s == nil {
		return nil
	}
	return s.TTFTProtectionSnapshotsBatch([]TTFTProtectionSnapshotRequest{{AccountID: accountID, KeyID: keyID, GroupID: groupID, Config: cfg}})[0]
}

// TTFTProtectionSnapshotsBatch never advances trial cadence, recovery or age.
func (s *OpenAIGatewayService) TTFTProtectionSnapshotsBatch(requests []TTFTProtectionSnapshotRequest) [][]TTFTProtectionModelView {
	if s == nil {
		return nil
	}
	return s.TTFTProtectionSnapshotsBatchAt(requests, s.getOpenAITTFTGuard().now())
}

func (s *OpenAIGatewayService) TTFTProtectionSnapshotsBatchAt(requests []TTFTProtectionSnapshotRequest, now time.Time) [][]TTFTProtectionModelView {
	if s == nil {
		return nil
	}
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	ttl := g.ttl
	reads := make([][]ttftProtectionRead, len(requests))
	for i, r := range requests {
		reads[i] = g.copyProtectionReadsLocked(r)
	}
	g.mu.Unlock()
	out := make([][]TTFTProtectionModelView, len(requests))
	for i, r := range requests {
		out[i] = make([]TTFTProtectionModelView, 0, len(reads[i]))
		for _, read := range reads[i] {
			e := read.raw.materialize()
			state := "insufficient"
			if r.GroupID > 0 && r.Config.Enabled {
				if p, ok := read.entry(r.GroupID, r.Config); ok {
					e = p.materialize()
					state = e.stateAt(now, ttl)
				}
			} else {
				e.pruneFactsAt(now, ttl)
				e.rebuildSamples(now, ttl)
				if r.GroupID <= 0 && e.sampleCount > 0 {
					state = "observed"
				}
				if r.GroupID > 0 {
					state = "disabled"
				}
			}
			lastTTFT := int64(0)
			if e.sampleLen > 0 {
				lastTTFT = int64(e.samples[(e.nextSample-1+openAITTFTGuardSampleWindow)%openAITTFTGuardSampleWindow])
			}
			expiresAt := time.Time{}
			if !e.lastSampleAt.IsZero() {
				expiresAt = e.lastSampleAt.Add(ttl)
			}
			view := TTFTProtectionModelView{Model: read.key.model, State: state, EWMAms: e.recentEWMA(), LastTTFTMs: lastTTFT, SampleCount: e.sampleCount, LastSampleAt: e.lastSampleAt, ExpiresAt: expiresAt, Reason: e.degradedReason, RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: openAITTFTGuardRecoverySamples}
			fillTTFTProtectionEvidence(&view, &e)
			view.Executions = s.ttftExecutionViews(r.AccountID, r.KeyID, r.GroupID, read.key.model, now)
			out[i] = append(out[i], view)
		}
		sort.Slice(out[i], func(a, b int) bool { return out[i][a].Model < out[i][b].Model })
	}
	return out
}

// Administrative degradation listing shares the same indexed, read-only copy
// boundary. All requested accounts are copied in one critical section.
func (g *openAITTFTGuard) indexedDegradations(ids []int64) map[int64][]OpenAITTFTGuardDegradation {
	if g == nil {
		return nil
	}
	type requestReads struct {
		request TTFTProtectionSnapshotRequest
		reads   []ttftProtectionRead
	}
	g.mu.Lock()
	now, ttl := g.now(), g.ttl
	g.deleteExpiredLocked(now)
	requests := make([]requestReads, 0)
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		// Key zero is the legacy default; newer records use the current key.
		for _, keyID := range []int64{g.accountKeys[id]} {
			for groupID, cfg := range g.groupConfigs {
				if !cfg.Enabled {
					continue
				}
				r := TTFTProtectionSnapshotRequest{AccountID: id, KeyID: keyID, GroupID: groupID, Config: cfg}
				requests = append(requests, requestReads{request: r, reads: g.copyProtectionReadsLocked(r)})
			}
		}
	}
	g.mu.Unlock()
	out := map[int64][]OpenAITTFTGuardDegradation{}
	for _, request := range requests {
		r := request.request
		for _, read := range request.reads {
			if !read.hasRaw {
				// A projection tombstone can outlive its raw observation while
				// waiting for revalidation. It is visible in snapshots, but it
				// cannot be reported as an active degradation without raw evidence.
				continue
			}
			if now.Sub(read.raw.lastSampleAt) >= ttl {
				continue
			}
			e, ok := read.entry(r.GroupID, r.Config)
			if !ok || e.stateAt(now, ttl) != "protected" {
				continue
			}
			last := (read.raw.nextSample - 1 + openAITTFTGuardSampleWindow) % openAITTFTGuardSampleWindow
			out[r.AccountID] = append(out[r.AccountID], OpenAITTFTGuardDegradation{UpstreamKeyID: read.key.upstreamKeyID, GroupID: r.GroupID, GroupName: r.Config.GroupName, PolicySource: r.Config.Source, Model: read.key.model, Reason: e.degradedReason, ThresholdMs: r.Config.Threshold.Milliseconds(), LastTTFTMs: int64(read.raw.samples[last]), EWMAms: e.recentEWMA(), SampleCount: e.sampleCount, DegradedAt: e.degradedAt, LastSampleAt: read.raw.lastSampleAt, ExpiresAt: read.raw.lastSampleAt.Add(ttl), RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: openAITTFTGuardRecoverySamples})
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
