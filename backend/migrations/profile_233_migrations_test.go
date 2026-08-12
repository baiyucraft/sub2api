package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamManagementMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "233_upstream_management.sql")
	require.Contains(t, sql, "WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL GROUP BY upstream_key_id HAVING COUNT(*) > 1")
	require.Contains(t, sql, "RAISE EXCEPTION 'upstream account key duplicate preflight failed'")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_upstream_key_id_active ON accounts(upstream_key_id) WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_health_observations")
	require.Contains(t, sql, "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE upstream_health_observations TO CURRENT_USER")
	require.Contains(t, sql, "GRANT USAGE, SELECT ON SEQUENCE upstream_health_observations_id_seq TO CURRENT_USER")
	require.Contains(t, sql, "NEW.priority := CEIL(key_actual_rate * 100)::INTEGER")
	require.NotContains(t, sql, "NEW.load_factor :=")
	require.NotContains(t, sql, "upstream account concurrency cannot derive a safe load factor")
	require.NotContains(t, sql, "DELETE FROM accounts")
	require.NotContains(t, sql, "UPDATE accounts")
}
