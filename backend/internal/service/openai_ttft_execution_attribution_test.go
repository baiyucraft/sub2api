package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func ttftExecutionAttribution(t *testing.T, svc *OpenAIGatewayService, account *Account, group int64) TTFTProtectionExecutionView {
	t.Helper()
	views := svc.ttftExecutionViews(account.ID, accountUpstreamKeyID(account), group, "gpt-test", time.Now())
	require.Len(t, views, 1)
	return views[0]
}

// Use the real send/terminal lifecycle; only its physical event timestamps are
// controlled so each outcome is independent of scheduler/test-machine latency.
func ttftExecutionAttributionSend(ctx context.Context, account *Account, ttft time.Duration) {
	markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
	a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	a.mu.Lock()
	a.sentAt = time.Now().Add(-ttft - time.Second)
	if ttft > 0 {
		a.firstTokenAt = a.sentAt.Add(ttft)
	}
	a.mu.Unlock()
}

func TestTTFTExecutionAttributionTerminalOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protected bool
		cancel    bool
		ttft      time.Duration
		err       error
		counted   bool
		reason    string
	}{
		{name: "ordinary_fast", protected: true, ttft: time.Second, counted: true, reason: "counted"},
		{name: "failure", protected: true, ttft: time.Second, err: errors.New("transport failed"), reason: "failure"},
		{name: "canceled", protected: true, ttft: time.Second, cancel: true, reason: "canceled"},
		{name: "no_ttft", protected: true, reason: "no_ttft"},
		{name: "initial_sample", ttft: time.Second, reason: "not_recovering"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, account, group := ttftAttemptService()
			if tc.protected {
				ttftSeedProtection(t, svc, account, group)
			}
			ctx, cancel := context.WithCancel(ttftAttemptContext(svc, group, "explicit:outcome"))
			defer cancel()
			ttftExecutionAttributionSend(ctx, account, tc.ttft)
			if tc.cancel {
				cancel()
			}
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{}, tc.err)
			view := ttftExecutionAttribution(t, svc, account, group)
			require.NotNil(t, view.RecoveryCounted, "terminal false must be present, rather than omitted")
			require.Equal(t, tc.counted, *view.RecoveryCounted)
			require.Equal(t, tc.reason, view.RecoveryReason)
			require.True(t, view.Completed)
			require.EqualValues(t, 1, view.CompletedCount)
			// Duplicate terminal callbacks must preserve attribution and counters.
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, errors.New("late duplicate"))
			require.Equal(t, view, ttftExecutionAttribution(t, svc, account, group))
		})
	}
}

func TestTTFTExecutionAttributionNewSendClearsPriorTerminalDecision(t *testing.T) {
	svc, _, account, group := ttftAttemptService()
	ttftSeedProtection(t, svc, account, group)
	first := ttftAttemptContext(svc, group, "explicit:first")
	ttftFinishFast(first, account)
	require.NotNil(t, ttftExecutionAttribution(t, svc, account, group).RecoveryCounted)

	second := ttftAttemptContext(svc, group, "explicit:second")
	ttftExecutionAttributionSend(second, account, time.Second)
	view := ttftExecutionAttribution(t, svc, account, group)
	require.True(t, view.SendAttempted)
	require.False(t, view.Completed)
	require.Nil(t, view.RecoveryCounted)
	require.Empty(t, view.RecoveryReason)
	require.EqualValues(t, 2, view.SentCount)
	require.EqualValues(t, 1, view.CompletedCount)
}

func TestTTFTExecutionAttributionLogicalRetryCannotClaimSecondCredit(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	ttftSeedProtection(t, svc, account, group)
	base := WithUpstreamCacheGuardLogicalSession(WithUpstreamCacheGuardScheduling(context.Background(), 8), 8, "explicit:retry")
	first := WithOpenAITTFTGuardAttempt(base, svc, 8, &group)
	ttftFinishFast(first, account)
	require.True(t, *ttftExecutionAttribution(t, svc, account, group).RecoveryCounted)
	second := WithOpenAITTFTGuardAttempt(base, svc, 8, &group)
	ttftFinishFast(second, account)
	view := ttftExecutionAttribution(t, svc, account, group)
	require.NotNil(t, view.RecoveryCounted)
	require.False(t, *view.RecoveryCounted)
	require.Equal(t, "duplicate_logical_request", view.RecoveryReason)
	require.Equal(t, 1, svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)[0].RecoverySamples)
}

func TestTTFTExecutionAttributionMissingSessionCountsRequestWithoutSessionCredit(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	ttftSeedProtection(t, svc, account, group)
	for i := 0; i < 3; i++ {
		ctx := ttftAttemptContext(svc, group, "")
		ttftFinishFast(ctx, account)
		view := ttftExecutionAttribution(t, svc, account, group)
		require.NotNil(t, view.RecoveryCounted)
		require.True(t, *view.RecoveryCounted)
		require.Equal(t, "counted", view.RecoveryReason)
	}
	view := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
	require.Len(t, view, 1)
	require.Equal(t, 3, view[0].RecoverySamples)
	require.Equal(t, 0, view[0].RecoverySessionCount)
	require.Equal(t, "recovery_sessions_insufficient", view[0].RecoveryReason)
	require.NotEqual(t, "normal", view[0].State)
}

func TestTTFTExecutionAttributionUsesExecutionGroupThreshold(t *testing.T) {
	for _, source := range []int64{100, 200} {
		t.Run(map[int64]string{100: "75s_group", 200: "35s_group"}[source], func(t *testing.T) {
			svc, _, account, _ := ttftAttemptService()
			policies := map[int64]GroupTTFTGuardResolvedPolicy{
				100: {GroupID: 100, Enabled: true, Threshold: 75 * time.Second, MinSamples: 20, Source: GroupTTFTGuardSourceGroup},
				200: {GroupID: 200, Enabled: true, Threshold: 35 * time.Second, MinSamples: 20, Source: GroupTTFTGuardSourceGroup},
			}
			svc.SetGroupTTFTGuardPolicyResolver(&openAITTFTGuardPolicyResolverStub{policies: policies})
			// Register both policies before the shared bad fact, so evaluating a
			// terminal event updates both existing projections in either order.
			for _, group := range []int64{100, 200} {
				svc.OpenAITTFTGuardObservationIdentity(group)
			}
			ttftSeedProtection(t, svc, account, 100)
			ctx := ttftAttemptContext(svc, source, "explicit:threshold")
			ttftExecutionAttributionSend(ctx, account, 30*time.Second)
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{}, nil)
			view := ttftExecutionAttribution(t, svc, account, source)
			require.NotNil(t, view.RecoveryCounted)
			require.Equal(t, source == 100, *view.RecoveryCounted, "30s qualifies only for the 45s recovery line")
			if source == 100 {
				require.Equal(t, "counted", view.RecoveryReason)
			} else {
				require.Equal(t, "slow_ttft", view.RecoveryReason)
			}
			for _, group := range []int64{100, 200} {
				cfg := enabledOpenAITTFTGuardConfig(policies[group].Threshold, 20)
				cfg.Source = GroupTTFTGuardSourceGroup
				projection := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
				require.Len(t, projection, 1)
				expected := 0
				if group == 100 {
					expected = 1
				}
				require.Equal(t, expected, projection[0].RecoverySamples, "the same physical fact must be projected independently")
			}
		})
	}
}
