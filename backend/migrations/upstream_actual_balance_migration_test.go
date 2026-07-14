package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamActualBalanceMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("187_upstream_actual_balance_and_cost_groups.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS recharge_rate")
	require.Contains(t, sql, "balance_formula_version = 2")
	require.Contains(t, sql, "ROUND(balance_cny * recharge_rate, 10)")
	require.Contains(t, sql, "broken recharge-rate event chain")
	require.Contains(t, sql, "jsonb_strip_nulls")
	require.Contains(t, sql, "historical recharge rate cannot be proven")
	require.Contains(t, sql, "balance_formula_version', '') <> '2'")
	require.Contains(t, sql, "upstream_cost_included_group_ids")
	require.Contains(t, sql, "jsonb_agg(g.id ORDER BY g.id)")
	require.NotContains(t, sql, "cost_group_filter_mode")
}
