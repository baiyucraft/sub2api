package service

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

func (s *GatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	ctx = withProtectionCandidateReads(ctx)
	if !cacheGuardTentativeSelection(ctx) && !protectionOrdinaryFallback(ctx) {
		preference, guardCtx, guardGroupID, base := s.gatewayCacheGuardPreference(ctx, groupID, sessionHash, requestedModel, excludedIDs)
		// This entrypoint does not acquire capacity, so it cannot commit a
		// cache-driven migration. Load-aware selection owns that decision.
		if preference.Active {
			tiers := preference.Tiers
			if preference.KeepAccountID > 0 {
				preference = gatewayCacheGuardSourcePreference(preference)
				tiers = [][]int64{{preference.KeepAccountID}}
			}
			guardCtx = withProtectionCandidateScope(guardCtx, preference.candidateScope())
			exclusions := preference.tierExclusions(base)
			for _, tier := range tiers {
				if !protectionPolicyEvaluationCurrent(guardCtx) {
					break
				}
				account, err := s.selectAccountForModelWithExclusionsCore(withCacheGuardTentativeSelection(withProtectionCandidateTier(guardCtx, preference.ScopeIDs, tier)), guardGroupID, sessionHash, requestedModel, exclusions(tier))
				if !protectionPolicyEvaluationCurrent(guardCtx) {
					break
				}
				if err == nil && account != nil {
					if preference.KeepAccountID > 0 && account.ID != preference.KeepAccountID {
						continue
					}
					_ = s.bindGatewayStickySessionDuringSelection(guardCtx, guardGroupID, sessionHash, account.ID)
					return account, nil
				}
				if err != nil && !errors.Is(err, ErrNoAvailableAccounts) {
					return account, err
				}
			}
		}
	}
	return s.selectAccountWithUpstreamHealth(withoutProtectionCandidateReads(ctx), groupID, sessionHash, requestedModel, excludedIDs)
}

func (s *GatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, sub2apiUserID int64) (*AccountSelectionResult, error) {
	ctx = withProtectionCandidateReads(ctx)
	if !cacheGuardTentativeSelection(ctx) && !protectionOrdinaryFallback(ctx) {
		preference, guardCtx, guardGroupID, base := s.gatewayCacheGuardPreference(ctx, groupID, sessionHash, requestedModel, excludedIDs)
		if preference.Active {
			result, err, handled := selectGatewayCacheGuardTiers(guardCtx, preference, base, func(selectCtx context.Context, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
				return s.selectAccountWithLoadAwarenessCore(selectCtx, guardGroupID, sessionHash, requestedModel, excluded, metadataUserID, sub2apiUserID)
			})
			if handled && err == nil && result != nil && result.Account != nil {
				result.cacheGuardProtectedSelection = true
				// Tentative tiers never reserve session capacity. Only the chosen
				// result may register, synchronously, before committing its binding.
				if s.commitGatewayProtectionSession(guardCtx, result, sessionHash) != nil {
					handled = false
				}
			}
			if handled {
				if err == nil && result != nil && result.Account != nil && result.Acquired {
					// Profit-controlled requests still bind at terminal admission.
					if preference.Migration && result.Account.ID != preference.KeepAccountID {
						result.cacheGuardMigrationAccountID = result.Account.ID
					} else if preference.KeepAccountID == 0 && sessionHash != "" {
						result.cacheGuardDeferredBinding = true
					}
				}
				return result, err
			}
		}
	}
	// Soft cache tiers never create a new no-account error or replace an old
	// session with a wait-only migration target. Retain the original health path.
	return s.selectAccountWithLoadAwarenessAndUpstreamHealth(withoutProtectionCandidateReads(ctx), groupID, sessionHash, requestedModel, excludedIDs, metadataUserID, sub2apiUserID)
}

