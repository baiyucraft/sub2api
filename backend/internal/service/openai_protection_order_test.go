package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/stretchr/testify/require"
)

type jointPolicyResolver struct {
	priority string
	cfg      OpenAITTFTGuardConfigSnapshot
}

func (r jointPolicyResolver) ResolveProtection(_ context.Context, id int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	return GroupSchedulingProtectionResolvedPolicy{GroupID: id, Priority: r.priority, TTFT: GroupTTFTGuardResolvedPolicy{GroupID: id, Enabled: r.cfg.Enabled, Threshold: r.cfg.Threshold, MinSamples: r.cfg.MinSamples, Source: r.cfg.Source}}, nil
}

func TestJointProtectionPriorityExamplesAndCapacity(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
			t.Run(priority+map[bool]string{false: "/legacy", true: "/advanced"}[advanced], func(t *testing.T) {
				svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{95, 70}, nil, false, time.Time{}, nil)
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
				group := int64(100)
				for id, ms := range map[int64]int{1: 35000, 2: 4000} {
					a, _ := svc.accountRepo.GetByID(ctx, id)
					for i := 0; i < 5; i++ {
						svc.ReportOpenAIAccountScheduleResultForGroup(&group, a, "gpt-test", true, &ms)
					}
				}
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				want := int64(2)
				require.Equal(t, want, result.Account.ID)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestJointProtectionSlowSessionMigratesDespiteHigherCacheAfterTwoSuccesses(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		t.Run(map[bool]string{false: "target_full", true: "target_available"}[capacity], func(t *testing.T) {
			svc, ctx, runner, cache := cacheGuardSelectionFixture(t, true, []float64{95, 70}, nil, false, time.Time{}, map[int64]bool{1: true, 2: capacity})
			group := int64(100)
			cache.sessionBindings[svc.openAISessionCacheKey("conversation")] = 1
			ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:reliable-session")
			cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: SchedulingPriorityCacheFirst, cfg: cfg})
			for id, ms := range map[int64]int{1: 35000, 2: 4000} {
				a, _ := svc.accountRepo.GetByID(ctx, id)
				for i := 0; i < 5; i++ {
					svc.ReportOpenAIAccountScheduleResultForGroup(&group, a, "gpt-test", true, &ms)
				}
			}
			for i := 0; i < 2; i++ {
				ctx = context.WithValue(ctx, cacheGuardSchedulingKey{}, cacheGuardSchedulingIdentity{})
				ctx = WithUpstreamCacheGuardScheduling(ctx, 1)
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Equal(t, int64(1), result.Account.ID)
				ordinary := *result
				ordinary.cacheGuardProtectedSelection = false
				attempt := WithUpstreamCacheGuardAttempt(ContextWithSelectionProfitGate(ctx, &ordinary), 1, &group, "conversation", true)
				// An account admission persistence outage does not suppress the
				// TTFT completion path for ordinary traffic or manufacture cache
				// evidence. Strict selections instead require ordinary reselection.
				store := runner.store.(*cacheGuardSelectionStore)
				store.failAccounts = map[int64]bool{result.Account.ID: true}
				require.NoError(t, BeginUpstreamCacheGuardSend(attempt, result.Account, "gpt-test", "openai"))
				require.Nil(t, attempt.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt).admission)
				store.failAccounts = nil
				ms := 35000
				FinishOpenAIUpstreamCacheGuardAttempt(attempt, &OpenAIForwardResult{FirstTokenMs: &ms}, nil)
				FinishOpenAIUpstreamCacheGuardAttempt(attempt, &OpenAIForwardResult{FirstTokenMs: &ms}, nil)
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			}
			ctx = context.WithValue(ctx, cacheGuardSchedulingKey{}, cacheGuardSchedulingIdentity{})
			ctx = WithUpstreamCacheGuardScheduling(ctx, 1)
			result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			want := int64(1)
			if capacity {
				want = 2
			}
			require.Equal(t, want, result.Account.ID)
			require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
			if capacity {
				require.True(t, result.RequiresStickyAdmissionBinding())
				admitted := ContextWithSelectionProfitGate(ctx, result)
				require.NoError(t, svc.BindStickySessionAfterProfitAdmission(admitted, &group, "conversation", result.Account.ID))
				require.Equal(t, int64(2), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
				require.True(t, svc.protectionSessionResident(ctx, group))
			}
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}

func TestJointProtectionUnknownIsNeutralAndPoolsRemainOuter(t *testing.T) {
	accounts := []Account{{ID: 1, Priority: 1}, {ID: 2, Priority: 1}, {ID: 3, Priority: 2}}
	q := map[int64]forkscheduling.TTFTQuality{1: {Degraded: true, SampleCount: 5}, 3: {SampleCount: 5, EWMAms: 1000}}
	tiers := jointTTFTOrder([][]int64{{1, 2}, {3}}, accounts, nil, q, 5, SchedulingPriorityTTFTFirst)
	require.Equal(t, [][]int64{{2}, {1}, {3}}, tiers)
	require.Equal(t, 1, jointTTFTClass(2, q, 5))
	require.Equal(t, 0, jointTTFTClass(3, q, 5))
}

func TestJointProtectionPriorityOrdersOnlyWithinQualityState(t *testing.T) {
	accounts := []Account{{ID: 1, Priority: 1}, {ID: 2, Priority: 1}, {ID: 3, Priority: 1}}
	quality := map[int64]forkscheduling.TTFTQuality{
		1: {State: "normal", SampleCount: 5, EWMAms: 5000},
		2: {State: "normal", SampleCount: 5, EWMAms: 1000},
		3: {State: "protected", Degraded: true, SampleCount: 5, EWMAms: 100},
	}
	cacheTiers := [][]int64{{1}, {2}, {3}}

	require.Equal(t, [][]int64{{1}, {2}, {3}}, jointTTFTOrder(cacheTiers, accounts, nil, quality, 5, SchedulingPriorityCacheFirst))
	require.Equal(t, [][]int64{{2}, {1}, {3}}, jointTTFTOrder(cacheTiers, accounts, nil, quality, 5, SchedulingPriorityTTFTFirst))
	quality[2] = quality[1]
	require.Equal(t, [][]int64{{1}, {2}, {3}}, jointTTFTOrder(cacheTiers, accounts, nil, quality, 5, SchedulingPriorityTTFTFirst), "equal EWMA keeps the cache preference as tie breaker")
}

func TestJointProtectionUnreliableSessionCannotAccumulateSlowStreak(t *testing.T) {
	svc, ctx, _, _ := cacheGuardSelectionFixture(t, false, []float64{95}, nil, false, time.Time{}, nil)
	a, _ := svc.accountRepo.GetByID(ctx, 1)
	_, ok := protectionSessionKey(ctx, 100, a, "gpt-test", "openai")
	require.False(t, ok)
	require.False(t, svc.sessionTTFTCanMigrate(ctx, 100, a, "gpt-test", "openai", 20*time.Second))
}

func TestJointProtectionMigrationCombinesTriggersBeforePriorityFallback(t *testing.T) {
	for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
		for _, intersection := range []bool{false, true} {
			t.Run(priority+map[bool]string{false: "/disjoint", true: "/intersection"}[intersection], func(t *testing.T) {
				svc, ctx, _, _ := cacheGuardSelectionFixture(t, false, []float64{40, 85, 90, 80}, nil, false, time.Time{}, nil)
				ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:joint-triggers")
				accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, 100)
				require.NoError(t, err)
				var source *Account
				for i := range accounts {
					if accounts[i].ID == 1 {
						source = &accounts[i]
					}
				}
				require.NotNil(t, source)
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				key, reliable := protectionSessionKey(ctx, 100, source, "gpt-test", "openai")
				require.True(t, reliable)
				streak := svc.protectionSession(key, time.Now())
				streak.slow, streak.threshold = 2, cfg.Threshold
				quality := map[int64]forkscheduling.TTFTQuality{
					1: {Degraded: true, SampleCount: 5, EWMAms: 35000},
					2: {SampleCount: 5, EWMAms: 4000},
					4: {SampleCount: 5, EWMAms: 6000},
				}
				preference := cacheGuardPreference{Active: true, KeepAccountID: 1, Migration: true, Tiers: [][]int64{{3}}}
				if intersection {
					preference.Tiers = append(preference.Tiers, []int64{2})
				}
				result := svc.jointSessionPreference(ctx, preference, 100, "gpt-test", "openai", accounts, quality, cfg, priority)
				want := [][]int64{{2}, {4}, {3}}
				require.True(t, result.Migration)
				require.Equal(t, want, result.Tiers)
				quality[3] = forkscheduling.TTFTQuality{Degraded: true, SampleCount: 5, EWMAms: 35000}
				result = svc.jointSessionPreference(ctx, preference, 100, "gpt-test", "openai", accounts, quality, cfg, priority)
				require.NotContains(t, result.Tiers, []int64{3}, "cache migration must not move to a known slow account in either mode")
			})
		}
	}
}

