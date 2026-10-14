//go:build cacheguardpg

package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGroupSchedulingProtectionPostgres(t *testing.T) {
	dsn := os.Getenv("SUB2API_CACHE_GUARD_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_CACHE_GUARD_TEST_DSN is required")
	}
	require.Equal(t, "1", os.Getenv("SUB2API_CACHE_GUARD_TEST_ISOLATED"), "disposable database acknowledgement required")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDB.Close() })
	var database string
	require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database))
	require.Contains(t, strings.ToLower(database), "test", "refusing non-test database")
	var nonce [8]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "protection_test_" + hex.EncodeToString(nonce[:])
	_, err = adminDB.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := adminDB.ExecContext(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err)
	})
	db, err := sql.Open("postgres", cacheGuardScopedDSN(dsn, schema))
	require.NoError(t, err)
	defer db.Close()
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := db.ExecContext(ctx, query, args...)
		require.NoError(t, err)
	}
	exec(`CREATE TABLE groups(id BIGINT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',platform TEXT NOT NULL DEFAULT 'openai',sort_order INT DEFAULT 0,deleted_at TIMESTAMPTZ);
 CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at TIMESTAMPTZ DEFAULT NOW());
 CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT);
 CREATE UNIQUE INDEX outbox_dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL;
 INSERT INTO groups(id) VALUES(1),(2),(3);
 INSERT INTO settings(key,value) VALUES('upstream_cache_guard_settings','{"mode":"protect","group_ids":[1,3]}');`)
	legacy, err := migrations.FS.ReadFile("278_fork_group_ttft_guard_policies.sql")
	require.NoError(t, err)
	exec(string(legacy))
	migration, err := migrations.FS.ReadFile("298_group_scheduling_protection_policies.sql")
	require.NoError(t, err)
	exec(string(migration))
	rows, err := db.QueryContext(ctx, `SELECT group_id,cache_mode FROM fork_group_scheduling_protection_policies ORDER BY group_id`)
	require.NoError(t, err)
	modes := map[int64]string{}
	for rows.Next() {
		var id int64
		var mode string
		require.NoError(t, rows.Scan(&id, &mode))
		modes[id] = mode
	}
	require.NoError(t, rows.Close())
	require.Equal(t, map[int64]string{1: "inherit", 2: "disabled", 3: "inherit"}, modes)
	var legacyIDs string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT value::jsonb->'group_ids' FROM settings WHERE key='upstream_cache_guard_settings'`).Scan(&legacyIDs))
	require.JSONEq(t, `[1,3]`, legacyIDs)
	exec(`UPDATE fork_group_scheduling_protection_policies SET cache_mode='enabled',priority='ttft_first' WHERE group_id=1`)
	exec(string(migration))
	var mode, priority string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT cache_mode,priority FROM fork_group_scheduling_protection_policies WHERE group_id=1`).Scan(&mode, &priority))
	require.Equal(t, "enabled", mode)
	require.Equal(t, "ttft_first", priority)
	for _, query := range []string{`UPDATE fork_group_scheduling_protection_policies SET cache_mode='invalid' WHERE group_id=1`, `UPDATE fork_group_scheduling_protection_policies SET threshold_percent=90,recovery_threshold_percent=80 WHERE group_id=1`, `UPDATE fork_group_scheduling_protection_policies SET min_valid_requests=0 WHERE group_id=1`, `UPDATE fork_group_scheduling_protection_policies SET priority='score' WHERE group_id=1`} {
		_, err := db.ExecContext(ctx, query)
		require.Error(t, err)
	}
	repo := &groupSchedulingProtectionPolicyRepository{db: db}
	write := service.GroupSchedulingProtectionPolicyWrite{TTFTMode: "enabled", TTFTThresholdSeconds: 12, TTFTMinSamples: 4, CacheMode: "enabled", ThresholdPercent: 70, RecoveryThresholdPercent: 80, MinValidRequests: 20, Priority: "cache_first"}
	_, err = repo.Put(ctx, 1, write)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox`).Scan(&count))
	require.Equal(t, 1, count)
	write.TTFTThresholdSeconds = 30
	write.RecoveryThresholdPercent = 60
	_, err = repo.Put(ctx, 1, write)
	require.Error(t, err)
	var threshold int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT degradation_ttft_seconds FROM fork_group_ttft_guard_policies WHERE group_id=1`).Scan(&threshold))
	require.Equal(t, 12, threshold, "cache write failure must roll back TTFT")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox`).Scan(&count))
	require.Equal(t, 1, count)
	// An empty former allowlist meant all groups, so migration creates no overrides.
	exec(`DELETE FROM fork_group_scheduling_protection_policies;UPDATE settings SET value='{"mode":"protect","group_ids":[]}' WHERE key='upstream_cache_guard_settings'`)
	exec(string(migration))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM fork_group_scheduling_protection_policies`).Scan(&count))
	require.Zero(t, count)
}
