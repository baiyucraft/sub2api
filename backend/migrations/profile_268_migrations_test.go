package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile268CacheGuardMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("296_upstream_cache_guard.sql")
	require.NoError(t, err)
	require.Equal(t, "2b898a8b429766799819f7ee83052adfc269bbb58f493ea44ebbc023f1576b5d", fmt.Sprintf("%x", sha256.Sum256(content)))
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS upstream_cache_guard_facts",
		"PRIMARY KEY (group_id, evidence_id)",
		"processed BOOLEAN NOT NULL DEFAULT FALSE",
		"claim_owner VARCHAR(64)", "claim_until TIMESTAMPTZ",
		"WHERE valid_sample = TRUE AND processed = TRUE",
		"idx_upstream_cache_guard_pending", "WHERE processed = FALSE",
		"CREATE TABLE IF NOT EXISTS upstream_cache_guard_runtime",
		"PRIMARY KEY (group_id, state_kind, state_key)",
		"state_kind IN ('group', 'account', 'session')",
		"jsonb_typeof(evidence_json) = 'object'", "jsonb_typeof(state_json) = 'object'",
	} {
		require.Contains(t, sql, marker)
	}
	// This migration is independent of billing and account entitlement tables.
	for _, marker := range []string{"ALTER TABLE users", "ALTER TABLE usage_logs", "UPDATE users", "UPDATE accounts"} {
		require.NotContains(t, sql, marker)
	}
}
