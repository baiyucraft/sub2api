package migrations

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile246PreservesRenumberedUpstreamMigrationBytes(t *testing.T) {
	checksums := map[string]string{
		"263_add_usage_log_upstream_request_id.sql":            "d13ec35cdf8383c769a6a3046d34d7809860601918648def06bf82b8c28bb80b",
		"264_add_usage_log_upstream_request_id_index_notx.sql": "41c32a383794730d2bfd88514bf999cfb156cb06324c46b54642736ac8f2fff6",
		"265_channel_max_reasoning_effort_multiplier.sql":      "448b59b3168fe4dfe2417f1abd9657d124708c279e2245d55149087838d8c8d6",
		"266_group_codex_models_manifest_config.sql":           "8ef9cd9a6a963e79823f8f5d703a6b31fa30ebfca9f8d2337dd451765178e5a0",
	}
	for filename, expected := range checksums {
		t.Run(filename, func(t *testing.T) {
			content, err := FS.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, expected, fmt.Sprintf("%x", sha256.Sum256(content)))
		})
	}
	for _, filename := range []string{
		"232_add_usage_log_upstream_request_id.sql",
		"233_add_usage_log_upstream_request_id_index_notx.sql",
		"234_channel_max_reasoning_effort_multiplier.sql",
		"234_group_codex_models_manifest_config.sql",
	} {
		_, err := FS.ReadFile(filename)
		require.Error(t, err, "upstream filename must not re-enter the fork catalog: %s", filename)
	}
}

func TestProfile246AdditiveSchemaContract(t *testing.T) {
	requestID := normalizedMigrationSQL(t, "263_add_usage_log_upstream_request_id.sql")
	require.Contains(t, requestID, "ADD COLUMN IF NOT EXISTS upstream_request_id VARCHAR(128)")
	require.NotContains(t, requestID, "NOT NULL")
	index := normalizedMigrationSQL(t, "264_add_usage_log_upstream_request_id_index_notx.sql")
	require.Contains(t, index, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_upstream_request_id")
	require.Contains(t, index, "WHERE upstream_request_id IS NOT NULL")
	multiplier := normalizedMigrationSQL(t, "265_channel_max_reasoning_effort_multiplier.sql")
	require.Contains(t, multiplier, "max_reasoning_effort_multiplier NUMERIC(10,4)")
	require.Contains(t, multiplier, "CHECK (max_reasoning_effort_multiplier IS NULL OR max_reasoning_effort_multiplier > 0)")
	manifest := normalizedMigrationSQL(t, "266_group_codex_models_manifest_config.sql")
	require.Contains(t, manifest, "codex_models_manifest_config JSONB NOT NULL DEFAULT '{}'::jsonb")
}
