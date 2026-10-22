package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardTTFTOnlyDoesNotRequireCacheSnapshot(t *testing.T) {
	for _, mode := range []string{"all_disabled", "observe", "group_disabled_other_active"} {
		for _, ticketState := range []string{"valid", "wrong_model", "invalidated"} {
			t.Run(mode+"/"+ticketState, func(t *testing.T) {
				runner, store, account, group := cacheGuardCaptureSendFixture(t)
				settings := runner.engine.Settings()
				settings.Mode = upstreamcacheguard.Disabled
				if mode == "observe" {
					settings.Mode = upstreamcacheguard.Observe
				}
				require.NoError(t, runner.engine.ApplySettings(settings))
				if mode == "group_disabled_other_active" {
					other := settings
					other.Mode = upstreamcacheguard.Protect
					require.NoError(t, runner.engine.ApplyPolicySettings(map[int64]upstreamcacheguard.Settings{group + 1: other}))
				}
				// Deliberately leave cache snapshots uninitialized. TTFT has its own
				// valid ticket and must not depend on cache publication readiness.
				ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
				guard := newOpenAITTFTGuard()
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				fundTTFTTrial(guard, group, cfg)
				ticket := guard.reserveTrialOpportunity(ctx, group, []Account{*account}, "gpt-test", cfg, nil)
				require.NotNil(t, ticket)
				t.Cleanup(func() { ticket.finish(false, nil) })
				selection := &AccountSelectionResult{Account: account, cacheGuardProtectedSelection: true,
					protection: &protectionSelection{groupID: group, trial: ticket, allocationModel: "gpt-test", newAllocation: true}}
				ctx = ContextWithSelectionProfitGate(ctx, selection)
				ctx = WithUpstreamCacheGuardAttempt(ctx, 7, &group, "ttft-only", true)
				PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
				actualModel := "gpt-test"
				if ticketState == "wrong_model" {
					actualModel = "wrong-model"
				} else if ticketState == "invalidated" {
					ticket.finish(false, nil)
				}
				err := runSchedulingProtectionBeforeSend(ctx, account, actualModel)
				identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
				require.False(t, identity.budget.selectionFallback.Load(), "cache unavailability must not disable TTFT-only scheduling")
				require.False(t, identity.budget.ordinary.Load())
				if ticketState == "valid" {
					require.NoError(t, err)
					require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, account, actualModel))
					require.True(t, ticket.sent.Load())
				} else {
					var rejected *UpstreamFailoverError
					require.ErrorAs(t, err, &rejected)
					require.Equal(t, GatewayFailureReason("scheduling_protection_scope_changed"), rejected.Reason)
					require.False(t, rejected.ProtectionOrdinaryFallback)
					require.False(t, ticket.sent.Load(), "cache mode must not weaken real TTFT ticket checks")
				}
				holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
				if mode == "all_disabled" {
					require.Nil(t, holder.admission)
					require.True(t, identity.budget.deadline.IsZero(), "disabled cache policies must not open a database stage")
					store.mu.Lock()
					require.Empty(t, store.rows)
					store.mu.Unlock()
				} else {
					require.NotNil(t, holder.admission)
					require.True(t, holder.admission.Admission.CaptureOnly)
					require.False(t, holder.admission.Admission.Trial)
				}
			})
		}
	}
}

func TestUpstreamCacheGuardDisabledDoesNotRejectTTFTWithOldCacheBudgetFallback(t *testing.T) {
	runner, _, account, group := cacheGuardCaptureSendFixture(t)
	settings := runner.engine.Settings()
	settings.Mode = upstreamcacheguard.Disabled
	require.NoError(t, runner.engine.ApplySettings(settings))
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
	identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	identity.budget.ordinary.Store(true)
	ctx = ContextWithSelectionProfitGate(ctx, &AccountSelectionResult{Account: account, cacheGuardProtectedSelection: true})
	ctx = WithUpstreamCacheGuardAttempt(ctx, 7, &group, "disabled-after-budget-fallback", true)
	admission, err := beginUpstreamCacheGuardRequest(ctx, 7, &group, "disabled-after-budget-fallback", "gpt-test", "openai", account)
	require.NoError(t, err)
	require.Nil(t, admission)
	require.True(t, identity.budget.deadline.IsZero())
}
