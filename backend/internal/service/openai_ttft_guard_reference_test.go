package service

// Frozen pre-optimization runtime from .tmp/protection-perf-baseline. This
// independent test oracle deliberately retains the former full scans and
// lock scope, enabling event-for-event differential tests and same-process
// baseline benchmarks. Keep behavioral changes out of this fixture.
import (
	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	referenceDefaultTTFTGuardThreshold  = 20 * time.Second
	referenceDefaultTTFTGuardMinSamples = 5
	referenceTTFTGuardSampleWindow      = 10
	referenceTTFTGuardRecoverySamples   = 3
	referenceTTFTGuardProbeEvery        = 20
	referenceTTFTGuardEntryTTL          = 15 * time.Minute
	referenceTTFTGuardDefaultMaxEntries = 4096
	referenceTTFTGuardEWMAAlpha         = 0.2
)

type referenceTTFTGuardKey struct {
	scopeID       int64 // Effective-policy signature; zero is the shared observation identity.
	upstreamKeyID int64
	accountID     int64
	model         string
}

type referenceTTFTGuardEntry struct {
	lastGroupID           int64
	lastGroupName         string
	samples               [referenceTTFTGuardSampleWindow]float64
	successes             [referenceTTFTGuardSampleWindow]bool
	sampleLen             int
	nextSample            int
	sampleCount           uint64
	consecutiveElevated   int
	consecutiveRecovered  int
	degraded              bool
	degradedReason        string
	degradedAt            time.Time
	lastSampleAt          time.Time
	lastTouchedAt         time.Time
	config                OpenAITTFTGuardConfigSnapshot
	trialCycle            uint64
	generation            uint64
	recoveryEpoch         uint64
	completedTrialCycles  [64]uint64
	completedTrialCount   int
	highestCompletedCycle uint64
}

func (e *referenceTTFTGuardEntry) addSample(sample float64) {
	e.samples[e.nextSample] = sample
	e.nextSample = (e.nextSample + 1) % referenceTTFTGuardSampleWindow
	if e.sampleLen < referenceTTFTGuardSampleWindow {
		e.sampleLen++
	}
	if e.sampleCount < math.MaxUint64 {
		e.sampleCount++
	}
}

func (e *referenceTTFTGuardEntry) recentEWMA() float64 {
	if e == nil || e.sampleLen == 0 {
		return 0
	}
	start := 0
	if e.sampleLen == referenceTTFTGuardSampleWindow {
		start = e.nextSample
	}
	ewma := e.samples[start]
	for i := 1; i < e.sampleLen; i++ {
		sample := e.samples[(start+i)%referenceTTFTGuardSampleWindow]
		ewma = referenceTTFTGuardEWMAAlpha*sample + (1-referenceTTFTGuardEWMAAlpha)*ewma
	}
	return ewma
}

func (e *referenceTTFTGuardEntry) resetAfterRecovery(fastSample float64, now time.Time) {
	e.recoveryEpoch++
	e.completedTrialCycles = [64]uint64{}
	e.completedTrialCount = 0
	e.highestCompletedCycle = 0
	e.samples = [referenceTTFTGuardSampleWindow]float64{}
	e.sampleLen = 1
	e.nextSample = 1
	e.samples[0] = fastSample
	e.sampleCount = 1
	e.consecutiveElevated = 0
	e.consecutiveRecovered = 0
	e.degraded = false
	e.degradedReason = ""
	e.degradedAt = time.Time{}
	e.lastSampleAt = now
	e.lastTouchedAt = now
}

type referenceTTFTGuardCandidate struct {
	upstreamKeyID int64
	groupID       int64
	accountID     int64
	model         string
}

type referenceTTFTGuard struct {
	mu                  sync.Mutex
	entries             map[referenceTTFTGuardKey]*referenceTTFTGuardEntry
	observations        map[referenceTTFTGuardKey]*referenceTTFTGuardEntry
	accountKeys         map[int64]int64
	maxEntries          int
	ttl                 time.Duration
	now                 func() time.Time
	groupProbes         map[referenceTTFTGuardProbeKey]*referenceTTFTGuardProbeState
	nextExpiryAt        time.Time
	groupConfigs        map[int64]OpenAITTFTGuardConfigSnapshot
	nextEntryGeneration uint64
}

