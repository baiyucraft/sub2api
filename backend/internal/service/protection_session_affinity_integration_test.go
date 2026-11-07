package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func idleAffinitySelectionFixture(t *testing.T, advanced, weighted bool, idle, ttl time.Duration) (*OpenAIGatewayService, context.Context, *UpstreamCacheGuardRunner, *schedulerTestGatewayCache) {
	t.Helper()
	svc, ctx, runner, sticky := cacheGuardSelectionFixture(t, advanced, []float64{95, 95}, nil, false, time.Time{}, nil)
	crossPriorityAdvancedMode(svc, advanced, weighted)
	accounts := crossPriorityConfigureAccounts(t, svc, 10, 15)
	for i := range accounts {
		rate := float64(accounts[i].Priority) / 100
		accounts[i].RateMultiplier = &rate
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	group := int64(100)
	req, ok := runner.logicalRequest(ctx, &group, "conversation", "gpt-test", "openai")
	require.True(t, ok)
	source := req
	source.AccountID, source.UpstreamKeyID = 2, 2
	last := req.StartedAt.Add(-idle)
	route := upstreamcacheguard.WarmRouteKey(source)
	store := runner.store.(*cacheGuardSelectionStore)
	store.row(t, upstreamcacheguard.SessionKind, upstreamcacheguard.SessionKey(req), upstreamcacheguard.SessionState{
		Key: upstreamcacheguard.SessionKey(req), FirstSeenAt: last.Add(-time.Hour), NaturalRequestID: "old-natural-request",
		LastStartedAt: last, LastCompletedAt: last, LastAccountID: 2, LastModel: "gpt-test", LastProtocol: "openai", LastRouteKey: route,
		Routes: map[string]upstreamcacheguard.SessionRoute{route: {
			AccountID: 2, UpstreamKeyID: 2, LastSuccessfulAt: last, LastCachePercent: 95, HighCacheSeen: true,
		}},
	})
	// Priority 10 has never served traffic; ordinary allocation must leave it
	// neutral while the prior priority-15 account remains fully measured.
	coldCandidateCacheSamples(t, ctx, runner, 1, 0)
	runner.engine.SetSessionAffinityTTL(ttl)
	sticky.sessionBindings[svc.openAISessionCacheKey("conversation")] = 2
	svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{
		priority: SchedulingPriorityCacheFirst, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5),
	})
	coldCandidateReportTTFT(t, svc, ctx, 2, 1000, 5)
	return svc, ctx, runner, sticky
}

func TestProtectionIdleAffinityRealSchedulersReleaseOnlyExpiredSoftBindings(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, owner := range []string{"session", "movable_previous", "hard_previous", "fixed_retry"} {
				for _, expired := range []bool{false, true} {
					t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/expired=%t", advanced, weighted, owner, expired), func(t *testing.T) {
						idle := 30 * time.Minute
						if expired {
							idle = 2 * time.Hour
						}
						svc, ctx, runner, sticky := idleAffinitySelectionFixture(t, advanced, weighted, idle, time.Hour)
						group := int64(100)
						previous, canMove := "", false
						if owner == "movable_previous" || owner == "hard_previous" {
							previous = "resp_idle_affinity_owner"
							canMove = owner == "movable_previous"
							require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 2, time.Hour))
						}
						if owner == "fixed_retry" {
							ctx = WithOpenAIFixedRetryAccount(ctx, 2)
						}
						result, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, canMove, true)
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.Acquired)
						if result.ReleaseFunc != nil {
							t.Cleanup(result.ReleaseFunc)
						}
						reallocated := expired && owner != "hard_previous" && owner != "fixed_retry"
						want := int64(2)
						if reallocated {
							want = 1
						}
						require.Equal(t, want, result.Account.ID)
						require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "unsent selection must not replace a prior binding")
						require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans, "releasing an old affinity must not request another natural trial plan")
						require.False(t, result.IsSchedulingProtectionMigration())
						if reallocated {
							require.False(t, decision.StickySessionHit)
							require.False(t, decision.StickyPreviousHit)
							require.True(t, result.cacheGuardDeferredBinding)
							require.NotNil(t, result.protection)
							require.True(t, result.protection.affinityReallocation)
							require.False(t, result.protection.newAllocation, "old sessions must not earn TTFT natural-new credit")
							require.Nil(t, result.protection.trial)
						}
					})
				}
			}
		}
	}
}

