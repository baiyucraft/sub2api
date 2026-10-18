package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestUpstreamCacheGuardAccountViewsBatchIsolatedPerAccountAndKey(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	settings.GroupIDs = []int64{100}
	configID, keyA, keyB := int64(9), int64(11), int64(22)
	accounts := []Account{
		{ID: 1, UpstreamConfigID: &configID, UpstreamKeyID: &keyA, GroupIDs: []int64{100, 200}},
		{ID: 2, UpstreamConfigID: &configID, UpstreamKeyID: &keyB, GroupIDs: []int64{100}},
		{ID: 3, GroupIDs: []int64{100}},
	}
	keys := map[int64]int64{}
	read := func(id, key int64) (upstreamcacheguard.AccountSnapshot, bool) {
		keys[id] = key
		return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
			Phase: upstreamcacheguard.Healthy, ValidRequests: 21, CachePercent: float64(id) * 25,
		}}, true
	}
	views := upstreamCacheGuardAccountViewsBatch(accounts, settings, true, read)
	require.Equal(t, map[int64]int64{1: 11, 2: 22}, keys)
	require.NotContains(t, views, int64(3))
	require.Equal(t, 25.0, *views[1].Summary.RatePercent)
	require.Equal(t, 50.0, *views[2].Summary.RatePercent)
	require.Equal(t, "disabled", views[1].States[1].State)
	rotated := int64(99)
	accounts[0].UpstreamKeyID = &rotated
	views = upstreamCacheGuardAccountViewsBatch(accounts, settings, true,
		func(id, key int64) (upstreamcacheguard.AccountSnapshot, bool) {
			if id == 1 {
				require.Equal(t, rotated, key)
				return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{Phase: upstreamcacheguard.Healthy}}, true
			}
			return read(id, key)
		})
	require.Equal(t, "insufficient", views[1].Summary.State)
	require.Nil(t, views[1].Summary.RatePercent)
	require.Equal(t, 50.0, *views[2].Summary.RatePercent)
}

func TestUpstreamCacheGuardAccountViewsBatchObserveAndUnavailable(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Observe
	configID, keyID := int64(9), int64(11)
	account := Account{ID: 1, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{100}}
	read := func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) {
		return upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
			Phase: upstreamcacheguard.Cooling, ValidRequests: 21, CachePercent: 60}}, true
	}
	view := upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true, read)[1]
	require.Equal(t, "observing", view.Summary.State)
	require.Equal(t, "observing", view.States[0].State)
	view = upstreamCacheGuardAccountViewsBatch([]Account{account}, settings, true,
		func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) {
			return upstreamcacheguard.AccountSnapshot{}, false
		})[1]
	require.Equal(t, "unavailable", view.Summary.State)
	require.Equal(t, "unavailable", view.States[0].State)
}

func TestUpstreamCacheGuardPolicyViewsResolvePerGroupWithGlobalDisabled(t *testing.T) {
	global := DefaultUpstreamCacheGuardSettings()
	custom := global
	custom.Mode, custom.ThresholdPercent, custom.RecoveryThresholdPercent, custom.MinValidRequests = upstreamcacheguard.Protect, 60, 85, 2
	custom.Priority = "ttft_first"
	configID, keyID := int64(1), int64(2)
	account := Account{ID: 3, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{10, 20}}
	snapshot := upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: keyID, Phase: upstreamcacheguard.Healthy, ValidRequests: 7, CachePercent: 50, Policies: map[string]upstreamcacheguard.PolicyProjection{
		upstreamcacheguard.PolicySignature(custom): {Settings: custom, State: upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: keyID, Phase: upstreamcacheguard.Cooling, ValidRequests: 7, CachePercent: 50}},
	}}}
	reads := 0
	views := upstreamCacheGuardPolicyAccountViewsBatch([]Account{account}, global, true, func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) { reads++; return snapshot, true },
		func(id int64) UpstreamCacheGuardSettings {
			if id == 20 {
				return custom
			}
			return global
		},
		func(id int64) (string, string) {
			if id == 20 {
				return "enabled", "group"
			}
			return "inherit", "global"
		})
	require.Equal(t, 1, reads)
	require.Equal(t, "protected", views[3].Summary.State)
	require.Equal(t, 7, views[3].Summary.ValidRequests)
	require.Equal(t, 50.0, *views[3].Summary.RatePercent)
	states := views[3].States
	require.Len(t, states, 2)
	require.Equal(t, "disabled", states[0].State)
	require.Equal(t, "protected", states[1].State)
	require.Equal(t, "group", states[1].PolicySource)
	require.Equal(t, 60.0, states[1].ThresholdPercent)
	require.Equal(t, 2, states[1].MinValidRequests)
	require.Equal(t, "ttft_first", states[1].SchedulingPriority)
}

func TestUpstreamCacheGuardPolicyViewsExpiredEvidenceClearsSharedRate(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	configID, keyID := int64(1), int64(2)
	account := Account{ID: 3, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, GroupIDs: []int64{10, 20}}
	snapshot := upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{
		AccountID: 3, UpstreamKeyID: keyID, Phase: upstreamcacheguard.Healthy, ValidRequests: 21, CachePercent: 50,
		Policies: map[string]upstreamcacheguard.PolicyProjection{
			upstreamcacheguard.PolicySignature(settings): {Settings: settings, State: upstreamcacheguard.AccountState{
				AccountID: 3, UpstreamKeyID: keyID, Phase: upstreamcacheguard.Unknown, ValidRequests: 21, CachePercent: 50,
			}},
		},
	}}
	views := upstreamCacheGuardPolicyAccountViewsBatch([]Account{account}, settings, true,
		func(_, _ int64) (upstreamcacheguard.AccountSnapshot, bool) { return snapshot, true },
		func(int64) UpstreamCacheGuardSettings { return settings }, nil)
	view := views[account.ID]
	require.Equal(t, "unavailable", view.Summary.State)
	require.Equal(t, "evidence_expired", view.Summary.Reason)
	require.Nil(t, view.Summary.RatePercent)
	require.Zero(t, view.Summary.ValidRequests)
	require.Len(t, view.States, 2)
	for _, state := range view.States {
		require.Equal(t, "unavailable", state.State)
		require.Equal(t, "evidence_expired", state.Reason)
		require.Nil(t, state.RatePercent)
	}
}
