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
	projection    ttftProtectionEntryCopy
	hasProjection bool
}

// Ticket deduplication history and mutable LRU bookkeeping are never needed
// by administration; copying those 64-cycle arrays would dominate page memory.
type ttftProtectionEntryCopy struct {
	samples                  [openAITTFTGuardSampleWindow]float64
	successes                [openAITTFTGuardSampleWindow]bool
	sampleLen, nextSample    int
	sampleCount              uint64
	lastSampleAt, degradedAt time.Time
	degraded                 bool
	degradedReason           string
	consecutiveRecovered     int
}

func copyTTFTProtectionEntry(e *openAITTFTGuardEntry) ttftProtectionEntryCopy {
	return ttftProtectionEntryCopy{samples: e.samples, successes: e.successes, sampleLen: e.sampleLen, nextSample: e.nextSample, sampleCount: e.sampleCount, lastSampleAt: e.lastSampleAt, degradedAt: e.degradedAt, degraded: e.degraded, degradedReason: e.degradedReason, consecutiveRecovered: e.consecutiveRecovered}
}

func (e *ttftProtectionEntryCopy) materialize() openAITTFTGuardEntry {
	return openAITTFTGuardEntry{samples: e.samples, successes: e.successes, sampleLen: e.sampleLen, nextSample: e.nextSample, sampleCount: e.sampleCount, lastSampleAt: e.lastSampleAt, degradedAt: e.degradedAt, degraded: e.degraded, degradedReason: e.degradedReason, consecutiveRecovered: e.consecutiveRecovered}
}

func (e *ttftProtectionEntryCopy) recentEWMA() float64 {
	entry := e.materialize()
	return entry.recentEWMA()
}

// Only bounded value copies happen under the runtime lock. Policy replay,
// EWMA computation, presentation and sorting run after the lock is released.
func (g *openAITTFTGuard) copyProtectionReadsLocked(r TTFTProtectionSnapshotRequest) []ttftProtectionRead {
	models := g.observationModels[openAITTFTGuardAccountKey{r.AccountID, r.KeyID}]
	reads := make([]ttftProtectionRead, 0, len(models))
	scope, _ := openAITTFTGuardScopeFor(r.GroupID, r.Config)
	for key := range models {
		raw := g.observations[key]
		if raw == nil {
			continue
		}
		read := ttftProtectionRead{key: key, raw: copyTTFTProtectionEntry(raw)}
		if r.GroupID > 0 && r.Config.Enabled {
			pk := key
			pk.scopeID = scope
			if p := g.entries[pk]; p != nil {
				read.projection = copyTTFTProtectionEntry(p)
				read.hasProjection = true
			}
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
			state := "observed"
			e := read.raw
			if now.Sub(read.raw.lastSampleAt) >= ttl {
				state = "expired"
			} else if r.GroupID > 0 {
				state = "normal"
				if !r.Config.Enabled {
					state = "disabled"
				} else if p, ok := read.entry(r.GroupID, r.Config); ok {
					e = p
					if p.degraded {
						state = "draining"
						if p.consecutiveRecovered > 0 {
							state = "recovering"
						}
					} else if p.sampleCount < uint64(r.Config.MinSamples) {
						state = "insufficient"
					}
				}
			}
			last := (read.raw.nextSample - 1 + openAITTFTGuardSampleWindow) % openAITTFTGuardSampleWindow
			out[i] = append(out[i], TTFTProtectionModelView{Model: read.key.model, State: state, EWMAms: e.recentEWMA(), LastTTFTMs: int64(read.raw.samples[last]), SampleCount: e.sampleCount, LastSampleAt: read.raw.lastSampleAt, ExpiresAt: read.raw.lastSampleAt.Add(ttl), Reason: e.degradedReason, RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: openAITTFTGuardRecoverySamples})
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
			if now.Sub(read.raw.lastSampleAt) >= ttl {
				continue
			}
			e, ok := read.entry(r.GroupID, r.Config)
			if !ok || !e.degraded {
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