func TestProtectionIdleAffinityRetainsAvailabilityAndProfitGate(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, scenario := range []string{"cheaper_full", "cheaper_unprofitable", "old_account_unprofitable"} {
			t.Run(fmt.Sprintf("advanced=%t/%s", advanced, scenario), func(t *testing.T) {
				svc, ctx, _, _ := idleAffinitySelectionFixture(t, advanced, true, 2*time.Hour, time.Hour)
				want := int64(2)
				if scenario == "cheaper_full" {
					svc.cfg.Gateway.Scheduling.LoadBatchEnabled = true
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
						acquireResults: map[int64]bool{1: false, 2: true},
						loadMap:        map[int64]*AccountLoadInfo{1: {AccountID: 1, LoadRate: 100}, 2: {AccountID: 2, LoadRate: 0}},
					})
				} else {
					accounts := crossPriorityConfigureAccounts(t, svc, 10, 15)
					vetoID := int64(1)
					if scenario == "old_account_unprofitable" {
						vetoID, want = 2, 1
					}
					for i := range accounts {
						if accounts[i].ID == vetoID {
							rate := 0.30
							accounts[i].RateMultiplier = &rate
						}
					}
					svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
					ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 100, platform: PlatformOpenAI, threshold: 0.16, pricingAt: time.Now()})
				}
				group := int64(100)
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				require.Equal(t, want, result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionIdleAffinityCommitsReplacementOnlyAtAdmittedSend(t *testing.T) {
	svc, ctx, _, sticky := idleAffinitySelectionFixture(t, true, true, 2*time.Hour, time.Hour)
	ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 100, platform: PlatformOpenAI, threshold: 0.16, pricingAt: time.Now()})
	group := int64(100)
	result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(1), result.Account.ID)
	if result.ReleaseFunc != nil {
		t.Cleanup(result.ReleaseFunc)
	}
	require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
	admitted := ContextWithSelectionProfitGate(ctx, result)
	veto, _ := OpenAIProfitControlVeto(admitted, result.Account)
	require.False(t, veto)
	admitted = WithUpstreamCacheGuardAttempt(admitted, 1, &group, "conversation", true)
	admitted = WithSchedulingProtectionBeforeSend(admitted, result.Account.ID, func(sent context.Context) error {
		return svc.BindStickySessionAfterProfitAdmission(sent, &group, "conversation", result.Account.ID)
	})
	PrepareOpenAIUpstreamCacheGuardSend(admitted, result.Account, "gpt-test", "openai")
	require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "preparing a request cannot commit its new binding")
	require.NoError(t, runSchedulingProtectionBeforeSend(admitted, result.Account, "gpt-test"))
	require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "terminal admission can replace an expired binding even with the profit gate enabled")
	require.False(t, svc.protectionSessionResident(admitted, group), "idle reallocation is not a quality-proven migration")
}

// Force the protected pass to see no slot while ordinary fallback has capacity.
// This ensures that clearing soft sticky applies beyond the first quality tier.
type idleAffinityFallbackConcurrencyCache struct {
	schedulerTestConcurrencyCache
	protectedAttempts, ordinaryAttempts int
}

func (c *idleAffinityFallbackConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	if cacheGuardTentativeSelection(ctx) {
		c.protectedAttempts++
		return false, nil
	}
	c.ordinaryAttempts++
	return c.schedulerTestConcurrencyCache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
}

