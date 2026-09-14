package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamKeyModelRoutesMigration(t *testing.T) {
	content, err := FS.ReadFile("272_upstream_key_model_routes.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_key_model_routes")
	require.Contains(t, sql, "upstream_key_id BIGINT NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS upstream_key_model_routes_key_model_active_idx")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
}
