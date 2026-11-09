package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// A qualified account's measured quality must not hide its lower configured
// priority from either real scheduler. Protection is a gate, not another price
// ordering layer for accounts that already pass it.
func TestProtectionQualifiedPoolUsesOriginalPriorityAcrossQualityValues(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for _, cold := range []bool{false, true} {
					t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/cold=%t", advanced, weighted, priority, cold), func(t *testing.T) {
						svc, ctx, runner, _ := cacheGuardSelectionFixture(t, advanced, []float64{85, 99}, nil, false, time.Time{}, nil)
						crossPriorityAdvancedMode(svc, advanced, weighted)
						accounts := crossPriorityConfigureAccounts(t, svc, 10, 20)
						qualifiedPoolRates(accounts, .10, .20)
						counted := &crossPriorityCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
						svc.accountRepo = counted
						cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
						svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
						if cold {
							coldCandidateCacheSamples(t, ctx, runner, 1, 0)
						} else {
							coldCandidateReportTTFT(t, svc, ctx, 1, 10000, 5)
						}
						coldCandidateReportTTFT(t, svc, ctx, 2, 1000, 5)
						counted.directoryReads.Store(0)
						attempts := []int64{}
						svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &attempts})
						group := int64(100)
						result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						require.Equal(t, int64(1), result.Account.ID, "both accounts pass protection; higher cache rate or lower TTFT must not override priority 10")
						require.Equal(t, []int64{1}, attempts)
						require.Equal(t, int64(1), counted.directoryReads.Load(), "unifying qualified candidates must reuse the existing directory")
						require.False(t, result.IsSchedulingProtectionMigration())
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					})
				}
			}
		}
	}
}

func qualifiedPoolRates(accounts []Account, target, owner float64) {
	for i := range accounts {
		rate := target
		if accounts[i].ID == 2 {
			rate = owner
		}
		accounts[i].RateMultiplier = &rate
	}
}

// Own the price inputs here: idle-affinity tests deliberately exercise a
// different rule and can keep equal account prices in their shared fixture.
func qualifiedPoolBoundFixture(t *testing.T, advanced, weighted bool, priority string) (*OpenAIGatewayService, context.Context, *UpstreamCacheGuardRunner, *schedulerTestGatewayCache) {
	t.Helper()
	svc, ctx, runner, sticky := idleAffinitySelectionFixture(t, advanced, weighted, 30*time.Minute, time.Hour)
	accounts := crossPriorityConfigureAccounts(t, svc, 10, 20)
	qualifiedPoolRates(accounts, .10, .20)
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	coldCandidateCacheSamples(t, ctx, runner, 1, 21)
	runner.engine.SetSessionAffinityTTL(time.Hour)
	svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)})
	coldCandidateReportTTFT(t, svc, ctx, 1, 10000, 5)
	return svc, ctx, runner, sticky
}

func TestProtectionQualifiedPoolCheaperAccountCanReplaceActiveSoftBinding(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for _, cold := range []bool{false, true} {
					t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/cold=%t", advanced, weighted, priority, cold), func(t *testing.T) {
						svc, ctx, runner, sticky := qualifiedPoolBoundFixture(t, advanced, weighted, priority)
						if cold {
							coldCandidateCacheSamples(t, ctx, runner, 1, 0)
							runner.engine.SetSessionAffinityTTL(time.Hour)
						}
						group := int64(100)
						ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: group, platform: PlatformOpenAI, threshold: .25, pricingAt: time.Now()})
						result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						require.Equal(t, int64(1), result.Account.ID)
						require.True(t, result.IsSchedulingProtectionMigration(), "qualified cheaper replacement must use existing migration admission")
						require.Equal(t, int64(1), result.cacheGuardMigrationAccountID)
						require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "selection is not a committed send")
						require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans, "an old session may not acquire another natural trial opportunity")
						require.NotNil(t, result.protection)
						require.False(t, result.protection.newAllocation)
						require.Nil(t, result.protection.trial)
						admitted := ContextWithSelectionProfitGate(ctx, result)
						veto, _ := OpenAIProfitControlVeto(admitted, result.Account)
						require.False(t, veto)
						require.NoError(t, svc.BindStickySessionAfterProfitAdmission(admitted, &group, "conversation", result.Account.ID))
						require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					})
				}
			}
		}
	}
}

func qualifiedPoolMutateAccountState(t *testing.T, ctx context.Context, runner *UpstreamCacheGuardRunner, mutate func(*upstreamcacheguard.AccountState)) {
	t.Helper()
	store := runner.store.(*cacheGuardSelectionStore)
	for i := range store.rows {
		row := &store.rows[i]
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var state upstreamcacheguard.AccountState
		require.NoError(t, json.Unmarshal(row.Payload, &state))
		mutate(&state)
		var err error
		row.Payload, err = json.Marshal(state)
		require.NoError(t, err)
	}
	store.accounts = nil
	// This fixed-row store does not publish changing DB account revisions.
	// Rehydrate exactly as the existing cold-sample fixture does, rather than
	// asking revision reuse to discover a mutation with the same fake version.
	settings := runner.engine.Settings()
	runner.engine = upstreamcacheguard.NewEngine(store)
	require.NoError(t, runner.engine.ApplySettings(settings))
	require.NoError(t, runner.engine.Refresh(ctx))
}

