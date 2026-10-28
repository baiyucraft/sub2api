//go:build cacheguardpg

package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Frozen before the set-based rewrite. It is intentionally test-only, so the
// differential oracle cannot silently change with the production query.
//
//go:embed testdata/upstream_cache_guard_account_windows_legacy.sql
var legacyAccountWindowsSQL string

type windowSQLReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Compare every one of the 17 SQL columns, including otherwise-unexported
// oldest/future boundaries. Only arrays whose contract is a set are sorted.
func accountWindowSQLRows(t *testing.T, ctx context.Context, reader windowSQLReader, query string, since, until time.Time, ids []int64) [][]any {
	t.Helper()
	rows, err := readAccountWindowSQLRows(ctx, reader, query, since, until, ids)
	require.NoError(t, err)
	return rows
}

func readAccountWindowSQLRows(ctx context.Context, reader windowSQLReader, query string, since, until time.Time, ids []int64) ([][]any, error) {
	rows, err := reader.QueryContext(ctx, query, since, until, upstreamcacheguard.MaxRuntimeRows+1, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		values := make([]any, 17)
		targets := make([]any, len(values))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, err
		}
		for _, i := range []int{3, 16} {
			var data []byte
			switch v := values[i].(type) {
			case []byte:
				data = v
			case string:
				data = []byte(v)
			default:
				return nil, fmt.Errorf("JSON column %d has type %T", i, v)
			}
			var decoded any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil {
				return nil, err
			}
			canonicalWindowJSON(decoded)
			values[i] = decoded
		}
		for _, i := range []int{14, 15} {
			if values[i] != nil {
				var groups pq.Int64Array
				if err := groups.Scan(values[i]); err != nil {
					return nil, err
				}
				sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
				values[i] = groups
			}
		}
		out = append(out, values)
	}
	return out, rows.Err()
}

func canonicalWindowJSON(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "GroupIDs" || key == "TrialGroupIDs" {
				if items, ok := child.([]any); ok {
					sort.Slice(items, func(i, j int) bool {
						a, _ := items[i].(json.Number).Int64()
						b, _ := items[j].(json.Number).Int64()
						return a < b
					})
				}
			}
			canonicalWindowJSON(child)
		}
	case []any:
		for _, child := range value {
			canonicalWindowJSON(child)
		}
	}
}

func compareAccountWindowSQL(t *testing.T, db *sql.DB, since, until time.Time, ids []int64) int {
	t.Helper()
	return compareAccountWindowSQLWithBudget(t, db, since, until, ids, 30*time.Second)
}

