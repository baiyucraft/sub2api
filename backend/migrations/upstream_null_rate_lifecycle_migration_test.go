package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamNullRateLifecycleMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("285_upstream_null_rate_lifecycle.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Equal(t, 1, strings.Count(sql, "CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()"))
	require.Less(t, strings.Index(sql, "IF NOT FOUND"), strings.Index(sql, "IF key_actual_rate IS NULL THEN"))
	for _, guard := range []string{
		"IF TG_OP = 'UPDATE' THEN",
		"OLD.upstream_config_id IS NOT NULL",
		"OLD.upstream_key_id IS NOT NULL",
		"NEW.upstream_config_id IS NOT DISTINCT FROM OLD.upstream_config_id",
		"NEW.upstream_key_id IS NOT DISTINCT FROM OLD.upstream_key_id",
		"NEW.platform IS NOT DISTINCT FROM OLD.platform",
		"NEW.rate_multiplier IS NOT DISTINCT FROM OLD.rate_multiplier",
		"NEW.upstream_source_rate_multiplier IS NOT DISTINCT FROM OLD.upstream_source_rate_multiplier",
		"NEW.priority IS NOT DISTINCT FROM OLD.priority",
		"OLD.deleted_at IS NULL OR NEW.deleted_at IS NOT NULL",
		"NEW.schedulable IS FALSE",
		"NEW.schedulable IS NOT DISTINCT FROM OLD.schedulable",
		"OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL",
		"cannot bind an upstream key without an actual rate",
		"NEW.rate_multiplier := key_actual_rate",
		"NEW.priority := CEIL(key_actual_rate * 100)::INTEGER",
		"rate_multiplier, upstream_source_rate_multiplier, priority, schedulable, deleted_at",
	} {
		require.Contains(t, sql, guard)
	}
	require.NotContains(t, sql, "UPDATE upstream_keys")
	require.NotContains(t, sql, "UPDATE accounts")
}
