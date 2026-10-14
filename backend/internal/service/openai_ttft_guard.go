package service

import (
	"context"
	"errors"
	"math"
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
	UpstreamKeyID           int64     `json:"-"`
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
	if cfg.Source == "" {
		cfg.Source = GroupTTFTGuardSourceGlobal
	}
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
	scopeID       int64 // Effective-policy signature; zero is the shared observation identity.
	upstreamKeyID int64
	accountID     int64
	model         string
}

type openAITTFTGuardEntry struct {
	lastGroupID           int64
	lastGroupName         string
	samples               [openAITTFTGuardSampleWindow]float64
	successes             [openAITTFTGuardSampleWindow]bool
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
	e.recoveryEpoch++
	e.completedTrialCycles = [64]uint64{}
	e.completedTrialCount = 0
	e.highestCompletedCycle = 0
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
	upstreamKeyID int64
	groupID       int64
	accountID     int64
	model         string
}

type openAITTFTGuard struct {
	mu                  sync.Mutex
	entries             map[openAITTFTGuardKey]*openAITTFTGuardEntry
	observations        map[openAITTFTGuardKey]*openAITTFTGuardEntry
	accountKeys         map[int64]int64
	maxEntries          int
	ttl                 time.Duration
	now                 func() time.Time
	groupProbes         map[openAITTFTGuardProbeKey]*openAITTFTGuardProbeState
	nextExpiryAt        time.Time
	groupConfigs        map[int64]OpenAITTFTGuardConfigSnapshot
	nextEntryGeneration uint64
}

type openAITTFTGuardProbeKey struct {
	scopeID int64
	model   string
}

type openAITTFTGuardProbeState struct {
	sequence  uint64
	cursor    uint64
	nominated int64
	cycle     uint64
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
		observations: make(map[openAITTFTGuardKey]*openAITTFTGuardEntry),
		accountKeys:  make(map[int64]int64),
		groupProbes:  make(map[openAITTFTGuardProbeKey]*openAITTFTGuardProbeState),
		groupConfigs: make(map[int64]OpenAITTFTGuardConfigSnapshot),
		maxEntries:   maxEntries,
		ttl:          ttl,
		now:          now,
	}
}

