package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupProfitControlMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "211_group_profit_control.sql")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS profit_control_enabled BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS profit_min_margin DECIMAL(10,4) NOT NULL DEFAULT 0")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS profit_safety_buffer DECIMAL(10,4) NOT NULL DEFAULT 0")
	require.NotContains(t, sql, "DROP COLUMN")
}

func TestGroupProfitControlAuthCacheMigrationPreservesForkDependencies(t *testing.T) {
	sql := normalizedMigrationSQL(t, "212_group_profit_control_auth_cache_invalidation.sql")
	for _, field := range []string{
		"status",
		"is_exclusive",
		"allow_image_generation",
		"platform",
		"subscription_type",
		"rate_multiplier",
		"peak_rate_enabled",
		"peak_start",
		"peak_end",
		"peak_rate_multiplier",
		"profit_control_enabled",
		"profit_min_margin",
		"profit_safety_buffer",
		"deleted_at",
	} {
		require.Contains(t, sql, "OLD."+field+" IS NOT DISTINCT FROM NEW."+field)
	}
	require.Contains(t, sql, "encode(sha256(convert_to(k.key, 'UTF8')), 'hex')")
	require.NotContains(t, sql, "SELECT k.key")
}

func TestProfile212DoesNotRetainConflictingUpstreamMigrationNames(t *testing.T) {
	for _, name := range []string{
		"192_group_profit_control.sql",
		"193_group_profit_control_auth_cache_invalidation.sql",
	} {
		_, err := FS.ReadFile(name)
		require.Error(t, err, strings.TrimSpace(name))
	}
}
