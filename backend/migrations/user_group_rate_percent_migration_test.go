package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserGroupRatePercentMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("277_user_group_rate_percent.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(content))

	require.Contains(t, sql, "add column if not exists rate_percent decimal(20,10)")
	require.Contains(t, sql, "ugr.rate_multiplier / g.rate_multiplier")
	require.Contains(t, sql, "g.rate_multiplier > 0")
	require.Contains(t, sql, "groups with non-positive rate_multiplier have legacy overrides")
	require.Contains(t, sql, "g.rate_multiplier <= 0")
	require.Contains(t, sql, "create or replace function sync_user_group_rate_percent_fields")
	require.Contains(t, sql, "pg_trigger_depth() > 1")
	require.Contains(t, sql, "new.rate_multiplier / group_rate * 100.0")
	require.Contains(t, sql, "group_rate * new.rate_percent / 100.0")
	require.Contains(t, sql, "create or replace function refresh_user_group_rate_multiplier_shadow")
	require.Contains(t, sql, "after update of rate_multiplier")
	require.Contains(t, sql, "rate_percent is not null")
}
