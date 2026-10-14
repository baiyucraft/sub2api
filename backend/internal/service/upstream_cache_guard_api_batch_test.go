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
