package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

const (
	defaultOpenAITTFTGuardThreshold  = 20 * time.Second
	defaultOpenAITTFTGuardMinSamples = 5
	openAITTFTGuardSampleWindow      = 10
	openAITTFTGuardRecoverySamples   = 3
	openAITTFTGuardProbeEvery        = 20
	openAITTFTGuardEntryTTL          = 15 * time.Minute
	openAITTFTGuardDefaultMaxEntries = 4096
	openAITTFTGuardEWMAAlpha         = 0.2
)

type OpenAITTFTGuardConfigSnapshot struct {
	Enabled    bool
	Threshold  time.Duration
	MinSamples int
	Source     string
	GroupName  string
}

type OpenAITTFTGuardConfigProvider interface {
	OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot
}

// OpenAITTFTGuardDegradation is a read-only snapshot of a degraded
// account/model pair. It intentionally contains only in-memory runtime state;
// it is not persisted and must not be used to make scheduling decisions.
type OpenAITTFTGuardDegradation struct {
	GroupID                 int64     `json:"group_id"`
	GroupName               string    `json:"group_name,omitempty"`
	PolicySource            string    `json:"policy_source"`
	Model                   string    `json:"model"`
	Reason                  string    `json:"reason"`
	ThresholdMs             int64     `json:"threshold_ms"`
	LastTTFTMs              int64     `json:"last_ttft_ms"`
	EWMAms                  float64   `json:"ewma_ms"`
	SampleCount             uint64    `json:"sample_count"`
	DegradedAt              time.Time `json:"degraded_at"`
	LastSampleAt            time.Time `json:"last_sample_at"`
	ExpiresAt               time.Time `json:"expires_at"`
	RecoverySamples         int       `json:"recovery_samples"`
	RecoverySamplesRequired int       `json:"recovery_samples_required"`
}

// OpenAITTFTGuardDegradationReader provides a batch, read-only view of
// currently degraded account/model states for administrative observability.
type OpenAITTFTGuardDegradationReader interface {
	OpenAITTFTGuardDegradations(accountIDs []int64) map[int64][]OpenAITTFTGuardDegradation
}

type openAITTFTGuardConfigProviderFunc func() OpenAITTFTGuardConfigSnapshot

func (f openAITTFTGuardConfigProviderFunc) OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot {
	return f()
}

func normalizeOpenAITTFTGuardConfig(cfg OpenAITTFTGuardConfigSnapshot) OpenAITTFTGuardConfigSnapshot {
	defaults := OpenAITTFTGuardConfigSnapshot{
		Threshold:  defaultOpenAITTFTGuardThreshold,
		MinSamples: defaultOpenAITTFTGuardMinSamples,
		Source:     cfg.Source,
		GroupName:  cfg.GroupName,
	}
	if !validOpenAITTFTGuardConfig(cfg) {
		return defaults
	}
	return cfg
}

func validOpenAITTFTGuardConfig(cfg OpenAITTFTGuardConfigSnapshot) bool {
	return cfg.Threshold >= 5*time.Second && cfg.Threshold <= 300*time.Second && cfg.MinSamples >= 2 && cfg.MinSamples <= 20
}

type openAITTFTGuardKey struct {
	groupID   int64
	accountID int64
	model     string
}

type openAITTFTGuardEntry struct {
	samples              [openAITTFTGuardSampleWindow]float64
	sampleLen            int
	nextSample           int
	sampleCount          uint64
	consecutiveElevated  int
	consecutiveRecovered int
	degraded             bool
	degradedReason       string
	degradedAt           time.Time
	lastSampleAt         time.Time
	lastTouchedAt        time.Time
	config               OpenAITTFTGuardConfigSnapshot
}

func (e *openAITTFTGuardEntry) addSample(sample float64) {
	e.samples[e.nextSample] = sample
	e.nextSample = (e.nextSample + 1) % openAITTFTGuardSampleWindow
	if e.sampleLen < openAITTFTGuardSampleWindow {
		e.sampleLen++
	}
	if e.sampleCount < math.MaxUint64 {
		e.sampleCount++
	}
}

