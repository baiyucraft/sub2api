package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAccountLifecycleMigrationContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("238_upstream_account_lifecycle.sql"))
	require.NoError(t, err)
	sql := string(content)
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS upstream_lifecycle_owner",
		"upstream_lifecycle_owner VARCHAR(20) NOT NULL DEFAULT 'manual'",
		"ADD COLUMN IF NOT EXISTS upstream_archive_reason",
		"accounts_upstream_lifecycle_owner_check",
		"upstream_lifecycle_owner IN ('manual', 'sync_managed')",
		"accounts_upstream_archive_reason_check",
		"upstream_archive_reason IS NULL OR upstream_archive_reason = 'key_missing'",
		"CREATE INDEX IF NOT EXISTS idx_accounts_upstream_key_lifecycle_archive",
		"WHERE deleted_at IS NOT NULL",
		"upstream_lifecycle_owner = 'sync_managed'",
		"upstream_archive_reason = 'key_missing'",
	} {
		require.Contains(t, strings.ToUpper(sql), strings.ToUpper(fragment))
	}
	require.NotContains(t, strings.ToUpper(sql), "UPDATE ACCOUNTS SET UPSTREAM_LIFECYCLE_OWNER")
}

func TestNonGrokVideoPricingReconciliationMigrationContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("239_reconcile_non_grok_video_pricing.sql"))
	require.NoError(t, err)
	sql := strings.ToUpper(string(content))
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS groups_video_price_backup_239",
		"ON CONFLICT (group_id) DO NOTHING",
		"UPDATE groups",
		"platform IS DISTINCT FROM 'grok'",
		"platform IS DISTINCT FROM 'composite'",
		"groups_video_pricing_platform_check",
		"NOT VALID",
		"VALIDATE CONSTRAINT groups_video_pricing_platform_check",
	} {
		require.Contains(t, sql, strings.ToUpper(fragment))
	}
}
