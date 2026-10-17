package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

type cacheGuardKeepKey struct{}

func cacheGuardKeepSession(ctx context.Context) bool {
	v, _ := ctx.Value(cacheGuardKeepKey{}).(bool)
	return v
}

func (s *OpenAIGatewayService) selectWithCacheGuardPreference(ctx context.Context, groupID *int64, previousResponseID, sessionHash, model string, callerExcluded map[int64]struct{}, transport OpenAIUpstreamTransport, capability OpenAIEndpointCapability, imageCapability OpenAIImagesCapability, compact bool, platform string, previousCanMove, useTokenCost bool) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error, bool) {
	neutral := OpenAIAccountScheduleDecision{}
	if s == nil || compact || imageCapability != "" || protectionOrdinaryFallback(ctx) {
		return nil, neutral, nil, false
	}
	if groupID == nil || *groupID <= 0 {
		return nil, neutral, nil, false
	}
	if !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, *groupID) {
		return nil, neutral, nil, false
	}
	ctx = withProtectionCandidateReads(ctx)
	ctx = withProtectionPolicyEvaluation(ctx, s.groupProtectionPolicyResolver)
	s.syncTTFTProtectionPolicies()
	cfg, resolved := s.resolveOpenAITTFTGuardConfig(ctx, *groupID)
	priority := SchedulingPriorityCacheFirst
	if policy, ok := protectionPolicyForEvaluation(ctx, *groupID); ok {
		priority = policy.Priority
	} else if s.groupProtectionPolicyResolver != nil {
		if policy, err := s.groupProtectionPolicyResolver.ResolveProtection(ctx, *groupID); err == nil {
			priority = policy.Priority
		}
	}
	r := currentUpstreamCacheGuardRunner()
	cacheActive := r != nil && !r.closed.Load() && r.engine.CachedMetadataAt(*groupID, protectionEvaluationTime(ctx, time.Now)).Available && r.engine.ResolvedSettings(*groupID).Mode == "protect"
	if !cacheActive && (!resolved || !cfg.Enabled) {
		return nil, neutral, nil, false
	}
	if preserveOpenAIGuardianParentBinding(ctx, sessionHash) || s.resolveOpenAIGuardianParentAccountID(ctx, groupID) > 0 {
		return nil, neutral, nil, false
	}
	// Account-affine error retries and response owners keep the official path.
	hardResponseBound := strings.TrimSpace(previousResponseID) != "" && !previousCanMove && s.ResolveAccountIDByPreviousResponseIDForScheduler(ctx, groupID, previousResponseID, model, callerExcluded, capability, compact) > 0
	if openAIFixedRetryAccountID(ctx) > 0 || hardResponseBound {
		result, decision, err := s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, callerExcluded, callerExcluded, false, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
		s.attachProtectionSelection(result, *groupID, model, cfg)
		if result != nil && result.protection != nil {
			result.protection.selectionPath = "session_binding"
		}
		return result, decision, err, true
	}
	health, _ := s.openAIGuardExclusionParts(ctx, groupID, platform, model, transport, capability, imageCapability, compact, callerExcluded)
	base := mergeOpenAIExcludedAccountIDs(callerExcluded, health)
	accounts, err := s.listSchedulableAccounts(ctx, groupID, NormalizeOpenAICompatiblePlatform(platform))
	if err != nil {
		return nil, neutral, nil, false
	}
	ctx = s.withAccountRPMPrefetch(ctx, accounts)
	ctx = s.prefetchOpenAISchedulingAdmission(ctx, accounts, model, compact)
	eligible := make([]Account, 0, len(accounts))
	selectedAccountAvailable := false
	for _, a := range accounts {
		if _, excluded := base[a.ID]; excluded {
			continue
		}
		if a.IsSchedulable() && a.IsModelSupported(model) && accountSupportsOpenAICapabilities(&a, capability, imageCapability) && s.isOpenAIAccountTransportCompatible(&a, transport) {
			eligible = append(eligible, a)
			selectedAccountAvailable = selectedAccountAvailable || schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, &a)
		}
	}
	if !selectedAccountAvailable {
		return nil, neutral, nil, false
	}
	var qualities map[int64]forkscheduling.TTFTQuality
	if reader, ok := s.forkTTFTRuntime().(forkscheduling.TTFTQualityReader); ok {
		candidates := make([]forkscheduling.CandidateView, 0, len(eligible))
		for i := range eligible {
			a := &eligible[i]
			if schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, a) && s.ttftGuardEligibleAccount(a) {
				candidates = append(candidates, forkscheduling.CandidateView{ID: a.ID, UpstreamKeyID: accountUpstreamKeyID(a), GroupID: *groupID, Model: canonicalOpenAIAccountSchedulingModel(a, model)})
			}
		}
		qualityConfig := forkscheduling.TTFTConfig{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName}
		if timed, ok := reader.(interface {
			QualityAt([]forkscheduling.CandidateView, forkscheduling.TTFTConfig, time.Time) map[int64]forkscheduling.TTFTQuality
		}); ok {
			qualities = timed.QualityAt(candidates, qualityConfig, protectionEvaluationTime(ctx, time.Now))
		} else {
			qualities = reader.Quality(candidates, qualityConfig)
		}
	}
	degraded := map[int64]struct{}{}
	for id, q := range qualities {
		if q.Degraded {
			degraded[id] = struct{}{}
		}
	}
	ctx = s.withCacheGuardOpenAIProtocolResolver(ctx)
	p := buildCacheGuardPreference(ctx, groupID, sessionHash, model, cacheGuardOpenAIProtocol(transport), eligible, degraded)
	if protectionOrdinaryFallback(ctx) {
		return nil, neutral, nil, false
	}
	if !p.Active {
		if !resolved || !cfg.Enabled {
			return nil, neutral, nil, false
		}
		p = cacheGuardPreference{Active: true, ScopeIDs: map[int64]struct{}{}}
		for i := range eligible {
			a := &eligible[i]
			if schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, a) && s.ttftGuardEligibleAccount(a) {
				p.CandidateIDs = append(p.CandidateIDs, a.ID)
				p.ScopeIDs[a.ID] = struct{}{}
			}
		}
		p.Tiers = jointTTFTOnlyTiers(eligible, groupID)
		if sessionHash != "" {
			p.KeepAccountID, _ = s.getStickySessionAccountID(ctx, groupID, sessionHash)
		}
	}
	// Cache preferences alone may not have a durable session record yet. The
	// official binding is authoritative, including when cache is disabled.
	if p.KeepAccountID == 0 && sessionHash != "" {
		p.KeepAccountID, _ = s.getStickySessionAccountID(ctx, groupID, sessionHash)
	}
	p = s.jointSessionPreference(ctx, p, *groupID, model, cacheGuardOpenAIProtocol(transport), eligible, qualities, cfg, priority)
	if !p.Migration {
		p.Tiers = jointTTFTOrder(p.Tiers, eligible, groupID, qualities, cfg.MinSamples, priority)
	}
	var trial *openAITTFTTrial
	trialSelected := false
	if p.KeepAccountID == 0 && cfg.Enabled {
		var trialCandidates []Account
		for i := range eligible {
			a := &eligible[i]
			if _, scoped := p.ScopeIDs[a.ID]; scoped && schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, a) && s.ttftGuardEligibleAccount(a) {
				trialCandidates = append(trialCandidates, *a)
			}
		}
		trial = s.getOpenAITTFTGuard().reserveTrialOpportunity(ctx, *groupID, trialCandidates, model, cfg, qualities)
		if trial != nil {
			// A trial is a bounded sampling exception within its own priority
			// pool. Cache cohorts still perform their ordinary send admission.
			p.Tiers = jointRecoveryTiers(p.Tiers, eligible, groupID, []int64{trial.key.accountID}, true)
		}
	}
	defer func() {
		if trial != nil && !trialSelected {
			trial.finish(false, nil)
		}
	}()
	attachAllocation := func(result *AccountSelectionResult) {
		var selectedTrial *openAITTFTTrial
		newAllocation := result != nil && result.Account != nil && result.Acquired && !p.Migration && p.KeepAccountID == 0 && schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, result.Account) && s.ttftGuardEligibleAccount(result.Account)
		if newAllocation {
			if trial != nil && result.Account.ID == trial.key.accountID && accountUpstreamKeyID(result.Account) == trial.key.upstreamKeyID && canonicalOpenAIAccountSchedulingModel(result.Account, model) == trial.key.model {
				selectedTrial, trialSelected = trial, true
				release := result.ReleaseFunc
				result.ReleaseFunc = func() {
					trial.finish(false, nil)
					if release != nil {
						release()
					}
				}
			}
		}
		s.attachProtectionSelection(result, *groupID, model, cfg, selectedTrial)
		if newAllocation && result != nil && result.protection != nil {
			result.protection.newAllocation = true
			result.protection.allocationAccountID = result.Account.ID
			result.protection.allocationUpstreamKeyID = accountUpstreamKeyID(result.Account)
			result.protection.allocationModel = normalizeOpenAIAccountModelTransientModel(canonicalOpenAIAccountSchedulingModel(result.Account, model))
			result.protection.allocationConfig = cfg
		}
		if result != nil && result.protection != nil {
			result.protection.selectionPath = "normal"
			if selectedTrial != nil {
				result.protection.selectionPath = "trial"
			}
		}
	}
	// cache_first uses latency only to refine a cache band. ttft_first has
	// already compared latency across cache bands within each quality state.
	if priority == SchedulingPriorityCacheFirst {
		p.Tiers = cacheGuardLatencyTiers(p.Tiers, func(id int64) (float64, bool) {
			if quality, known := qualities[id]; known && quality.SampleCount > 0 {
				return quality.EWMAms, true
			}
			return 0, false
		})
	}
	ctx = withProtectionCandidateScope(ctx, p.candidateScope())
	tierExclusions := p.tierExclusions(base)
	selectTier := func(tier []int64, keep bool) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		if !protectionPolicyEvaluationCurrent(ctx) {
			return nil, neutral, nil
		}
		trialCtx := withCacheGuardTentativeSelection(withProtectionCandidateTier(ctx, p.ScopeIDs, tier))
		if keep {
			trialCtx = context.WithValue(trialCtx, cacheGuardKeepKey{}, true)
		}
		result, decision, err := s.selectAccountWithSchedulerOnce(trialCtx, groupID, previousResponseID, sessionHash, model, tierExclusions(tier), callerExcluded, true, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
		if !protectionPolicyEvaluationCurrent(ctx) {
			if result != nil && result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			return nil, neutral, nil
		}
		if p.Migration && err == nil && result != nil && result.Account != nil {
			allowed := false
			for _, id := range tier {
				allowed = allowed || result.Account.ID == id
			}
			identityMatches := false
			for i := range eligible {
				observed := &eligible[i]
				if observed.ID == result.Account.ID {
					identityMatches = schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, result.Account) && accountUpstreamKeyID(observed) == accountUpstreamKeyID(result.Account) && canonicalOpenAIAccountSchedulingModel(observed, model) == canonicalOpenAIAccountSchedulingModel(result.Account, model)
					break
				}
			}
			// The original scheduler may see a newer candidate snapshot. A newly
			// added account or changed Key/model cannot borrow the previous
			// identity's observations to justify a session migration.
			if !allowed || !identityMatches {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				return nil, neutral, nil
			}
		}
		return result, decision, err
	}
	selectOld := func() (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
		if !protectionPolicyEvaluationCurrent(ctx) {
			return nil, neutral, nil
		}
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
		if !protectionPolicyEvaluationCurrent(ctx) {
			if result != nil && result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			return nil, neutral, nil
		}
		decision := OpenAIAccountScheduleDecision{Layer: openAIAccountScheduleLayerSessionSticky, StickySessionHit: result != nil && result.Account != nil}
		applyLegacySelectionDecision(&decision, result)
		if result != nil {
			result.cacheGuardProtectedSelection = true
		}
		return result, decision, err
	}
	if p.KeepAccountID > 0 && !p.Migration {
		result, decision, err := selectOld()
		if !openAITTFTGuardShouldFailOpen(result, err) {
			s.attachProtectionSelection(result, *groupID, model, cfg)
			if result != nil && result.protection != nil {
				result.protection.selectionPath = "session_binding"
			}
			return result, decision, err, true
		}
	}
	for _, tier := range p.Tiers {
		result, decision, err := selectTier(tier, false)
		if err != nil && !openAITTFTGuardShouldFailOpen(result, err) {
			return result, decision, err, true
		}
		if err == nil && result != nil && result.Account != nil && result.Acquired {
			result.cacheGuardProtectedSelection = true
			// The tentative passes never write or clear a sticky binding. Commit
			// after obtaining real capacity; profit-gated binding stays terminal.
			if p.Migration && result.Account.ID != p.KeepAccountID {
				result.cacheGuardMigrationAccountID = result.Account.ID
			} else if sessionHash != "" && p.KeepAccountID == 0 {
				result.cacheGuardDeferredBinding = true
			}
			attachAllocation(result)
			return result, decision, nil, true
		}
		if result != nil && result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	if p.Migration && p.KeepAccountID > 0 {
		result, decision, err := selectOld()
		if !openAITTFTGuardShouldFailOpen(result, err) {
			s.attachProtectionSelection(result, *groupID, model, cfg)
			if result != nil && result.protection != nil {
				result.protection.selectionPath = "session_binding"
			}
			return result, decision, err, true
		}
	}
	// Cache and TTFT are soft preferences. Health retains its existing fallback
	// policy separately; caller exclusions are never removed by these passes.
	if !protectionPolicyEvaluationCurrent(ctx) {
		return nil, neutral, nil, false
	}
	ctx = withoutProtectionCandidateReads(ctx)
	result, decision, err := s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, base, callerExcluded, true, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
	if len(health) > 0 && openAITTFTGuardShouldFailOpen(result, err) {
		result, decision, err = s.selectAccountWithSchedulerOnce(ctx, groupID, previousResponseID, sessionHash, model, callerExcluded, callerExcluded, false, transport, capability, imageCapability, compact, platform, previousCanMove, useTokenCost)
	}
	attachAllocation(result)
	if result != nil && result.protection != nil {
		result.protection.selectionPath = "capacity_fallback"
		result.protection.fallbackReason = "preferred_capacity_unavailable"
	}
	return result, decision, err, true
}
