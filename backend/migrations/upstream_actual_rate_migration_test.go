package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamActualRateMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("182_upstream_actual_rate_multiplier.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "source_rate_multiplier DECIMAL(20,10)")
	require.Contains(t, sql, "ROUND(k.rate_multiplier * c.recharge_rate, 4)")
	require.Contains(t, sql, "NEW.rate_multiplier := key_actual_rate")
	require.NotContains(t, sql, "FOR NO KEY UPDATE")
	require.NotContains(t, sql, "FOR KEY SHARE")
	require.Contains(t, sql, "concurrency > 1073741823")
	require.Contains(t, sql, "concurrency, deleted_at")
	require.Contains(t, sql, "UPDATE OF upstream_config_id, upstream_key_id, platform,")
	require.Contains(t, sql, "'account_bulk_changed'")
	require.Contains(t, sql, "migration_182_changed_account_ids")
	for _, key := range []string{
		"upstream_source_rate_multiplier",
		"upstream_effective_cost_multiplier",
		"sub2api_upstream_rate_multiplier",
		"default_rate_multiplier",
		"dedicated_rate_multiplier",
	} {
		require.Contains(t, sql, key)
	}
	require.Equal(t, 1, strings.Count(sql, "CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()"))
}
