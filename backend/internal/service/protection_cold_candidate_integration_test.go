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

// Rehydrate the existing transactional fixture with genuinely missing/partial
// cache facts. A zero rate with 21 facts would be bad evidence, not cold start.
func coldCandidateCacheSamples(t *testing.T, ctx context.Context, runner *UpstreamCacheGuardRunner, id int64, count int) {
	t.Helper()
	store := runner.store.(*cacheGuardSelectionStore)
	for i := range store.rows {
		row := &store.rows[i]
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var state upstreamcacheguard.AccountState
		require.NoError(t, json.Unmarshal(row.Payload, &state))
		if state.AccountID == id {
			state.ValidRequests = count
			var err error
			row.Payload, err = json.Marshal(state)
			require.NoError(t, err)
		}
	}
	settings := runner.engine.Settings()
	store.accounts = nil
	runner.engine = upstreamcacheguard.NewEngine(store)
	require.NoError(t, runner.engine.ApplySettings(settings))
	require.NoError(t, runner.engine.Refresh(ctx))
}

func coldCandidateReportTTFT(t *testing.T, svc *OpenAIGatewayService, ctx context.Context, id int64, ms, count int) {
	t.Helper()
	group := int64(100)
	account, err := svc.accountRepo.GetByID(ctx, id)
	require.NoError(t, err)
	for i := 0; i < count; i++ {
		svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &ms)
	}
}

func TestProtectionColdCandidateRealSchedulersUseOriginalPriority(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, stickyWeighted := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for _, samples := range []struct {
					name        string
					cache, ttft int
				}{
					{"both_missing", 0, 0},
					{"cache_missing", 0, 5},
					{"ttft_missing", 21, 0},
					{"both_partial", 1, 1},
				} {
					t.Run(fmt.Sprintf("advanced=%t/sticky=%t/%s/%s", advanced, stickyWeighted, priority, samples.name), func(t *testing.T) {
						svc, ctx, runner, _ := cacheGuardSelectionFixture(t, advanced, []float64{95, 95}, nil, false, time.Time{}, nil)
						crossPriorityAdvancedMode(svc, advanced, stickyWeighted)
						accounts := crossPriorityConfigureAccounts(t, svc, 10, 15)
						for i := range accounts {
							rate := float64(accounts[i].Priority) / 100
							accounts[i].RateMultiplier = &rate
						}
						counted := &crossPriorityCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
						svc.accountRepo = counted
						coldCandidateCacheSamples(t, ctx, runner, 1, samples.cache)
						cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
						svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
						coldCandidateReportTTFT(t, svc, ctx, 1, 1000, samples.ttft)
						coldCandidateReportTTFT(t, svc, ctx, 2, 1000, 5)
						quality := svc.getOpenAITTFTGuard().quality([]openAITTFTGuardCandidate{
							{groupID: 100, accountID: 1, upstreamKeyID: 1, model: "gpt-test"},
							{groupID: 100, accountID: 2, upstreamKeyID: 2, model: "gpt-test"},
						}, cfg)
						require.Equal(t, "normal", quality[2].State, "fixture must include a proven normal competitor")
						if samples.ttft == 0 {
							require.NotContains(t, quality, int64(1))
						} else if samples.ttft < cfg.MinSamples {
							require.Equal(t, "insufficient", quality[1].State)
						}
						counted.accountReads.Store(0)
						attempts := []int64{}
						svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &attempts})
						group := int64(100)
						result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						require.Equal(t, int64(1), result.Account.ID, "missing traffic must not rank priority 10 behind a proven priority 15 account")
						require.Equal(t, []int64{1}, attempts, "neutral candidates need no extra acquire/retry loop")
						require.Equal(t, int64(1), counted.directoryReads.Load(), "one candidate directory per request")
						require.Positive(t, counted.accountReads.Load(), "final account rechecks remain active")
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

func TestProtectionColdCandidateRealSchedulersKeepBadEvidenceAndFallbacks(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
			for _, scenario := range []string{"full", "profit_veto", "cache_protected", "ttft_protected", "ttft_revalidation", "healthy_binding", "migration_needs_proof"} {
				t.Run(fmt.Sprintf("advanced=%t/%s/%s", advanced, priority, scenario), func(t *testing.T) {
					protected := map[int64]bool{}
					pending := scenario == "migration_needs_proof"
					if scenario == "cache_protected" || pending {
						protected[1] = true
					}
					svc, ctx, runner, sticky := cacheGuardSelectionFixture(t, advanced, []float64{20, 95}, protected, pending, time.Time{}, nil)
					accounts := crossPriorityConfigureAccounts(t, svc, 10, 15)
					for i := range accounts {
						rate := float64(accounts[i].Priority) / 100
						accounts[i].RateMultiplier = &rate
					}
					svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
					coldID := int64(1)
					if pending {
						coldID = 2
					}
					coldCandidateCacheSamples(t, ctx, runner, coldID, 0)
					cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
					svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
					group := int64(100)
					want := int64(2)
					switch scenario {
					case "full":
						svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
						svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
							acquireResults: map[int64]bool{1: false, 2: true},
							loadMap: map[int64]*AccountLoadInfo{
								1: {AccountID: 1, LoadRate: 100},
								2: {AccountID: 2, LoadRate: 0},
							},
						})
					case "profit_veto":
						rate := 0.20
						accounts[0].RateMultiplier = &rate
						svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
						ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: group, platform: PlatformOpenAI, threshold: 0.16, pricingAt: time.Now()})
					case "ttft_protected":
						coldCandidateReportTTFT(t, svc, ctx, 1, 90000, 1)
					case "ttft_revalidation":
						now := time.Now()
						guard := svc.getOpenAITTFTGuard()
						guard.now = func() time.Time { return now.Add(-16 * time.Minute) }
						coldCandidateReportTTFT(t, svc, ctx, 1, 90000, 1)
						guard.now = time.Now
					case "healthy_binding":
						sticky.sessionBindings[svc.openAISessionCacheKey("conversation")] = 2
					case "migration_needs_proof":
						want = 1
					}
					if scenario == "ttft_revalidation" || scenario == "ttft_protected" {
						quality := svc.getOpenAITTFTGuard().quality([]openAITTFTGuardCandidate{{groupID: group, accountID: 1, upstreamKeyID: 1, model: "gpt-test"}}, cfg)
						require.True(t, quality[1].NeedsRevalidation)
						require.Equal(t, scenario == "ttft_protected", quality[1].Degraded)
					}
					result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.True(t, result.Acquired)
					require.Equal(t, want, result.Account.ID)
					require.False(t, result.IsSchedulingProtectionMigration())
					if scenario == "profit_veto" {
						require.True(t, result.ProfitGateActive())
					}
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
				})
			}
		}
	}
}

func TestProtectionColdCandidateLegacyWaitPlanRemainsAvailable(t *testing.T) {
	for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
		t.Run(priority, func(t *testing.T) {
			svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{95, 95}, nil, false, time.Time{}, map[int64]bool{1: false, 2: true})
			crossPriorityConfigureAccounts(t, svc, 10, 15)
			coldCandidateCacheSamples(t, ctx, runner, 1, 0)
			svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)})
			group := int64(100)
			result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.Acquired)
			require.NotNil(t, result.WaitPlan, "legacy selection without batch load keeps its existing bounded waiting fallback")
			require.Equal(t, int64(1), result.Account.ID)
		})
	}
}
