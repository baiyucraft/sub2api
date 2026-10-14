package service

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func accountUpstreamKeyID(a *Account) int64 {
	if a != nil && a.UpstreamKeyID != nil {
		return *a.UpstreamKeyID
	}
	return 0
}

func (s *OpenAIGatewayService) syncTTFTProtectionPolicies() {
	if s == nil {
		return
	}
	provider, ok := s.groupProtectionPolicyResolver.(interface {
		ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy
	})
	if !ok {
		return
	}
	policies := provider.ProtectionPolicies()
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	defer g.mu.Unlock()
	ids := make([]int64, 0, len(policies))
	for id := range policies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if policies[ids[i]].TTFT.Enabled != policies[ids[j]].TTFT.Enabled {
			return policies[ids[i]].TTFT.Enabled
		}
		return ids[i] < ids[j]
	})
	next := make(map[int64]OpenAITTFTGuardConfigSnapshot)
	for _, id := range ids {
		if len(next) >= g.maxEntries {
			break
		}
		p := policies[id]
		next[id] = OpenAITTFTGuardConfigSnapshot{Enabled: p.TTFT.Enabled, Threshold: p.TTFT.Threshold, MinSamples: p.TTFT.MinSamples, Source: p.TTFT.Source, GroupName: p.TTFT.GroupName}
	}
	g.groupConfigs = next
	g.pruneInactivePoliciesLocked()
}

type TTFTProtectionModelView struct {
	Model                   string    `json:"model"`
	State                   string    `json:"state"`
	EWMAms                  float64   `json:"ewma_ms"`
	LastTTFTMs              int64     `json:"last_ttft_ms"`
	SampleCount             uint64    `json:"sample_count"`
	LastSampleAt            time.Time `json:"last_sample_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	Reason                  string    `json:"reason,omitempty"`
	RecoverySamples         int       `json:"recovery_samples"`
	RecoverySamplesRequired int       `json:"recovery_samples_required"`
}

// Read-only projections do not advance trial cadence or touch observation age.
func (s *OpenAIGatewayService) TTFTProtectionSnapshots(accountID, keyID, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) []TTFTProtectionModelView {
	if s == nil {
		return nil
	}
	g := s.getOpenAITTFTGuard()
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []TTFTProtectionModelView{}
	now := g.now()
	for k, raw := range g.observations {
		if k.accountID != accountID || k.upstreamKeyID != keyID {
			continue
		}
		state := "observed"
		e := raw
		if now.Sub(raw.lastSampleAt) >= g.ttl {
			state = "expired"
		} else if groupID > 0 {
			state = "normal"
			if !cfg.Enabled {
				state = "disabled"
			} else if p := g.readProjectionLocked(k, groupID, cfg); p != nil {
				e = p
				if p.degraded {
					state = "draining"
					if p.consecutiveRecovered > 0 {
						state = "recovering"
					}
				} else if p.sampleCount < uint64(cfg.MinSamples) {
					state = "insufficient"
				}
			}
		}
		last := (raw.nextSample - 1 + openAITTFTGuardSampleWindow) % openAITTFTGuardSampleWindow
		out = append(out, TTFTProtectionModelView{Model: k.model, State: state, EWMAms: e.recentEWMA(), LastTTFTMs: int64(raw.samples[last]), SampleCount: e.sampleCount, LastSampleAt: raw.lastSampleAt, ExpiresAt: raw.lastSampleAt.Add(g.ttl), Reason: e.degradedReason, RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: openAITTFTGuardRecoverySamples})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

type openAITTFTTrial struct {
	guard         *openAITTFTGuard
	key           openAITTFTGuardKey
	degradedAt    time.Time
	cycle         uint64
	generation    uint64
	recoveryEpoch uint64
	once          sync.Once
	sent          atomic.Bool
}

func (g *openAITTFTGuard) claimTrial(groupID int64, a *Account, model string, cfg OpenAITTFTGuardConfigSnapshot) *openAITTFTTrial {
	if g == nil || a == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	scope, ok := openAITTFTGuardScopeFor(groupID, cfg)
	if !ok {
		return nil
	}
	k, ok := g.sampleKeyLocked(a.ID, accountUpstreamKeyID(a), model)
	if !ok {
		return nil
	}
	k.scopeID = scope
	e := g.entries[k]
	p := g.groupProbes[openAITTFTGuardProbeKey{scopeID: scope, model: k.model}]
	if e == nil || !e.degraded || p == nil || p.nominated != a.ID {
		return nil
	}
	p.nominated = 0
	e.trialCycle = p.cycle
	return &openAITTFTTrial{guard: g, key: k, degradedAt: e.degradedAt, cycle: p.cycle, generation: e.generation, recoveryEpoch: e.recoveryEpoch}
}

func (t *openAITTFTTrial) finish(success bool, ms *int) {
	if t == nil {
		return
	}
	t.once.Do(func() {
		g := t.guard
		g.mu.Lock()
		defer g.mu.Unlock()
		e := g.entries[t.key]
		if e == nil || !e.degraded || !e.degradedAt.Equal(t.degradedAt) || e.generation != t.generation || e.recoveryEpoch != t.recoveryEpoch {
			return
		}
		if e.highestCompletedCycle > t.cycle && e.highestCompletedCycle-t.cycle >= uint64(len(e.completedTrialCycles)) {
			return
		}
		for _, cycle := range e.completedTrialCycles {
			if cycle == t.cycle {
				return
			}
		}
		e.completedTrialCycles[e.completedTrialCount%len(e.completedTrialCycles)] = t.cycle
		e.completedTrialCount++
		if t.cycle > e.highestCompletedCycle {
			e.highestCompletedCycle = t.cycle
		}
		if success && ms != nil && *ms > 0 && float64(*ms) <= .6*float64(e.config.Threshold.Milliseconds()) {
			e.consecutiveRecovered++
		} else {
			e.consecutiveRecovered = 0
		}
		if e.consecutiveRecovered >= openAITTFTGuardRecoverySamples {
			e.resetAfterRecovery(float64(*ms), g.now())
		}
	})
}
