package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedMonitorKeyNameMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("198_normalize_managed_monitor_key_names.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE api_keys ALTER COLUMN name TYPE VARCHAR(103)")
	require.Contains(t, sql, "UPDATE api_keys AS k SET name = '监控-' || BTRIM(m.name)")
	require.Contains(t, sql, "FROM channel_monitors AS m")
	require.Contains(t, sql, "k.managed_monitor_id = m.id")
	require.Contains(t, sql, "m.managed_api_key_id = k.id")
	require.Contains(t, sql, "k.purpose = 'managed_monitor'")
	require.Contains(t, sql, "k.deleted_at IS NULL")
	require.Contains(t, sql, "k.group_id IS NOT NULL")
	require.Contains(t, sql, "invalid live managed monitor key bindings")
	require.NotContains(t, sql, "m.deleted_at")
	require.NotContains(t, sql, "SET key =")
	require.NotContains(t, sql, "SET quota =")
}
