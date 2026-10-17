package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func freshTTFTOpportunityContext() context.Context {
	return WithUpstreamCacheGuardScheduling(context.Background(), 1)
}

func fundTTFTTrial(g *openAITTFTGuard, group int64, cfg OpenAITTFTGuardConfigSnapshot) {
	for i := 0; i < openAITTFTGuardProbeEvery; i++ {
		g.recordAllocationOpportunity(freshTTFTOpportunityContext(), group, "gpt-test", cfg)
	}
}

func TestTTFTTrialReadsAndRepeatedSelectionDoNotFundCredit(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	candidates := []openAITTFTGuardCandidate{{groupID: 100, accountID: 1, upstreamKeyID: 10, model: "gpt-test"}}
	for i := 0; i < 100; i++ {
		g.exclusions(candidates, nil, cfg)
		g.quality(candidates, cfg)
	}
	require.Empty(t, g.groupProbes)
	ctx := freshTTFTOpportunityContext()
	for i := 0; i < 100; i++ {
		g.recordAllocationOpportunity(ctx, 100, "gpt-test", cfg)
	}
	scope, _ := openAITTFTGuardScopeFor(100, cfg)
	p := g.groupProbes[openAITTFTGuardProbeKey{scopeID: scope, model: "gpt-test"}]
	require.Equal(t, uint64(1), p.sequence)
	require.False(t, g.budgetStateLocked(100, "gpt-test").credit)
	key := int64(10)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, []Account{{ID: 1, UpstreamKeyID: &key}}, "gpt-test", cfg, nil))
}

func TestTTFTTrialRefundFairnessSendAndSingleCredit(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	key := int64(10)
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}, {ID: 2, Platform: PlatformOpenAI, UpstreamKeyID: &key}}
	q := map[int64]forkscheduling.TTFTQuality{1: {State: "protected", Degraded: true}, 2: {State: "insufficient"}}
	fundTTFTTrial(g, 100, cfg)
	first := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, q)
	require.NotNil(t, first)
	require.Equal(t, int64(1), first.key.accountID)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, q), "one credit reserves only one lease")
	first.finish(false, nil)
	second := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, q)
	require.NotNil(t, second, "unsent lease refunds its credit")
	require.Equal(t, int64(2), second.key.accountID, "insufficient candidates participate in fair rotation")
	ctx := context.WithValue(context.Background(), protectionSelectionKey{}, &protectionSelection{trial: second})
	require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, &accounts[1], "gpt-test"))
	require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, &accounts[1], "gpt-test"), "repeat send boundary consumes once")
	second.finish(true, nil)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, q), "send consumes the only credit")
	// The sent trial was the first allocation of the next twenty opportunities.
	for i := 0; i < openAITTFTGuardProbeEvery-1; i++ {
		g.recordAllocationOpportunity(freshTTFTOpportunityContext(), 100, "gpt-test", cfg)
	}
	third := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, q)
	require.NotNil(t, third)
	require.Equal(t, int64(1), third.key.accountID)
	third.finish(false, nil)
}

func TestTTFTTrialIdentityLeaseSurvivesPolicyProjectionRemoval(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	other := enabledOpenAITTFTGuardConfig(30*time.Second, 5)
	key := int64(10)
	accounts := []Account{{ID: 1, UpstreamKeyID: &key}}
	fundTTFTTrial(g, 100, cfg)
	first := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, nil)
	require.NotNil(t, first)
	fundTTFTTrial(g, 200, other)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", other, nil), "same raw identity cannot be probed in a second policy scope")
	g.mu.Lock()
	delete(g.groupProbes, first.probeKey)
	g.mu.Unlock()
	require.False(t, first.markSent(), "removed scope cannot send an old ticket")
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", other, nil))
	first.finish(false, nil)
	second := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", other, nil)
	require.NotNil(t, second)
	second.finish(false, nil)
}

func TestTTFTTrialSentOpportunitySharesLogicalReselectionBudget(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	key := int64(10)
	account := Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}
	fundTTFTTrial(g, 100, cfg)
	ctx := freshTTFTOpportunityContext()
	ticket := g.reserveTrialOpportunity(ctx, 100, []Account{account}, "gpt-test", cfg, nil)
	require.NotNil(t, ticket)
	send := context.WithValue(ctx, protectionSelectionKey{}, &protectionSelection{trial: ticket})
	require.NoError(t, MarkSchedulingProtectionTrialSent(send, &account, "gpt-test"))
	require.NoError(t, MarkSchedulingProtectionTrialSent(send, &account, "gpt-test"))
	g.recordAllocationOpportunity(ctx, 100, "gpt-test", cfg)
	require.Equal(t, uint8(1), g.budgetStateLocked(100, "gpt-test").opportunities, "a sent trial and later reselection share one logical opportunity")
	require.Equal(t, uint64(21), g.budgetStateLocked(100, "gpt-test").sequence)
	ticket.finish(false, nil)
}

