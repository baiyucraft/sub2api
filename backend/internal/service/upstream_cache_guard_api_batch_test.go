package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func cacheGuardAPIBatchFixture(t *testing.T) ([]Account, *UpstreamCacheGuardRunner) {
	t.Helper()
	store := &cacheGuardSelectionStore{}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	r := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store)}
	require.NoError(t, r.engine.ApplySettings(settings))
	old := installedCacheGuardRunner.Swap(r)
	t.Cleanup(func() { installedCacheGuardRunner.Store(old) })
	r.available.Store(true)
	now := time.Now().UTC()
	for _, row := range []struct {
		group int64
		state upstreamcacheguard.AccountState
	}{
		{100, upstreamcacheguard.AccountState{AccountID: 1, UpstreamKeyID: 11, Phase: upstreamcacheguard.Cooling, ValidRequests: 21, CachePercent: 60, UpdatedAt: now, CooldownUntil: now.Add(time.Hour)}},
		{200, upstreamcacheguard.AccountState{AccountID: 1, UpstreamKeyID: 11, Phase: upstreamcacheguard.Healthy, ValidRequests: 42, CachePercent: 95, UpdatedAt: now}},
		{200, upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 22, Phase: upstreamcacheguard.Trial, ValidRequests: 30, CachePercent: 40, TrialValidRequests: 25, TrialSessions: 3, TrialCachePercent: 82, FirstTrialAt: now.Add(-time.Minute), UpdatedAt: now}},
		{300, upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: 33, Phase: upstreamcacheguard.Healthy, ValidRequests: 21, CachePercent: 0, UpdatedAt: now}},
	} {
		payload, err := json.Marshal(row.state)
		require.NoError(t, err)
		store.rows = append(store.rows, upstreamcacheguard.RuntimeRow{GroupID: row.group, Kind: upstreamcacheguard.AccountKind, Key: fmt.Sprint(row.state.AccountID), Version: 1, Payload: payload, UpdatedAt: now})
	}
	require.NoError(t, r.engine.Refresh(context.Background()))
	configID := int64(9)
	keys := []int64{11, 22, 33}
	accounts := []Account{
		{ID: 1, UpstreamConfigID: &configID, UpstreamKeyID: &keys[0], GroupIDs: []int64{200, 100}, Groups: []*Group{{ID: 100, Name: "Source"}, {ID: 200, Name: "Target"}}},
		{ID: 2, UpstreamConfigID: &configID, UpstreamKeyID: &keys[1], GroupIDs: []int64{200}},
		{ID: 3, UpstreamConfigID: &configID, UpstreamKeyID: &keys[2], GroupIDs: []int64{300}},
		{ID: 4, GroupIDs: []int64{100}},
	}
	return accounts, r
}

func TestUpstreamCacheGuardAccountStatesBatchPreservesNativeAndLinkedEvidence(t *testing.T) {
	accounts, r := cacheGuardAPIBatchFixture(t)
	for _, mode := range []upstreamcacheguard.Mode{upstreamcacheguard.Protect, upstreamcacheguard.Observe} {
		t.Run(string(mode), func(t *testing.T) {
			settings := r.engine.Settings()
			settings.Mode = mode
			require.NoError(t, r.engine.ApplySettings(settings))
			require.NoError(t, r.engine.Refresh(context.Background()))
			batch := UpstreamCacheGuardAccountStatesBatch(accounts)
			for i := range accounts {
				require.Equal(t, UpstreamCacheGuardAccountStates(&accounts[i]), batch[accounts[i].ID])
			}
			require.NotContains(t, batch, int64(4), "ordinary accounts do not participate")
			target := batch[1][1]
			require.Equal(t, int64(200), target.GroupID)
			require.Equal(t, "normal", target.NativeState)
			require.Equal(t, "linked_group_protection", target.Reason)
			require.Equal(t, 95.0, *target.RatePercent)
			require.Equal(t, 42, target.ValidRequests)
			require.Zero(t, target.RecoveryValidRequests)
			require.Nil(t, target.RecoveryRatePercent)
			require.Equal(t, "Source", target.LinkedProtection.Sources[0].GroupName)
			require.Equal(t, 60.0, *target.LinkedProtection.Sources[0].RatePercent)
			trial := batch[2][0]
			require.Equal(t, 25, trial.RecoveryValidRequests)
			require.Equal(t, 3, trial.RecoverySessions)
			require.Equal(t, 82.0, *trial.RecoveryRatePercent)
			require.NotNil(t, trial.RecoveryStartedAt)
			if mode == upstreamcacheguard.Protect {
				require.Equal(t, "protected", target.State)
				require.Equal(t, "recovering", trial.State)
			} else {
				require.Equal(t, "observing", target.State)
				require.Equal(t, "observing", trial.State)
			}
			require.NotNil(t, batch[3][0].RatePercent, "explicit zero stays known")
			require.Zero(t, *batch[3][0].RatePercent)
		})
	}
	rotatedKey := int64(99)
	accounts[0].UpstreamKeyID = &rotatedKey
	for _, state := range UpstreamCacheGuardAccountStatesBatch(accounts)[1] {
		require.Equal(t, "insufficient", state.State)
		require.Equal(t, "insufficient_samples", state.Reason)
		require.Nil(t, state.RatePercent)
		require.Nil(t, state.LinkedProtection)
		require.Zero(t, state.ValidRequests)
	}
}

