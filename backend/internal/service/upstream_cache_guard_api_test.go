package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type cacheGuardAccountViewStore struct {
	upstreamcacheguard.Store
	upstreamcacheguard.AccountStore
	window upstreamcacheguard.AccountWindow
}

func (s *cacheGuardAccountViewStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return nil, nil
}

func (s *cacheGuardAccountViewStore) AccountRows(context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	return nil, nil
}

func (s *cacheGuardAccountViewStore) AccountWindows(context.Context, time.Time, time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	return map[string]upstreamcacheguard.AccountWindow{"7:11": s.window}, nil
}

func TestUpstreamCacheGuardAccountSummaryRespectsPlatformScope(t *testing.T) {
	for _, tc := range []struct {
		name          string
		platform      string
		otherPlatform string
		groupIDs      []int64
		phase         upstreamcacheguard.Phase
		want          string
	}{
		{"unselected_anthropic_protected", PlatformOpenAI, PlatformAnthropic, []int64{1, 2}, upstreamcacheguard.Cooling, "normal"},
		{"unselected_anthropic_recovering", PlatformOpenAI, PlatformAnthropic, []int64{1, 2}, upstreamcacheguard.Trial, "normal"},
		{"unselected_composite_protected", PlatformOpenAI, PlatformComposite, []int64{1, 2}, upstreamcacheguard.Cooling, "normal"},
		{"unselected_composite_recovering", PlatformOpenAI, PlatformComposite, []int64{1, 2}, upstreamcacheguard.Trial, "normal"},
		{"no_selected_group", PlatformOpenAI, PlatformAnthropic, []int64{2}, upstreamcacheguard.Cooling, "platform_not_selected"},
		{"no_group", PlatformOpenAI, PlatformAnthropic, nil, upstreamcacheguard.Cooling, "platform_not_selected"},
		{"unselected_account", PlatformAnthropic, PlatformAnthropic, []int64{1, 2}, upstreamcacheguard.Cooling, "platform_not_selected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := DefaultUpstreamCacheGuardSettings()
			settings.Mode = upstreamcacheguard.Protect
			settings.ThresholdPercent, settings.RecoveryThresholdPercent = 60, 65
			strict := settings
			strict.ThresholdPercent, strict.RecoveryThresholdPercent = 85, 90
			strict.GroupIDs = []int64{2}
			now := time.Now().UTC()
			healthy := upstreamcacheguard.AccountState{AccountID: 7, UpstreamKeyID: 11,
				Phase: upstreamcacheguard.Healthy, ValidRequests: 43, CachePercent: 75, UpdatedAt: now}
			protected := healthy
			protected.Phase, protected.CooldownUntil = tc.phase, now.Add(time.Hour)
			if tc.phase == upstreamcacheguard.Trial {
				protected.FirstTrialAt = now.Add(-time.Minute)
				protected.TrialValidRequests, protected.TrialSessions, protected.TrialCachePercent = 22, 3, 84
			}
			shared := healthy
			shared.GroupIDs = tc.groupIDs
			shared.Policies = map[string]upstreamcacheguard.PolicyProjection{
				upstreamcacheguard.PolicySignature(settings): {Settings: settings, State: healthy},
				upstreamcacheguard.PolicySignature(strict):   {Settings: strict, State: protected},
			}
			store := &cacheGuardAccountViewStore{window: upstreamcacheguard.AccountWindow{
				AccountID: 7, UpstreamKeyID: 11, Version: 1, State: shared,
				ValidRequests: 43, TotalInputTokens: 43000, CacheReadTokens: 32250,
			}}
			runner := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store}
			require.NoError(t, runner.engine.ApplySettings(settings))
			require.NoError(t, runner.engine.ApplyPolicySettings(map[int64]upstreamcacheguard.Settings{1: settings, 2: strict}))
			reader := newProtectionPlatformScopeResolver()
			reader.groups = map[int64]string{1: PlatformOpenAI, 2: tc.otherPlatform}
			runner.SetProtectionPolicyResolver(reader)
			require.NoError(t, runner.engine.Refresh(context.Background()))
			runner.available.Store(true)
			previous := installedCacheGuardRunner.Swap(runner)
			t.Cleanup(func() { installedCacheGuardRunner.Store(previous) })
			configID, keyID := int64(1), int64(11)
			account := Account{ID: 7, Platform: tc.platform, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: tc.groupIDs}
			// Confirm the raw policy projection contains the stronger excluded
			// strategy before the public API applies the platform scope.
			raw := upstreamCacheGuardPolicyAccountViewsBatch([]Account{account}, settings, true,
				runner.engine.CachedSharedAccount, runner.engine.ResolvedSettings, nil)[account.ID]
			if len(tc.groupIDs) > 0 && tc.groupIDs[len(tc.groupIDs)-1] == 2 {
				wantRaw := "protected"
				if tc.phase == upstreamcacheguard.Trial {
					wantRaw = "recovering"
				}
				require.Equal(t, wantRaw, raw.Summary.State)
			}
			view := UpstreamCacheGuardAccountViewsBatch([]Account{account})[account.ID]
			require.NotNil(t, view.Summary)
			require.Equal(t, tc.want, view.Summary.State)
			wantReason := "healthy"
			if tc.want == "platform_not_selected" {
				wantReason = tc.want
			}
			require.Equal(t, wantReason, view.Summary.Reason)
			require.Equal(t, 43, view.Summary.ValidRequests, "shared observations remain visible")
			require.Equal(t, 75.0, *view.Summary.RatePercent)
			require.Zero(t, view.Summary.RecoveryValidRequests)
			require.Zero(t, view.Summary.RecoverySessions)
			require.Nil(t, view.Summary.RecoveryRatePercent)
			require.Nil(t, view.Summary.RecoveryStartedAt)
			require.Nil(t, view.Summary.CooldownUntil)
			for _, state := range view.States {
				wantState := "platform_not_selected"
				if tc.platform == PlatformOpenAI && state.GroupID == 1 {
					wantState = "normal"
				}
				require.Equal(t, wantState, state.State)
			}
		})
	}
}

