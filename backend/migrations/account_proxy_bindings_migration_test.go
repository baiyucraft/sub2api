package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountProxyBindingsMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "255_account_proxy_bindings.sql")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS account_proxy_bindings")
	require.Contains(t, sql, "PRIMARY KEY (account_id, proxy_id)")
	require.Contains(t, sql, "CHECK (position >= 0)")
	require.Contains(t, sql, "FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE")
	require.Contains(t, sql, "FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE RESTRICT")
	require.Contains(t, sql, "SELECT a.id, a.proxy_id, 0")
	require.Contains(t, sql, "ON CONFLICT (account_id, proxy_id) DO NOTHING")
}
