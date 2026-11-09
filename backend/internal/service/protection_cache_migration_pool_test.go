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

// Reproduce the price interval missed by the independent cost-reallocation
// rule: bad owner .07 < ordinary cold target .08 < proven quality target .20.
func cacheMigrationPoolFixture(t *testing.T, advanced, weighted bool, priority string, proven bool, pending bool) (*OpenAIGatewayService, context.Context, *UpstreamCacheGuardRunner, *schedulerTestGatewayCache, []Account) {
	t.Helper()
	rates, priorities := []float64{20, 95}, []int{7, 8}
	if proven {
		rates, priorities = append(rates, 99), append(priorities, 20)
	}
	svc, ctx, runner, sticky := cacheGuardSelectionFixture(t, advanced, rates, map[int64]bool{1: true}, pending, time.Now().Add(-time.Minute), nil)
	crossPriorityAdvancedMode(svc, advanced, weighted)
	accounts := crossPriorityConfigureAccounts(t, svc, priorities...)
	for i := range accounts {
		rate := float64(accounts[i].Priority) / 100
		accounts[i].RateMultiplier = &rate
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	coldCandidateCacheSamples(t, ctx, runner, 2, 0)
	svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)})
	coldCandidateReportTTFT(t, svc, ctx, 1, 1000, 5)
	if proven {
		coldCandidateReportTTFT(t, svc, ctx, 3, 1000, 5)
	}
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 100, platform: PlatformOpenAI, threshold: .25, pricingAt: time.Now()})
	return svc, ctx, runner, sticky, accounts
}

func TestProtectionCacheMigrationPoolColdCandidateBetweenOwnerAndProvenTarget(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for _, scenario := range []string{"cold_before_proven", "only_cold", "cold_full", "all_targets_full"} {
					t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/%s", advanced, weighted, priority, scenario), func(t *testing.T) {
						svc, ctx, runner, sticky, accounts := cacheMigrationPoolFixture(t, advanced, weighted, priority, scenario != "only_cold", true)
						repo := &crossPriorityCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
						svc.accountRepo = repo
						capacity := map[int64]bool{1: true, 2: true, 3: true}
						if scenario == "cold_full" || scenario == "all_targets_full" {
							capacity[2] = false
						}
						if scenario == "all_targets_full" {
							capacity[3] = false
						}
						group := int64(100)
						tiers := [][]int64{{2, 3}}
						if scenario == "only_cold" {
							tiers = [][]int64{{2}}
						}
						// Independently invoke the unwrapped native scheduler for
						// this explicit ordinary pool, including its existing bounded
						// retry and owner fallback behavior. Do not invent a new cap.
						native := jointOracleNativeQualifiedReference(t, svc, ctx, group, capacity, sticky, tiers, true)
						repo.directoryReads.Store(0)
						var attempts []int64
						svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: capacity, acquiredIDs: &attempts})
						result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						want := int64(2)
						if scenario == "all_targets_full" || (scenario == "cold_full" && !advanced) {
							// The legacy scheduler's bounded selection may return a
							// WaitPlan. This change must not add another search loop.
							want = 1
						} else if scenario == "cold_full" {
							want = 3
						}
						require.Equal(t, want, result.Account.ID)
						require.Equal(t, want != 1, result.IsSchedulingProtectionMigration())
						require.Equal(t, int64(1), repo.directoryReads.Load(), "the ordinary migration pool must reuse the existing directory")
						require.Equal(t, native.AttemptOrder, attempts, "qualification must preserve the native acquisition attempts and owner fallback")
						require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans, "old-session migration cannot acquire a natural trial slot")
						require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "a tentative selection is not a committed owner")
						if result.protection != nil {
							require.False(t, result.protection.newAllocation)
							require.Nil(t, result.protection.trial)
						}
						admitted := ContextWithSelectionProfitGate(ctx, result)
						veto, _ := OpenAIProfitControlVeto(admitted, result.Account)
						require.False(t, veto)
						require.NoError(t, svc.BindStickySessionAfterProfitAdmission(admitted, &group, "conversation", result.Account.ID))
						require.Equal(t, want, sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
						if result.ReleaseFunc != nil {
							result.ReleaseFunc()
						}
					})
				}
			}
		}
	}
}

