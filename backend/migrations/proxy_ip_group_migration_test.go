package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyIPGroupMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "279_proxy_ip_groups.sql")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS proxy_ip_groups")
	require.Contains(t, sql, "per_ip_concurrency INTEGER NOT NULL DEFAULT 10")
	require.Contains(t, sql, "per_ip_concurrency BETWEEN 1 AND 1000")
	require.Contains(t, sql, "created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS proxy_ip_group_members")
	require.Contains(t, sql, "proxy_ip_group_id BIGINT NOT NULL REFERENCES proxy_ip_groups(id) ON DELETE CASCADE")
	require.Contains(t, sql, "proxy_id BIGINT NOT NULL REFERENCES proxies(id) ON DELETE RESTRICT")
	require.Contains(t, sql, "PRIMARY KEY (proxy_ip_group_id, proxy_id)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS proxy_ip_group_id BIGINT")
	require.Contains(t, sql, "FOREIGN KEY (proxy_ip_group_id) REFERENCES proxy_ip_groups(id) ON DELETE RESTRICT")
	require.Contains(t, sql, "CHECK (proxy_id IS NULL OR proxy_ip_group_id IS NULL)")
}
