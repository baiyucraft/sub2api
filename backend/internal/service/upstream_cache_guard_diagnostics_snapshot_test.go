package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheGuardDiagnosticSnapshotFiltersCurrentIdentityAndOwnsCounts(t *testing.T) {
	var diagnostics cacheGuardDiagnostics
	diagnostics.record("admitted_warm", 2, 1, 3)
	diagnostics.record("admitted_cold", 1, 1, 3)
	diagnostics.record("admitted_cold", 1, 1, 4)
	diagnostics.record("admitted_cold", 1, 2, 3)
	rows, evictions := diagnostics.snapshot(1, 3)
	require.Zero(t, evictions)
	require.Len(t, rows, 2)
	require.Equal(t, int64(1), rows[0].GroupID)
	require.Equal(t, int64(2), rows[1].GroupID)
	require.False(t, rows[0].ObservedFrom.IsZero())
	require.False(t, rows[0].ObservedAt.Before(rows[0].ObservedFrom))
	rows[0].Counts["admitted_cold"] = 999
	again, _ := diagnostics.snapshot(1, 3)
	require.Equal(t, uint64(1), again[0].Counts["admitted_cold"])
	diagnostics.record("admitted_warm", 1, 1, 3)
	require.NotContains(t, rows[0].Counts, "admitted_warm", "published maps cannot change after later recording")
}

func TestCacheGuardDiagnosticSnapshotReportsBoundedEvictionEpoch(t *testing.T) {
	var diagnostics cacheGuardDiagnostics
	diagnostics.record("admitted_warm", 1, 1, 1)
	first, _ := diagnostics.snapshot(1, 1)
	for id := int64(2); id <= cacheGuardDiagnosticCapacity+1; id++ {
		diagnostics.record("admitted_warm", 1, id, 1)
	}
	missing, evictions := diagnostics.snapshot(1, 1)
	require.Empty(t, missing)
	require.Equal(t, uint64(1), evictions)
	diagnostics.record("admitted_warm", 1, 1, 1)
	current, evictions := diagnostics.snapshot(1, 1)
	require.Greater(t, current[0].CounterEpoch, first[0].CounterEpoch)
	require.Equal(t, uint64(1), current[0].Counts["admitted_warm"])
	require.Equal(t, uint64(2), evictions)
	require.Len(t, diagnostics.entries, cacheGuardDiagnosticCapacity)
}

func TestCacheGuardSamplingDiagnosticsRunnerMissingAndCurrentKey(t *testing.T) {
	old := installedCacheGuardRunner.Swap(nil)
	t.Cleanup(func() { installedCacheGuardRunner.Store(old) })
	missing := GetUpstreamCacheGuardSamplingDiagnostics(&Account{ID: 1})
	require.False(t, missing.Ready)
	require.False(t, missing.RunnerInstalled)
	require.False(t, missing.CompleteHistory)
	require.NotEmpty(t, missing.ProcessID)
	require.Equal(t, "process_account_window", missing.CountScope)
	require.Empty(t, missing.Counters)
	runner := &UpstreamCacheGuardRunner{}
	runner.diagnostics.record("admitted_cold", 1, 1, 2)
	runner.diagnostics.record("admitted_warm", 1, 1, 3)
	runner.closed.Store(true)
	installedCacheGuardRunner.Store(runner)
	key := int64(3)
	closed := GetUpstreamCacheGuardSamplingDiagnostics(&Account{ID: 1, UpstreamKeyID: &key})
	require.True(t, closed.RunnerInstalled)
	require.True(t, closed.RunnerClosed)
	require.False(t, closed.Ready)
	require.Len(t, closed.Counters, 1)
	require.Equal(t, uint64(1), closed.Counters[0].Counts["admitted_warm"])
	require.NotContains(t, closed.Counters[0].Counts, "admitted_cold")
}

func TestCacheGuardSamplingDiagnosticsIncludesActualSnapshotReasons(t *testing.T) {
	_, _, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, true, time.Time{}, nil)
	key := int64(1)
	account := &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key, UpstreamConfigID: &key, GroupIDs: []int64{100, 100}}
	result := GetUpstreamCacheGuardSamplingDiagnostics(account)
	require.True(t, result.Ready)
	require.Len(t, result.Snapshots, 1)
	require.True(t, result.Snapshots[0].Consistent)
	require.NotNil(t, result.Snapshots[0].AccountLoadedAt)
	require.Equal(t, runner.engine.PolicyVersion(), result.Snapshots[0].PolicyVersion)
}

func TestCacheGuardDiagnosticSnapshotConcurrentRecording(t *testing.T) {
	var diagnostics cacheGuardDiagnostics
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				diagnostics.record("admitted_warm", 1, 1, 2)
				diagnostics.snapshot(1, 2)
			}
		}()
	}
	workers.Wait()
	rows, _ := diagnostics.snapshot(1, 2)
	require.Equal(t, uint64(400), rows[0].Counts["admitted_warm"])
}