type referenceTTFTGuardProbeKey struct {
	scopeID int64
	model   string
}

type referenceTTFTGuardProbeState struct {
	sequence  uint64
	cursor    uint64
	nominated int64
	cycle     uint64
}

func newReferenceTTFTGuard() *referenceTTFTGuard {
	return newReferenceTTFTGuardWithOptions(referenceTTFTGuardDefaultMaxEntries, referenceTTFTGuardEntryTTL, time.Now)
}

func newReferenceTTFTGuardWithOptions(maxEntries int, ttl time.Duration, now func() time.Time) *referenceTTFTGuard {
	if maxEntries <= 0 {
		maxEntries = referenceTTFTGuardDefaultMaxEntries
	}
	if ttl <= 0 {
		ttl = referenceTTFTGuardEntryTTL
	}
	if now == nil {
		now = time.Now
	}
	return &referenceTTFTGuard{
		entries:      make(map[referenceTTFTGuardKey]*referenceTTFTGuardEntry),
		observations: make(map[referenceTTFTGuardKey]*referenceTTFTGuardEntry),
		accountKeys:  make(map[int64]int64),
		groupProbes:  make(map[referenceTTFTGuardProbeKey]*referenceTTFTGuardProbeState),
		groupConfigs: make(map[int64]OpenAITTFTGuardConfigSnapshot),
		maxEntries:   maxEntries,
		ttl:          ttl,
		now:          now,
	}
}

func referenceTTFTGuardKeyFor(scopeID, accountID int64, model string) (referenceTTFTGuardKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if scopeID < 0 || accountID <= 0 || model == "" {
		return referenceTTFTGuardKey{}, false
	}
	return referenceTTFTGuardKey{scopeID: scopeID, accountID: accountID, model: model}, true
}

func (g *referenceTTFTGuard) size() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteExpiredLocked(g.now())
	return len(g.entries)
}

func sameReferenceTTFTGuardRuntimeConfig(left, right OpenAITTFTGuardConfigSnapshot) bool {
	left = normalizeOpenAITTFTGuardConfig(left)
	right = normalizeOpenAITTFTGuardConfig(right)
	return left.Enabled == right.Enabled && left.Threshold == right.Threshold && left.MinSamples == right.MinSamples && left.Source == right.Source
}

func (g *referenceTTFTGuard) clearGroup(groupID int64) {
	if g == nil || groupID <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clearGroupLocked(groupID)
}

func (g *referenceTTFTGuard) clearGlobal() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clearScopeLocked(0)
}

func (g *referenceTTFTGuard) recalculateNextExpiryLocked() {
	g.nextExpiryAt = time.Time{}
	for _, entry := range g.entries {
		if entry == nil || entry.lastSampleAt.IsZero() {
			continue
		}
		expiresAt := entry.lastSampleAt.Add(g.ttl)
		if g.nextExpiryAt.IsZero() || expiresAt.Before(g.nextExpiryAt) {
			g.nextExpiryAt = expiresAt
		}
	}
}

func (g *referenceTTFTGuard) evictLRULocked() {
	if len(g.entries) < g.maxEntries {
		return
	}
	var oldestKey referenceTTFTGuardKey
	var oldestTime time.Time
	found := false
	for key, entry := range g.entries {
		if entry == nil {
			oldestKey = key
			found = true
			break
		}
		if !found || entry.lastTouchedAt.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.lastTouchedAt
			found = true
		}
	}
	if found {
		delete(g.entries, oldestKey)
	}
}

// A policy is a projection of shared observations, never a group-owned sample
// store. Identical thresholds share recovery progress regardless of source.
func referenceTTFTGuardScopeFor(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) (int64, bool) {
	if groupID <= 0 || !validOpenAITTFTGuardConfig(cfg) {
		return 0, false
	}
	return cfg.Threshold.Milliseconds()*100 + int64(cfg.MinSamples), true
}

func (g *referenceTTFTGuard) sampleKeyLocked(accountID, keyID int64, model string) (referenceTTFTGuardKey, bool) {
	k, ok := referenceTTFTGuardKeyFor(0, accountID, model)
	if keyID == 0 {
		keyID = g.accountKeys[accountID]
	}
	k.upstreamKeyID = keyID
	return k, ok
}