func TestProtectionCacheMigrationPoolKeepsTriggerAndQualificationBoundaries(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, scenario := range []string{"no_low_trigger", "cache_protected", "ttft_protected", "needs_revalidation", "cache_unavailable", "different_model", "different_protocol", "residence", "profit_veto", "fixed_retry", "hard_previous", "movable_previous"} {
			t.Run(fmt.Sprintf("advanced=%t/%s", advanced, scenario), func(t *testing.T) {
				svc, ctx, runner, sticky, accounts := cacheMigrationPoolFixture(t, advanced, true, SchedulingPriorityCacheFirst, true, scenario != "no_low_trigger")
				group := int64(100)
				previous, platform := "", PlatformOpenAI
				want := int64(3)
				switch scenario {
				case "no_low_trigger":
					want = 1
				case "cache_protected":
					qualifiedPoolMutateAccountState(t, ctx, runner, func(state *upstreamcacheguard.AccountState) {
						if state.AccountID == 2 {
							state.Phase = upstreamcacheguard.Cooling
						}
					})
				case "ttft_protected", "needs_revalidation":
					guard := svc.getOpenAITTFTGuard()
					if scenario == "needs_revalidation" {
						anchor := time.Now().Add(-16 * time.Minute)
						guard.now = func() time.Time { return anchor }
					}
					coldCandidateReportTTFT(t, svc, ctx, 2, 90000, 1)
					guard.now = time.Now
					quality := guard.quality([]openAITTFTGuardCandidate{{groupID: group, accountID: 2, upstreamKeyID: 2, model: "gpt-test"}}, enabledOpenAITTFTGuardConfig(20*time.Second, 5))
					require.True(t, quality[2].NeedsRevalidation)
				case "cache_unavailable":
					runner.store.(*cacheGuardSelectionStore).failAccounts = map[int64]bool{2: true}
					require.Error(t, runner.engine.Refresh(ctx))
				case "different_model":
					accounts[1].Credentials = map[string]any{"model_mapping": map[string]any{"gpt-test": "other-model"}}
				case "different_protocol":
					platform = PlatformKimi
					for i := range accounts {
						accounts[i].Platform = platform
						accounts[i].Credentials = map[string]any{"api_protocol": APIProtocolResponses}
					}
					accounts[1].Credentials = map[string]any{"api_protocol": APIProtocolAnthropic}
					resolver := qualifiedPoolProtocolResolver{jointPolicyResolver{priority: SchedulingPriorityCacheFirst, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)}}
					runner.SetProtectionPolicyResolver(resolver)
					svc.SetGroupSchedulingProtectionPolicyResolver(resolver)
					protocolCtx := svc.withCacheGuardOpenAIProtocolResolver(ctx)
					require.Equal(t, "openai", cacheGuardAccountProtocol(protocolCtx, &accounts[0], "gpt-test", "openai"))
					require.Equal(t, "anthropic", cacheGuardAccountProtocol(protocolCtx, &accounts[1], "gpt-test", "openai"))
				case "residence":
					want = 1
					store := runner.store.(*cacheGuardSelectionStore)
					for i := range store.rows {
						if store.rows[i].Kind == upstreamcacheguard.SessionKind {
							var session upstreamcacheguard.SessionState
							require.NoError(t, json.Unmarshal(store.rows[i].Payload, &session))
							session.ResidenceUntil = time.Now().Add(5 * time.Minute)
							var err error
							store.rows[i].Payload, err = json.Marshal(session)
							require.NoError(t, err)
						}
					}
					require.NoError(t, runner.engine.Refresh(ctx))
				case "profit_veto":
					// Only the .07 source passes this gate. The .08 cold account
					// may not return through either migration or ordinary fallback.
					want = 1
					ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: group, platform: PlatformOpenAI, threshold: .075, pricingAt: time.Now()})
				case "fixed_retry":
					want = 1
					ctx = WithOpenAIFixedRetryAccount(ctx, 1)
				case "hard_previous":
					want = 1
					previous = "resp_cache_migration_hard_owner"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 1, time.Hour))
				case "movable_previous":
					want = 2
					previous = "resp_cache_migration_soft_owner"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 1, time.Hour))
				}
				svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
				result, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, scenario == "movable_previous", true, platform)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				require.Equal(t, want, result.Account.ID, "cold neutrality does not erase bad evidence, identity, residency, or hard constraints")
				require.Equal(t, want != 1, result.IsSchedulingProtectionMigration())
				require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
				require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionCacheMigrationPoolRefreshedKeyCannotBorrowQualification(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("advanced=%t", advanced), func(t *testing.T) {
			svc, ctx, runner, sticky, initial := cacheMigrationPoolFixture(t, advanced, true, SchedulingPriorityCacheFirst, true, true)
			fresh := append([]Account(nil), initial...)
			key := int64(102)
			fresh[1].UpstreamKeyID = &key
			repo := &cacheGuardRefreshingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: fresh}, initial: initial}
			svc.accountRepo = repo
			group := int64(100)
			result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.Acquired)
			require.NotEqual(t, int64(2), result.Account.ID, "a changed Key cannot inherit cold qualification from the observed identity")
			require.Positive(t, repo.gets)
			require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
			require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}
