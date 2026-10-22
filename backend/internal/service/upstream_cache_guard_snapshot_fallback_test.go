package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardSnapshotFallbackPreservesCaptureBudget(t *testing.T) {
	for _, decision := range []string{"migration", "new_allocation"} {
		t.Run(decision, func(t *testing.T) {
			runner, store, account, group := cacheGuardCaptureSendFixture(t)
			ctx := WithUpstreamCacheGuardScheduling(context.Background(), 7)
			identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
			selection := &AccountSelectionResult{Account: account, cacheGuardProtectedSelection: true}
			if decision == "migration" {
				selection.cacheGuardMigrationAccountID = account.ID
			} else {
				selection.protection = &protectionSelection{newAllocation: true}
			}
			selected := ContextWithSelectionProfitGate(ctx, selection)
			selected = WithUpstreamCacheGuardAttempt(selected, 7, &group, "snapshot-fallback", true)
			commits := 0
			selected = WithSchedulingProtectionBeforeSend(selected, account.ID, func(context.Context) error {
				commits++
				return nil
			})
			PrepareOpenAIUpstreamCacheGuardSend(selected, account, "gpt-test", "openai")
			var fallback *UpstreamFailoverError
			require.ErrorAs(t, runSchedulingProtectionBeforeSend(selected, account, "gpt-test"), &fallback)
			require.Equal(t, GatewayFailureReason("scheduling_protection_snapshot_unavailable"), fallback.Reason)
			require.True(t, fallback.ProtectionOrdinaryFallback)
			require.True(t, fallback.PluginAdmissionRejected)
			require.False(t, fallback.ShouldReportAccountScheduleFailure())
			require.Zero(t, commits)
			callback := selected.Value(schedulingProtectionBeforeSendKey{}).(*schedulingProtectionBeforeSend)
			require.False(t, callback.committed.Load())
			oldHolder := selected.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
			require.Nil(t, oldHolder.admission)
			require.True(t, protectionOrdinaryFallback(ctx))
			require.True(t, identity.budget.selectionFallback.Load())
			require.False(t, identity.budget.ordinary.Load(), "a snapshot rejection must preserve the remaining SQL budget")
			require.False(t, identity.budget.exhausted.Load())
			require.True(t, identity.budget.deadline.IsZero(), "reject before opening any admission transaction")
			store.mu.Lock()
			require.Empty(t, store.rows)
			require.Empty(t, store.events)
			store.mu.Unlock()

			ordinary := ContextWithSelectionProfitGate(selected, &AccountSelectionResult{Account: account})
			ordinary = WithSchedulingProtectionBeforeSend(ordinary, 0, nil)
			ordinary = WithUpstreamCacheGuardAttempt(ordinary, 7, &group, "snapshot-fallback", true)
			newIdentity := ordinary.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
			require.Equal(t, identity.RequestID, newIdentity.RequestID)
			require.Same(t, identity.budget, newIdentity.budget)
			holder := ordinary.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
			require.NotSame(t, oldHolder, holder, "ordinary reselection needs an independent send boundary")
			PrepareOpenAIUpstreamCacheGuardSend(ordinary, account, "gpt-test", "openai")
			require.NoError(t, runSchedulingProtectionBeforeSend(ordinary, account, "gpt-test"))
			require.NotNil(t, holder.admission)
			require.True(t, holder.admission.Admission.CaptureOnly)
			require.False(t, holder.admission.Admission.Trial)
			require.True(t, holder.admission.Admission.NaturalNew)
			finishCacheGuardCaptureSend(t, runner, ordinary, 900)
			require.Zero(t, commits, "ordinary reselection must not retain a rejected migration callback")

			store.mu.Lock()
			var session upstreamcacheguard.SessionState
			err := json.Unmarshal(store.rows[upstreamcacheguard.SessionKind+":"+holder.admission.Admission.SessionKey], &session)
			var quota upstreamcacheguard.GroupState
			quotaErr := json.Unmarshal(store.rows[upstreamcacheguard.GroupKind+":quota"], &quota)
			eventCount := len(store.events)
			store.mu.Unlock()
			require.NoError(t, err)
			require.NoError(t, quotaErr)
			require.Equal(t, 1, eventCount)
			require.Zero(t, session.LastAccountID)
			require.Empty(t, session.LastRouteKey)
			require.Empty(t, session.CohortID)
			require.Zero(t, session.TrialAccountID)
			require.True(t, session.ResidenceUntil.IsZero())
			require.Equal(t, int64(1), quota.NaturalNewCount)
			require.Zero(t, quota.TrialCount)
		})
	}
}

func TestUpstreamCacheGuardStaleSendOnlyKeepsExistingPreference(t *testing.T) {
	for _, decision := range []string{"keep", "migration", "new_allocation"} {
		t.Run(decision, func(t *testing.T) {
			svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, true, time.Time{}, nil)
			group := int64(100)
			account, err := svc.accountRepo.GetByID(ctx, 1)
			require.NoError(t, err)
			store := runner.store.(*cacheGuardSelectionStore)
			store.failAccounts = map[int64]bool{account.ID: true}
			require.Error(t, runner.engine.Refresh(ctx))
			store.failAccounts = nil
			view, usable := runner.engine.CachedAccountForGroup(group, account.ID, *account.UpstreamKeyID)
			require.True(t, usable)
			require.False(t, view.Fresh)
			selection := &AccountSelectionResult{Account: account, cacheGuardProtectedSelection: true}
			if decision == "migration" {
				selection.cacheGuardMigrationAccountID = account.ID
			} else if decision == "new_allocation" {
				selection.protection = &protectionSelection{newAllocation: true}
			}
			ctx = ContextWithSelectionProfitGate(ctx, selection)
			ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "conversation", true)
			commits := 0
			ctx = WithSchedulingProtectionBeforeSend(ctx, account.ID, func(context.Context) error {
				commits++
				return nil
			})
			PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
			err = runSchedulingProtectionBeforeSend(ctx, account, "gpt-test")
			holder := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
			if decision == "keep" {
				require.NoError(t, err)
				require.Equal(t, 1, commits)
				require.NotNil(t, holder.admission)
				require.False(t, holder.admission.Admission.CaptureOnly)
				require.False(t, holder.admission.Admission.NaturalNew)
				require.False(t, holder.admission.Admission.Trial)
			} else {
				var fallback *UpstreamFailoverError
				require.ErrorAs(t, err, &fallback)
				require.True(t, fallback.ProtectionOrdinaryFallback)
				require.Equal(t, GatewayFailureReason("scheduling_protection_snapshot_unavailable"), fallback.Reason)
				require.Zero(t, commits)
				require.Nil(t, holder.admission)
			}
			require.Zero(t, store.trialPlans, "stale send decisions must not allocate a trial plan")
		})
	}
}
