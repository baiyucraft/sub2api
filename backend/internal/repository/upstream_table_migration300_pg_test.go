//go:build upstreamtablepg

package repository

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUpstreamTableMigration300Compatibility(t *testing.T) {
	dsn := os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_DSN")
	if dsn == "" {
		t.Skip("dedicated PostgreSQL test database required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_UPSTREAM_TABLE_TEST_ISOLATED"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	var database string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_database()").Scan(&database))
	require.Contains(t, strings.ToLower(database), "test")
	// Frozen legacy table contract from migrations 233, 248 and 250. This is
	// persistent only in the caller-owned disposable database, never production.
	_, err = db.ExecContext(ctx, `CREATE TABLE upstream_health_observations (
 id BIGSERIAL PRIMARY KEY, upstream_config_id BIGINT NOT NULL, upstream_key_id BIGINT NOT NULL,
 account_id BIGINT, platform VARCHAR(50) NOT NULL DEFAULT '', model VARCHAR(255) NOT NULL DEFAULT '',
 protocol VARCHAR(50) NOT NULL DEFAULT '', source VARCHAR(20) NOT NULL DEFAULT 'probe', state VARCHAR(20) NOT NULL,
 result VARCHAR(100) NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', http_status INTEGER, ttft_ms BIGINT,
 duration_ms BIGINT,input_tokens BIGINT,output_tokens BIGINT,output_tps DOUBLE PRECISION,
 observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 confidence_score INTEGER,confidence_prompt_version VARCHAR(64),requested_effort VARCHAR(32),reasoning_tokens BIGINT,
 confidence_checks JSONB,confidence_status VARCHAR(32),confidence_evidence JSONB);
 CREATE INDEX idx_upstream_health_observations_key_observed ON upstream_health_observations(upstream_key_id,observed_at);
 INSERT INTO upstream_health_observations(upstream_config_id,upstream_key_id,state,observed_at) VALUES (1,7,'healthy','2026-10-17T12:00:00Z')`)
	require.NoError(t, err)
	defer db.ExecContext(context.Background(), "DROP TABLE upstream_health_observations")
	columnsSQL := `SELECT json_agg(row(column_name,data_type,is_nullable,column_default,character_maximum_length) ORDER BY ordinal_position)::text FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_health_observations'`
	var before, after string
	require.NoError(t, db.QueryRowContext(ctx, columnsSQL).Scan(&before))
	migration, err := os.ReadFile(os.Getenv("SUB2API_MIGRATION300_PATH"))
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = db.ExecContext(ctx, string(migration))
		require.NoError(t, err)
	}
	require.NoError(t, db.QueryRowContext(ctx, columnsSQL).Scan(&after))
	require.Equal(t, before, after, "migration must not alter legacy columns/defaults/nullability")
	var keys string
	var unique bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT array_to_string(ARRAY(SELECT pg_get_indexdef(indexrelid,n,true) FROM generate_series(1,indnkeyatts) n),','),indisunique FROM pg_index WHERE indexrelid='idx_upstream_health_observations_key_recent'::regclass`).Scan(&keys, &unique))
	require.Equal(t, "upstream_key_id,observed_at,id", keys)
	require.False(t, unique)
	var directions string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT indoption::text FROM pg_index WHERE indexrelid='idx_upstream_health_observations_key_recent'::regclass`).Scan(&directions))
	require.Equal(t, "0 3 3", directions, "ASC key and DESC time/id, including existing null-order semantics")
	// Old insert/update contracts still work, and equal timestamps retain id ties.
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_health_observations(upstream_config_id,upstream_key_id,state,observed_at) VALUES (1,7,'degraded','2026-10-17T12:00:00Z'); UPDATE upstream_health_observations SET reason='legacy-write',confidence_score=99 WHERE id=2`)
	require.NoError(t, err)
	var id int64
	var reason string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT id,reason FROM upstream_health_observations WHERE upstream_key_id=7 ORDER BY observed_at DESC,id DESC LIMIT 1`).Scan(&id, &reason))
	require.EqualValues(t, 2, id)
	require.Equal(t, "legacy-write", reason)
}