func TestTTFTTrialOrdinaryHTTPWithoutObservationHolderFundsAtSendOnce(t *testing.T) {
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	key := int64(10)
	account := &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}
	ctx := context.WithValue(freshTTFTOpportunityContext(), protectionSelectionKey{}, &protectionSelection{
		service: svc, groupID: 100, newAllocation: true,
		allocationAccountID: account.ID, allocationUpstreamKeyID: key,
		allocationModel: "gpt-test", allocationConfig: cfg,
	})
	require.Nil(t, ctx.Value(cacheUsageAttemptContextKey{}))
	require.Nil(t, ctx.Value(openAITTFTAttemptKey{}))
	g := svc.getOpenAITTFTGuard()
	require.Empty(t, g.groupProbes, "selection does not fund the trial budget")
	for i := 0; i < 2; i++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))
		require.NoError(t, err)
		_, err = svc.doOpenAIUpstream(request, "", account)
		require.NoError(t, err)
	}
	require.Len(t, upstream.requests, 2)
	require.Equal(t, uint64(1), g.budgetStateLocked(100, "gpt-test").sequence, "actual HTTP sends share the logical request budget without requiring observation holders")
}

func TestTTFTTrialWrongActualIdentityCannotConsumeCredit(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	key := int64(10)
	a := Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}
	fundTTFTTrial(g, 100, cfg)
	ticket := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, []Account{a}, "gpt-test", cfg, nil)
	require.NotNil(t, ticket)
	ctx := context.WithValue(context.Background(), protectionSelectionKey{}, &protectionSelection{trial: ticket})
	require.Error(t, MarkSchedulingProtectionTrialSent(ctx, &a, "other-model"))
	require.False(t, ticket.sent.Load())
	ticket.finish(false, nil)
	require.NotNil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, []Account{a}, "gpt-test", cfg, nil))
}

func TestTTFTQualityPrecedesCacheInBothModes(t *testing.T) {
	accounts := []Account{{ID: 1, Priority: 1}, {ID: 2, Priority: 1}, {ID: 3, Priority: 1}, {ID: 4, Priority: 2}}
	q := map[int64]forkscheduling.TTFTQuality{1: {State: "protected", Degraded: true, SampleCount: 5}, 2: {State: "insufficient", SampleCount: 10}, 3: {State: "normal", SampleCount: 5}, 4: {State: "normal", SampleCount: 5}}
	for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
		require.Equal(t, [][]int64{{3}, {2}, {1}, {4}}, jointTTFTOrder([][]int64{{1}, {2}, {3}, {4}}, accounts, nil, q, 5, priority))
	}
}

func TestTTFTRecoveryNormalWithThreeSamplesIgnoresInitializationMin20(t *testing.T) {
	q := map[int64]forkscheduling.TTFTQuality{1: {State: "normal", SampleCount: 3, EWMAms: 1000}, 2: {State: "insufficient", SampleCount: 20}}
	require.Equal(t, 0, jointTTFTClass(1, q, 20), "core's terminal recovery decision is authoritative over cold-start sample minimum")
	require.Equal(t, 1, jointTTFTClass(2, q, 20))
	q[1] = forkscheduling.TTFTQuality{State: "normal", Degraded: true, SampleCount: 3}
	require.Equal(t, 2, jointTTFTClass(1, q, 20), "a contradictory degraded flag remains protected")
}