func TestJointProtectionMigrationRejectsTargetWhoseKeyChangedAfterObservation(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			svc, ctx, _, cache := cacheGuardSelectionFixture(t, advanced, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
			svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return OpenAITTFTGuardConfigSnapshot{} }))
			initial := append([]Account(nil), svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts...)
			fresh := append([]Account(nil), initial...)
			newKey := int64(22)
			fresh[1].UpstreamKeyID = &newKey
			repo := &cacheGuardRefreshingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: fresh}, initial: initial}
			svc.accountRepo = repo
			group := int64(100)
			result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, repo.reads, "protection tiers share one candidate directory")
			require.Positive(t, repo.gets, "current account identity is still rechecked before capacity is acquired")
			require.Equal(t, int64(1), result.Account.ID, "a fresh Key without the target's measured evidence cannot justify migration")
			require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
			require.False(t, result.RequiresStickyAdmissionBinding())
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
		})
	}
}

func TestJointProtectionGatewayMigrationRejectsUnobservedReplacementKey(t *testing.T) {
	p := cacheGuardPreference{Active: true, Migration: true, KeepAccountID: 1, Tiers: [][]int64{{2}}, ScopeIDs: map[int64]struct{}{1: {}, 2: {}}, CandidateKeyIDs: map[int64]int64{1: 1, 2: 2}}
	newKey, sourceKey := int64(22), int64(1)
	calls, released := 0, 0
	result, err, handled := selectGatewayCacheGuardTiers(context.Background(), p, nil, func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
		calls++
		if calls == 1 {
			return &AccountSelectionResult{Account: &Account{ID: 2, UpstreamKeyID: &newKey}, Acquired: true, ReleaseFunc: func() { released++ }}, nil
		}
		return &AccountSelectionResult{Account: &Account{ID: 1, UpstreamKeyID: &sourceKey}, Acquired: true}, nil
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, int64(1), result.Account.ID)
	require.Equal(t, 2, calls)
	require.Equal(t, 1, released, "release the rejected target's real capacity before retaining the source")
}
