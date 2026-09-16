package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeySchedulingModeMigration(t *testing.T) {
	content, err := FS.ReadFile("276_api_key_scheduling_mode.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS scheduling_mode VARCHAR(20) NOT NULL DEFAULT 'cache_first'")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS api_keys_scheduling_mode_check")
	require.Contains(t, sql, "CHECK (scheduling_mode IN ('cache_first', 'speed_first'))")
}