func TestProtectionQualifiedPoolSoftReplacementKeepsSafetyBoundaries(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, scenario := range []string{"same_priority", "same_rate", "higher_rate", "cache_protected", "ttft_protected", "needs_revalidation", "unavailable", "different_model", "residence", "full", "profit_veto"} {
			t.Run(fmt.Sprintf("advanced=%t/%s", advanced, scenario), func(t *testing.T) {
				svc, ctx, runner, sticky := qualifiedPoolBoundFixture(t, advanced, true, SchedulingPriorityCacheFirst)
				accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, 100)
				require.NoError(t, err)
				group := int64(100)
				switch scenario {
				case "same_priority":
					accounts[0].Priority = 20
				case "same_rate":
					qualifiedPoolRates(accounts, .20, .20)
				case "higher_rate":
					qualifiedPoolRates(accounts, .30, .20)
				case "cache_protected":
					qualifiedPoolMutateAccountState(t, ctx, runner, func(state *upstreamcacheguard.AccountState) {
						if state.AccountID == 1 {
							state.Phase, state.CachePercent = upstreamcacheguard.Cooling, 20
						}
					})
				case "ttft_protected", "needs_revalidation":
					guard := svc.getOpenAITTFTGuard()
					if scenario == "needs_revalidation" {
						anchor := time.Now().Add(-16 * time.Minute)
						guard.now = func() time.Time { return anchor }
					}
					coldCandidateReportTTFT(t, svc, ctx, 1, 90000, 1)
					guard.now = time.Now
					quality := guard.quality([]openAITTFTGuardCandidate{{groupID: group, accountID: 1, upstreamKeyID: 1, model: "gpt-test"}}, enabledOpenAITTFTGuardConfig(20*time.Second, 5))
					require.True(t, quality[1].NeedsRevalidation)
				case "unavailable":
					runner.store.(*cacheGuardSelectionStore).failAccounts = map[int64]bool{1: true}
					require.Error(t, runner.engine.Refresh(ctx))
				case "different_model":
					accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"gpt-test": "other-model"}}
				case "residence":
					store := runner.store.(*cacheGuardSelectionStore)
					for i := range store.rows {
						if store.rows[i].Kind == upstreamcacheguard.SessionKind {
							var session upstreamcacheguard.SessionState
							require.NoError(t, json.Unmarshal(store.rows[i].Payload, &session))
							session.ResidenceUntil = time.Now().Add(5 * time.Minute)
							store.rows[i].Payload, err = json.Marshal(session)
							require.NoError(t, err)
						}
					}
					require.NoError(t, runner.engine.Refresh(ctx))
				case "full":
					svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
						acquireResults: map[int64]bool{1: false, 2: true},
						loadMap:        map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}, 2: {AccountID: 2, LoadRate: 0}},
					})
				case "profit_veto":
					// A stricter candidate price gate rejects the target while the
					// existing owner remains legal, independently of its priority.
					qualifiedPoolRates(accounts, .30, .20)
					ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: group, platform: PlatformOpenAI, threshold: .25, pricingAt: time.Now()})
				}
				svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				require.Equal(t, int64(2), result.Account.ID, "a price opportunity is not permission to bypass protection, identities, residence, or real capacity")
				require.False(t, result.IsSchedulingProtectionMigration())
				require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
				require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionQualifiedPoolKeepsHardOwners(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, owner := range []string{"previous", "fixed_retry"} {
				t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s", advanced, weighted, owner), func(t *testing.T) {
					svc, ctx, runner, sticky := qualifiedPoolBoundFixture(t, advanced, weighted, SchedulingPriorityCacheFirst)
					group := int64(100)
					previous := ""
					if owner == "previous" {
						previous = "resp_qualified_pool_owner"
						require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 2, time.Hour))
					} else {
						ctx = WithOpenAIFixedRetryAccount(ctx, 2)
					}
					result, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, false, true)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.True(t, result.Acquired)
					require.Equal(t, int64(2), result.Account.ID)
					require.False(t, result.IsSchedulingProtectionMigration())
					require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
					require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
				})
			}
		}
	}
}

func TestProtectionQualifiedPoolTerminalProfitVetoCannotCommitSoftReplacement(t *testing.T) {
	svc, ctx, _, sticky := qualifiedPoolBoundFixture(t, true, true, SchedulingPriorityCacheFirst)
	group := int64(100)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: group, platform: PlatformOpenAI, threshold: .25, pricingAt: time.Now()})
	result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.Account.ID)
	require.True(t, result.IsSchedulingProtectionMigration())
	if result.ReleaseFunc != nil {
		t.Cleanup(result.ReleaseFunc)
	}
	// The target was affordable during candidate selection but no longer passes
	// the final send-time gate. Nothing may commit the selected replacement yet.
	admitted := ContextWithSelectionProfitGate(ctx, result)
	rate := .30
	result.Account.RateMultiplier = &rate
	veto, _ := OpenAIProfitControlVeto(admitted, result.Account)
	require.True(t, veto)
	require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
}

