package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAccountKeyUniqueMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "233_upstream_account_key_unique.sql")
	require.Contains(t, sql, "WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL GROUP BY upstream_key_id HAVING COUNT(*) > 1")
	require.Contains(t, sql, "RAISE EXCEPTION 'upstream account key duplicate preflight failed'")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_upstream_key_id_active ON accounts(upstream_key_id) WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL")
	require.NotContains(t, sql, "DELETE FROM accounts")
	require.NotContains(t, sql, "UPDATE accounts")
}