func TestUpstreamCacheGuardAccountStatesBatchDisabledAndUnavailable(t *testing.T) {
	accounts, r := cacheGuardAPIBatchFixture(t)
	settings := r.engine.Settings()
	settings.Mode = upstreamcacheguard.Disabled
	require.NoError(t, r.engine.ApplySettings(settings))
	for _, states := range UpstreamCacheGuardAccountStatesBatch(accounts) {
		for _, state := range states {
			require.Equal(t, "disabled", state.State)
			require.Nil(t, state.RatePercent)
			require.Nil(t, state.LinkedProtection)
		}
	}
	settings.Mode = upstreamcacheguard.Protect
	settings.GroupIDs = []int64{100}
	require.NoError(t, r.engine.ApplySettings(settings))
	states := UpstreamCacheGuardAccountStatesBatch(accounts)[1]
	require.Equal(t, "protected", states[0].State)
	require.Equal(t, "disabled", states[1].State, "excluded groups stay disabled")
	settings.GroupIDs = nil
	require.NoError(t, r.engine.ApplySettings(settings))
	r.available.Store(false)
	for _, states := range UpstreamCacheGuardAccountStatesBatch(accounts) {
		for _, state := range states {
			require.Equal(t, "snapshot_unavailable", state.Reason)
			require.Equal(t, "insufficient", state.State)
			require.Nil(t, state.RatePercent)
		}
	}
	r.available.Store(true)
	r.engine = upstreamcacheguard.NewEngine(&cacheGuardSelectionStore{})
	require.NoError(t, r.engine.ApplySettings(settings))
	require.Equal(t, "snapshot_unavailable", UpstreamCacheGuardAccountStatesBatch(accounts)[1][0].Reason, "an engine without a loaded snapshot is unavailable")
	installedCacheGuardRunner.Store(nil)
	require.Equal(t, "disabled", UpstreamCacheGuardAccountStatesBatch(accounts)[1][0].State)
	require.Nil(t, UpstreamCacheGuardAccountStates(nil))
	require.Empty(t, UpstreamCacheGuardAccountStatesBatch(nil))
}

func TestUpstreamCacheGuardAccountStatesBatchReadsEachGroupOncePerRequest(t *testing.T) {
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	settings.GroupIDs = []int64{100, 200}
	var accounts []Account
	var rows []upstreamcacheguard.AccountSnapshot
	for id := int64(1); id <= 50; id++ {
		key, configID := id+1000, int64(9)
		accounts = append(accounts, Account{ID: id, UpstreamKeyID: &key, UpstreamConfigID: &configID, GroupIDs: []int64{100, 100, 200, 300}})
		rows = append(rows, upstreamcacheguard.AccountSnapshot{AccountState: upstreamcacheguard.AccountState{AccountID: id, UpstreamKeyID: key, Phase: upstreamcacheguard.Healthy, ValidRequests: 21, CachePercent: 90}})
	}
	counts := map[int64]int{}
	read := func(groupID int64) upstreamcacheguard.Snapshot {
		counts[groupID]++
		return upstreamcacheguard.Snapshot{GroupID: groupID, Available: groupID == 100, Accounts: rows}
	}
	batch := upstreamCacheGuardAccountStatesBatch(accounts, settings, true, read)
	require.Equal(t, map[int64]int{100: 1, 200: 1}, counts)
	for _, states := range batch {
		require.Len(t, states, 3)
		require.Equal(t, "normal", states[0].State)
		require.Equal(t, "snapshot_unavailable", states[1].Reason)
		require.Equal(t, "disabled", states[2].State)
	}
	rows[0].CachePercent = 10
	second := upstreamCacheGuardAccountStatesBatch(accounts, settings, true, read)
	require.Equal(t, map[int64]int{100: 2, 200: 2}, counts)
	require.Equal(t, 10.0, *second[1][0].RatePercent, "no prior request's snapshot is retained")
	counts = map[int64]int{}
	upstreamCacheGuardAccountStatesBatch(accounts, settings, false, read)
	require.Empty(t, counts, "an unavailable runner must not read engine snapshots")
}