// Extend the legacy resolver's scope without changing its cache-policy
// evaluation behavior; Kimi exposes both real HTTP protocol identities.
type qualifiedPoolProtocolResolver struct{ jointPolicyResolver }

func (qualifiedPoolProtocolResolver) PlatformSelected(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformKimi
}

func TestProtectionQualifiedPoolRefreshedTargetCannotBorrowCostQualification(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, change := range []string{"protocol", "price", "key"} {
			t.Run(fmt.Sprintf("advanced=%t/%s", advanced, change), func(t *testing.T) {
				svc, ctx, runner, sticky := qualifiedPoolBoundFixture(t, advanced, true, SchedulingPriorityCacheFirst)
				initial, err := svc.accountRepo.ListSchedulableByGroupID(ctx, 100)
				require.NoError(t, err)
				platform := PlatformOpenAI
				if change == "protocol" {
					platform = PlatformKimi
					for i := range initial {
						initial[i].Platform = platform
						initial[i].Credentials = map[string]any{"api_protocol": APIProtocolResponses}
					}
					reader := qualifiedPoolProtocolResolver{jointPolicyResolver{priority: SchedulingPriorityCacheFirst, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)}}
					runner.SetProtectionPolicyResolver(reader)
					svc.SetGroupSchedulingProtectionPolicyResolver(reader)
				}
				fresh := append([]Account(nil), initial...)
				switch change {
				case "protocol":
					fresh[0].Credentials = map[string]any{"api_protocol": APIProtocolAnthropic}
					protocolCtx := svc.withCacheGuardOpenAIProtocolResolver(ctx)
					require.Equal(t, "openai", cacheGuardAccountProtocol(protocolCtx, &initial[0], "gpt-test", "openai"))
					require.Equal(t, "anthropic", cacheGuardAccountProtocol(protocolCtx, &fresh[0], "gpt-test", "openai"), "the recheck must observe a genuinely different protocol")
				case "price":
					rate := .30
					fresh[0].RateMultiplier = &rate
				case "key":
					key := int64(101)
					fresh[0].UpstreamKeyID = &key
				}
				repo := &cacheGuardRefreshingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: fresh}, initial: initial}
				svc.accountRepo = repo
				group := int64(100)
				result, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, false, true, platform)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				require.Equal(t, int64(2), result.Account.ID, "a freshly changed account cannot reuse its previous cost/identity qualification")
				require.Positive(t, repo.gets, "the test must reach fresh account rechecks")
				require.False(t, result.IsSchedulingProtectionMigration())
				require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
				require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionQualifiedPoolColdTargetSharesOrdinaryPoolWithProvenTarget(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for _, targetAvailable := range []bool{false, true} {
					t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/cold_available=%t", advanced, weighted, priority, targetAvailable), func(t *testing.T) {
						// The low-cache trigger releases source 1 at .20. Ordinary
						// targets 2 (.30, proven) and 3 (.10, cold) share one native
						// pool; missing samples do not move account 3 behind 2.
						svc, ctx, runner, sticky := cacheGuardSelectionFixture(t, advanced, []float64{20, 99, 95}, map[int64]bool{1: true}, true, time.Time{}, map[int64]bool{1: true, 2: true, 3: targetAvailable})
						crossPriorityAdvancedMode(svc, advanced, weighted)
						accounts := crossPriorityConfigureAccounts(t, svc, 20, 30, 10)
						for i := range accounts {
							rate := float64(accounts[i].Priority) / 100
							accounts[i].RateMultiplier = &rate
						}
						counted := &crossPriorityCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
						svc.accountRepo = counted
						coldCandidateCacheSamples(t, ctx, runner, 3, 0)
						cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
						svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
						coldCandidateReportTTFT(t, svc, ctx, 1, 1000, 5)
						coldCandidateReportTTFT(t, svc, ctx, 2, 1000, 5)
						quality := svc.getOpenAITTFTGuard().quality([]openAITTFTGuardCandidate{{groupID: 100, accountID: 3, upstreamKeyID: 3, model: "gpt-test"}}, cfg)
						require.NotContains(t, quality, int64(3), "the cost target has no TTFT proof")
						group := int64(100)
						preference := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
						require.True(t, preference.Migration)
						require.Equal(t, [][]int64{{2}}, preference.Tiers, "the old quality path must remain justified independently")
						counted.directoryReads.Store(0)
						result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						want := int64(3)
						if !targetAvailable {
							want = 2
							if !advanced {
								// Keep the native bounded WaitPlan/fallback behavior.
								want = 1
							}
						}
						require.Equal(t, want, result.Account.ID, "ordinary targets share the original scheduler's priority and bounded capacity selection")
						require.Equal(t, want != 1, result.IsSchedulingProtectionMigration())
						require.Equal(t, int64(1), counted.directoryReads.Load())
						require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "neither path commits the new owner before send admission")
						require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
						require.NotNil(t, result.protection)
						require.False(t, result.protection.newAllocation)
						require.Nil(t, result.protection.trial)
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					})
				}
			}
		}
	}
}