func selectGatewayCacheGuardTiers(ctx context.Context, preference cacheGuardPreference, base map[int64]struct{}, selectAccount func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error)) (*AccountSelectionResult, error, bool) {
	tiers := preference.Tiers
	if preference.KeepAccountID > 0 && !preference.Migration {
		preference = gatewayCacheGuardSourcePreference(preference)
		tiers = [][]int64{{preference.KeepAccountID}}
	} else if preference.Migration && preference.KeepAccountID > 0 {
		// A saturated destination must leave an existing session on its source.
		// Only a hard-invalid source allows the original scheduler to replace it.
		tiers = append(append([][]int64(nil), tiers...), []int64{preference.KeepAccountID})
	}
	ctx = withProtectionCandidateScope(ctx, preference.candidateScope())
	exclusions := preference.tierExclusions(base)
	for _, tier := range tiers {
		if !protectionPolicyEvaluationCurrent(ctx) {
			return nil, nil, false
		}
		result, err := selectAccount(withCacheGuardTentativeSelection(withProtectionCandidateTier(ctx, preference.ScopeIDs, tier)), exclusions(tier))
		if !protectionPolicyEvaluationCurrent(ctx) {
			if result != nil && result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			return nil, nil, false
		}
		if err != nil {
			if !errors.Is(err, ErrNoAvailableAccounts) {
				return result, err, true
			}
			continue
		}
		if result == nil || result.Account == nil {
			continue
		}
		if preference.Migration || preference.KeepAccountID > 0 {
			allowed := false
			for _, id := range tier {
				allowed = allowed || result.Account.ID == id
			}
			if !allowed {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				continue
			}
		}
		if preference.Migration && result.Account.ID != preference.KeepAccountID {
			if keyID, observed := preference.CandidateKeyIDs[result.Account.ID]; observed && keyID != accountUpstreamKeyID(result.Account) {
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
				continue
			}
		}
		source := preference.KeepAccountID > 0 && len(tier) == 1 && tier[0] == preference.KeepAccountID && result.Account.ID == preference.KeepAccountID
		if result.Acquired || source {
			return result, nil, true
		}
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	return nil, nil, false
}

func gatewayCacheGuardSourcePreference(preference cacheGuardPreference) cacheGuardPreference {
	// The source is exclusive even when ordinary accounts share the group.
	// They remain eligible in the final hard-fault fallback, never in this try.
	preference.ScopeIDs = make(map[int64]struct{}, len(preference.CandidateIDs))
	for _, id := range preference.CandidateIDs {
		preference.ScopeIDs[id] = struct{}{}
	}
	preference.Migration = false
	return preference
}

func (s *GatewayService) gatewayCacheGuardPreference(ctx context.Context, groupID *int64, sessionHash, model string, excludedIDs map[int64]struct{}) (cacheGuardPreference, context.Context, *int64, map[int64]struct{}) {
	noPreference := cacheGuardPreference{}
	runner := currentUpstreamCacheGuardRunner()
	if s == nil || runner == nil || runner.closed.Load() || groupID == nil {
		return noPreference, ctx, groupID, nil
	}
	group, resolvedGroupID, err := s.checkClaudeCodeRestriction(ctx, groupID)
	if err != nil || resolvedGroupID == nil {
		return noPreference, ctx, groupID, nil
	}
	settings := runner.engine.ResolvedSettings(*resolvedGroupID)
	if settings.Mode != upstreamcacheguard.Protect || !settings.ContainsGroup(*resolvedGroupID) {
		return noPreference, ctx, groupID, nil
	}
	guardCtx := s.withGroupContext(ctx, group)
	guardCtx = withProtectionPolicyEvaluation(guardCtx, runner.protectionPolicyReader())
	guardCtx = withGatewayProtectionRecheck(guardCtx, resolvedGroupID, model)
	guardCtx = s.withGatewayProfitControlGate(guardCtx, resolvedGroupID)
	platform, forced, err := s.resolvePlatform(guardCtx, resolvedGroupID, group, model)
	if err != nil {
		return noPreference, ctx, groupID, nil
	}
	accounts, _, err := s.listSchedulableAccounts(guardCtx, resolvedGroupID, platform, forced)
	if err != nil {
		return noPreference, ctx, groupID, nil
	}
	base := cloneExcludedAccountIDs(excludedIDs)
	if base == nil {
		base = make(map[int64]struct{})
	}
	for id := range s.upstreamHealthExcludedAccountIDs(guardCtx, resolvedGroupID) {
		base[id] = struct{}{}
	}
	eligible := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if _, excluded := base[account.ID]; !excluded {
			eligible = append(eligible, account)
		}
	}
	protocol := "anthropic"
	if platform == PlatformGemini || platform == PlatformAntigravity {
		protocol = "gemini"
	} else if platform == PlatformOpenAI {
		protocol = "openai"
	}
	preference := buildCacheGuardPreference(guardCtx, resolvedGroupID, sessionHash, model, protocol, eligible, nil)
	if protectionOrdinaryFallback(ctx) {
		return noPreference, ctx, groupID, nil
	}
	return preference, guardCtx, resolvedGroupID, base
}