func (g *referenceTTFTGuard) report(groupID, accountID int64, model string, success bool, ms *int, cfg OpenAITTFTGuardConfigSnapshot) {
	g.reportForKey(groupID, accountID, 0, model, success, ms, cfg)
}

func (g *referenceTTFTGuard) reportForKey(groupID, accountID, keyID int64, model string, success bool, ms *int, cfg OpenAITTFTGuardConfigSnapshot) {
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
			var oldest referenceTTFTGuardKey
			var at time.Time
			for id, e := range g.observations {
				if at.IsZero() || e.lastSampleAt.Before(at) {
					oldest, at = id, e.lastSampleAt
				}
			}
			delete(g.observations, oldest)
		}
		raw = &referenceTTFTGuardEntry{}
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
			applyReferenceTTFTProjectionSample(e, float64(*ms), success, now)
		}
	}
	if cfg.Enabled {
		g.projectionLocked(k, groupID, cfg)
	}
	g.recalculateNextExpiryLocked()
}

func applyReferenceTTFTProjectionSample(e *referenceTTFTGuardEntry, sample float64, success bool, now time.Time) {
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

func (g *referenceTTFTGuard) projectionLocked(rawKey referenceTTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *referenceTTFTGuardEntry {
	scope, ok := referenceTTFTGuardScopeFor(groupID, cfg)
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
	e := deriveReferenceTTFTProjection(raw, groupID, cfg)
	g.nextEntryGeneration++
	e.generation = g.nextEntryGeneration
	g.entries[k] = e
	return e
}

func deriveReferenceTTFTProjection(raw *referenceTTFTGuardEntry, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *referenceTTFTGuardEntry {
	e := &referenceTTFTGuardEntry{config: cfg, lastGroupID: groupID, lastGroupName: cfg.GroupName}
	// The cumulative count preserves min_samples > ring size; only bounded
	// recent evidence is replayed, never invented historical recovery samples.
	e.sampleCount = raw.sampleCount - uint64(raw.sampleLen)
	start := 0
	if raw.sampleLen == referenceTTFTGuardSampleWindow {
		start = raw.nextSample
	}
	for i := 0; i < raw.sampleLen; i++ {
		j := (start + i) % referenceTTFTGuardSampleWindow
		applyReferenceTTFTProjectionSample(e, raw.samples[j], raw.successes[j], raw.lastSampleAt)
	}
	return e
}

func (g *referenceTTFTGuard) readProjectionLocked(key referenceTTFTGuardKey, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) *referenceTTFTGuardEntry {
	scope, ok := referenceTTFTGuardScopeFor(groupID, cfg)
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
	return deriveReferenceTTFTProjection(raw, groupID, cfg)
}

func (g *referenceTTFTGuard) syncConfigLocked(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
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

func (g *referenceTTFTGuard) pruneInactivePoliciesLocked() {
	active := map[int64]bool{}
	for id, cfg := range g.groupConfigs {
		if scope, ok := referenceTTFTGuardScopeFor(id, cfg); ok && cfg.Enabled {
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

func (g *referenceTTFTGuard) exclusions(candidates []referenceTTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot, tickets ...map[int64]*referenceTTFTTrial) map[int64]struct{} {
	if g == nil || len(candidates) == 0 {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	groupID := candidates[0].groupID
	g.syncConfigLocked(groupID, cfg)
	scope, ok := referenceTTFTGuardScopeFor(groupID, cfg)
	if !ok || !cfg.Enabled {
		return nil
	}
	g.deleteExpiredLocked(g.now())
	byModel := map[string][]int64{}
	seen := map[referenceTTFTGuardKey]bool{}
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
		k := referenceTTFTGuardProbeKey{scopeID: scope, model: m}
		probe := g.groupProbes[k]
		if probe == nil {
			if len(g.groupProbes) >= g.maxEntries {
				for old := range g.groupProbes {
					delete(g.groupProbes, old)
					break
				}
			}
			probe = &referenceTTFTGuardProbeState{}
			g.groupProbes[k] = probe
		}
		probe.sequence++
		admit := int64(0)
		probe.nominated = 0
		if probe.sequence%referenceTTFTGuardProbeEvery == 0 {
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
						tickets[0][admit] = &referenceTTFTTrial{guard: g, key: key, degradedAt: e.degradedAt, cycle: probe.cycle, generation: e.generation, recoveryEpoch: e.recoveryEpoch}
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

func (g *referenceTTFTGuard) quality(candidates []referenceTTFTGuardCandidate, cfg OpenAITTFTGuardConfigSnapshot) map[int64]forkscheduling.TTFTQuality {
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

func (g *referenceTTFTGuard) degradations(ids []int64) map[int64][]OpenAITTFTGuardDegradation {
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
			last := (raw.nextSample - 1 + referenceTTFTGuardSampleWindow) % referenceTTFTGuardSampleWindow
			out[key.accountID] = append(out[key.accountID], OpenAITTFTGuardDegradation{UpstreamKeyID: key.upstreamKeyID, GroupID: groupID, GroupName: cfg.GroupName, PolicySource: cfg.Source, Model: key.model, Reason: e.degradedReason, ThresholdMs: cfg.Threshold.Milliseconds(), LastTTFTMs: int64(raw.samples[last]), EWMAms: e.recentEWMA(), SampleCount: e.sampleCount, DegradedAt: e.degradedAt, LastSampleAt: raw.lastSampleAt, ExpiresAt: raw.lastSampleAt.Add(g.ttl), RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: referenceTTFTGuardRecoverySamples})
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

func (g *referenceTTFTGuard) isDegraded(groupID, accountID int64, model string) bool {
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
func (g *referenceTTFTGuard) deleteExpiredLocked(now time.Time) {
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
func (g *referenceTTFTGuard) clearGroupLocked(groupID int64) { delete(g.groupConfigs, groupID) }
func (g *referenceTTFTGuard) clearScopeLocked(scopeID int64) { delete(g.groupConfigs, scopeID) }
func (g *referenceTTFTGuard) clearForConfig(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
	if g != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.syncConfigLocked(groupID, cfg)
	}
}
func (g *referenceTTFTGuard) clearInheritedConfigMismatch(cfg OpenAITTFTGuardConfigSnapshot) {
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

type referenceTTFTTrial struct {
	guard         *referenceTTFTGuard
	key           referenceTTFTGuardKey
	degradedAt    time.Time
	cycle         uint64
	generation    uint64
	recoveryEpoch uint64
	once          sync.Once
	sent          atomic.Bool
}

func (g *referenceTTFTGuard) claimTrial(groupID int64, a *Account, model string, cfg OpenAITTFTGuardConfigSnapshot) *referenceTTFTTrial {
	if g == nil || a == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	scope, ok := referenceTTFTGuardScopeFor(groupID, cfg)
	if !ok {
		return nil
	}
	k, ok := g.sampleKeyLocked(a.ID, accountUpstreamKeyID(a), model)
	if !ok {
		return nil
	}
	k.scopeID = scope
	e := g.entries[k]
	p := g.groupProbes[referenceTTFTGuardProbeKey{scopeID: scope, model: k.model}]
	if e == nil || !e.degraded || p == nil || p.nominated != a.ID {
		return nil
	}
	p.nominated = 0
	e.trialCycle = p.cycle
	return &referenceTTFTTrial{guard: g, key: k, degradedAt: e.degradedAt, cycle: p.cycle, generation: e.generation, recoveryEpoch: e.recoveryEpoch}
}

func (t *referenceTTFTTrial) finish(success bool, ms *int) {
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
		if e.consecutiveRecovered >= referenceTTFTGuardRecoverySamples {
			e.resetAfterRecovery(float64(*ms), g.now())
		}
	})
}

// Read-only projections do not advance trial cadence or touch observation age.
func (g *referenceTTFTGuard) snapshots(accountID, keyID, groupID int64, cfg OpenAITTFTGuardConfigSnapshot) []TTFTProtectionModelView {
	if g == nil {
		return nil
	}
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
			state = "insufficient"
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
		last := (raw.nextSample - 1 + referenceTTFTGuardSampleWindow) % referenceTTFTGuardSampleWindow
		out = append(out, TTFTProtectionModelView{Model: k.model, State: state, EWMAms: e.recentEWMA(), LastTTFTMs: int64(raw.samples[last]), SampleCount: e.sampleCount, LastSampleAt: raw.lastSampleAt, ExpiresAt: raw.lastSampleAt.Add(g.ttl), Reason: e.degradedReason, RecoverySamples: e.consecutiveRecovered, RecoverySamplesRequired: referenceTTFTGuardRecoverySamples})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}
