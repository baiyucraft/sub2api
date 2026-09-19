package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupTTFTGuardPolicyMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "278_fork_group_ttft_guard_policies.sql")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS fork_group_ttft_guard_policies")
	require.Contains(t, sql, "group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CHECK (mode IN ('inherit', 'enabled', 'disabled'))")
	require.Contains(t, sql, "CHECK (degradation_ttft_seconds BETWEEN 5 AND 300)")
	require.Contains(t, sql, "CHECK (min_samples BETWEEN 2 AND 20)")
	require.Contains(t, sql, "created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	require.Contains(t, sql, "updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	require.NotContains(t, sql, "ALTER TABLE groups")
}
