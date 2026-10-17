package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	observation "github.com/Wei-Shaw/sub2api/ent/upstreamhealthobservation"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Optional EXPLAIN artifacts use only the caller's synthetic TEMP tables.
func writeUpstreamTableExplain(t *testing.T, ctx context.Context, db sqlExecutor, name, query string, args ...any) {
	t.Helper()
	directory := os.Getenv("SUB2API_UPSTREAM_TABLE_EXPLAIN_DIR")
	if directory == "" {
		return
	}
	rows, err := db.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...)
	require.NoError(t, err)
	defer rows.Close()
	require.True(t, rows.Next())
	var plan []byte
	require.NoError(t, rows.Scan(&plan))
	require.NoError(t, rows.Err())
	require.NoError(t, os.MkdirAll(directory, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, name+".json"), plan, 0600))
}

// This opt-in test only creates connection-local TEMP tables. It never migrates
// or modifies persistent tables. Run it against a dedicated synthetic database.
func TestUpstreamHealthPostgresExactEquivalence(t *testing.T) {
	dsn := os.Getenv("SUB2API_HEALTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUB2API_HEALTH_TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1) // TEMP tables must use the same backend session.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `CREATE TEMP TABLE upstream_keys (id bigint PRIMARY KEY, deleted_at timestamptz, extra jsonb DEFAULT '{}');
 INSERT INTO upstream_keys(id) VALUES (1),(2),(3),(4),(5),(6);
 CREATE TEMP TABLE upstream_health_observations (
 id bigint PRIMARY KEY, upstream_config_id bigint NOT NULL DEFAULT 1,
 upstream_key_id bigint NOT NULL, account_id bigint, platform text NOT NULL DEFAULT 'openai',
 model text NOT NULL DEFAULT '',protocol text NOT NULL DEFAULT '', source text NOT NULL DEFAULT 'probe',
 state text NOT NULL DEFAULT 'healthy', result text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '',
 http_status int,ttft_ms bigint,duration_ms bigint,input_tokens bigint,output_tokens bigint,output_tps float8,
 confidence_score int,confidence_prompt_version text,requested_effort text,reasoning_tokens bigint,
 confidence_checks jsonb,confidence_status text,confidence_evidence jsonb,observed_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX ON upstream_health_observations(upstream_key_id,observed_at DESC,id DESC)`)
	require.NoError(t, err)
	now := time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, pq.CopyIn("upstream_health_observations", "id", "upstream_key_id", "platform", "source", "confidence_prompt_version", "confidence_checks", "confidence_evidence", "confidence_score", "requested_effort", "reasoning_tokens", "confidence_status", "observed_at"))
	require.NoError(t, err)
	random := rand.New(rand.NewSource(73511))
	for i := 1; i <= 5000; i++ {
		at := now.Add(-time.Duration(random.Intn(8*24*60)) * time.Minute)
		// Pin both inclusive boundaries, future facts and deterministic ID ties.
		switch i % 11 {
		case 0:
			at = now.Add(-24 * time.Hour)
		case 1:
			at = now.Add(-7 * 24 * time.Hour)
		case 2:
			at = now.Add(-7*24*time.Hour - time.Microsecond)
		case 3:
			at = now.Add(time.Minute)
		}
		platform, source, version := "openai", "probe", service.UpstreamConfidencePromptVersion
		if i%17 == 0 {
			platform = "anthropic"
		}
		if i%19 == 0 {
			source = "business"
		}
		if i%23 == 0 {
			version = "different-version"
		}
		checks := map[string]int{"attempted": random.Intn(4) - 1, "valid_completed": random.Intn(5), "current_success": random.Intn(3), "mixed": random.Intn(2), "unsuccessful": random.Intn(2), "network_error": random.Intn(2)}
		if i%7 == 0 {
			checks = nil
		}
		kinds := []any{"juice", "coverage", "output_integrity", "unknown", "", nil, 3, false}
		evidence := map[string]any{"kind": kinds[i%len(kinds)], "classification": "output_rewrite_40_prefix", "hard_anomaly": i%2 == 0}
		if i%9 == 0 {
			evidence["hard_anomaly"] = "true"
		}
		var score any
		if i%4 != 0 {
			score = random.Intn(101)
		}
		checksJSON, err := json.Marshal(checks)
		require.NoError(t, err)
		evidenceJSON, err := json.Marshal(evidence)
		require.NoError(t, err)
		_, err = stmt.ExecContext(ctx, i, 1+i%5, platform, source, version, string(checksJSON), string(evidenceJSON), score, "high", i, "mixed", at)
		require.NoError(t, err)
	}
	_, err = stmt.ExecContext(ctx)
	require.NoError(t, err)
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	// Explicit null/unknown-field evidence with equal timestamps: the higher ID
	// determines latest evidence, while the previous row supplies the last score.
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(id,upstream_key_id,confidence_prompt_version,observed_at,confidence_checks,confidence_evidence,confidence_score) VALUES
 (5001,1,$1,$2,'{"unknown":null,"attempted":0,"valid_completed":null}','null',88),
 (5002,1,$1,$2,NULL,NULL,NULL)`, service.UpstreamConfidencePromptVersion, now.Add(2*time.Minute))
	require.NoError(t, err)

	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := &upstreamConfigRepository{client: client}
	ids := []int64{1, 2, 3, 4, 5, 6}
	_, err = db.ExecContext(ctx, "ANALYZE upstream_health_observations; ANALYZE upstream_keys")
	require.NoError(t, err)
	for _, version := range []string{"baseline", "optimized"} {
		query := os.Getenv("SUB2API_HEALTH_HISTORY_" + version + "_SQL")
		if query != "" {
			writeUpstreamTableExplain(t, ctx, db, "health_history_"+version, query, pq.Array(ids), 24)
		}
	}
	writeUpstreamTableExplain(t, ctx, db, "health_confidence_optimized", upstreamHealthConfidenceSummarySQL, pq.Array(ids),
		service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour), confidenceCountBoundary(now.Add(-24*time.Hour)), confidenceCountBoundary(now.Add(-7*24*time.Hour)))
	if query := os.Getenv("SUB2API_HEALTH_CONFIDENCE_BEFORE_DEDUP_SQL"); query != "" {
		writeUpstreamTableExplain(t, ctx, db, "health_confidence_before_dedup", query, pq.Array(ids), service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour))
	}
	// Frozen legacy Ent reader's complete entity projection for this schema.
	writeUpstreamTableExplain(t, ctx, db, "health_confidence_baseline", `SELECT * FROM upstream_health_observations WHERE upstream_key_id=ANY($1) AND platform=$2 AND source='probe' AND confidence_prompt_version=$3 AND observed_at >= $4 ORDER BY observed_at DESC,id DESC`, pq.Array(ids), service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour))
	started := time.Now()
	got, err := repo.GetUpstreamHealthConfidencesAt(ctx, ids, now)
	require.NoError(t, err)
	t.Logf("aggregate 5002 observations / 6 keys: %s", time.Since(started))
	started = time.Now()
	for _, id := range ids {
		rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(id), observation.PlatformEQ(service.PlatformOpenAI), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(now.Add(-7*24*time.Hour))).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).All(ctx)
		require.NoError(t, err)
		require.Equal(t, aggregateUpstreamHealthConfidence(now, rows), got[id], "full-window oracle key %d", id)
	}
	t.Logf("legacy full-entity reads / 6 keys: %s", time.Since(started))
	// SQL timestamp parameters round to microseconds while the legacy Go age
	// comparison retains nanoseconds. Probe both sides of each stored boundary.
	for _, offset := range []time.Duration{-900, -100, 100, 900} {
		evaluation := now.Add(offset)
		actual, err := repo.GetUpstreamHealthConfidencesAt(ctx, ids, evaluation)
		require.NoError(t, err)
		for _, id := range ids {
			rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(id), observation.PlatformEQ(service.PlatformOpenAI), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(evaluation.Add(-7*24*time.Hour))).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).All(ctx)
			require.NoError(t, err)
			require.Equal(t, aggregateUpstreamHealthConfidence(evaluation, rows), actual[id], "nanosecond offset %s key %d", offset, id)
		}
	}

	for _, limit := range []int{1, 24} {
		histories, err := repo.listPersistedUpstreamHealthHistories(ctx, ids, limit)
		require.NoError(t, err)
		for _, id := range ids {
			rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(id)).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).Limit(limit).All(ctx)
			require.NoError(t, err)
			var want []service.UpstreamHealthObservation
			for i := len(rows) - 1; i >= 0; i-- {
				want = append(want, upstreamHealthObservationEntityToService(rows[i]))
			}
			require.Equal(t, want, histories[id], "bounded recent history key %d limit %d", id, limit)
		}
	}
	_, err = db.ExecContext(ctx, `UPDATE upstream_keys SET deleted_at=$1 WHERE id=5`, now)
	require.NoError(t, err)
	histories, err := repo.ListKeyHealthHistories(ctx, []int64{1, 5, 6, 999}, 24)
	require.NoError(t, err)
	require.Len(t, histories[1], 24)
	require.NotContains(t, histories, int64(5), "soft-deleted keys retain observations but never reappear in display histories")
	require.NotContains(t, histories, int64(999))
	if os.Getenv("SUB2API_UPSTREAM_TABLE_EXPLAIN_DIR") != "" {
		// Worst-case shape cardinality: every valid checks map has a distinct,
		// otherwise irrelevant integer field. Classification must remain equal.
		_, err = db.ExecContext(ctx, `UPDATE upstream_health_observations SET confidence_checks=COALESCE(NULLIF(confidence_checks,'null'::jsonb),'{}'::jsonb)||jsonb_build_object('unique_fixture',id)`)
		require.NoError(t, err)
		writeUpstreamTableExplain(t, ctx, db, "health_confidence_optimized_all_unique", upstreamHealthConfidenceSummarySQL, pq.Array(ids), service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour), confidenceCountBoundary(now.Add(-24*time.Hour)), confidenceCountBoundary(now.Add(-7*24*time.Hour)))
		if query := os.Getenv("SUB2API_HEALTH_CONFIDENCE_BEFORE_DEDUP_SQL"); query != "" {
			writeUpstreamTableExplain(t, ctx, db, "health_confidence_before_dedup_all_unique", query, pq.Array(ids), service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour))
		}
		actual, err := repo.GetUpstreamHealthConfidencesAt(ctx, ids, now)
		require.NoError(t, err)
		for _, id := range ids {
			rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(id), observation.PlatformEQ(service.PlatformOpenAI), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(now.Add(-7*24*time.Hour))).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).All(ctx)
			require.NoError(t, err)
			require.Equal(t, aggregateUpstreamHealthConfidence(now, rows), actual[id], "unique checks key %d", id)
		}
	}
	// The interface-valued legacy evidence decoder accepts underflow (including
	// signed zero) and rounds some decimal values greater than MaxFloat64 back
	// to MaxFloat64. Validate the overflow rounding boundary rather than casting
	// every PostgreSQL numeric to float8, which would also reject underflow.
	overflowBoundary := new(big.Int).Sub(
		new(big.Int).Lsh(big.NewInt(1), 1024),
		new(big.Int).Lsh(big.NewInt(1), 970),
	)
	belowOverflowBoundary := new(big.Int).Sub(new(big.Int).Set(overflowBoundary), big.NewInt(1))
	for _, evidence := range []string{
		`{"unknown":1e-1000}`,
		`{"unknown":-1e-1000}`,
		`{"unknown":[1e-324,-1e-324,{"nested":1e-1000}]}`,
		`{"unknown":1.7976931348623157e308}`,
		`{"unknown":1.7976931348623158e308}`,
		`{"unknown":-1.7976931348623158e308}`,
		`{"unknown":` + belowOverflowBoundary.String() + `}`,
		`{"unknown":-` + belowOverflowBoundary.String() + `}`,
		`{"unknown":"1e1000","nested":["-1e1000",null,true]}`,
	} {
		_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(id,upstream_key_id,confidence_checks,confidence_evidence,confidence_prompt_version,observed_at) VALUES (5998,1,'{}',$1::jsonb,$2,$3)`, evidence, service.UpstreamConfidencePromptVersion, now.Add(-48*time.Hour))
		require.NoError(t, err)
		rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(1), observation.PlatformEQ(service.PlatformOpenAI), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(now.Add(-7*24*time.Hour))).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).All(ctx)
		require.NoError(t, err, "legacy reader must accept finite/underflow evidence %s", evidence)
		actual, err := repo.GetUpstreamHealthConfidencesAt(ctx, []int64{1}, now)
		require.NoError(t, err, "aggregate must accept finite/underflow evidence %s", evidence)
		require.Equal(t, aggregateUpstreamHealthConfidence(now, rows), actual[1], "finite/underflow evidence %s", evidence)
		_, err = db.ExecContext(ctx, `DELETE FROM upstream_health_observations WHERE id=5998`)
		require.NoError(t, err)
	}
	// The previous Ent reader rejects malformed JSON even on old/non-latest
	// observations. SQL aggregation must not silently reinterpret such facts.
	for _, bad := range []struct{ checks, evidence string }{
		{`{"unknown":"1"}`, `{}`},
		{`{"valid_completed":"1"}`, `{}`},
		{`{"unknown":1.5}`, `{}`},
		{`{"unknown":1.0}`, `{}`},
		{`{"unknown":true}`, `{}`},
		{`{"unknown":{}}`, `{}`},
		{`"checks"`, `{}`},
		{`{"unknown":9223372036854775808}`, `{}`},
		{`[]`, `{}`},
		{`{}`, `[]`},
		{`{}`, `1`},
		{`{}`, `"evidence"`},
		{`{}`, `{"unknown":1e1000}`},
		{`{}`, `{"unknown":-1e1000}`},
		{`{}`, `{"unknown":[0,{"nested":[1e1000]}]}`},
		{`{}`, `{"unknown":{"nested":[-1e1000]}}`},
		{`{}`, `{"unknown":1.7976931348623159e308}`},
		{`{}`, `{"unknown":-1.7976931348623159e308}`},
		{`{}`, `{"unknown":` + overflowBoundary.String() + `}`},
		{`{}`, `{"unknown":-` + overflowBoundary.String() + `}`},
	} {
		// A valid map with numerically equal JSONB must never hide an invalid
		// fractional representation when validation deduplicates repeated maps.
		_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(id,upstream_key_id,confidence_checks,confidence_evidence,confidence_prompt_version,observed_at) VALUES (5999,1,'{"unknown":1}','{}',$1,$2)`, service.UpstreamConfidencePromptVersion, now.Add(-48*time.Hour))
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(id,upstream_key_id,confidence_checks,confidence_evidence,confidence_prompt_version,observed_at) VALUES (6000,1,$1::jsonb,$2::jsonb,$3,$4)`, bad.checks, bad.evidence, service.UpstreamConfidencePromptVersion, now.Add(-48*time.Hour))
		require.NoError(t, err)
		_, oracleErr := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(1), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(now.Add(-7*24*time.Hour))).All(ctx)
		require.Error(t, oracleErr, "legacy reader must reject malformed fact %s / %s", bad.checks, bad.evidence)
		_, aggregateErr := repo.GetUpstreamHealthConfidencesAt(ctx, []int64{1}, now)
		require.Error(t, aggregateErr, "aggregate must reject malformed fact %s / %s", bad.checks, bad.evidence)
		_, err = db.ExecContext(ctx, `DELETE FROM upstream_health_observations WHERE id IN (5999,6000)`)
		require.NoError(t, err)
	}
	for _, extreme := range []int64{math.MaxInt64, math.MinInt64} {
		checks, err := json.Marshal(map[string]int64{"attempted": extreme, "valid_completed": extreme, "current_success": extreme, "mixed": extreme, "unsuccessful": extreme, "network_error": extreme})
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(id,upstream_key_id,confidence_checks,confidence_evidence,confidence_prompt_version,observed_at) VALUES (7001,1,$1::jsonb,'{}',$2,$3),(7002,1,$1::jsonb,'{}',$2,$3)`, string(checks), service.UpstreamConfidencePromptVersion, now)
		require.NoError(t, err)
		actual, err := repo.GetUpstreamHealthConfidencesAt(ctx, []int64{1}, now)
		require.NoError(t, err)
		rows, err := client.UpstreamHealthObservation.Query().Where(observation.UpstreamKeyIDEQ(1), observation.PlatformEQ(service.PlatformOpenAI), observation.SourceEQ("probe"), observation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion), observation.ObservedAtGTE(now.Add(-7*24*time.Hour))).Order(dbent.Desc(observation.FieldObservedAt), dbent.Desc(observation.FieldID)).All(ctx)
		require.NoError(t, err)
		require.Equal(t, aggregateUpstreamHealthConfidence(now, rows), actual[1], "extreme integer aggregation %d", extreme)
		_, err = db.ExecContext(ctx, `DELETE FROM upstream_health_observations WHERE id IN (7001,7002)`)
		require.NoError(t, err)
	}

}
