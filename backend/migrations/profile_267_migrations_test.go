package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile267MigrationRawByteContracts(t *testing.T) {
	for filename, checksum := range map[string]string{
		"292_group_i18n.sql":                            "9df295380bd4349bb510a64a3f3d829d7ee28904eab2bdf60ec50599b2128f28",
		"293_gemini_cached_contents.sql":                "48d05b5795e22f45458fdd56e32e3af89ebc68002ddb9320bff4d19bacc71dc3",
		"294_gemini_cached_contents_channel_usage.sql":  "282f55ed98d623f0a07cf4e7975f3ba0de09777ec664de612817a0ed2698577d",
		"295_gemini_cached_contents_mutation_lease.sql": "d9e2fdc9e974cf35870cf0d46dba34399fde760a08e97632d6d20abae412ea25",
	} {
		t.Run(filename, func(t *testing.T) {
			content, err := FS.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, checksum, fmt.Sprintf("%x", sha256.Sum256(content)))
		})
	}
	for _, filename := range []string{
		"242_group_i18n.sql", "243_gemini_cached_contents.sql",
		"244_gemini_cached_contents_channel_usage.sql", "245_gemini_cached_contents_mutation_lease.sql",
	} {
		_, err := FS.ReadFile(filename)
		require.ErrorIs(t, err, fs.ErrNotExist)
	}
	for _, filename := range []string{
		"242_user_platform_quotas_add_cn_providers.sql", "243_backfill_codex_fingerprint_seed.sql",
		"244_channel_model_time_pricing.sql", "245_channel_monitor_quota_mode.sql", "291_user_invoice_annotations.sql",
	} {
		_, err := FS.ReadFile(filename)
		require.NoError(t, err)
	}
}

func TestProfile267CacheSchemaFollowsCreationOrder(t *testing.T) {
	names, err := fs.Glob(FS, "29*.sql")
	require.NoError(t, err)
	sort.Strings(names)
	require.Equal(t, []string{
		"290_drop_platform_check_constraints.sql", "291_user_invoice_annotations.sql", "292_group_i18n.sql",
		"293_gemini_cached_contents.sql", "294_gemini_cached_contents_channel_usage.sql", "295_gemini_cached_contents_mutation_lease.sql",
		"296_upstream_cache_guard.sql",
		"297_upstream_cache_guard_account_scope.sql",
	}, names)
	checks := map[string][]string{
		"292_group_i18n.sql":                            {"ALTER TABLE groups", "ADD COLUMN IF NOT EXISTS i18n JSONB NOT NULL DEFAULT '{}'::jsonb"},
		"293_gemini_cached_contents.sql":                {"CREATE TABLE IF NOT EXISTS gemini_cached_contents", "CREATE UNIQUE INDEX IF NOT EXISTS idx_gemini_cached_contents_public_id", "WHERE deleted_at IS NULL"},
		"294_gemini_cached_contents_channel_usage.sql":  {"ALTER TABLE gemini_cached_contents", "ADD COLUMN IF NOT EXISTS channel_usage JSONB NOT NULL DEFAULT '{}'::jsonb"},
		"295_gemini_cached_contents_mutation_lease.sql": {"ALTER TABLE gemini_cached_contents", "mutation_token VARCHAR(64) NULL", "mutation_lease_until TIMESTAMPTZ NULL", "WHERE mutation_token IS NOT NULL AND deleted_at IS NULL"},
	}
	for filename, markers := range checks {
		content, err := FS.ReadFile(filename)
		require.NoError(t, err)
		sql := strings.Join(strings.Fields(string(content)), " ")
		for _, marker := range markers {
			require.Contains(t, sql, marker, filename)
		}
	}
}
