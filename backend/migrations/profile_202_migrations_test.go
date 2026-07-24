package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func normalizedMigrationSQL(t *testing.T, name string) string {
	t.Helper()
	content, err := FS.ReadFile(name)
	require.NoError(t, err)
	return strings.Join(strings.Fields(string(content)), " ")
}

func TestAlipayMobilePrecreateDeepLinkMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "200_alipay_mobile_precreate_deep_link.sql")
	require.Contains(t, sql, "VALUES ('ALIPAY_MOBILE_PRECREATE_DEEP_LINK', 'false', NOW())")
	require.Contains(t, sql, "ON CONFLICT (key) DO NOTHING")
	require.NotContains(t, sql, "DO UPDATE")
}

func TestGroupAuthCacheImageGenerationMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "201_group_auth_cache_image_generation.sql")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION enqueue_group_auth_cache_invalidation()")
	require.Contains(t, sql, "OLD.allow_image_generation IS NOT DISTINCT FROM NEW.allow_image_generation")
	require.Contains(t, sql, "encode(sha256(convert_to(k.key, 'UTF8')), 'hex')")
	require.NotContains(t, sql, "SELECT k.key")
}

func TestCompositeModelRoutesMigrationContract(t *testing.T) {
	sql := normalizedMigrationSQL(t, "202_composite_model_routes.sql")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS composite_model_routes")
	require.Contains(t, sql, "group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE")
	require.Contains(t, sql, "CONSTRAINT composite_model_routes_match_type_check")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_composite_model_routes_unique_active")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
}

func TestProfile202MigrationsDoNotRetainConflictingUpstreamNames(t *testing.T) {
	for _, name := range []string{
		"172_composite_model_routes.sql",
		"186_alipay_mobile_precreate_deep_link.sql",
		"186_group_auth_cache_image_generation.sql",
	} {
		_, err := FS.ReadFile(name)
		require.Error(t, err, name)
	}
}
