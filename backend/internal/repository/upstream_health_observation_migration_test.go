package repository

import (
	"strings"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthObservationMigrationDefinesHistoryAndLegacyImport(t *testing.T) {
	content, err := dbmigrations.FS.ReadFile("233_upstream_management.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_health_observations")
	require.Contains(t, sql, "upstream_key_id, observed_at")
	require.Contains(t, sql, "jsonb_array_elements")
	require.Contains(t, sql, "INTERVAL '35 days'")
	require.Contains(t, sql, "TO CURRENT_USER")
	require.Equal(t, 1, strings.Count(sql, "CREATE TABLE IF NOT EXISTS upstream_health_observations"))
}