func TestTTFTTrialRealSelectionCountsOnlyNewAcquiredAllocations(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint(advanced), func(t *testing.T) {
			svc, base, _, cache := cacheGuardSelectionFixture(t, advanced, []float64{95, 70}, nil, false, time.Time{}, nil)
			cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: SchedulingPriorityCacheFirst, cfg: cfg})
			group := int64(100)
			g := svc.getOpenAITTFTGuard()
			accounts, err := svc.accountRepo.ListSchedulableByGroupID(base, group)
			require.NoError(t, err)
			for i := 0; i < 5; i++ {
				ms := 1000
				svc.ReportOpenAIAccountScheduleResultForGroup(&group, &accounts[0], "gpt-test", true, &ms)
			}
			ms := 60000
			svc.ReportOpenAIAccountScheduleResultForGroup(&group, &accounts[1], "gpt-test", true, &ms)
			for i := 0; i < 20; i++ {
				cancelCtx, cancel := context.WithCancel(freshTTFTOpportunityContext())
				result, _, err := svc.SelectAccountWithScheduler(cancelCtx, &group, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.True(t, result.Acquired)
				cancel()
				require.ErrorIs(t, MarkSchedulingProtectionTrialSent(ContextWithSelectionProfitGate(cancelCtx, result), result.Account, "gpt-test"), context.Canceled)
				result.ReleaseFunc()
			}
			budget := g.budgetStateLocked(group, "gpt-test")
			require.Equal(t, uint64(0), budget.sequence, "twenty cancelled allocations cannot fund a trial")
			require.False(t, budget.credit)
			ctx := freshTTFTOpportunityContext()
			for i := 0; i < 3; i++ {
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.True(t, result.Acquired)
				require.Nil(t, result.protection.trial)
				send := ContextWithSelectionProfitGate(ctx, result)
				require.NoError(t, MarkSchedulingProtectionTrialSent(send, result.Account, "gpt-test"))
				require.NoError(t, MarkSchedulingProtectionTrialSent(send, result.Account, "gpt-test"))
				result.ReleaseFunc()
			}
			scope, _ := openAITTFTGuardScopeFor(group, cfg)
			pk := openAITTFTGuardProbeKey{scopeID: scope, model: "gpt-test"}
			require.Equal(t, uint64(1), g.groupProbes[pk].sequence, "repeat selection of one logical request counts once")
			cache.sessionBindings[svc.openAISessionCacheKey("sticky")] = 1
			for i := 0; i < 30; i++ {
				stickyCtx := freshTTFTOpportunityContext()
				result, _, err := svc.SelectAccountWithScheduler(stickyCtx, &group, "", "sticky", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Nil(t, result.protection.trial)
				require.NoError(t, MarkSchedulingProtectionTrialSent(ContextWithSelectionProfitGate(stickyCtx, result), result.Account, "gpt-test"))
				result.ReleaseFunc()
			}
			require.Equal(t, uint64(1), g.groupProbes[pk].sequence, "sticky traffic never funds or reserves trial credit")
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: false}})
			result, _, err := svc.SelectAccountWithScheduler(freshTTFTOpportunityContext(), &group, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.False(t, result.Acquired)
			require.Equal(t, uint64(1), g.groupProbes[pk].sequence, "wait plans without capacity are not new allocations")
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{})
			for i := 0; i < 19; i++ {
				sendCtx := freshTTFTOpportunityContext()
				result, _, err := svc.SelectAccountWithScheduler(sendCtx, &group, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.True(t, result.Acquired)
				require.Equal(t, int64(1), result.Account.ID)
				require.NoError(t, MarkSchedulingProtectionTrialSent(ContextWithSelectionProfitGate(sendCtx, result), result.Account, "gpt-test"))
				result.ReleaseFunc()
			}
			require.Equal(t, uint64(20), g.groupProbes[pk].sequence)
			result, _, err = svc.SelectAccountWithScheduler(freshTTFTOpportunityContext(), &group, "", "", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, result.protection.trial)
			require.Equal(t, int64(2), result.Account.ID)
			ticket := result.protection.trial
			require.False(t, ticket.sent.Load(), "selection alone never consumes the funded credit")
			result.ReleaseFunc()
			require.Empty(t, g.trialLeases)
			require.True(t, g.budgetStateLocked(group, "gpt-test").credit, "not-sent cancellation returns the credit")
		})
	}
}

func TestTTFTTrialBudgetsAreGroupLocalDespiteSharedQualityScope(t *testing.T) {
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	key := int64(10)
	accounts := []Account{{ID: 1, UpstreamKeyID: &key}}
	fundTTFTTrial(g, 100, cfg)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", cfg, nil), "one group cannot spend another group's credit")
	fundTTFTTrial(g, 200, cfg)
	first := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, accounts, "gpt-test", cfg, nil)
	require.NotNil(t, first)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", cfg, nil), "funded groups still share raw identity's single in-flight lease")
	first.finish(false, nil)
	second := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 200, accounts, "gpt-test", cfg, nil)
	require.NotNil(t, second)
	second.finish(false, nil)
}

func TestTTFTTrialCacheAdmissionRejectionCannotSpendCredit(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, false, time.Time{}, nil)
	group := int64(100)
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: SchedulingPriorityCacheFirst, cfg: cfg})
	g := svc.getOpenAITTFTGuard()
	fundTTFTTrial(g, group, cfg)
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection.protection.trial)
	ticket := selection.protection.trial
	store := &budgetContractStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore)}
	runner.store, runner.engine = store, upstreamcacheguard.NewEngine(store)
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, runner.engine.ApplySettings(settings))
	require.NoError(t, runner.engine.Refresh(ctx))
	store.slow = true
	attempt := WithUpstreamCacheGuardAttempt(ContextWithSelectionProfitGate(ctx, selection), 1, &group, "conversation", true)
	PrepareOpenAIUpstreamCacheGuardSend(attempt, selection.Account, "gpt-test", "openai")
	upstream := &httpUpstreamRecorder{}
	svc.httpUpstream = upstream
	req, err := http.NewRequestWithContext(attempt, http.MethodPost, "https://fixture.invalid/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))
	require.NoError(t, err)
	_, err = svc.doOpenAIUpstream(req, "", selection.Account)
	require.Error(t, err)
	require.Empty(t, upstream.requests)
	require.False(t, ticket.sent.Load(), "TTFT trial never bypasses cache admission to spend its credit")
	FinishOpenAIUpstreamCacheGuardAttempt(attempt, nil, err)
	selection.ReleaseFunc()
	require.Empty(t, g.trialLeases)
	require.True(t, g.budgetStateLocked(group, "gpt-test").credit)
}
