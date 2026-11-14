package service

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheGuardRefreshReadCountsAreCopiedAndLoggedOnFailure(t *testing.T) {
	timings := newCacheGuardRefreshTimings()
	timings.observeCount("active_runtime_resident_sessions", 65536)
	timings.observeCount("active_runtime_returned_rows", 7)
	timings.observeCount("active_runtime_payload_bytes", 326)
	counts := timings.countsSnapshot()
	counts["active_runtime_resident_sessions"] = 0
	require.EqualValues(t, 65536, timings.countsSnapshot()["active_runtime_resident_sessions"])

	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	var diagnostics cacheGuardRefreshDiagnostics
	diagnostics.recordAt(time.Now(), "refresh", context.DeadlineExceeded, cacheGuardRefreshBudget, cacheGuardRefreshBudget,
		map[string]float64{"active_runtime_query": 2990}, timings.countsSnapshot())
	var entry struct {
		Counts map[string]int64 `json:"stage_counts"`
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, timings.countsSnapshot(), entry.Counts)
	logs.Reset()
	diagnostics.recordAt(time.Now(), "refresh", nil, time.Millisecond, cacheGuardRefreshBudget, nil, timings.countsSnapshot())
	require.Contains(t, logs.String(), "upstream_cache_guard_refresh_recovered")
	logs.Reset()
	diagnostics.recordAt(time.Now(), "refresh", nil, time.Millisecond, cacheGuardRefreshBudget, nil, timings.countsSnapshot())
	require.Empty(t, logs.String(), "successful steady-state refreshes must not add periodic logs")
}