// SelectAccountForModelWithExclusions adds the provider-neutral Key health
// protection layer while leaving the original scheduler untouched.
func (s *GatewayService) selectAccountWithUpstreamHealth(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	healthExcluded := s.upstreamHealthExcludedAccountIDs(ctx, groupID)
	if len(healthExcluded) == 0 {
		return s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, excludedIDs)
	}
	effective := cloneExcludedAccountIDs(excludedIDs)
	if effective == nil {
		effective = make(map[int64]struct{}, len(healthExcluded))
	}
	for id := range healthExcluded {
		effective[id] = struct{}{}
	}
	account, err := s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, effective)
	if err == nil {
		return account, nil
	}
	// Fail-open only when the health layer exhausted the group's candidates.
	if err == ErrNoAvailableAccounts {
		return s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, cloneExcludedAccountIDs(excludedIDs))
	}
	return nil, err
}

func (s *GatewayService) selectAccountWithLoadAwarenessAndUpstreamHealth(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, sub2apiUserID int64) (*AccountSelectionResult, error) {
	healthExcluded := s.upstreamHealthExcludedAccountIDs(ctx, groupID)
	if len(healthExcluded) == 0 {
		return s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, excludedIDs, metadataUserID, sub2apiUserID)
	}
	effective := cloneExcludedAccountIDs(excludedIDs)
	if effective == nil {
		effective = make(map[int64]struct{}, len(healthExcluded))
	}
	for id := range healthExcluded {
		effective[id] = struct{}{}
	}
	result, err := s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, effective, metadataUserID, sub2apiUserID)
	if err == nil && result != nil && result.Account != nil {
		return result, nil
	}
	if err == ErrNoAvailableAccounts || (err == nil && (result == nil || result.Account == nil)) {
		return s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, cloneExcludedAccountIDs(excludedIDs), metadataUserID, sub2apiUserID)
	}
	return result, err
}

func (s *GatewayService) upstreamHealthExcludedAccountIDs(ctx context.Context, groupID *int64) map[int64]struct{} {
	if s == nil || s.accountRepo == nil {
		return nil
	}
	healthReader := s.forkHealthReader()
	if !healthReader.HasTemporaryExclusions() {
		return nil
	}
	var accounts []Account
	var err error
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, PlatformAnthropic)
	}
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(accounts))
	keyByAccount := make(map[int64]int64, len(accounts))
	for i := range accounts {
		if accounts[i].UpstreamKeyID == nil {
			continue
		}
		ids = append(ids, accounts[i].ID)
		keyByAccount[accounts[i].ID] = *accounts[i].UpstreamKeyID
	}
	keyIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		keyIDs = append(keyIDs, keyByAccount[id])
	}
	keyExcluded := healthReader.ExcludedKeyIDs(keyIDs)
	out := make(map[int64]struct{}, len(keyExcluded))
	for accountID, keyID := range keyByAccount {
		if _, ok := keyExcluded[keyID]; ok {
			out[accountID] = struct{}{}
		}
	}
	return out
}