func TestProtectionIdleAffinityOrdinaryFallbackDoesNotResurrectSoftSticky(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/weighted=%t", advanced, weighted), func(t *testing.T) {
				svc, ctx, _, sticky := idleAffinitySelectionFixture(t, advanced, weighted, 2*time.Hour, time.Hour)
				group := int64(100)
				const previous = "resp_idle_affinity_fallback"
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 2, time.Hour))
				capacity := &idleAffinityFallbackConcurrencyCache{}
				svc.concurrencyService = NewConcurrencyService(capacity)
				result, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, true, true)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				require.Equal(t, int64(1), result.Account.ID)
				require.Positive(t, capacity.protectedAttempts)
				require.Positive(t, capacity.ordinaryAttempts)
				require.False(t, decision.StickySessionHit)
				require.False(t, decision.StickyPreviousHit)
				require.True(t, result.SchedulingProtectionBindingDeferred())
				require.True(t, result.protection.affinityReallocation)
				require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionIdleAffinityStaleSendCannotCommitReallocation(t *testing.T) {
	svc, ctx, runner, sticky := idleAffinitySelectionFixture(t, false, false, 2*time.Hour, time.Hour)
	group := int64(100)
	result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, result)
	if result.ReleaseFunc != nil {
		t.Cleanup(result.ReleaseFunc)
	}
	store := runner.store.(*cacheGuardSelectionStore)
	store.failAccounts = map[int64]bool{result.Account.ID: true}
	require.Error(t, runner.engine.Refresh(ctx))
	store.failAccounts = nil
	ctx = ContextWithSelectionProfitGate(ctx, result)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "conversation", true)
	commits := 0
	ctx = WithSchedulingProtectionBeforeSend(ctx, result.Account.ID, func(sent context.Context) error {
		commits++
		return svc.BindStickySessionAfterProfitAdmission(sent, &group, "conversation", result.Account.ID)
	})
	PrepareOpenAIUpstreamCacheGuardSend(ctx, result.Account, "gpt-test", "openai")
	var fallback *UpstreamFailoverError
	require.ErrorAs(t, runSchedulingProtectionBeforeSend(ctx, result.Account, "gpt-test"), &fallback)
	require.True(t, fallback.ProtectionOrdinaryFallback)
	require.Zero(t, commits)
	require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
}

func TestProtectionIdleAffinityUsesConfiguredOriginalStickyTTL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds int
		idle    time.Duration
		want    int64
	}{
		{"default_retains_30_minutes", 0, 30 * time.Minute, 2},
		{"default_releases_2_hours", 0, 2 * time.Hour, 1},
		{"configured_releases_30_minutes", 600, 30 * time.Minute, 1},
		{"configured_retains_2_hours", 10800, 2 * time.Hour, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = tc.seconds
			ttl := openAIConfiguredSessionStickyTTL(cfg)
			svc, ctx, _, _ := idleAffinitySelectionFixture(t, false, false, tc.idle, ttl)
			svc.cfg = cfg
			require.Equal(t, ttl, svc.openAIWSSessionStickyTTL())
			group := int64(100)
			result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, tc.want, result.Account.ID)
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
	var missing *OpenAIGatewayService
	require.Equal(t, time.Hour, missing.openAIWSSessionStickyTTL())
}