func openAITTFTGuardKeyFor(scopeID, accountID int64, model string) (openAITTFTGuardKey, bool) {
	model = normalizeOpenAIAccountModelTransientModel(model)
	if scopeID < 0 || accountID <= 0 || model == "" {
		return openAITTFTGuardKey{}, false
	}
	return openAITTFTGuardKey{scopeID: scopeID, accountID: accountID, model: model}, true
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

func sameOpenAITTFTGuardRuntimeConfig(left, right OpenAITTFTGuardConfigSnapshot) bool {
	left = normalizeOpenAITTFTGuardConfig(left)
	right = normalizeOpenAITTFTGuardConfig(right)
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

func (g *openAITTFTGuard) clearGlobal() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.clearScopeLocked(0)
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

func (s *OpenAIGatewayService) SetGroupSchedulingProtectionPolicyResolver(resolver GroupSchedulingProtectionPolicyResolver) {
	if s != nil {
		s.groupProtectionPolicyResolver = resolver
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
	if s.groupProtectionPolicyResolver != nil {
		var policy GroupSchedulingProtectionResolvedPolicy
		if raw, ok := s.groupProtectionPolicyResolver.(interface {
			ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy
		}); ok {
			// Runtime state tracks the stored policy independently from the hot
			// platform gate. An effective disabled API view during a scope pause
			// must never prune historical observations or recovery cohorts.
			var known bool
			policy, known = raw.ProtectionPolicies()[groupID]
			if !known {
				return OpenAITTFTGuardConfigSnapshot{Source: "unavailable"}, false
			}
		} else {
			// Keep legacy resolvers and small test integrations compatible.
			var err error
			policy, err = s.groupProtectionPolicyResolver.ResolveProtection(ctx, groupID)
			if err != nil {
				return OpenAITTFTGuardConfigSnapshot{Source: "unavailable"}, false
			}
		}
		p := policy.TTFT
		cfg := OpenAITTFTGuardConfigSnapshot{Enabled: p.Enabled, Threshold: p.Threshold, MinSamples: p.MinSamples, Source: p.Source, GroupName: p.GroupName}
		return cfg, validOpenAITTFTGuardConfig(cfg)
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

func (s *OpenAIGatewayService) reportOpenAITTFTGuard(groupID, accountID int64, model string, success bool, firstTokenMs *int, upstreamKeyIDs ...int64) {
	if s == nil || groupID <= 0 {
		return
	}
	if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, groupID) || !schedulingProtectionPlatformSelected(s.groupProtectionPolicyResolver, PlatformOpenAI) {
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
	s.syncTTFTProtectionPolicies()
	if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, groupID) || !schedulingProtectionPlatformSelected(s.groupProtectionPolicyResolver, PlatformOpenAI) {
		return
	}
	keyID := int64(0)
	if len(upstreamKeyIDs) > 0 {
		keyID = upstreamKeyIDs[0]
	}
	s.forkTTFTRuntime().Report(
		forkscheduling.TTFTSample{GroupID: groupID, AccountID: accountID, UpstreamKeyID: keyID, Model: model, Success: success, FirstTokenMs: firstTokenMs},
		forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName},
	)
}

// OpenAITTFTGuardDegradations implements OpenAITTFTGuardDegradationReader.
// The returned map and slices are owned by the caller and may be modified.
func (s *OpenAIGatewayService) OpenAITTFTGuardDegradations(accountIDs []int64) map[int64][]OpenAITTFTGuardDegradation {
	if s == nil || !schedulingProtectionPlatformSelected(s.groupProtectionPolicyResolver, PlatformOpenAI) {
		return nil
	}
	s.syncTTFTProtectionPolicies()
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
			if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, item.GroupID) {
				continue
			}
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
		if len(converted) > 0 {
			result[accountID] = converted
		}
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
	if selection, decision, err, handled := s.selectWithCacheGuardPreference(ctx, groupID, previousResponseID, sessionHash, requestedModel, excludedIDs, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, platform, previousResponseCanMove, useUpstreamTokenCost); handled {
		return selection, decision, err
	}
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
	health, ttft := s.openAIGuardExclusionParts(ctx, groupID, platform, requestedModel, requiredTransport, requiredCapability, requiredImageCapability, requireCompact, callerExcluded)
	return mergeOpenAIExcludedAccountIDs(health, ttft)
}

// Keep health admission and TTFT preference separate so cache affinity is
// evaluated before a slow account can be removed by the latency guard.
func (s *OpenAIGatewayService) openAIGuardExclusionParts(ctx context.Context, groupID *int64, platform, requestedModel string, requiredTransport OpenAIUpstreamTransport, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requireCompact bool, callerExcluded map[int64]struct{}, tickets ...map[int64]*openAITTFTTrial) (map[int64]struct{}, map[int64]struct{}) {
	if s == nil {
		return nil, nil
	}
	hasGroupTTFTContext := groupID != nil && *groupID > 0 && NormalizeOpenAICompatiblePlatform(platform) == PlatformOpenAI && SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, *groupID)
	cfg := OpenAITTFTGuardConfigSnapshot{Source: "disabled"}
	policyResolved := false
	if hasGroupTTFTContext {
		cfg, policyResolved = s.resolveOpenAITTFTGuardConfig(ctx, *groupID)
		if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, *groupID) || !schedulingProtectionPlatformSelected(s.groupProtectionPolicyResolver, PlatformOpenAI) {
			cfg, policyResolved = OpenAITTFTGuardConfigSnapshot{Source: "disabled"}, false
		}
	}
	healthReader := s.forkHealthReader()
	if !cfg.Enabled {
		if policyResolved && groupID != nil && *groupID > 0 {
			s.getOpenAITTFTGuard().clearForConfig(*groupID, cfg)
		}
		if !healthReader.HasTemporaryExclusions() {
			return nil, nil
		}
	}
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil || len(accounts) == 0 {
		return nil, nil
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
		if hasGroupTTFTContext && cfg.Enabled && schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, account) {
			candidates = append(candidates, openAITTFTGuardCandidate{
				upstreamKeyID: accountUpstreamKeyID(account),
				groupID:       *groupID,
				accountID:     account.ID,
				model:         canonicalOpenAIAccountSchedulingModel(account, requestedModel),
			})
		}
	}
	contractCandidates := make([]forkscheduling.CandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		contractCandidates = append(contractCandidates, forkscheduling.CandidateView{ID: candidate.accountID, UpstreamKeyID: candidate.upstreamKeyID, GroupID: candidate.groupID, Model: candidate.model})
	}
	var ttftExcluded map[int64]struct{}
	if len(contractCandidates) > 0 {
		if runtime, ok := s.forkTTFTRuntime().(legacyTTFTRuntime); ok {
			ttftExcluded = runtime.guard.exclusions(candidates, callerExcluded, cfg, tickets...)
		} else {
			ttftExcluded = s.forkTTFTRuntime().Exclusions(contractCandidates, callerExcluded, forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName})
		}
	}
	return healthExcluded, ttftExcluded
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
