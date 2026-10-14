package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

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