func TestProtectionIdleAffinityRetryFallbackKeepsExpiry(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, weighted := range []bool{false, true} {
			for _, tc := range []struct {
				reason  string
				canMove bool
			}{{"snapshot", true}, {"budget", true}, {"snapshot", false}, {"budget", false}} {
				t.Run(fmt.Sprintf("advanced=%t/weighted=%t/%s/previous_can_move=%t", advanced, weighted, tc.reason, tc.canMove), func(t *testing.T) {
					svc, ctx, runner, sticky := idleAffinitySelectionFixture(t, advanced, weighted, 2*time.Hour, time.Hour)
					group := int64(100)
					const previous = "resp_idle_affinity_retry"
					if tc.canMove {
						require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 2, time.Hour))
					}
					first, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, tc.canMove, true)
					require.NoError(t, err)
					require.NotNil(t, first)
					require.Equal(t, int64(1), first.Account.ID)
					if first.ReleaseFunc != nil {
						first.ReleaseFunc()
					}
					if tc.reason == "snapshot" {
						store := runner.store.(*cacheGuardSelectionStore)
						store.failAccounts = map[int64]bool{first.Account.ID: true}
						require.Error(t, runner.engine.Refresh(ctx))
						store.failAccounts = nil
						ctx = ContextWithSelectionProfitGate(ctx, first)
						ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "conversation", true)
						PrepareOpenAIUpstreamCacheGuardSend(ctx, first.Account, "gpt-test", "openai")
						var failure *UpstreamFailoverError
						require.ErrorAs(t, runSchedulingProtectionBeforeSend(ctx, first.Account, "gpt-test"), &failure)
						require.True(t, failure.ProtectionOrdinaryFallback)
					} else {
						identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
						identity.budget.mu.Lock()
						identity.budget.activeElapsed = cacheGuardDatabaseBudget
						identity.budget.mu.Unlock()
						limited, cancel := cacheGuardDatabaseContextForStage(ctx, "admission")
						require.ErrorIs(t, limited.Err(), context.DeadlineExceeded)
						cancel()
					}
					require.True(t, protectionOrdinaryFallback(ctx))
					retry, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, tc.canMove, true)
					require.NoError(t, err)
					require.NotNil(t, retry)
					require.True(t, retry.Acquired)
					require.Equal(t, int64(1), retry.Account.ID, "ordinary retry must not revive the expired Redis session or movable previous owner")
					require.False(t, decision.StickySessionHit)
					require.False(t, decision.StickyPreviousHit)
					require.True(t, retry.SchedulingProtectionBindingDeferred())
					require.True(t, retry.protection.affinityReallocation)
					require.False(t, retry.protection.newAllocation)
					require.Nil(t, retry.protection.trial)
					require.Zero(t, runner.store.(*cacheGuardSelectionStore).trialPlans)
					require.Equal(t, int64(2), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
					if retry.ReleaseFunc != nil {
						retry.ReleaseFunc()
					}
				})
			}
		}
	}
}

func TestProtectionIdleAffinityRetryExpiryKeepsOtherGroupsAndHardOwners(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, owner := range []string{"other_group", "hard_previous", "fixed_retry", "guardian"} {
			t.Run(fmt.Sprintf("advanced=%t/%s", advanced, owner), func(t *testing.T) {
				svc, ctx, _, sticky := idleAffinitySelectionFixture(t, advanced, true, 2*time.Hour, time.Hour)
				group := int64(100)
				first, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, first)
				require.Equal(t, int64(1), first.Account.ID)
				if first.ReleaseFunc != nil {
					first.ReleaseFunc()
				}
				require.True(t, protectionAffinityExpiredForRequest(ctx, group))
				require.True(t, protectionSnapshotFallbackError(ctx).ProtectionOrdinaryFallback)
				previous := ""
				switch owner {
				case "other_group":
					// Ordinary hard-sticky mode provides a stable baseline for the
					// other group's healthy binding; weighted mode may prefer P10.
					crossPriorityAdvancedMode(svc, advanced, false)
					accounts := crossPriorityConfigureAccounts(t, svc, 10, 15)
					for i := range accounts {
						accounts[i].GroupIDs = []int64{100, 200}
					}
					svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
					group = 200
					require.False(t, protectionAffinityExpiredForRequest(ctx, group), "a composite retry cannot borrow another group's expiry")
				case "hard_previous":
					previous = "resp_idle_affinity_hard_retry"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, previous, 2, time.Hour))
				case "fixed_retry":
					ctx = WithOpenAIFixedRetryAccount(ctx, 2)
				case "guardian":
					ctx = context.WithValue(ctx, openAIGuardianParentAffinityContextKey{}, openAIGuardianParentAffinity{currentSessionHash: "guardian-parent"})
					sticky.sessionBindings[svc.openAISessionCacheKey("guardian-parent")] = 2
				}
				result, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, previous, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, false, true)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, int64(2), result.Account.ID)
				if result.protection != nil {
					require.False(t, result.protection.affinityReallocation)
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}
