//go:build cacheguardpg

package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

type cacheGuardRunnerPerformanceStore struct {
	upstreamcacheguard.Store
	upstreamcacheguard.AccountStore
	upstreamcacheguard.IncrementalStore
	upstreamcacheguard.AdmissionStore
	windows, windowAccounts, pending int
}

func (s *cacheGuardRunnerPerformanceStore) AccountWindowsFor(ctx context.Context, ids []int64, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	s.windows++
	s.windowAccounts += len(ids)
	return s.IncrementalStore.AccountWindowsFor(ctx, ids, since, until)
}

func (s *cacheGuardRunnerPerformanceStore) HasPendingBefore(ctx context.Context, cutoff time.Time) (bool, error) {
	s.pending++
	return s.Store.HasPendingBefore(ctx, cutoff)
}

type cacheGuardRunnerPerformanceSample struct {
	service.CacheGuardRunnerPostgresPerformanceRun
	WindowQueries  int `json:"window_queries"`
	WindowAccounts int `json:"window_accounts"`
	PendingQueries int `json:"pending_queries"`
}

func TestCacheGuardRunnerPostgresPerformance(t *testing.T) {
	dsn := os.Getenv("SUB2API_CACHE_GUARD_RUNNER_PERF_DSN")
	if dsn == "" {
		t.Skip("explicit isolated runner-performance DSN required")
	}
	receipt := os.Getenv("SUB2API_CACHE_GUARD_RUNNER_PERF_RECEIPT")
	report := struct {
		Passed    bool                                `json:"passed"`
		CheckedAt string                              `json:"checked_at"`
		Scale     int                                 `json:"scale"`
		Note      string                              `json:"note"`
		Initial   *cacheGuardRunnerPerformanceSample  `json:"initial,omitempty"`
		Runs      []cacheGuardRunnerPerformanceSample `json:"runs"`
		P50Millis float64                             `json:"runner_p50_ms"`
		P95Millis float64                             `json:"runner_p95_ms"`
		MaxMillis float64                             `json:"runner_max_ms"`
	}{CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Note: "real synchronous runner.refresh including settings read, policy application, engine refresh and independent pending-health query; synthetic parent-owned isolated schema; no workers or model requests"}
	defer func() {
		report.Passed = !t.Failed()
		if receipt == "" {
			return
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.MkdirAll(filepath.Dir(receipt), 0700)
		}
		if err == nil {
			err = os.WriteFile(receipt, append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error("cannot write runner-performance receipt")
		}
	}()
	require.Equal(t, "1", os.Getenv("SUB2API_CACHE_GUARD_TEST_ISOLATED"), "explicit isolated-test acknowledgement required")
	require.NotEmpty(t, receipt, "explicit runner-performance receipt required")
	schema := os.Getenv("SUB2API_CACHE_GUARD_RUNNER_PERF_SCHEMA")
	require.True(t, regexp.MustCompile(`^cache_guard_(admission_)?test_[0-9a-f]{16}$`).MatchString(schema), "a parent-owned private cache-guard test schema is required")
	scale, err := strconv.Atoi(os.Getenv("SUB2API_CACHE_GUARD_RUNNER_PERF_SCALE"))
	require.NoError(t, err)
	require.Contains(t, []int{1, 4}, scale)
	report.Scale = scale
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("cannot open the explicit runner-performance database")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var database, currentSchema string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT current_database(),current_schema()`).Scan(&database, &currentSchema))
	require.Contains(t, strings.ToLower(database), "test", "refusing a non-test database")
	require.Equal(t, schema, currentSchema, "connection must already target the exact parent-owned schema")
	_, err = db.ExecContext(ctx, `SET work_mem='4MB'`)
	require.NoError(t, err)
	var workMem string
	require.NoError(t, db.QueryRowContext(ctx, `SHOW work_mem`).Scan(&workMem))
	require.Equal(t, "4MB", workMem)
	base := repository.NewUpstreamCacheGuardRepository(db)
	store := &cacheGuardRunnerPerformanceStore{Store: base, AccountStore: base.(upstreamcacheguard.AccountStore), IncrementalStore: base.(upstreamcacheguard.IncrementalStore), AdmissionStore: base.(upstreamcacheguard.AdmissionStore)}
	handle := service.NewCacheGuardRunnerPostgresPerformanceHandle(t, store)
	refresh := func() cacheGuardRunnerPerformanceSample {
		store.windows, store.windowAccounts, store.pending = 0, 0, 0
		run := handle.Refresh()
		return cacheGuardRunnerPerformanceSample{CacheGuardRunnerPostgresPerformanceRun: run, WindowQueries: store.windows, WindowAccounts: store.windowAccounts, PendingQueries: store.pending}
	}
	validate := func(sample cacheGuardRunnerPerformanceSample) {
		require.True(t, sample.Succeeded, "the actual runner must publish successfully and finish pending health")
		require.Equal(t, 1, sample.SettingsReads, "real SettingService must read and parse settings each round")
		require.Equal(t, 1, sample.WindowQueries, "a changed-identity round must query actual account windows")
		require.Equal(t, 56, sample.WindowAccounts)
		require.Equal(t, 1, sample.PendingQueries, "the measured round must include actual pending health")
		require.Equal(t, 56, sample.AvailableAccounts)
		require.Equal(t, 168, sample.Policies)
		require.Less(t, sample.ElapsedMillis, float64(3000), "every complete runner round must fit the 3s refresh budget")
	}
	initial := refresh()
	report.Initial = &initial
	validate(initial)
	var durations []float64
	for round := 0; round < 30; round++ {
		result, err := db.ExecContext(ctx, `UPDATE upstream_cache_guard_facts AS fact SET processed=fact.processed
			FROM (SELECT DISTINCT ON (account_id) group_id,evidence_id FROM upstream_cache_guard_facts
			WHERE completed_at >= $1 ORDER BY account_id,evidence_id) AS changed
			WHERE fact.group_id=changed.group_id AND fact.evidence_id=changed.evidence_id`, time.Now().UTC().Add(-time.Hour))
		require.NoError(t, err)
		changed, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(56), changed, "real processed-column trigger must invalidate all 56 identities")
		sample := refresh()
		report.Runs = append(report.Runs, sample)
		durations = append(durations, sample.ElapsedMillis)
		validate(sample)
	}
	sort.Float64s(durations)
	report.P50Millis, report.P95Millis, report.MaxMillis = durations[15], durations[28], durations[29]
	if scale == 1 {
		require.LessOrEqual(t, report.P95Millis, float64(1500), "current-scale whole-runner p95 must retain headroom")
	}
	t.Logf("actual runner scale=%d rounds=30 p50=%.3fms p95=%.3fms max=%.3fms", scale, report.P50Millis, report.P95Millis, report.MaxMillis)
}
