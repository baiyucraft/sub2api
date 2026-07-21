package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
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
}

type OpenAITTFTGuardConfigProvider interface {
	OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot
}

type openAITTFTGuardConfigProviderFunc func() OpenAITTFTGuardConfigSnapshot

func (f openAITTFTGuardConfigProviderFunc) OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot {
	return f()
}

func normalizeOpenAITTFTGuardConfig(cfg OpenAITTFTGuardConfigSnapshot) OpenAITTFTGuardConfigSnapshot {
	defaults := OpenAITTFTGuardConfigSnapshot{
		Threshold:  defaultOpenAITTFTGuardThreshold,
		MinSamples: defaultOpenAITTFTGuardMinSamples,
	}
	if cfg.Threshold < 5*time.Second || cfg.Threshold > 300*time.Second || cfg.MinSamples < 2 || cfg.MinSamples > 20 {
		return defaults
	}
	return cfg
}

type openAITTFTGuardKey struct {
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
	lastSampleAt         time.Time
	lastTouchedAt        time.Time
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
	e.lastSampleAt = now
	e.lastTouchedAt = now
}

type openAITTFTGuardCandidate struct {
	accountID int64
	model     string
}

type openAITTFTGuard struct {
	mu            sync.Mutex
	entries       map[openAITTFTGuardKey]*openAITTFTGuardEntry
	maxEntries    int
	ttl           time.Duration
	now           func() time.Time
	probeSequence uint64
	probeCursor   uint64
	nextExpiryAt  time.Time
	config        OpenAITTFTGuardConfigSnapshot
	configSet     bool
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
		entries:    make(map[openAITTFTGuardKey]*openAITTFTGuardEntry),
		maxEntries: maxEntries,
		ttl:        ttl,
		now:        now,
	}
}

func openAITTFTGuardKeyFor(accountID int64, model string) (openAITTFTGuardKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if accountID <= 0 || model == "" {
		return openAITTFTGuardKey{}, false
	}
	return openAITTFTGuardKey{accountID: accountID, model: model}, true
}

func (g *openAITTFTGuard) report(accountID int64, model string, success bool, firstTokenMs *int, cfg OpenAITTFTGuardConfigSnapshot) {
	if g == nil {
		return
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.syncConfigLocked(cfg)
	if !cfg.Enabled || firstTokenMs == nil || *firstTokenMs <= 0 {
		return
	}
	key, ok := openAITTFTGuardKeyFor(accountID, model)
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
		entry = &openAITTFTGuardEntry{}
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
	case entry.consecutiveElevated >= 2:
		entry.degraded = true
		entry.degradedReason = "consecutive_elevated"
	case entry.sampleCount >= uint64(cfg.MinSamples) && entry.recentEWMA() >= threshold:
		entry.degraded = true
		entry.degradedReason = "ewma"
	}
}

func (g *openAITTFTGuard) exclusions(candidates []openAITTFTGuardCandidate, callerExcluded map[int64]struct{}, cfg OpenAITTFTGuardConfigSnapshot) map[int64]struct{} {
	if g == nil {
		return nil
	}
	cfg = normalizeOpenAITTFTGuardConfig(cfg)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.syncConfigLocked(cfg)
	if !cfg.Enabled || len(candidates) == 0 {
		return nil
	}
	g.deleteExpiredLocked(now)

	degraded := make([]int64, 0, len(candidates))
	seen := make(map[int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, excluded := callerExcluded[candidate.accountID]; excluded {
			continue
		}
		key, ok := openAITTFTGuardKeyFor(candidate.accountID, candidate.model)
		if !ok {
			continue
		}
		entry := g.entries[key]
		if entry == nil || !entry.degraded {
			continue
		}
		entry.lastTouchedAt = now
		if _, duplicate := seen[candidate.accountID]; duplicate {
			continue
		}
		seen[candidate.accountID] = struct{}{}
		degraded = append(degraded, candidate.accountID)
	}
	if len(degraded) == 0 {
		return nil
	}
	sort.Slice(degraded, func(i, j int) bool { return degraded[i] < degraded[j] })

	g.probeSequence++
	probeAccountID := int64(0)
	if g.probeSequence%openAITTFTGuardProbeEvery == 0 {
		probeAccountID = degraded[g.probeCursor%uint64(len(degraded))]
		g.probeCursor++
	}
	excluded := make(map[int64]struct{}, len(degraded))
	for _, accountID := range degraded {
		if accountID != probeAccountID {
			excluded[accountID] = struct{}{}
		}
	}
	return excluded
}

func (g *openAITTFTGuard) isDegraded(accountID int64, model string) bool {
	if g == nil {
		return false
	}
	key, ok := openAITTFTGuardKeyFor(accountID, model)
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

func (g *openAITTFTGuard) syncConfigLocked(cfg OpenAITTFTGuardConfigSnapshot) {
	if g.configSet && g.config == cfg {
		return
	}
	g.entries = make(map[openAITTFTGuardKey]*openAITTFTGuardEntry)
	g.probeSequence = 0
	g.probeCursor = 0
	g.nextExpiryAt = time.Time{}
	g.config = cfg
	g.configSet = true
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

func (s *OpenAIGatewayService) openAITTFTGuardConfig() OpenAITTFTGuardConfigSnapshot {
	if s == nil {
		return normalizeOpenAITTFTGuardConfig(OpenAITTFTGuardConfigSnapshot{})
	}
	provider := s.openaiTTFTGuardConfigProvider
	if provider == nil && s.settingService != nil {
		provider, _ = any(s.settingService).(OpenAITTFTGuardConfigProvider)
	}
	if provider == nil {
		return normalizeOpenAITTFTGuardConfig(OpenAITTFTGuardConfigSnapshot{})
	}
	return normalizeOpenAITTFTGuardConfig(provider.OpenAITTFTGuardConfigSnapshot())
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

func (s *OpenAIGatewayService) reportOpenAITTFTGuard(accountID int64, model string, success bool, firstTokenMs *int) {
	if s == nil {
		return
	}
	cfg := s.openAITTFTGuardConfig()
	s.getOpenAITTFTGuard().report(accountID, model, success, firstTokenMs, cfg)
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
	if s == nil || strings.TrimSpace(requestedModel) == "" {
		return nil
	}
	cfg := s.openAITTFTGuardConfig()
	guard := s.getOpenAITTFTGuard()
	if !cfg.Enabled {
		guard.exclusions(nil, callerExcluded, cfg)
		return nil
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID, normalizeOpenAICompatiblePlatform(platform))
	if err != nil || len(accounts) == 0 {
		return nil
	}
	candidates := make([]openAITTFTGuardCandidate, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if _, excluded := callerExcluded[account.ID]; excluded {
			continue
		}
		if !account.IsSchedulable() || account.Platform != normalizeOpenAICompatiblePlatform(platform) || !account.IsOpenAICompatible() ||
			!account.IsModelSupported(requestedModel) || !accountSupportsOpenAICapabilities(account, requiredCapability, requiredImageCapability) ||
			!s.isOpenAIAccountTransportCompatible(account, requiredTransport) || (requireCompact && openAICompactSupportTier(account) == 0) {
			continue
		}
		candidates = append(candidates, openAITTFTGuardCandidate{
			accountID: account.ID,
			model:     canonicalOpenAIAccountSchedulingModel(account, requestedModel),
		})
	}
	return guard.exclusions(candidates, callerExcluded, cfg)
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
