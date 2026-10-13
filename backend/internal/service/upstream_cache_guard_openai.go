package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

type cacheGuardKeepKey struct{}

func cacheGuardKeepSession(ctx context.Context) bool {
	v, _ := ctx.Value(cacheGuardKeepKey{}).(bool)
	return v
}

func (s *OpenAIGatewayService) selectWithCacheGuardPreference(ctx context.Context, groupID *int64, previousResponseID, sessionHash, model string, callerExcluded map[int64]struct{}, transport OpenAIUpstreamTransport, capability OpenAIEndpointCapability, imageCapability OpenAIImagesCapability, compact bool, platform string, previousCanMove, useTokenCost bool) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error, bool) {
	neutral := OpenAIAccountScheduleDecision{}
	if s == nil || compact || imageCapability != "" {
		return nil, neutral, nil, false
	}
	r := currentUpstreamCacheGuardRunner()
	if r == nil || !r.available.Load() || r.engine.Settings().Mode != "protect" {
		return nil, neutral, nil, false
	}
	if groupID == nil || !r.engine.Settings().ContainsGroup(*groupID) {
		return nil, neutral, nil, false
	}
	if _, ok := r.logicalRequest(ctx, groupID, sessionHash, model, cacheGuardOpenAIProtocol(transport)); !ok {
		return nil, neutral, nil, false
	}
	if preserveOpenAIGuardianParentBinding(ctx, sessionHash) || s.resolveOpenAIGuardianParentAccountID(ctx, groupID) > 0 {
		return nil, neutral, nil, false
	}
	// Account-affine error retries and response owners keep the official path.
	if openAIFixedRetryAccountID(ctx) > 0 || (strings.TrimSpace(previousResponseID) != "" && !previousCanMove) {
		result, decision, err := s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, callerExcluded, callerExcluded, false, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
		return result, decision, err, true
	}
	health, ttft := s.openAIGuardExclusionParts(ctx, groupID, platform, model, transport, capability, imageCapability, compact, callerExcluded)
	base := mergeOpenAIExcludedAccountIDs(callerExcluded, health)
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil {
		return nil, neutral, nil, false
	}
	eligible := make([]Account, 0, len(accounts))
	for _, a := range accounts {
		if _, excluded := base[a.ID]; excluded {
			continue
		}
		if a.IsSchedulable() && a.IsModelSupported(model) && accountSupportsOpenAICapabilities(&a, capability, imageCapability) && s.isOpenAIAccountTransportCompatible(&a, transport) {
			eligible = append(eligible, a)
		}
	}
	var qualities map[int64]forkscheduling.TTFTQuality
	if reader, ok := s.forkTTFTRuntime().(forkscheduling.TTFTQualityReader); ok {
		cfg, _ := s.resolveOpenAITTFTGuardConfig(ctx, *groupID)
		candidates := make([]forkscheduling.CandidateView, 0, len(eligible))
		for i := range eligible {
			a := &eligible[i]
			candidates = append(candidates, forkscheduling.CandidateView{ID: a.ID, GroupID: *groupID, Model: canonicalOpenAIAccountSchedulingModel(a, model)})
		}
		qualities = reader.Quality(candidates, forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName})
		ttft = map[int64]struct{}{}
		for id, q := range qualities {
			if q.Degraded {
				ttft[id] = struct{}{}
			}
		}
	}
	ctx = s.withCacheGuardOpenAIProtocolResolver(ctx)
	p := buildCacheGuardPreference(ctx, groupID, sessionHash, model, cacheGuardOpenAIProtocol(transport), eligible, ttft)
	if !p.Active {
		return nil, neutral, nil, false
	}
	p.Tiers = cacheGuardLatencyTiers(p.Tiers, func(id int64) (float64, bool) {
		if quality, known := qualities[id]; known && quality.SampleCount > 0 {
			return quality.EWMAms, true
		}
		_, latency, known := s.openaiAccountStats.snapshot(id)
		return latency, known
	})
	selectTier := func(tier []int64, keep bool) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		trialCtx := withCacheGuardTentativeSelection(ctx)
		if keep {
			trialCtx = context.WithValue(trialCtx, cacheGuardKeepKey{}, true)
		}
		result, decision, err := s.selectAccountWithSchedulerOnce(trialCtx, groupID, previousResponseID, sessionHash, model, p.exclusionsFor(tier, base), callerExcluded, true, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
		if p.Migration && err == nil && result != nil && result.Account != nil {
			allowed := false
			for _, id := range tier {
				allowed = allowed || result.Account.ID == id
			}
			// The original scheduler may see a newer candidate snapshot. A newly
			// added account still needs sufficient cache evidence for migration.
			if !allowed {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				return nil, neutral, nil
			}
		}
		return result, decision, err
	}
	selectOld := func() (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		if s.checkChannelPricingRestriction(ctx, groupID, model) {
			return nil, OpenAIAccountScheduleDecision{}, ErrNoAvailableAccounts
		}
		oldCtx := s.withOpenAIQuotaAutoPauseContext(withCacheGuardTentativeSelection(ctx))
		oldCtx = s.withOpenAISchedulingAdmission(oldCtx, model, false)
		oldCtx = s.withOpenAIGroupPrivacyRequirement(oldCtx, groupID)
		oldCtx = s.withOpenAIProfitControlGate(oldCtx, groupID)
		scheduler := &defaultOpenAIAccountScheduler{service: s, stats: s.openaiAccountStats}
		if scheduler.stats == nil {
			scheduler.stats = newOpenAIAccountRuntimeStats()
		}
		result, _, err := scheduler.selectBySessionHash(oldCtx, OpenAIAccountScheduleRequest{GroupID: groupID, Platform: platform, SessionHash: sessionHash, StickyAccountID: p.KeepAccountID, PreserveStickyBinding: true, DisableStickyEscape: true, RequestedModel: model, RequiredTransport: transport, RequiredCapability: capability, RequiredImageCapability: imageCapability, ExcludedIDs: base, ExcludedConcurrencyTargets: openAICapacityExcludedTargets(ctx), RequirePrivacySet: s.openAIGroupRequiresPrivacySet(oldCtx, groupID)})
		decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerSessionSticky, StickySessionHit: result != nil && result.Account != nil}
		applyLegacySelectionDecision(&decision, result)
		return result, decision, err
	}
	if p.KeepAccountID > 0 && !p.Migration {
		result, decision, err := selectOld()
		if !openAITTFTGuardShouldFailOpen(result, err) {
			return result, decision, err, true
		}
	}
	for _, tier := range p.Tiers {
		result, decision, err := selectTier(tier, false)
		if err != nil && !openAITTFTGuardShouldFailOpen(result, err) {
			return result, decision, err, true
		}
		if err == nil && result != nil && result.Account != nil && result.Acquired {
			// The tentative passes never write or clear a sticky binding. Commit
			// after obtaining real capacity; profit-gated binding stays terminal.
			if p.Migration && result.Account.ID != p.KeepAccountID {
				result.cacheGuardMigrationAccountID = result.Account.ID
			} else if sessionHash != "" {
				_ = s.bindOpenAIStickySessionDuringSelection(ContextWithSelectionProfitGate(ctx, result), groupID, sessionHash, result.Account.ID)
			}
			return result, decision, nil, true
		}
		if result != nil && result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	if p.Migration && p.KeepAccountID > 0 {
		result, decision, err := selectOld()
		if !openAITTFTGuardShouldFailOpen(result, err) {
			return result, decision, err, true
		}
	}
	// Cache and TTFT are soft preferences. Health retains its existing fallback
	// policy separately; caller exclusions are never removed by these passes.
	result, decision, err := s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, base, callerExcluded, true, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
	if len(health) > 0 && openAITTFTGuardShouldFailOpen(result, err) {
		result, decision, err = s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, callerExcluded, callerExcluded, false, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
	}
	return result, decision, err, true
}
