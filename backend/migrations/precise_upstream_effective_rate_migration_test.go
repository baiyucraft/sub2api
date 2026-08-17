package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreciseUpstreamEffectiveRateMigrationPreservesTenDecimalPrecision(t *testing.T) {
	sql, err := FS.ReadFile("241_precise_upstream_effective_rate.sql")
	require.NoError(t, err)
	content := strings.ToLower(string(sql))
	for _, table := range []string{"upstream_keys", "accounts", "usage_logs", "batch_image_jobs"} {
		require.Contains(t, content, table)
	}
	require.Contains(t, content, "decimal(20,10)")
	require.Contains(t, content, "round(k.source_rate_multiplier * coalesce(c.recharge_rate, 1), 10)")
	require.Contains(t, content, "key_actual_rate numeric(20,10)")
	require.Contains(t, content, "drop trigger if exists trg_validate_account_upstream_key_binding on accounts")
	require.Less(t,
		strings.Index(content, "drop trigger if exists trg_validate_account_upstream_key_binding on accounts"),
		strings.Index(content, "alter table accounts"),
	)
	require.Contains(t, content, "create trigger trg_validate_account_upstream_key_binding")
	require.NotContains(t, content, "BEGIN;")
	require.NotContains(t, content, "COMMIT;")
	require.NotContains(t, content, "ceil(source_rate_multiplier")
}
