package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardDiagnosticCountsAreAccountAndKeyScoped(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	runner := &UpstreamCacheGuardRunner{}
	for i := 0; i < 1001; i++ {
		runner.recordAdmissionSkip(cacheGuardSkipAdmissionError, 22, 1, 3)
	}
	runner.recordAdmissionSkip(cacheGuardSkipAdmissionError, 22, 2, 3)
	runner.recordAdmissionSkip(cacheGuardSkipAdmissionError, 22, 2, 4)
	require.Equal(t, uint64(1003), runner.skipCounts[cacheGuardSkipAdmissionError].Load())
	decoder := json.NewDecoder(&logs)
	var entries []map[string]any
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		entries = append(entries, entry)
	}
	require.Len(t, entries, 4)
	require.Equal(t, float64(1), entries[2]["count"], "a second account's first failure must be visible")
	require.Equal(t, float64(1), entries[3]["count"], "a changed Key has an independent counter")
	for _, entry := range entries {
		require.Equal(t, "process_account_window", entry["count_scope"])
		require.NotZero(t, entry["counter_epoch"])
	}
}

func TestCacheGuardDiagnosticWindowIsBoundedAndEvictionHasNewEpoch(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var diagnostics cacheGuardDiagnostics
	diagnostics.record("admitted_warm", 1, 1, 1)
	epoch := diagnostics.entries[cacheGuardDiagnosticIdentity{1, 1, 1}].epoch
	for i := int64(2); i <= cacheGuardDiagnosticCapacity+1; i++ {
		diagnostics.record("admitted_warm", 1, i, 1)
	}
	require.Len(t, diagnostics.entries, cacheGuardDiagnosticCapacity)
	diagnostics.record("admitted_warm", 1, 1, 1)
	require.Greater(t, diagnostics.entries[cacheGuardDiagnosticIdentity{1, 1, 1}].epoch, epoch)
	require.Len(t, diagnostics.order, cacheGuardDiagnosticCapacity)
	require.Empty(t, logs.String(), "success identity churn must not emit per-request logs")
	for i := 0; i < 999; i++ {
		diagnostics.record("admitted_warm", 1, 1, 1)
	}
	var entry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, float64(1000), entry["count"], "long-running identities retain periodic diagnostic summaries")
}

func TestCacheGuardBudgetStagesReportCumulativeTimeWithoutCancelingOrdinaryContext(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	identity.budget.startedAt = time.Now().Add(-170 * time.Millisecond)
	identity.budget.activeElapsed = 100 * time.Millisecond
	identity.budget.groupID, identity.budget.accountID, identity.budget.upstreamKeyID = 22, 13328, 296
	child, cancel := cacheGuardDatabaseContextForStage(ctx, "admission")
	finish := upstreamcacheguard.MeasureStage(child, "account_lock")
	finish()
	cancel()
	active := identity.budget.activeElapsed
	cancel()
	require.Equal(t, active, identity.budget.activeElapsed, "cancel remains idempotent")
	require.NoError(t, ctx.Err())
	require.True(t, protectionOrdinaryFallback(ctx))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, "admission", entry["operation"])
	require.Equal(t, float64(13328), entry["account_id"])
	require.GreaterOrEqual(t, entry["budget_elapsed_ms"].(float64), float64(100))
	require.Greater(t, entry["between_operations_ms"].(float64), float64(0))
	require.Greater(t, entry["wall_elapsed_ms"].(float64), entry["budget_elapsed_ms"].(float64))
	stages := entry["stages_ms"].(map[string]any)
	require.Contains(t, stages, "account_lock")
	require.GreaterOrEqual(t, stages["account_lock"].(float64), float64(0), "Windows clocks can round a very short stage to zero")
}

func TestCacheGuardDiagnosticConcurrentObservations(t *testing.T) {
	var diagnostics cacheGuardDiagnostics
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 100; i++ {
				diagnostics.record("admitted_warm", 1, 2, 3)
			}
		}()
	}
	workers.Wait()
	require.Len(t, diagnostics.entries, 1)
	require.Equal(t, uint64(1600), diagnostics.entries[cacheGuardDiagnosticIdentity{1, 2, 3}].counts["admitted_warm"])
}