func TestUpstreamCacheGuardAccountSummaryAndGroupProjection(t *testing.T) {
	configID, keyID := int64(1), int64(11)
	account := Account{ID: 7, UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
		GroupIDs: []int64{2, 1}, Groups: []*Group{{ID: 1, Name: "Executed"}, {ID: 2, Name: "Samples only"}}}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode, settings.GroupIDs = upstreamcacheguard.Protect, []int64{1}
	now := time.Now().UTC()
	snapshot := upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
		AccountID: 7, UpstreamKeyID: 11, Phase: upstreamcacheguard.Trial, ValidRequests: 43,
		CachePercent: 74.4, TrialValidRequests: 22, TrialCachePercent: 84.6,
		TrialSessions: 3, FirstTrialAt: now.Add(-time.Minute), CooldownUntil: now.Add(time.Hour),
		GroupIDs: []int64{1, 2, 3}, TrialGroupIDs: []int64{1}, UpdatedAt: now,
	}}
	calls := 0
	read := func(accountID, currentKey int64) (upstreamcacheguard.AccountSnapshot, bool) {
		calls++
		require.Equal(t, int64(7), accountID)
		require.Equal(t, int64(11), currentKey)
		return snapshot, true
	}
	view := upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true, read)[account.ID]
	require.Equal(t, 1, calls)
	require.Equal(t, "recovering", view.Summary.State)
	require.Equal(t, 74.4, *view.Summary.RatePercent)
	require.Equal(t, 22, view.Summary.RecoveryValidRequests)
	require.Equal(t, 84.6, *view.Summary.RecoveryRatePercent)
	require.Equal(t, settings.WindowMinutes, view.Summary.WindowMinutes)
	require.Len(t, view.States, 3)
	require.Equal(t, "recovering", view.States[0].State)
	require.Equal(t, *view.Summary.RatePercent, *view.States[0].RatePercent)
	require.True(t, view.States[0].TrialParticipant)
	require.Equal(t, "disabled", view.States[1].State)
	require.Nil(t, view.States[1].RatePercent)
	require.Equal(t, "disabled", view.States[2].State, "past fact groups can contribute without execution")
	require.Nil(t, view.States[0].LinkedProtection)
}

func TestUpstreamCacheGuardAccountSummaryUnknownZeroAndUnavailable(t *testing.T) {
	configID, keyID := int64(1), int64(11)
	account := Account{ID: 7, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{1}}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	read := func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) {
		return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{Phase: upstreamcacheguard.Healthy}}, true
	}
	view := upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true, read)[account.ID]
	require.Equal(t, "insufficient", view.Summary.State)
	require.Nil(t, view.Summary.RatePercent)
	require.Equal(t, "insufficient", view.States[0].State)
	read = func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) {
		return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
			Phase: upstreamcacheguard.Cooling, ValidRequests: settings.MinValidRequests + 1, CachePercent: 0}}, true
	}
	view = upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true, read)[account.ID]
	require.Equal(t, "protected", view.Summary.State)
	require.NotNil(t, view.Summary.RatePercent)
	require.Zero(t, *view.Summary.RatePercent)
	view = upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, false, read)[account.ID]
	require.Equal(t, "unavailable", view.Summary.State)
	require.Equal(t, "snapshot_unavailable", view.Summary.Reason)
	require.Nil(t, view.Summary.RatePercent)
	settings.Mode = upstreamcacheguard.Disabled
	require.Empty(t, upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true, read))
}

func TestUpstreamCacheGuardPastFactGroupDoesNotClaimCurrentExecution(t *testing.T) {
	configID, keyID := int64(1), int64(11)
	account := Account{ID: 7, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{1}}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	view := upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true,
		func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) {
			return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
				Phase: upstreamcacheguard.Healthy, ValidRequests: 21, CachePercent: 91, GroupIDs: []int64{1, 9}}}, true
		})[account.ID]
	require.Len(t, view.States, 2)
	require.Equal(t, "normal", view.States[0].State)
	require.Equal(t, "disabled", view.States[1].State, "past fact groups cannot execute after membership is removed")
}
