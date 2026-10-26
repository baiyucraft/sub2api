//go:build cacheguardpg

package repository

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// Hide only the newly introduced recovery capability. In particular, retain
// split admissions and the account transaction, so the reference executes the
// original bounded AccountFacts read and exact trial-only recomputation.
type ordinaryTrialOnlyTx struct {
	upstreamcacheguard.AccountTx
	upstreamcacheguard.AdmissionTx
	upstreamcacheguard.FactVersionTx
}

type ordinaryTrialOnlyStore struct{ *upstreamCacheGuardRepository }

func (s ordinaryTrialOnlyStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(upstreamcacheguard.Tx) error) error {
	return s.upstreamCacheGuardRepository.TransactAccount(ctx, group, account, key, func(tx upstreamcacheguard.Tx) error {
		return fn(ordinaryTrialOnlyTx{tx.(upstreamcacheguard.AccountTx), tx.(upstreamcacheguard.AdmissionTx), tx.(upstreamcacheguard.FactVersionTx)})
	})
}

func TestCacheGuardOrdinaryPostgresCompletionTransactionComparison(t *testing.T) {
	db, _, store := ordinaryPGFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	settings := ordinaryPGSettings()
	signature := upstreamcacheguard.PolicySignature(settings)
	type result struct {
		Run     int    `json:"run"`
		Method  string `json:"method"`
		Samples int    `json:"samples"`
		Facts   int    `json:"facts_read"`
		P50     int64  `json:"p50_us"`
		P95     int64  `json:"p95_us"`
		Min     int64  `json:"min_us"`
		Max     int64  `json:"max_us"`
	}
	var results []result
	for run := 1; run <= 3; run++ {
		methods := []string{"trial_only_original_read", "with_ordinary_recovery"}
		if run%2 == 0 {
			methods[0], methods[1] = methods[1], methods[0]
		}
		// Both modes consume the exact same 199 prior facts plus one new fact.
		at := time.Now().UTC().Truncate(time.Microsecond)
		for _, method := range methods {
			_, err := db.ExecContext(ctx, `DELETE FROM upstream_cache_guard_facts; DELETE FROM upstream_cache_guard_runtime; DELETE FROM upstream_cache_guard_admissions; DELETE FROM upstream_cache_guard_accounts`)
			require.NoError(t, err)
			for i := 0; i < 199; i++ {
				ordinaryPGInsertFact(t, db, ctx, ordinaryPGEvidence(i, at.Add(-time.Duration(i+1)*time.Second)), true)
			}
			var engineStore upstreamcacheguard.Store = store
			if method == "trial_only_original_read" {
				engineStore = ordinaryTrialOnlyStore{store}
			}
			engine := upstreamcacheguard.NewEngine(engineStore)
			require.NoError(t, engine.ApplyRecoveryConfiguration(settings, map[int64]upstreamcacheguard.Settings{8: settings, 21: settings}, map[string]string{signature: "durable-pg-policy"}))
			engine.SetOrdinaryRecoveryIdentityGeneration(ordinaryPGIdentity)
			current := ordinaryPGEvidence(1000, at)
			var durations []int64
			for sample := 0; sample < 26; sample++ {
				// Reset outside the timed region. Dataset and account/session state
				// never grow between samples; sample zero warms caches only.
				_, err = db.ExecContext(ctx, `DELETE FROM upstream_cache_guard_facts WHERE evidence_id=$1;`, current.ID)
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, `DELETE FROM upstream_cache_guard_runtime; DELETE FROM upstream_cache_guard_admissions; DELETE FROM upstream_cache_guard_accounts`)
				require.NoError(t, err)
				ordinaryPGWriteState(t, store, ctx, ordinaryPGProtectedState(settings, at))
				started := time.Now()
				inserted, err := engine.CompleteWithResult(ctx, current)
				elapsed := time.Since(started).Microseconds()
				require.NoError(t, err)
				require.True(t, inserted)
				if sample > 0 {
					durations = append(durations, elapsed)
				}
				state := ordinaryPGReadState(t, store, ctx).Policies[signature].State
				require.Equal(t, 200, state.ValidRequests)
				require.InDelta(t, 95, state.CachePercent, 1e-9)
				require.NotEqual(t, upstreamcacheguard.Healthy, state.Phase)
				if method == "with_ordinary_recovery" {
					require.NotNil(t, state.OrdinaryRecovery)
					require.Equal(t, 200, state.OrdinaryRecovery.ValidRequests)
				}
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			results = append(results, result{run, method, len(durations), 200, durations[len(durations)/2], durations[len(durations)*95/100], durations[0], durations[len(durations)-1]})
		}
	}
	encoded, err := json.MarshalIndent(struct {
		CheckedAt string         `json:"checked_at"`
		Note      string         `json:"note"`
		SQL       map[string]int `json:"static_sql_per_completion"`
		Steps     []string       `json:"static_sql_steps"`
		Results   []result       `json:"results"`
	}{
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Note:      "Isolated local PostgreSQL; 3 paired rounds, 25 warm completion transactions per mode, identical 199 processed facts + 1 inserted completion. Includes BEGIN, account/session locks, original exact recomputation, admission/account/session writes and COMMIT. Excludes enqueue, queue waiting, consumer claiming, refresh, upstream requests, fixture reset and assertion reads. Reference hides RecoveryFactsTx only, retaining AccountTx/AdmissionTx/FactVersionTx and identical nonempty generations; it exercises the original trial-only read, not an old binary. Static SQL counts are source-inspected for this existing-account, empty-session, non-trial fixture, not driver-instrumented; triggers and protocol/prepare messages excluded. No P99 or production-load claim.",
		SQL:       map[string]int{"trial_only_original_read": 12, "with_ordinary_recovery": 12, "per_mode_excluding_begin_commit": 10},
		Steps:     []string{"BEGIN", "account advisory lock", "insert/acknowledge fact", "session advisory lock", "read session", "write completed admission", "read existing account", "read account facts (reference) or facts+pending (ordinary)", "read pending session admissions", "write account", "write session", "COMMIT"},
		Results:   results,
	}, "", "  ")
	require.NoError(t, err)
	if path := os.Getenv("SUB2API_CACHE_GUARD_COMPLETION_PERF_RECEIPT"); path != "" {
		require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0600))
	}
	t.Log(string(encoded))
}