func compareAccountWindowSQLWithBudget(t *testing.T, db *sql.DB, since, until time.Time, ids []int64, timeout time.Duration) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SET LOCAL work_mem='4MB'`)
	require.NoError(t, err)
	want := accountWindowSQLRows(t, ctx, tx, legacyAccountWindowsSQL, since, until, ids)
	got := accountWindowSQLRows(t, ctx, tx, upstreamCacheGuardAccountWindowsSQL, since, until, ids)
	require.Equal(t, want, got, "all account/policy columns must match in one read snapshot")
	return len(got)
}

func TestCacheGuardAccountWindowsPostgresMatchesFrozenQuery(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	state := ordinaryPGProtectedState(s, now)
	state.CohortID = "legacy-cohort"
	state.QualitySince = now.Add(-40 * time.Minute)
	state.LegacyQualitySince = map[int64]time.Time{8: now.Add(-20 * time.Minute)}
	for i := 0; i < 3; i++ {
		settings := s
		settings.ThresholdPercent += float64(i)
		projection := upstreamcacheguard.PolicyProjection{Settings: settings, State: upstreamcacheguard.AccountState{
			AccountID: 2, UpstreamKeyID: 5, CohortID: fmt.Sprintf("cohort-%d", i),
			QualitySince:     now.Add(-time.Duration(30-i*10) * time.Minute),
			OrdinaryRecovery: &upstreamcacheguard.OrdinaryRecoveryState{IdentityGeneration: ordinaryPGIdentity, BarrierAt: now.Add(-15 * time.Minute)},
		}}
		state.Policies[upstreamcacheguard.PolicySignature(settings)] = projection
	}
	ordinaryPGWriteState(t, store, ctx, state)
	require.NoError(t, store.TransactAccount(ctx, 8, 3, 5, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.(upstreamcacheguard.AccountTx).WriteAccount(3, 5, upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: 5})
		return err
	}))
	_, err := db.ExecContext(ctx, `INSERT INTO accounts(id,upstream_key_id) VALUES(4,5),(5,5)`)
	require.NoError(t, err)
	for _, id := range []int64{4, 5} {
		require.NoError(t, store.TransactAccount(ctx, 8, id, 5, func(tx upstreamcacheguard.Tx) error {
			_, err := tx.(upstreamcacheguard.AccountTx).WriteAccount(id, 5, upstreamcacheguard.AccountState{AccountID: id, UpstreamKeyID: 5})
			return err
		}))
		f := ordinaryPGEvidence(int(id)+200, now.Add(-time.Second))
		f.Request.AccountID, f.Admission.AccountID = id, id
		ordinaryPGInsertFact(t, db, ctx, f, true)
	}
	_, err = db.ExecContext(ctx, `UPDATE accounts SET deleted_at=CASE WHEN id=4 THEN $1 ELSE deleted_at END,upstream_key_id=CASE WHEN id=5 THEN 6 ELSE upstream_key_id END WHERE id IN (4,5)`, now)
	require.NoError(t, err)
	for i := 0; i < 70; i++ {
		f := ordinaryPGEvidence(i, now.Add(-time.Duration(i)*time.Minute))
		f.Admission.CohortID = fmt.Sprintf("cohort-%d", i%3)
		switch i % 7 {
		case 0:
			f.Admission.Trial = true
		case 1:
			f.Admission.CaptureOnly = true
		case 2:
			f.ProtectionPaused = true
		case 3:
			f.Request.OrdinaryRecoverySessionID = strings.Repeat("b", 64)
		case 4:
			f.Admission.PolicySignature = "different-policy"
		case 5:
			f.Admission.CohortID = "legacy-cohort"
			f.Admission.Trial = true
		}
		ordinaryPGInsertFact(t, db, ctx, f, i%11 != 0)
	}
	// These exact BIGINTs differ by one beyond 2^53. A float64 JSON oracle
	// would silently hide some incorrect policy sums/group identifiers.
	large := ordinaryPGEvidence(1000, now.Add(-time.Second))
	large.Request.GroupID, large.Admission.GroupID = 9007199254740993, 9007199254740993
	large.RawUsage.TotalInputTokens, large.RawUsage.CacheReadTokens = 9007199254740993, 9007199254740992
	ordinaryPGInsertFact(t, db, ctx, large, true)
	// The future minimum drives NextBoundary even though it is not in the window.
	ordinaryPGInsertFact(t, db, ctx, ordinaryPGEvidence(100, now.Add(time.Second)), true)
	for _, ids := range [][]int64{nil, {2}, {3}, {2, 3}, {999}} {
		compareAccountWindowSQL(t, db, now.Add(-time.Hour), now, ids)
	}
	// Legacy quality_since differs from policy quality_since and retains its
	// original behavior when the policies key is absent.
	delete(state.Policies, upstreamcacheguard.PolicySignature(s))
	state.Policies = nil
	ordinaryPGWriteState(t, store, ctx, state)
	compareAccountWindowSQL(t, db, now.Add(-time.Hour), now, nil)
}

func TestCacheGuardAccountWindowsPostgresEmptyPolicyDoesNotDecodeFactFields(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	state := ordinaryPGProtectedState(s, now)
	ordinaryPGWriteState(t, store, ctx, state)
	_, err := db.ExecContext(ctx, `UPDATE upstream_cache_guard_accounts SET state_json=jsonb_set(state_json,ARRAY['policies',$1,'state','ordinary_recovery'], '{"identity_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","barrier_at":"legacy-invalid-time"}'::JSONB) WHERE account_id=2`, upstreamcacheguard.PolicySignature(s))
	require.NoError(t, err)
	// No eligible fact means recovery expressions were never evaluated in the
	// frozen correlated query. The set-based rewrite must retain that behavior.
	require.Equal(t, 1, compareAccountWindowSQL(t, db, now.Add(-time.Hour), now, nil))
}

func TestCacheGuardAccountWindowsPostgresJSONFieldCompatibility(t *testing.T) {
	db, ctx, store := ordinaryPGFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	s := ordinaryPGSettings()
	state := ordinaryPGProtectedState(s, now)
	ordinaryPGWriteState(t, store, ctx, state)
	// Keep wrong-type nested values as JSONB, rather than converting them to
	// typed records that would introduce new errors or accept number strings.
	for i, patch := range []string{
		`{"admission":null}`, `{"admission":false}`, `{"admission":7}`, `{"admission":[]}`, `{"admission":"legacy"}`,
		`{"request":null}`, `{"request":false}`, `{"request":[]}`, `{"request":"legacy"}`,
		`{"protection_paused":true}`, `{"protection_paused":"true"}`, `{"protection_paused":{"legacy":true}}`,
		`{"raw_usage":null}`, `{"raw_usage":[]}`, `{"raw_usage":"legacy"}`,
		`{"raw_usage":{"Status":"known","TotalInputTokens":"100","CacheReadTokens":95}}`,
		`{"raw_usage":{"Status":"known","TotalInputTokens":true,"CacheReadTokens":95}}`,
	} {
		f := ordinaryPGEvidence(i, now.Add(-time.Second))
		ordinaryPGInsertFact(t, db, ctx, f, true)
		_, err := db.ExecContext(ctx, `UPDATE upstream_cache_guard_facts SET evidence_json=evidence_json || $1::JSONB WHERE evidence_id=$2`, patch, f.ID)
		require.NoError(t, err)
		compareAccountWindowSQL(t, db, now.Add(-time.Hour), now, nil)
	}
}

func TestCacheGuardAccountWindowsPostgresRetainsInvalidFieldErrors(t *testing.T) {
	for _, patch := range []string{
		`{"raw_usage":{"Status":"known","TotalInputTokens":100.5,"CacheReadTokens":95}}`,
		`{"raw_usage":{"Status":"known","TotalInputTokens":9223372036854775808,"CacheReadTokens":95}}`,
		`{"request":{"started_at":"not-a-time","ordinary_recovery_session_id":"0000000000000000000000000000000000000000000000000000000000000001","ordinary_recovery_identity_generation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`,
	} {
		t.Run(fmt.Sprintf("patch_%x", sha256.Sum256([]byte(patch)))[:16], func(t *testing.T) {
			db, ctx, store := ordinaryPGFixture(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			s := ordinaryPGSettings()
			state := ordinaryPGProtectedState(s, now)
			sig := upstreamcacheguard.PolicySignature(s)
			projection := state.Policies[sig]
			projection.State.OrdinaryRecovery = &upstreamcacheguard.OrdinaryRecoveryState{IdentityGeneration: ordinaryPGIdentity, BarrierAt: now.Add(-time.Minute)}
			state.Policies[sig] = projection
			ordinaryPGWriteState(t, store, ctx, state)
			f := ordinaryPGEvidence(0, now.Add(-time.Second))
			ordinaryPGInsertFact(t, db, ctx, f, true)
			_, err := db.ExecContext(ctx, `UPDATE upstream_cache_guard_facts SET evidence_json=evidence_json || $1::JSONB WHERE evidence_id=$2`, patch, f.ID)
			require.NoError(t, err)
			var states []pq.ErrorCode
			for _, query := range []string{legacyAccountWindowsSQL, upstreamCacheGuardAccountWindowsSQL} {
				rows, err := db.QueryContext(ctx, query, now.Add(-time.Hour), now, upstreamcacheguard.MaxRuntimeRows+1, pq.Array([]int64(nil)))
				if err == nil {
					for rows.Next() {
					}
					err = rows.Err()
					rows.Close()
				}
				var pgErr *pq.Error
				require.ErrorAs(t, err, &pgErr, "invalid fields must not be coerced into successful samples")
				states = append(states, pgErr.Code)
			}
			require.Equal(t, states[0], states[1])
		})
	}
}

// The same helper is available to whole-engine refresh tests in this package.
// Fixtures are synthetic and never connect to a non-test database.
func cacheGuardWindowPerformanceFixture(t *testing.T, scale int) (*sql.DB, context.Context, *upstreamCacheGuardRepository, time.Time, time.Time) {
	t.Helper()
	db, _, store := ordinaryPGFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	until := time.Now().UTC().Truncate(time.Microsecond)
	since := until.Add(-time.Hour)
	_, err := db.ExecContext(ctx, `INSERT INTO accounts(id,upstream_key_id) SELECT i,i+1000 FROM generate_series(100,155) i`)
	require.NoError(t, err)
	for i := int64(100); i < 156; i++ {
		state := upstreamcacheguard.AccountState{AccountID: i, UpstreamKeyID: i + 1000, Phase: upstreamcacheguard.Healthy, Policies: map[string]upstreamcacheguard.PolicyProjection{}}
		count := 3
		if i >= 151 {
			count = 2
		}
		for p := 0; p < count; p++ {
			s := ordinaryPGSettings()
			s.ThresholdPercent += float64(p)
			state.Policies[upstreamcacheguard.PolicySignature(s)] = upstreamcacheguard.PolicyProjection{Settings: s, State: upstreamcacheguard.AccountState{
				AccountID: i, UpstreamKeyID: i + 1000, Phase: upstreamcacheguard.Healthy, CohortID: fmt.Sprintf("cohort-%d", p),
				QualitySince:     since.Add(time.Duration(p*10) * time.Minute),
				OrdinaryRecovery: &upstreamcacheguard.OrdinaryRecoveryState{IdentityGeneration: ordinaryPGIdentity, BarrierAt: until.Add(-30 * time.Minute)},
			}}
		}
		ordinaryPGWriteState(t, store, ctx, state)
	}
	// This private-schema bulk import models already-persisted history. Seed the
	// exact revision counts once; leave the real trigger enabled for every
	// measured Engine.Refresh iteration below.
	load, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer load.Rollback()
	_, err = load.ExecContext(ctx, `ALTER TABLE upstream_cache_guard_facts DISABLE TRIGGER upstream_cache_guard_fact_revision`)
	require.NoError(t, err)
	// Historical rows exercise the index window without copying live evidence.
	_, err = load.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,upstream_key_id,completed_at,valid_sample,processed,evidence_json)
		SELECT 8,'history-'||g,100+(g%56),1100+(g%56),$1::TIMESTAMPTZ-INTERVAL '2 hours'-(g%3600)*INTERVAL '1 second',TRUE,TRUE,
		jsonb_build_object('raw_usage',jsonb_build_object('Status','known','TotalInputTokens',100,'CacheReadTokens',90))
		FROM generate_series(1,$2::integer) g`, until, 120000*scale)
	require.NoError(t, err)
	var padding strings.Builder
	for i := 0; i < 16; i++ {
		digest := sha256.Sum256([]byte(fmt.Sprintf("synthetic-metadata-%d", i)))
		padding.WriteString(hex.EncodeToString(digest[:]))
	}
	_, err = load.ExecContext(ctx, `INSERT INTO upstream_cache_guard_facts(group_id,evidence_id,account_id,upstream_key_id,completed_at,valid_sample,processed,evidence_json)
		SELECT 8+(g%2)*13,'current-'||g,account_id,account_id+1000,at,(g%6)<>0,TRUE,
		jsonb_build_object('request',jsonb_build_object('started_at',at-INTERVAL '1 second','ordinary_recovery_session_id',session_id,'ordinary_recovery_identity_generation',$3::text,'synthetic_metadata',$4::text),
		'admission',jsonb_build_object('session_key',session_id,'trial',(g%17)=0,'capture_only',(g%19)=0,'cohort_id','cohort-'||(g%3),
		'ordinary_recovery_session_id',session_id,'ordinary_recovery_identity_generation',$3::text,'synthetic_metadata',$4::text),
		'raw_usage',jsonb_build_object('Status','known','TotalInputTokens',100+(g%100),'CacheReadTokens',90+(g%10)))
		FROM (SELECT g,CASE WHEN g<=$2::integer*3700 THEN 100 WHEN g<=$2::integer*4736 THEN 101 ELSE 102+(g%54) END AS account_id,
		$1::TIMESTAMPTZ-INTERVAL '15 minutes'-(g%1800)*INTERVAL '1 second' AS at,LPAD(TO_HEX(g%100),64,'0') AS session_id
		FROM generate_series(1,5400*$2::integer) g) fixture`, until, scale, ordinaryPGIdentity, padding.String())
	require.NoError(t, err)
	_, err = load.ExecContext(ctx, `INSERT INTO upstream_cache_guard_fact_versions(account_id,upstream_key_id,version)
		SELECT account_id,upstream_key_id,COUNT(*) FROM upstream_cache_guard_facts GROUP BY account_id,upstream_key_id
		ON CONFLICT(account_id,upstream_key_id) DO UPDATE SET version=EXCLUDED.version`)
	require.NoError(t, err)
	_, err = load.ExecContext(ctx, `ALTER TABLE upstream_cache_guard_facts ENABLE TRIGGER upstream_cache_guard_fact_revision`)
	require.NoError(t, err)
	require.NoError(t, load.Commit())
	_, err = db.ExecContext(ctx, `ANALYZE upstream_cache_guard_accounts; ANALYZE upstream_cache_guard_facts; ANALYZE accounts`)
	require.NoError(t, err)
	return db, ctx, store, since, until
}

type accountWindowPerfSample struct {
	Scale                int      `json:"scale"`
	Method               string   `json:"method"`
	Runs                 int      `json:"measured_attempts"`
	CompletedRuns        int      `json:"completed_runs"`
	CensoredRuns         int      `json:"censored_runs"`
	BudgetMillis         int      `json:"statement_budget_ms"`
	Rows                 int      `json:"expected_rows"`
	Status               string   `json:"status"`
	P50Millis            *float64 `json:"completed_p50_ms,omitempty"`
	P95Millis            *float64 `json:"completed_p95_ms,omitempty"`
	ExplainStatus        string   `json:"explain_status"`
	ExecutionMS          *float64 `json:"explain_execution_ms,omitempty"`
	TempReadBlocks       *int64   `json:"temp_read_blocks,omitempty"`
	TempWriteBlocks      *int64   `json:"temp_write_blocks,omitempty"`
	MaxEligibleScanLoops *float64 `json:"max_eligible_scan_loops,omitempty"`
}

func accountWindowSQLPerformance(t *testing.T, tx *sql.Tx, ctx context.Context, since, until time.Time, scale int, name, query string) (accountWindowPerfSample, error) {
	t.Helper()
	budget := 3 * time.Second
	if name == "legacy" {
		budget = 2 * time.Second
	}
	sample := accountWindowPerfSample{Scale: scale, Method: name, Rows: 56, BudgetMillis: int(budget / time.Millisecond), Status: "complete", ExplainStatus: "not_run"}
	_, err := tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout='%dms'", budget/time.Millisecond))
	if err != nil {
		return sample, err
	}
	var durations []float64
	for run := 0; run < 33; run++ {
		if _, err := tx.ExecContext(ctx, `SAVEPOINT window_benchmark`); err != nil {
			return sample, err
		}
		callCtx := ctx
		cancel := func() {}
		if name != "legacy" {
			callCtx, cancel = context.WithTimeout(ctx, budget)
		}
		started := time.Now()
		rows, readErr := readAccountWindowSQLRows(callCtx, tx, query, since, until, nil)
		elapsed := float64(time.Since(started).Microseconds()) / 1000
		cancel()
		if run >= 3 {
			sample.Runs++
		}
		if readErr != nil {
			_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT window_benchmark`)
			if rollbackErr != nil {
				sample.Status = "failed"
				return sample, errors.Join(readErr, rollbackErr)
			}
			var pgErr *pq.Error
			censored := name == "legacy" && ctx.Err() == nil && errors.As(readErr, &pgErr) && pgErr.Code == "57014"
			if !censored {
				sample.Status = "failed"
				return sample, readErr
			}
			if run >= 3 {
				sample.CensoredRuns++
				sample.Status = "censored"
			}
		} else {
			if len(rows) != 56 {
				sample.Status = "failed"
				return sample, fmt.Errorf("%s returned %d accounts", name, len(rows))
			}
			if name != "legacy" && elapsed >= 3000 {
				sample.Status = "failed"
				return sample, fmt.Errorf("new SQL exceeded strict 3s budget: %.3fms", elapsed)
			}
			if run >= 3 {
				durations = append(durations, elapsed)
				sample.CompletedRuns++
			}
		}
		if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT window_benchmark`); err != nil {
			return sample, err
		}
	}
	if len(durations) > 0 {
		sort.Float64s(durations)
		p50, p95 := durations[len(durations)/2], durations[(len(durations)*95+99)/100-1]
		sample.P50Millis, sample.P95Millis = &p50, &p95
	}
	if name == "legacy" && sample.CompletedRuns == 0 {
		sample.ExplainStatus = "not_run_all_reads_censored"
		return sample, nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT window_explain`); err != nil {
		return sample, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, since, until, upstreamcacheguard.MaxRuntimeRows+1, pq.Array([]int64(nil))).Scan(&raw)
	if err != nil {
		_, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT window_explain`)
		if rollbackErr != nil {
			return sample, errors.Join(err, rollbackErr)
		}
		var pgErr *pq.Error
		if name == "legacy" && ctx.Err() == nil && errors.As(err, &pgErr) && pgErr.Code == "57014" {
			sample.ExplainStatus = "censored"
			return sample, nil
		}
		return sample, err
	}
	var plan []map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return sample, err
	}
	execution := plan[0]["Execution Time"].(float64)
	root := plan[0]["Plan"].(map[string]any)
	reads, writes := int64(root["Temp Read Blocks"].(float64)), int64(root["Temp Written Blocks"].(float64))
	loops := accountWindowEligibleScanLoops(root)
	sample.ExplainStatus = "complete"
	sample.ExecutionMS, sample.TempReadBlocks, sample.TempWriteBlocks, sample.MaxEligibleScanLoops = &execution, &reads, &writes, &loops
	if name == "set_based" && scale == 1 && (reads != 0 || writes != 0 || loops > 2) {
		return sample, fmt.Errorf("new query plan violates bounded current-scale IO: read=%d write=%d eligible_loops=%g", reads, writes, loops)
	}
	return sample, nil
}

type accountWindowEnginePerfSample struct {
	Scale                int                `json:"scale"`
	Runs                 int                `json:"runs"`
	InitialMillis        float64            `json:"initial_refresh_ms"`
	P50Millis            float64            `json:"engine_p50_ms"`
	P95Millis            float64            `json:"engine_p95_ms"`
	MaxMillis            float64            `json:"engine_max_ms"`
	WindowP95Millis      float64            `json:"account_windows_p95_ms"`
	AccountsPerQuery     int                `json:"accounts_per_query"`
	PoliciesAfterRebuild int                `json:"policies_after_rebuild"`
	StageP95Millis       map[string]float64 `json:"stage_p95_ms"`
}

type accountWindowMeasuredRefreshStore struct {
	*upstreamCacheGuardRepository
	windowQueries int
	windowIDs     int
}

func (s *accountWindowMeasuredRefreshStore) AccountWindowsFor(ctx context.Context, ids []int64, since, until time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	if len(ids) > 0 {
		s.windowQueries++
		s.windowIDs = len(ids)
	}
	return s.upstreamCacheGuardRepository.AccountWindowsFor(ctx, ids, since, until)
}

func accountWindowEnginePerformance(t *testing.T, db *sql.DB, ctx context.Context, store *upstreamCacheGuardRepository, since time.Time, scale int) accountWindowEnginePerfSample {
	t.Helper()
	var workMem string
	require.NoError(t, db.QueryRowContext(ctx, `SHOW work_mem`).Scan(&workMem))
	require.Equal(t, "4MB", workMem, "whole-engine queries must use the same bounded memory as the SQL comparison")
	measured := &accountWindowMeasuredRefreshStore{upstreamCacheGuardRepository: store}
	engine := upstreamcacheguard.NewEngine(measured)
	settings := ordinaryPGSettings()
	require.NoError(t, engine.ApplySettings(settings))
	policies := map[int64]upstreamcacheguard.Settings{}
	generations := map[string]string{}
	for i, group := range []int64{8, 21, 42} {
		s := settings
		s.ThresholdPercent += float64(i)
		policies[group] = s
		generations[upstreamcacheguard.PolicySignature(s)] = "synthetic-policy-generation"
	}
	require.NoError(t, engine.ApplyPolicySettings(policies))
	engine.SetOrdinaryRecoveryIdentityGeneration(ordinaryPGIdentity)
	engine.SetOrdinaryRecoveryPolicyGenerations(generations)
	refresh := func() (float64, map[string]float64) {
		measured.windowQueries, measured.windowIDs = 0, 0
		stages := map[string]float64{}
		budget, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		observed := upstreamcacheguard.WithStageObserver(budget, func(stage string, elapsed time.Duration) {
			stages[stage] += float64(elapsed.Microseconds()) / 1000
		})
		started := time.Now()
		require.NoError(t, engine.Refresh(observed))
		elapsed := float64(time.Since(started).Microseconds()) / 1000
		require.Equal(t, 1, measured.windowQueries, "every refresh must query windows instead of reusing the unchanged fast path")
		require.Equal(t, 56, measured.windowIDs)
		for account := int64(100); account < 156; account++ {
			snapshot, available := engine.CachedSharedAccount(account, account+1000)
			require.True(t, available, "account %d must publish a usable snapshot", account)
			require.False(t, snapshot.LoadedAt.IsZero())
		}
		return elapsed, stages
	}
	initial, _ := refresh()
	policyCount := 0
	for account := int64(100); account < 156; account++ {
		snapshot, _ := engine.CachedSharedAccount(account, account+1000)
		policyCount += len(snapshot.Policies)
	}
	var durations []float64
	stageDurations := map[string][]float64{}
	for run := 0; run < 30; run++ {
		// A real processed-column UPDATE invokes migration 299's fact-version
		// trigger once per account. Evidence/counts stay fixed, but all 56
		// identities are changed, so the engine must execute its window query.
		result, err := db.ExecContext(ctx, `UPDATE upstream_cache_guard_facts AS fact SET processed=fact.processed
			FROM (SELECT DISTINCT ON (account_id) group_id,evidence_id FROM upstream_cache_guard_facts
			WHERE completed_at >= $1 ORDER BY account_id,evidence_id) AS changed
			WHERE fact.group_id=changed.group_id AND fact.evidence_id=changed.evidence_id`, since)
		require.NoError(t, err)
		changed, err := result.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(56), changed)
		elapsed, stages := refresh()
		durations = append(durations, elapsed)
		for stage, millis := range stages {
			stageDurations[stage] = append(stageDurations[stage], millis)
		}
	}
	sort.Float64s(durations)
	stageP95 := map[string]float64{}
	for stage, samples := range stageDurations {
		require.Len(t, samples, 30, "refresh stage %s must run every time", stage)
		sort.Float64s(samples)
		stageP95[stage] = samples[28]
	}
	result := accountWindowEnginePerfSample{Scale: scale, Runs: 30, InitialMillis: initial,
		P50Millis: durations[15], P95Millis: durations[28], MaxMillis: durations[29],
		WindowP95Millis: stageP95["account_windows"], AccountsPerQuery: 56,
		PoliciesAfterRebuild: policyCount, StageP95Millis: stageP95}
	if scale == 1 {
		require.LessOrEqual(t, result.P95Millis, float64(1500), "current synthetic scale needs whole-engine budget headroom")
	}
	require.Less(t, result.MaxMillis, float64(3000), "all 30 whole-engine runs must finish inside the refresh budget")
	return result
}

func accountWindowRunnerPerformance(t *testing.T, db *sql.DB, ctx context.Context, scale int) (json.RawMessage, error) {
	t.Helper()
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return nil, err
	}
	receipt := os.Getenv("SUB2API_CACHE_GUARD_WINDOW_PERF_RECEIPT")
	if receipt == "" {
		receipt = filepath.Join(t.TempDir(), "window-performance.json")
	}
	receipt = strings.TrimSuffix(receipt, filepath.Ext(receipt)) + fmt.Sprintf("-runner-scale-%d.json", scale)
	receipt, err := filepath.Abs(receipt)
	if err != nil {
		return nil, err
	}
	childCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(childCtx, "go", "test", "-tags=cacheguardpg", "-p", "1", "-parallel", "1", "./internal/service", "-run", "^TestCacheGuardRunnerPostgresPerformance$", "-count=1", "-timeout=3m", "-v")
	// A Go package test starts in backend/internal/repository. The runner is
	// tested in a separate process to avoid the service/repository import cycle.
	command.Dir = filepath.Join("..", "..")
	inputs := map[string]string{
		"GOTOOLCHAIN":                             "go1.27.3",
		"SUB2API_CACHE_GUARD_TEST_ISOLATED":       "1",
		"SUB2API_CACHE_GUARD_RUNNER_PERF_DSN":     cacheGuardScopedDSN(os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN"), schema),
		"SUB2API_CACHE_GUARD_RUNNER_PERF_SCHEMA":  schema,
		"SUB2API_CACHE_GUARD_RUNNER_PERF_SCALE":   strconv.Itoa(scale),
		"SUB2API_CACHE_GUARD_RUNNER_PERF_RECEIPT": receipt,
	}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replace := inputs[strings.ToUpper(key)]; !replace {
			command.Env = append(command.Env, entry)
		}
	}
	for key, value := range inputs {
		command.Env = append(command.Env, key+"="+value)
	}
	// No DSN is logged; this is the same isolated schema already owned by the
	// parent. SQL/Engine measurements have finished before this serial child.
	output, runErr := command.CombinedOutput()
	data, readErr := os.ReadFile(receipt)
	if readErr == nil && !json.Valid(data) {
		readErr = errors.New("runner performance receipt is not valid JSON")
	}
	if readErr != nil {
		data = nil
	}
	if runErr != nil {
		return data, fmt.Errorf("real runner performance subprocess: %w\n%s", runErr, output)
	}
	if readErr != nil {
		return nil, readErr
	}
	t.Logf("real runner performance scale %d passed; receipt %s", scale, receipt)
	return data, nil
}

func TestCacheGuardAccountWindowsPostgresPerformanceComparison(t *testing.T) {
	if os.Getenv("SUB2API_CACHE_GUARD_WINDOW_PERF") != "1" {
		t.Skip("set SUB2API_CACHE_GUARD_WINDOW_PERF=1 for the serial synthetic-scale benchmark")
	}
	var report []accountWindowPerfSample
	var engineReport []accountWindowEnginePerfSample
	var runnerReport []json.RawMessage
	for _, scale := range []int{1, 4} {
		t.Run(fmt.Sprintf("scale_%d", scale), func(t *testing.T) {
			db, ctx, _, since, until := cacheGuardWindowPerformanceFixture(t, scale)
			require.Equal(t, 56, compareAccountWindowSQLWithBudget(t, db, since, until, nil, 90*time.Second))
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			require.NoError(t, err)
			defer tx.Rollback()
			_, err = tx.ExecContext(ctx, `SET LOCAL work_mem='4MB'`)
			require.NoError(t, err)
			for _, method := range []struct{ name, query string }{{"legacy", legacyAccountWindowsSQL}, {"set_based", upstreamCacheGuardAccountWindowsSQL}} {
				sample, err := accountWindowSQLPerformance(t, tx, ctx, since, until, scale, method.name, method.query)
				report = append(report, sample)
				require.NoError(t, err)
			}
			require.NoError(t, tx.Rollback())
			engineReport = append(engineReport, accountWindowEnginePerformance(t, db, ctx, NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository), since, scale))
			runner, err := accountWindowRunnerPerformance(t, db, ctx, scale)
			if len(runner) > 0 {
				runnerReport = append(runnerReport, runner)
			}
			require.NoError(t, err)
		})
	}
	encoded, err := json.MarshalIndent(struct {
		Passed        bool                            `json:"passed"`
		CheckedAt     string                          `json:"checked_at"`
		Note          string                          `json:"note"`
		Results       []accountWindowPerfSample       `json:"results"`
		EngineResults []accountWindowEnginePerfSample `json:"engine_results"`
		RunnerResults []json.RawMessage               `json:"runner_results"`
	}{!t.Failed(), time.Now().UTC().Format(time.RFC3339Nano), "Synthetic isolated PostgreSQL; 56 accounts, initially 51x3+5x2 policies, 120000 historical/5400 current/4500 valid current facts per scale; 1KiB synthetic metadata per request and admission (2KiB total), plus normal fields; private-schema bulk import seeds exact fact versions and restores the real trigger before measurements; work_mem 4MB; 3 warmups and 30 SQL attempts per method. Legacy statement budget is 2s and cancellations are censored with that lower bound, never imputed as completed durations; completed percentiles exclude censored attempts. New SQL and Engine retain strict 3s budgets. SQL timings include transport and exact-number 17-column normalization. All old/new columns compared in one repeatable-read snapshot. Engine measures initial rebuild plus 30 actual refreshes forced by fact-version changes for all 56 accounts, with a 3s budget; policy totals after rebuild are reported. Engine timing excludes runner settings/pending checks. runner_results are independent serial child tests of the actual runner refresh including settings/apply/engine/pending, against the same still-live private schema. No simulated delay, real model request, or production evidence.", report, engineReport, runnerReport}, "", "  ")
	require.NoError(t, err)
	if path := os.Getenv("SUB2API_CACHE_GUARD_WINDOW_PERF_RECEIPT"); path != "" {
		require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0600))
	}
	t.Log(string(encoded))
}

func accountWindowEligibleScanLoops(plan map[string]any) float64 {
	max := float64(0)
	if plan["Node Type"] == "CTE Scan" && plan["CTE Name"] == "eligible" {
		max, _ = plan["Actual Loops"].(float64)
	}
	if children, ok := plan["Plans"].([]any); ok {
		for _, child := range children {
			if loops := accountWindowEligibleScanLoops(child.(map[string]any)); loops > max {
				max = loops
			}
		}
	}
	return max
}

// Diagnostic only: no legacy attempts, no timing acceptance claim, and a
// deliberately separate opt-in from the full performance acceptance test.
func TestCacheGuardAccountWindowsPostgresDiagnosticPlan(t *testing.T) {
	if os.Getenv("SUB2API_CACHE_GUARD_WINDOW_DIAGNOSTIC") != "1" {
		t.Skip("diagnostic-only plan capture is opt-in")
	}
	for _, scale := range []int{1, 4} {
		t.Run(fmt.Sprintf("scale_%d", scale), func(t *testing.T) {
			db, ctx, _, since, until := cacheGuardWindowPerformanceFixture(t, scale)
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			require.NoError(t, err)
			defer tx.Rollback()
			_, err = tx.ExecContext(ctx, `SET LOCAL work_mem='4MB'; SET LOCAL statement_timeout='20s'`)
			require.NoError(t, err)
			var raw []byte
			require.NoError(t, tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) "+upstreamCacheGuardAccountWindowsSQL,
				since, until, upstreamcacheguard.MaxRuntimeRows+1, pq.Array([]int64(nil))).Scan(&raw))
			var plan []map[string]any
			require.NoError(t, json.Unmarshal(raw, &plan))
			if directory := os.Getenv("SUB2API_CACHE_GUARD_WINDOW_DIAGNOSTIC_DIR"); directory != "" {
				digest := sha256.Sum256([]byte(upstreamCacheGuardAccountWindowsSQL))
				path := filepath.Join(directory, fmt.Sprintf("window-plan-%x-scale-%d.json", digest[:6], scale))
				require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0600))
				t.Logf("DIAGNOSTIC ONLY full plan: %s", path)
			}
			t.Logf("DIAGNOSTIC ONLY scale=%d execution_ms=%v", scale, plan[0]["Execution Time"])
			var summarize func(map[string]any)
			summarize = func(node map[string]any) {
				brief := map[string]any{}
				for _, key := range []string{"Node Type", "CTE Name", "Relation Name", "Actual Total Time", "Actual Rows", "Actual Loops", "Plan Width", "Temp Read Blocks", "Temp Written Blocks", "Sort Space Used", "Sort Space Type", "Subplan Name"} {
					if value, ok := node[key]; ok {
						brief[key] = value
					}
				}
				encoded, _ := json.Marshal(brief)
				t.Log(string(encoded))
				if children, ok := node["Plans"].([]any); ok {
					for _, child := range children {
						summarize(child.(map[string]any))
					}
				}
			}
			summarize(plan[0]["Plan"].(map[string]any))
			if os.Getenv("SUB2API_CACHE_GUARD_WINDOW_DIAGNOSTIC_VALIDATE") == "1" {
				validation := struct {
					DiagnosticOnly bool                           `json:"diagnostic_only"`
					Passed         bool                           `json:"passed"`
					Scale          int                            `json:"scale"`
					SQL            *accountWindowPerfSample       `json:"sql,omitempty"`
					Engine         *accountWindowEnginePerfSample `json:"engine,omitempty"`
					Runner         json.RawMessage                `json:"runner,omitempty"`
				}{DiagnosticOnly: true, Scale: scale}
				defer func() {
					validation.Passed = !t.Failed()
					if directory := os.Getenv("SUB2API_CACHE_GUARD_WINDOW_DIAGNOSTIC_DIR"); directory != "" {
						digest := sha256.Sum256([]byte(upstreamCacheGuardAccountWindowsSQL))
						encoded, err := json.MarshalIndent(validation, "", "  ")
						require.NoError(t, err)
						require.NoError(t, os.WriteFile(filepath.Join(directory, fmt.Sprintf("window-diagnostic-validation-%x-scale-%d.json", digest[:6], scale)), append(encoded, '\n'), 0600))
					}
				}()
				require.NoError(t, tx.Rollback())
				bench, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
				require.NoError(t, err)
				defer bench.Rollback()
				_, err = bench.ExecContext(ctx, `SET LOCAL work_mem='4MB'`)
				require.NoError(t, err)
				sample, err := accountWindowSQLPerformance(t, bench, ctx, since, until, scale, "set_based", upstreamCacheGuardAccountWindowsSQL)
				validation.SQL = &sample
				require.NoError(t, err)
				require.NoError(t, bench.Rollback())
				engine := accountWindowEnginePerformance(t, db, ctx, NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository), since, scale)
				validation.Engine = &engine
				validation.Runner, err = accountWindowRunnerPerformance(t, db, ctx, scale)
				require.NoError(t, err)
			}
		})
	}
}