func TestUpstreamCacheGuardAccountStatesBatchRejectsExpiredSnapshot(t *testing.T) {
	accounts, r := cacheGuardAPIBatchFixture(t)
	require.Equal(t, "protected", UpstreamCacheGuardAccountStatesBatch(accounts)[1][1].State)
	// Exercise the engine's real freshness check, rather than introducing a
	// service-side clock or retaining an extra cache with its own lifetime.
	time.Sleep(upstreamcacheguard.RefreshLifetime + 25*time.Millisecond)
	require.False(t, r.engine.CachedSnapshot(100).Available)
	for _, states := range UpstreamCacheGuardAccountStatesBatch(accounts) {
		for _, state := range states {
			require.Equal(t, "snapshot_unavailable", state.Reason)
			require.Equal(t, "insufficient", state.State)
			require.Nil(t, state.LinkedProtection)
			require.Nil(t, state.RatePercent)
		}
	}
}

var cacheGuardAccountStatesBenchmarkSink any

// This measures synthetic shared-snapshot projection only. Database, Redis,
// network, complete list serialization, and frontend rendering are excluded.
func BenchmarkUpstreamCacheGuardAccountStatesProjection(b *testing.B) {
	store := &cacheGuardSelectionStore{}
	r := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store)}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(b, r.engine.ApplySettings(settings))
	r.available.Store(true)
	old := installedCacheGuardRunner.Swap(r)
	b.Cleanup(func() { installedCacheGuardRunner.Store(old) })
	accounts := make([]Account, 0, 50)
	now := time.Now().UTC()
	for id := int64(1); id <= 50; id++ {
		key, configID := id+1000, int64(9)
		accounts = append(accounts, Account{ID: id, UpstreamConfigID: &configID, UpstreamKeyID: &key,
			GroupIDs: []int64{100, 200}, Groups: []*Group{{ID: 100, Name: "Source"}, {ID: 200, Name: "Target"}}})
		for _, groupID := range []int64{100, 200} {
			state := upstreamcacheguard.AccountState{AccountID: id, UpstreamKeyID: key,
				Phase: upstreamcacheguard.Healthy, ValidRequests: 42, CachePercent: 90, UpdatedAt: now}
			if groupID == 100 && id%10 == 0 {
				state.Phase = upstreamcacheguard.Cooling
				state.CachePercent = 60
				state.CooldownUntil = now.Add(time.Hour)
			}
			payload, err := json.Marshal(state)
			require.NoError(b, err)
			store.rows = append(store.rows, upstreamcacheguard.RuntimeRow{GroupID: groupID, Kind: upstreamcacheguard.AccountKind,
				Key: fmt.Sprint(id), Version: 1, Payload: payload, UpdatedAt: now})
		}
	}
	for _, tc := range []struct {
		name  string
		batch bool
	}{{"single_per_account", false}, {"page_batch", true}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// Refresh outside the measured work on every iteration so long
				// benchmark runs never compare unavailable/expired snapshots.
				b.StopTimer()
				require.NoError(b, r.engine.Refresh(context.Background()))
				b.StartTimer()
				if tc.batch {
					cacheGuardAccountStatesBenchmarkSink = UpstreamCacheGuardAccountStatesBatch(accounts)
				} else {
					for i := range accounts {
						cacheGuardAccountStatesBenchmarkSink = UpstreamCacheGuardAccountStates(&accounts[i])
					}
				}
			}
		})
	}
}
