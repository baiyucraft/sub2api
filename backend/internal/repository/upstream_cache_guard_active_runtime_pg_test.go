//go:build cacheguardpg

package repository

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Freeze the pre-fix query independently of the production query so a new
// planner shape must preserve exactly the existing row and boundary contract.
//
//go:embed testdata/upstream_cache_guard_active_runtime_legacy.sql
var legacyActiveRuntimeRowsSQL string

func readActiveRuntimeSQL(ctx context.Context, db windowSQLReader, query string, active []upstreamcacheguard.SessionIdentity, since time.Time, limit int) ([]upstreamcacheguard.RuntimeRow, error) {
	groups, keys, versions := make([]int64, len(active)), make([]string, len(active)), make([]int64, len(active))
	for i, session := range active {
		groups[i], keys[i], versions[i] = session.GroupID, session.Key, session.Version
	}
	rows, err := db.QueryContext(ctx, query, since, pq.Array(groups), pq.Array(keys), limit, pq.Array(versions))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []upstreamcacheguard.RuntimeRow
	for rows.Next() {
		var row upstreamcacheguard.RuntimeRow
		if err := rows.Scan(&row.GroupID, &row.Kind, &row.Key, &row.Version, &row.Payload, &row.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func explainActiveRuntimeSQL(ctx context.Context, tx *sql.Tx, query string, active []upstreamcacheguard.SessionIdentity, since time.Time) (json.RawMessage, error) {
	groups, keys, versions := make([]int64, len(active)), make([]string, len(active)), make([]int64, len(active))
	for i, session := range active {
		groups[i], keys[i], versions[i] = session.GroupID, session.Key, session.Version
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, since, pq.Array(groups), pq.Array(keys), upstreamcacheguard.MaxRuntimeRows+1, pq.Array(versions)).Scan(&raw)
	return json.RawMessage(raw), err
}

func activeRuntimeResidentScanLoops(node map[string]any) float64 {
	var max float64
	if node["Node Type"] == "CTE Scan" && node["CTE Name"] == "resident" {
		max, _ = node["Actual Loops"].(float64)
	}
	children, _ := node["Plans"].([]any)
	for _, raw := range children {
		child, _ := raw.(map[string]any)
		if loops := activeRuntimeResidentScanLoops(child); loops > max {
			max = loops
		}
	}
	return max
}

func TestCacheGuardActiveRuntimePostgresMatchesFrozenQuery(t *testing.T) {
	db, ctx := cacheGuardAdmissionPostgres(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-30 * time.Minute)
	type seed struct {
		group     int64
		kind, key string
		version   int64
		at        time.Time
	}
	for _, row := range []seed{
		{8, "group", "quota", 2, since.Add(-time.Hour)},
		{9, "group", "quota", 3, now},
		{8, "account", "ignored", 1, now},
		{8, "session", "recent-unchanged", 5, now},
		{8, "session", "recent-changed", 6, now},
		{8, "session", "recent-unknown", 1, now},
		{8, "session", "boundary", 1, since},
		{8, "session", "old-changed", 8, since.Add(-time.Microsecond)},
		{8, "session", "old-unchanged", 3, since.Add(-time.Hour)},
		{8, "session", "old-not-resident", 1, since.Add(-time.Hour)},
		{9, "session", "recent-changed", 7, now},
		{10, "session", "recent-changed", 7, since.Add(-time.Hour)},
	} {
		_, err := db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at) VALUES($1,$2,$3::text,$4,jsonb_build_object('key',$3::text),$5)`, row.group, row.kind, row.key, row.version, row.at)
		require.NoError(t, err)
	}
	active := []upstreamcacheguard.SessionIdentity{
		{GroupID: 8, Key: "recent-unchanged", Version: 5},
		{GroupID: 8, Key: "recent-changed", Version: 5},
		{GroupID: 8, Key: "old-changed", Version: 7},
		{GroupID: 8, Key: "old-unchanged", Version: 3},
		{GroupID: 9, Key: "recent-changed", Version: 7},
		{GroupID: 10, Key: "recent-changed", Version: 6},
		{GroupID: 8, Key: "no-durable-row", Version: 1},
	}
	for _, tc := range []struct {
		name     string
		resident []upstreamcacheguard.SessionIdentity
	}{
		{"cold", nil}, {"empty", []upstreamcacheguard.SessionIdentity{}}, {"mixed", active},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
			require.NoError(t, err)
			defer tx.Rollback()
			for _, limit := range []int{1, 4, upstreamcacheguard.MaxRuntimeRows + 1} {
				want, err := readActiveRuntimeSQL(ctx, tx, legacyActiveRuntimeRowsSQL, tc.resident, since, limit)
				require.NoError(t, err)
				got, err := readActiveRuntimeSQL(ctx, tx, upstreamCacheGuardActiveRuntimeRowsSQL, tc.resident, since, limit)
				require.NoError(t, err)
				require.Equal(t, want, got, "all six columns, identity, ordering and boundary must match")
			}
		})
	}
	store := NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)
	rows, err := store.ActiveRuntimeRows(ctx, active, since)
	require.NoError(t, err)
	var got []string
	for _, row := range rows {
		got = append(got, fmt.Sprintf("%d/%s/%s", row.GroupID, row.Kind, row.Key))
	}
	require.Equal(t, []string{"8/group/quota", "8/session/boundary", "8/session/old-changed", "8/session/recent-changed", "8/session/recent-unknown", "9/group/quota", "10/session/recent-changed"}, got)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = store.ActiveRuntimeRows(canceled, active, since)
	require.Error(t, err, "query must honor caller cancellation")
}

type activeRuntimePerfSample struct {
	Method   string          `json:"method"`
	Resident int             `json:"resident"`
	Run      int             `json:"run"`
	Status   string          `json:"status"`
	Millis   float64         `json:"elapsed_ms"`
	Rows     int             `json:"rows"`
	Plan     json.RawMessage `json:"plan,omitempty"`
}

// Opt-in, synthetic and disposable only. The legacy query has a strict server
// timeout, so reproducing a quadratic plan cannot hang a test process.
func TestCacheGuardActiveRuntimePostgresPerformance(t *testing.T) {
	if os.Getenv("SUB2API_CACHE_GUARD_RUNTIME_PERF") != "1" {
		t.Skip("set SUB2API_CACHE_GUARD_RUNTIME_PERF=1 for synthetic performance comparison")
	}
	db, _ := cacheGuardAdmissionPostgres(t)
	// Each legacy statement is capped at four seconds. Allow the entire
	// optional matrix enough time on slower development machines without
	// changing either the production refresh budget or its snapshot lifetime.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-30 * time.Minute)
	_, err := db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at)
	SELECT 5+(i%7),'session',lpad(to_hex(i),64,'0'),5,jsonb_build_object('key',lpad(to_hex(i),64,'0'),'body',repeat('x',300)),
	CASE WHEN i<=186000 THEN $1::timestamptz-interval '1 day' ELSE $1::timestamptz END FROM generate_series(1,186190) AS s(i)`, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at)
	SELECT i,'group','quota',1,'{}',$1 FROM generate_series(5,11) AS s(i)`, now)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `ANALYZE upstream_cache_guard_runtime`)
	require.NoError(t, err)
	var samples []activeRuntimePerfSample
	for _, n := range []int{0, 190, 5000, 20000, 65536} {
		active := make([]upstreamcacheguard.SessionIdentity, n)
		for i := range active {
			id := i + 1
			active[i] = upstreamcacheguard.SessionIdentity{GroupID: int64(5 + id%7), Key: fmt.Sprintf("%064x", id), Version: 5}
		}
		for run := 0; run < 3; run++ {
			for _, method := range []struct{ name, query string }{{"legacy", legacyActiveRuntimeRowsSQL}, {"optimized", upstreamCacheGuardActiveRuntimeRowsSQL}} {
				if filter := os.Getenv("SUB2API_CACHE_GUARD_RUNTIME_PERF_METHOD"); filter != "" && method.name != filter {
					continue
				}
				tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
				require.NoError(t, err)
				_, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout='4000ms'; SET LOCAL work_mem='4MB'`)
				require.NoError(t, err)
				started := time.Now()
				rows, err := readActiveRuntimeSQL(ctx, tx, method.query, active, since, upstreamcacheguard.MaxRuntimeRows+1)
				sample := activeRuntimePerfSample{Method: method.name, Resident: n, Run: run, Status: "ok", Millis: float64(time.Since(started).Microseconds()) / 1000, Rows: len(rows)}
				if err != nil {
					var pgErr *pq.Error
					if method.name == "legacy" && errors.As(err, &pgErr) && pgErr.Code == "57014" {
						sample.Status = "censored_statement_timeout"
					} else {
						require.NoError(t, err)
					}
				} else {
					require.Len(t, rows, 197)
				}
				require.NoError(t, tx.Rollback())
				if run == 0 && (n == 190 || n == 5000) && sample.Status == "ok" {
					planTx, planErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
					require.NoError(t, planErr)
					_, planErr = planTx.ExecContext(ctx, `SET LOCAL statement_timeout='4000ms'; SET LOCAL work_mem='4MB'`)
					require.NoError(t, planErr)
					sample.Plan, planErr = explainActiveRuntimeSQL(ctx, planTx, method.query, active, since)
					require.NoError(t, planErr)
					require.NoError(t, planTx.Rollback())
					if method.name == "optimized" && n == 5000 {
						var plan []map[string]any
						require.NoError(t, json.Unmarshal(sample.Plan, &plan))
						root, _ := plan[0]["Plan"].(map[string]any)
						require.LessOrEqual(t, activeRuntimeResidentScanLoops(root), float64(2), "resident CTE must not be rescanned once for every returned runtime identity")
					}
				}
				samples = append(samples, sample)
				t.Logf("runtime method=%s resident=%d run=%d status=%s elapsed_ms=%.3f rows=%d", sample.Method, n, run, sample.Status, sample.Millis, sample.Rows)
			}
		}
	}
	if path := os.Getenv("SUB2API_CACHE_GUARD_RUNTIME_PERF_OUTPUT"); path != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		raw, err := json.MarshalIndent(samples, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, raw, 0600))
	}
}
