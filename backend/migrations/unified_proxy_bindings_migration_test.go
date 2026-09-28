package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnifiedProxyBindingsMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "284_unified_proxy_bindings.sql")

	for _, fragment := range []string{
		"LOCK TABLE proxies, proxy_ip_groups, accounts IN ACCESS EXCLUSIVE MODE",
		"CREATE TABLE IF NOT EXISTS proxy_bindings",
		"id BIGINT PRIMARY KEY",
		"CONSTRAINT proxy_bindings_type_check CHECK (binding_type IN ('proxy', 'proxy_ip_group'))",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_bindings_proxy_id",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_bindings_group_id",
		"SELECT p.id, 'proxy', p.id FROM proxies p",
		"ALTER TABLE proxy_ip_groups ADD COLUMN IF NOT EXISTS binding_id BIGINT",
		"FOR group_row IN SELECT id FROM proxy_ip_groups WHERE binding_id IS NULL ORDER BY id LOOP",
		"UPDATE accounts a SET proxy_id = b.id FROM proxy_bindings b",
		"WHERE a.proxy_ip_group_id IS NOT NULL AND b.proxy_ip_group_id = a.proxy_ip_group_id",
		"FOREIGN KEY (proxy_id) REFERENCES proxy_bindings(id) ON DELETE SET NULL",
		"ALTER TABLE accounts DROP COLUMN IF EXISTS proxy_ip_group_id",
		"CREATE TRIGGER trg_register_real_proxy_binding AFTER INSERT ON proxies",
		"CREATE TRIGGER trg_register_proxy_group_binding AFTER INSERT ON proxy_ip_groups",
		"ALTER TABLE proxy_bindings ALTER COLUMN id SET DEFAULT nextval('proxies_id_seq')",
	} {
		require.Contains(t, sql, fragment)
	}

	require.Contains(t, sql, "(binding_type = 'proxy' AND proxy_id IS NOT NULL AND proxy_ip_group_id IS NULL)")
	require.Contains(t, sql, "(binding_type = 'proxy_ip_group' AND proxy_id IS NULL AND proxy_ip_group_id IS NOT NULL)")
	require.Equal(t, 2, strings.Count(sql, "new_binding_id := nextval('proxies_id_seq')"))
	require.Contains(t, sql, "INSERT INTO proxy_bindings(id, binding_type, proxy_id) VALUES(NEW.id, 'proxy', NEW.id)")
	require.Contains(t, sql, "INSERT INTO proxy_bindings(id, binding_type, proxy_ip_group_id) VALUES(new_binding_id, 'proxy_ip_group', NEW.id)")
	require.NotContains(t, sql, "FOREIGN KEY (proxy_id) REFERENCES proxies(id)")
	require.NotContains(t, sql, "FOREIGN KEY (proxy_ip_group_id) REFERENCES proxy_ip_groups(id)")
	backfill := strings.Index(sql, "UPDATE accounts a SET proxy_id = b.id FROM proxy_bindings b")
	for _, oldConstraint := range []string{"accounts_proxy_id_fkey", "accounts_proxy_source_exclusive"} {
		drop := strings.Index(sql, "ALTER TABLE accounts DROP CONSTRAINT IF EXISTS "+oldConstraint)
		require.GreaterOrEqual(t, drop, 0)
		require.Less(t, drop, backfill, "legacy account constraints must be dropped before group backfill")
	}

	// A deleted source keeps its binding as a tombstone, so neither ID can be reused.
	require.Contains(t, sql, "ALTER TABLE proxy_bindings DROP CONSTRAINT IF EXISTS proxy_bindings_proxy_fk")
	require.Contains(t, sql, "ALTER TABLE proxy_bindings DROP CONSTRAINT IF EXISTS proxy_bindings_group_fk")
	require.NotContains(t, strings.ToUpper(sql), "DELETE FROM PROXY_BINDINGS")

	// Replay must not create another binding for an already migrated source.
	require.Contains(t, sql, "WHERE NOT EXISTS (SELECT 1 FROM proxy_bindings b WHERE b.id = p.id)")
	require.Contains(t, sql, "WHERE binding_id IS NULL ORDER BY id")
	require.Contains(t, sql, "DROP TRIGGER IF EXISTS trg_register_real_proxy_binding ON proxies")
	require.Contains(t, sql, "DROP TRIGGER IF EXISTS trg_register_proxy_group_binding ON proxy_ip_groups")
}
