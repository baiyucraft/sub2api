package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupReasoningEffortPolicyMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("199_group_reasoning_effort_policy.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE groups")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS max_reasoning_effort VARCHAR(20) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS reasoning_effort_mappings JSONB NOT NULL DEFAULT '[]'::jsonb")
	require.NotContains(t, sql, "DROP COLUMN")
	require.NotContains(t, sql, "UPDATE groups")
}
