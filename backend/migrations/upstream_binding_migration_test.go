package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration175EnforcesCanonicalUpstreamAccountBinding(t *testing.T) {
	content, err := FS.ReadFile("175_enforce_upstream_account_binding.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "(upstream_config_id IS NULL) <> (upstream_key_id IS NULL)")
	require.Contains(t, sql, "type NOT IN ('apikey', 'upstream')")
	require.Contains(t, sql, "SET type = 'apikey'")
	require.Contains(t, sql, "accounts_upstream_binding_complete")
	require.Contains(t, sql, "AND type = 'apikey'")
	require.Contains(t, sql, "NOT VALID")
	require.Contains(t, sql, "VALIDATE CONSTRAINT")
	require.NotContains(t, strings.ToLower(sql), "deleted_at is null")
}
