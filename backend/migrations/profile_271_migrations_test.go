package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile271CacheGuardRevisionsMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("299_upstream_cache_guard_revisions.sql")
	require.NoError(t, err)
	require.Equal(t, "203ff042c1129f24549c696d6fdcb0dc27b0fedcff8f7e61413520b4bf9cc856", fmt.Sprintf("%x", sha256.Sum256(content)))
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS upstream_cache_guard_fact_versions",
		"PRIMARY KEY (account_id, upstream_key_id)",
		"SELECT account_id,upstream_key_id,1 FROM upstream_cache_guard_facts GROUP BY account_id,upstream_key_id ON CONFLICT DO NOTHING",
		"CREATE TRIGGER upstream_cache_guard_fact_revision AFTER INSERT OR UPDATE OF processed OR DELETE",
		"version=upstream_cache_guard_fact_versions.version+1",
		"ADD COLUMN IF NOT EXISTS has_pending_admissions BOOLEAN NOT NULL DEFAULT FALSE",
		"CREATE TRIGGER upstream_cache_guard_pending_sessions BEFORE INSERT OR UPDATE OF state_json",
		"CASE WHEN jsonb_typeof(NEW.state_json->'admissions')='object' THEN NEW.state_json->'admissions' ELSE '{}'::jsonb END",
		"UPDATE upstream_cache_guard_runtime SET state_json=state_json WHERE state_kind='session'",
		"idx_cache_guard_runtime_active_sessions", "idx_cache_guard_processed_account_time", "idx_cache_guard_runtime_pending_sessions",
	} {
		require.Contains(t, sql, marker)
	}
	for _, marker := range []string{"ALTER TABLE users", "ALTER TABLE usage_logs", "UPDATE users", "UPDATE accounts", "idx_cache_guard_runtime_legacy_identity"} {
		require.NotContains(t, sql, marker)
	}
}