func (e *openAITTFTGuardEntry) recentEWMA() float64 {
	if e == nil || e.sampleLen == 0 {
		return 0
	}
	start := 0
	if e.sampleLen == openAITTFTGuardSampleWindow {
		start = e.nextSample
	}
	ewma := e.samples[start]
	for i := 1; i < e.sampleLen; i++ {
		sample := e.samples[(start+i)%openAITTFTGuardSampleWindow]
		ewma = openAITTFTGuardEWMAAlpha*sample + (1-openAITTFTGuardEWMAAlpha)*ewma
	}
	return ewma
}

func (e *openAITTFTGuardEntry) resetAfterRecovery(fastSample float64, now time.Time) {
	e.samples = [openAITTFTGuardSampleWindow]float64{}
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

type openAITTFTGuardCandidate struct {
	groupID   int64
	accountID int64
	model     string
}

type openAITTFTGuard struct {
	mu           sync.Mutex
	entries      map[openAITTFTGuardKey]*openAITTFTGuardEntry
	maxEntries   int
	ttl          time.Duration
	now          func() time.Time
	groupProbes  map[openAITTFTGuardProbeKey]*openAITTFTGuardProbeState
	nextExpiryAt time.Time
	groupConfigs map[int64]OpenAITTFTGuardConfigSnapshot
}

type openAITTFTGuardProbeKey struct {
	groupID int64
	model   string
}

type openAITTFTGuardProbeState struct {
	sequence uint64
	cursor   uint64
}

func newOpenAITTFTGuard() *openAITTFTGuard {
	return newOpenAITTFTGuardWithOptions(openAITTFTGuardDefaultMaxEntries, openAITTFTGuardEntryTTL, time.Now)
}

func newOpenAITTFTGuardWithOptions(maxEntries int, ttl time.Duration, now func() time.Time) *openAITTFTGuard {
	if maxEntries <= 0 {
		maxEntries = openAITTFTGuardDefaultMaxEntries
	}
	if ttl <= 0 {
		ttl = openAITTFTGuardEntryTTL
	}
	if now == nil {
		now = time.Now
	}
	return &openAITTFTGuard{
		entries:      make(map[openAITTFTGuardKey]*openAITTFTGuardEntry),
		groupProbes:  make(map[openAITTFTGuardProbeKey]*openAITTFTGuardProbeState),
		groupConfigs: make(map[int64]OpenAITTFTGuardConfigSnapshot),
		maxEntries:   maxEntries,
		ttl:          ttl,
		now:          now,
	}
}

func openAITTFTGuardKeyFor(groupID, accountID int64, model string) (openAITTFTGuardKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if groupID <= 0 || accountID <= 0 || model == "" {
		return openAITTFTGuardKey{}, false
	}
	return openAITTFTGuardKey{groupID: groupID, accountID: accountID, model: model}, true
}

func (g *openAITTFTGuard) report(groupID, accountID int64, model string, success bool, firstTokenMs *int, cfg OpenAITTFTGuardConfigSnapshot) {
	if g == nil {
		return
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.syncConfigLocked(groupID, cfg)
	if !cfg.Enabled || firstTokenMs == nil || *firstTokenMs <= 0 {
		return
	}
	key, ok := openAITTFTGuardKeyFor(groupID, accountID, model)
	if !ok {
		return
	}
	now := g.now()
	sample := float64(*firstTokenMs)
	threshold := float64(cfg.Threshold.Milliseconds())
	g.deleteExpiredLocked(now)
	entry := g.entries[key]
	if entry == nil {
		g.evictLRULocked()
		entry = &openAITTFTGuardEntry{config: cfg}
		g.entries[key] = entry
	}
	entry.lastTouchedAt = now
	entry.lastSampleAt = now
	entry.addSample(sample)
	expiresAt := now.Add(g.ttl)
	if g.nextExpiryAt.IsZero() || expiresAt.Before(g.nextExpiryAt) {
		g.nextExpiryAt = expiresAt
	}

	if entry.degraded {
		if success && sample <= 0.6*threshold {
			entry.consecutiveRecovered++
		} else {
			entry.consecutiveRecovered = 0
		}
		if entry.consecutiveRecovered >= openAITTFTGuardRecoverySamples {
			entry.resetAfterRecovery(sample, now)
		}
		return
	}

	if sample >= 1.5*threshold {
		entry.consecutiveElevated++
	} else {
		entry.consecutiveElevated = 0
	}
	switch {
	case sample >= 3*threshold:
		entry.degraded = true
		entry.degradedReason = "critical_sample"
		entry.degradedAt = now
	case entry.consecutiveElevated >= 2:
		entry.degraded = true
		entry.degradedReason = "consecutive_elevated"
		entry.degradedAt = now
	case entry.sampleCount >= uint64(cfg.MinSamples) && entry.recentEWMA() >= threshold:
		entry.degraded = true
		entry.degradedReason = "ewma"
		entry.degradedAt = now
	}
}

// degradations returns a copied snapshot while holding the guard lock. The
// lookup performs TTL cleanup and config synchronization under the same lock,
// but deliberately does not update LRU touch timestamps.
func (g *openAITTFTGuard) degradations(accountIDs []int64) map[int64][]OpenAITTFTGuardDegradation {
	if g == nil {
		return nil
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(accountIDs) == 0 {
		return nil
	}
	g.deleteExpiredLocked(now)

	requested := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID > 0 {
			requested[accountID] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return nil
	}

	result := make(map[int64][]OpenAITTFTGuardDegradation)
	for key, entry := range g.entries {
		if entry == nil || !entry.degraded {
			continue
		}
		if _, ok := requested[key.accountID]; !ok {
			continue
		}
		degradedAt := entry.degradedAt
		if degradedAt.IsZero() {
			// Defensive fallback for entries created before degraded_at was added.
			degradedAt = entry.lastSampleAt
		}
		lastTTFTMs := int64(0)
		if entry.sampleLen > 0 {
			lastIndex := (entry.nextSample - 1 + openAITTFTGuardSampleWindow) % openAITTFTGuardSampleWindow
			lastTTFTMs = int64(entry.samples[lastIndex])
		}
		result[key.accountID] = append(result[key.accountID], OpenAITTFTGuardDegradation{
			GroupID:                 key.groupID,
			GroupName:               entry.config.GroupName,
			PolicySource:            entry.config.Source,
			Model:                   key.model,
			Reason:                  entry.degradedReason,
			ThresholdMs:             entry.config.Threshold.Milliseconds(),
			LastTTFTMs:              lastTTFTMs,
			EWMAms:                  entry.recentEWMA(),
			SampleCount:             entry.sampleCount,
			DegradedAt:              degradedAt,
			LastSampleAt:            entry.lastSampleAt,
			ExpiresAt:               entry.lastSampleAt.Add(g.ttl),
			RecoverySamples:         entry.consecutiveRecovered,
			RecoverySamplesRequired: openAITTFTGuardRecoverySamples,
		})
	}
	for accountID := range result {
		sort.Slice(result[accountID], func(i, j int) bool {
			if result[accountID][i].GroupID != result[accountID][j].GroupID {
				return result[accountID][i].GroupID < result[accountID][j].GroupID
			}
			return result[accountID][i].Model < result[accountID][j].Model
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func (g *openAITTFTGuard) exclusions(candidates []openAITTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot) map[int64]struct{} {
	if g == nil {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(candidates) == 0 {
		return nil
	}
	groupID := candidates[0].groupID
	g.syncConfigLocked(groupID, cfg)
	if !cfg.Enabled || groupID <= 0 {
		return nil
	}
	g.deleteExpiredLocked(now)

	degradedByModel := make(map[string][]int64)
	seen := make(map[openAITTFTGuardKey]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, excluded := callerExcluded[candidate.accountID]; excluded {
			continue
		}
		if candidate.groupID != groupID {
			continue
		}
		key, ok := openAITTFTGuardKeyFor(groupID, candidate.accountID, candidate.model)
		if !ok {
			continue
		}
		entry := g.entries[key]
		if entry == nil || !entry.degraded {
			continue
		}
		entry.lastTouchedAt = now
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		degradedByModel[key.model] = append(degradedByModel[key.model], candidate.accountID)
	}
	if len(degradedByModel) == 0 {
		return nil
	}
	models := make([]string, 0, len(degradedByModel))
	for model := range degradedByModel {
		models = append(models, model)
	}
	sort.Strings(models)

	excluded := make(map[int64]struct{}, len(candidates))
	for _, model := range models {
		degraded := degradedByModel[model]
		sort.Slice(degraded, func(i, j int) bool { return degraded[i] < degraded[j] })
		probeKey := openAITTFTGuardProbeKey{groupID: groupID, model: model}
		probe := g.groupProbes[probeKey]
		if probe == nil {
			probe = &openAITTFTGuardProbeState{}
			g.groupProbes[probeKey] = probe
		}
		probe.sequence++
		probeAccountID := int64(0)
		if probe.sequence%openAITTFTGuardProbeEvery == 0 {
			probeAccountID = degraded[probe.cursor%uint64(len(degraded))]
			probe.cursor++
		}
		for _, accountID := range degraded {
			if accountID != probeAccountID {
				excluded[accountID] = struct{}{}
			}
		}
	}
	return excluded
}

func (g *openAITTFTGuard) isDegraded(groupID, accountID int64, model string) bool {
	if g == nil {
		return false
	}
	key, ok := openAITTFTGuardKeyFor(groupID, accountID, model)
	if !ok {
		return false
	}
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteExpiredLocked(now)
	entry := g.entries[key]
	if entry == nil {
		return false
	}
	entry.lastTouchedAt = now
	return entry.degraded
}

func (g *openAITTFTGuard) size() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleteExpiredLocked(g.now())
	return len(g.entries)
}

func (g *openAITTFTGuard) deleteExpiredLocked(now time.Time) {
	if !g.nextExpiryAt.IsZero() && now.Before(g.nextExpiryAt) {
		return
	}
	var nextExpiryAt time.Time
	for key, entry := range g.entries {
		if entry == nil || entry.lastSampleAt.IsZero() || now.Sub(entry.lastSampleAt) >= g.ttl {
			delete(g.entries, key)
			continue
		}
		expiresAt := entry.lastSampleAt.Add(g.ttl)
		if nextExpiryAt.IsZero() || expiresAt.Before(nextExpiryAt) {
			nextExpiryAt = expiresAt
		}
	}
	g.nextExpiryAt = nextExpiryAt
}

func (g *openAITTFTGuard) syncConfigLocked(groupID int64, cfg OpenAITTFTGuardConfigSnapshot) {
	if groupID <= 0 {
		return
	}
	if previous, ok := g.groupConfigs[groupID]; ok && sameOpenAITTFTGuardRuntimeConfig(previous, cfg) {
		g.groupConfigs[groupID] = cfg
		for key, entry := range g.entries {
			if key.groupID == groupID && entry != nil {
				entry.config = cfg
			}
		}
		return
	}
	g.clearGroupLocked(groupID)
	if cfg.Enabled {
		g.groupConfigs[groupID] = cfg
	}
}

func sameOpenAITTFTGuardRuntimeConfig(left, right OpenAITTFTGuardConfigSnapshot) bool {
	return left.Enabled == right.Enabled && left.Threshold == right.Threshold && left.MinSamples == right.MinSamples && left.Source == right.Source
}

func (g *openAITTFTGuard) clearGroup(groupID int64) {
	if g == nil || groupID <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clearGroupLocked(groupID)
}

func (g *openAITTFTGuard) clearGroupLocked(groupID int64) {
	for key := range g.entries {
		if key.groupID == groupID {
			delete(g.entries, key)
		}
	}
	delete(g.groupConfigs, groupID)
	for key := range g.groupProbes {
		if key.groupID == groupID {
			delete(g.groupProbes, key)
		}
	}
	g.recalculateNextExpiryLocked()
}

func (g *openAITTFTGuard) clearInheritedConfigMismatch(global OpenAITTFTGuardConfigSnapshot) {
	if g == nil {
		return
	}
	global = normalizeOpenAITTFTGuardConfig(global)
	g.mu.Lock()
	defer g.mu.Unlock()
	groups := make(map[int64]struct{})
	for groupID, cfg := range g.groupConfigs {
		if cfg.Source == "global" && (cfg.Enabled != global.Enabled || cfg.Threshold != global.Threshold || cfg.MinSamples != global.MinSamples) {
			groups[groupID] = struct{}{}
		}
	}
	for groupID := range groups {
		g.clearGroupLocked(groupID)
	}
}

func (g *openAITTFTGuard) recalculateNextExpiryLocked() {
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

func (g *openAITTFTGuard) evictLRULocked() {
	if len(g.entries) < g.maxEntries {
		return
	}
	var oldestKey openAITTFTGuardKey
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

type openAITTFTGuardExcludedIDsContextKey struct{}

func withOpenAITTFTGuardExcludedIDs(ctx context.Context, ids map[int64]struct{}) context.Context {
	if ctx == nil || len(ids) == 0 {
		return ctx
	}
	cloned := make(map[int64]struct{}, len(ids))
	for id := range ids {
		cloned[id] = struct{}{}
	}
	return context.WithValue(ctx, openAITTFTGuardExcludedIDsContextKey{}, cloned)
}

func openAITTFTGuardExcludedAccount(ctx context.Context, accountID int64) bool {
	if ctx == nil || accountID <= 0 {
		return false
	}
	ids, _ := ctx.Value(openAITTFTGuardExcludedIDsContextKey{}).(map[int64]struct{})
	_, excluded := ids[accountID]
	return excluded
}

func (s *OpenAIGatewayService) SetOpenAITTFTGuardConfigProvider(provider OpenAITTFTGuardConfigProvider) {
	if s != nil {
		s.openaiTTFTGuardConfigProvider = provider
	}
}

func (s *OpenAIGatewayService) SetGroupTTFTGuardPolicyResolver(resolver GroupTTFTGuardPolicyResolver) {
	if s != nil {
		s.groupTTFTGuardPolicyResolver = resolver
	}
}

func (s *OpenAIGatewayService) InvalidateGroupTTFTGuardRuntime(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}
	if guard := s.getOpenAITTFTGuard(); guard != nil {
		guard.clearGroup(groupID)
	}
}

func (s *OpenAIGatewayService) InvalidateInheritedOpenAITTFTGuardRuntime(global OpenAITTFTGuardConfigSnapshot) {
	if s == nil {
		return
	}
	if guard := s.getOpenAITTFTGuard(); guard != nil {
		guard.clearInheritedConfigMismatch(global)
	}
}

// SetOpenAITTFTGuardUpstreamOnly is primarily useful for compatibility test
// doubles. Production constructors enable the upstream-only boundary by
// default; legacy unit fixtures can explicitly retain the old behavior.
func (s *OpenAIGatewayService) SetOpenAITTFTGuardUpstreamOnly(enabled bool) {
	if s != nil {
		s.openaiTTFTGuardUpstreamOnly = enabled
	}
}

func (s *OpenAIGatewayService) ttftGuardEligibleAccount(account *Account) bool {
	if account == nil {
		return false
	}
	eligible := account.IsUpstreamBound() || !s.openaiTTFTGuardUpstreamOnly
	if eligible && s.openaiTTFTGuardUpstreamOnly && account.ID > 0 {
		s.openaiTTFTGuardEligibleAccounts.Store(account.ID, struct{}{})
	}
	return eligible
}

func (s *OpenAIGatewayService) isOpenAITTFTGuardEligibleAccount(accountID int64) bool {
	if s == nil || accountID <= 0 {
		return false
	}
	if !s.openaiTTFTGuardUpstreamOnly {
		return true
	}
	_, ok := s.openaiTTFTGuardEligibleAccounts.Load(accountID)
	return ok
}

func (s *OpenAIGatewayService) openAITTFTGuardConfig() OpenAITTFTGuardConfigSnapshot {
	cfg, _ := s.openAITTFTGuardConfigWithValidity()
	return cfg
}

func (s *OpenAIGatewayService) openAITTFTGuardConfigWithValidity() (OpenAITTFTGuardConfigSnapshot, bool) {
	if s == nil {
		return normalizeOpenAITTFTGuardConfig(OpenAITTFTGuardConfigSnapshot{Source: "global"}), false
	}
	provider := s.openaiTTFTGuardConfigProvider
	if provider == nil && s.settingService != nil {
		provider, _ = any(s.settingService).(OpenAITTFTGuardConfigProvider)
	}
	if provider == nil {
		return normalizeOpenAITTFTGuardConfig(OpenAITTFTGuardConfigSnapshot{Source: "global"}), true
	}
	cfg := provider.OpenAITTFTGuardConfigSnapshot()
	if cfg.Source == "" {
		cfg.Source = "global"
	}
	valid := validOpenAITTFTGuardConfig(cfg)
	return normalizeOpenAITTFTGuardConfig(cfg), valid
}

func (s *OpenAIGatewayService) resolveOpenAITTFTGuardConfig(ctx context.Context, groupID int64) (OpenAITTFTGuardConfigSnapshot, bool) {
	if s == nil || groupID <= 0 {
		return OpenAITTFTGuardConfigSnapshot{Source: "disabled"}, false
	}
	if s.groupTTFTGuardPolicyResolver == nil {
		return s.openAITTFTGuardConfigWithValidity()
	}
	policy, err := s.groupTTFTGuardPolicyResolver.Resolve(ctx, groupID)
	if err != nil {
		return OpenAITTFTGuardConfigSnapshot{Source: "unavailable"}, false
	}
	cfg := OpenAITTFTGuardConfigSnapshot{
		Enabled:    policy.Enabled,
		Threshold:  policy.Threshold,
		MinSamples: policy.MinSamples,
		Source:     policy.Source,
		GroupName:  policy.GroupName,
	}
	if !validOpenAITTFTGuardConfig(cfg) {
		return normalizeOpenAITTFTGuardConfig(cfg), false
	}
	return cfg, true
}

func (s *OpenAIGatewayService) getOpenAITTFTGuard() *openAITTFTGuard {
	if s == nil {
		return nil
	}
	s.openaiTTFTGuardOnce.Do(func() {
		if s.openaiTTFTGuard == nil {
			s.openaiTTFTGuard = newOpenAITTFTGuard()
		}
	})
	return s.openaiTTFTGuard
}

func (s *OpenAIGatewayService) reportOpenAITTFTGuard(groupID, accountID int64, model string, success bool, firstTokenMs *int) {
	if s == nil || groupID <= 0 {
		return
	}
	// TTFT Guard is an upstream-account protection layer. Ordinary OAuth/API
	// accounts continue to be handled solely by the built-in scheduler. The
	// eligibility cache is populated from the same candidate snapshot used by
	// selection, avoiding an account-table read on every completed request.
	if s.openaiTTFTGuardUpstreamOnly {
		if _, eligible := s.openaiTTFTGuardEligibleAccounts.Load(accountID); !eligible {
			return
		}
	}
	cfg, resolved := s.resolveOpenAITTFTGuardConfig(context.Background(), groupID)
	if !resolved {
		return
	}
	if !cfg.Enabled {
		s.getOpenAITTFTGuard().clearGroup(groupID)
		return
	}
	s.forkTTFTRuntime().Report(
		forkscheduling.TTFTSample{GroupID: groupID, AccountID: accountID, Model: model, Success: success, FirstTokenMs: firstTokenMs},
		forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName},
	)
}

// OpenAITTFTGuardDegradations implements OpenAITTFTGuardDegradationReader.
// The returned map and slices are owned by the caller and may be modified.
func (s *OpenAIGatewayService) OpenAITTFTGuardDegradations(accountIDs []int64) map[int64][]OpenAITTFTGuardDegradation {
	if s == nil {
		return nil
	}
	if global, valid := s.openAITTFTGuardConfigWithValidity(); valid {
		s.getOpenAITTFTGuard().clearInheritedConfigMismatch(global)
	}
	degradations := s.forkTTFTRuntime().Degradations(accountIDs)
	if len(degradations) == 0 {
		return nil
	}
	result := make(map[int64][]OpenAITTFTGuardDegradation, len(degradations))
	for accountID, items := range degradations {
		converted := make([]OpenAITTFTGuardDegradation, 0, len(items))
		for _, item := range items {
			converted = append(converted, OpenAITTFTGuardDegradation{
				GroupID:                 item.GroupID,
				GroupName:               item.GroupName,
				PolicySource:            item.PolicySource,
				Model:                   item.Model,
				Reason:                  item.Reason,
				ThresholdMs:             item.ThresholdMs,
				LastTTFTMs:              item.LastTTFTMs,
				EWMAms:                  item.EWMAms,
				SampleCount:             item.SampleCount,
				DegradedAt:              item.DegradedAt,
				LastSampleAt:            item.LastSampleAt,
				ExpiresAt:               item.ExpiresAt,
				RecoverySamples:         item.RecoverySamples,
				RecoverySamplesRequired: item.RecoverySamplesRequired,
			})
		}
		result[accountID] = converted
	}
	return result
}

func (s *OpenAIGatewayService) selectAccountWithScheduler(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	platform string,
	previousResponseCanMove bool,
	useUpstreamTokenCost bool,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	guardExcludedIDs := s.openAITTFTGuardExclusions(ctx, groupID, platform, requestedModel, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, excludedIDs)
	if len(guardExcludedIDs) == 0 {
		return s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, excludedIDs, false, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	}

	guardCtx := withOpenAITTFTGuardExcludedIDs(ctx, guardExcludedIDs)
	effectiveExcludedIDs := mergeOpenAIExcludedAccountIDs(excludedIDs, guardExcludedIDs)
	// A valid hard previous_response binding must remain account-affine. The
	// original scheduler resolves it with caller exclusions only; if it does not
	// resolve, ordinary fallback scheduling uses the Guard exclusions.
	selection, decision, err := s.selectAccountWithSchedulerOnce(guardCtx, groupID, previousResponseID, sessionHash, requestedModel, effectiveExcludedIDs, excludedIDs, true, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
	if !openAITTFTGuardShouldFailOpen(selection, err) {
		return selection, decision, err
	}
	return s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, excludedIDs, false, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
}

type imageCostRoutingContextKey struct{}
type imageCostRoutingContext struct {
	sizeTier   string
	mode       string
	tolerance  float64
	staleAfter int
}

func withImageCostRouting(ctx context.Context, sizeTier, mode string, tolerance float64, staleAfter int) context.Context {
	return context.WithValue(ctx, imageCostRoutingContextKey{}, imageCostRoutingContext{sizeTier: sizeTier, mode: mode, tolerance: tolerance, staleAfter: staleAfter})
}

func imageCostRoutingFromContext(ctx context.Context) (string, string, float64, int) {
	if ctx == nil {
		return "", "", 0, 0
	}
	value, _ := ctx.Value(imageCostRoutingContextKey{}).(imageCostRoutingContext)
	return value.sizeTier, value.mode, value.tolerance, value.staleAfter
}

func (s *OpenAIGatewayService) selectAccountWithSchedulerWithImageCost(
	ctx context.Context,
	groupID *int64,
	previousResponseID, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability,
	requireCompact bool, platform string, previousResponseCanMove, useUpstreamTokenCost bool,
	imageSizeTier, imageCostMode string, imageCostTolerance float64, imageCostStaleAfter int,
) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	return s.selectAccountWithScheduler(withImageCostRouting(ctx, imageSizeTier, imageCostMode, imageCostTolerance, imageCostStaleAfter), groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost)
}

func (s *OpenAIGatewayService) openAITTFTGuardExclusions(
	ctx context.Context,
	groupID *int64,
	platform string,
	requestedModel string,
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	callerExcluded map[int64]struct{},
) map[int64]struct{} {
	if s == nil {
		return nil
	}
	hasGroupTTFTContext := groupID != nil && *groupID > 0 && NormalizeOpenAICompatiblePlatform(platform) == PlatformOpenAI
	cfg := OpenAITTFTGuardConfigSnapshot{Source: "disabled"}
	policyResolved := false
	if hasGroupTTFTContext {
		cfg, policyResolved = s.resolveOpenAITTFTGuardConfig(ctx, *groupID)
	}
	healthReader := s.forkHealthReader()
	if !cfg.Enabled {
		if policyResolved && groupID != nil && *groupID > 0 {
			s.getOpenAITTFTGuard().clearGroup(*groupID)
		}
		if !healthReader.HasTemporaryExclusions() {
			return nil
		}
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil || len(accounts) == 0 {
		return nil
	}
	candidates := make([]openAITTFTGuardCandidate, 0, len(accounts))
	healthExcluded := make(map[int64]struct{})
	for i := range accounts {
		account := &accounts[i]
		if !s.ttftGuardEligibleAccount(account) {
			continue
		}
		if _, excluded := callerExcluded[account.ID]; excluded {
			continue
		}
		if account.UpstreamKeyID != nil {
			health := healthReader.Snapshot(*account.UpstreamKeyID)
			if health.Status == forkscheduling.HealthStatus(UpstreamHealthSuspended) || health.Status == forkscheduling.HealthStatus(UpstreamHealthRecovering) {
				healthExcluded[account.ID] = struct{}{}
				continue
			}
		}
		if !account.IsSchedulable() || account.Platform != NormalizeOpenAICompatiblePlatform(platform) || !account.IsOpenAICompatible() ||
			!account.IsModelSupported(requestedModel) || !accountSupportsOpenAICapabilities(account, requiredCapability, requiredImageCapability) ||
			!s.isOpenAIAccountTransportCompatible(account, requiredTransport) || (requireCompact && openAICompactSupportTier(account) == 0) {
			continue
		}
		if strings.TrimSpace(requestedModel) == "" {
			continue
		}
		if hasGroupTTFTContext && cfg.Enabled {
			candidates = append(candidates, openAITTFTGuardCandidate{
				groupID:   *groupID,
				accountID: account.ID,
				model:     canonicalOpenAIAccountSchedulingModel(account, requestedModel),
			})
		}
	}
	contractCandidates := make([]forkscheduling.CandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		contractCandidates = append(contractCandidates, forkscheduling.CandidateView{ID: candidate.accountID, GroupID: candidate.groupID, Model: candidate.model})
	}
	var ttftExcluded map[int64]struct{}
	if len(contractCandidates) > 0 {
		ttftExcluded = s.forkTTFTRuntime().Exclusions(contractCandidates, callerExcluded, forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName})
	}
	return mergeOpenAIExcludedAccountIDs(healthExcluded, ttftExcluded)
}

func mergeOpenAIExcludedAccountIDs(base, additions map[int64]struct{}) map[int64]struct{} {
	if len(additions) == 0 {
		return base
	}
	merged := cloneExcludedAccountIDs(base)
	if merged == nil {
		merged = make(map[int64]struct{}, len(additions))
	}
	for id := range additions {
		merged[id] = struct{}{}
	}
	return merged
}

func openAITTFTGuardShouldFailOpen(selection *AccountSelectionResult, err error) bool {
	if err == nil {
		return selection == nil || selection.Account == nil
	}
	return errors.Is(err, ErrNoAvailableAccounts) || errors.Is(err, ErrNoAvailableCompactAccounts)
}
