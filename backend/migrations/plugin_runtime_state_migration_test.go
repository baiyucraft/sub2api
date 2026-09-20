package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginRuntimeStateMigration(t *testing.T) {
	content, err := FS.ReadFile("280_plugin_runtime_state.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS config_revision BIGINT NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS managed_scope JSONB NULL",
		"CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_state",
		"CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_leases",
		"value_encrypted TEXT", "version BIGINT NOT NULL CHECK (version > 0)",
		"deleted BOOLEAN NOT NULL DEFAULT FALSE",
		"fence BIGINT NOT NULL DEFAULT 0 CHECK (fence >= 0)",
		"expires_at TIMESTAMPTZ NOT NULL DEFAULT '-infinity'",
		"(deleted AND value_encrypted IS NULL) OR (NOT deleted AND value_encrypted IS NOT NULL)",
	} {
		require.Contains(t, sql, fragment)
	}
	require.Equal(t, 2, strings.Count(sql, "PRIMARY KEY (plugin_key, namespace, key)"))
	require.Equal(t, 2, strings.Count(sql, "REFERENCES sub2api_plugin_installations(plugin_key) ON DELETE CASCADE"))
	require.NotContains(t, sql, "managed_scope JSONB NOT NULL")
	require.NotContains(t, sql, "managed_scope JSONB NULL DEFAULT")
	require.NotContains(t, strings.ToUpper(sql), "ALTER TABLE ACCOUNTS")
}

func TestPluginRuntimeStateMigrationHostPrivateUpgradeDrain(t *testing.T) {
	content, err := FS.ReadFile("280_plugin_runtime_state.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, fragment := range []string{
		"DROP CONSTRAINT IF EXISTS sub2api_plugin_installations_state_check",
		"ADD CONSTRAINT sub2api_plugin_installations_state_check",
		"CHECK (state IN ('disabled', 'starting', 'enabled', 'error', 'incompatible', 'upgrading'))",
		"CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_requests",
		"plugin_id BIGINT NOT NULL REFERENCES sub2api_plugin_installations(id) ON DELETE CASCADE",
		"request_id VARCHAR(64) NOT NULL",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()",
		"PRIMARY KEY (plugin_id, request_id)",
	} {
		require.Contains(t, sql, fragment)
	}
	start := strings.Index(sql, "CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_requests")
	end := strings.Index(sql[start:], ");")
	require.NotContains(t, sql[start:start+end], "expires_at")
	require.NotContains(t, sql, "DELETE FROM sub2api_plugin_runtime_requests")
}

func TestPluginRuntimeStateMigrationMaintenanceJournal(t *testing.T) {
	content, err := FS.ReadFile("280_plugin_runtime_state.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS sub2api_plugin_maintenance",
		"plugin_id BIGINT PRIMARY KEY REFERENCES sub2api_plugin_installations(id) ON DELETE CASCADE",
		"owner_token VARCHAR(128) NOT NULL CHECK (octet_length(owner_token) BETWEEN 32 AND 128)",
		"previous_installation JSONB NOT NULL CHECK (jsonb_typeof(previous_installation) = 'object')",
		"installed_sha256 VARCHAR(64) NOT NULL",
		"installed_config_revision BIGINT NOT NULL CHECK (installed_config_revision >= 0)",
		"expires_at TIMESTAMPTZ NOT NULL",
		"ON sub2api_plugin_maintenance(expires_at)",
	} {
		require.Contains(t, sql, fragment)
	}
	require.NotContains(t, sql, "DELETE FROM sub2api_plugin_maintenance")
}
