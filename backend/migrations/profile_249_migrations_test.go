package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile249ResetsOnlyLegacyConfidenceFields(t *testing.T) {
	sql := normalizedMigrationSQL(t, "249_upstream_confidence_juice_reset.sql")
	require.Contains(t, sql, "confidence_prompt_version IS DISTINCT FROM 'openai-juice-high-v1'")
	require.Contains(t, sql, "SET confidence_score = NULL")
	require.Contains(t, sql, "confidence_checks = NULL")
	require.NotContains(t, strings.ToUpper(sql), "DELETE FROM UPSTREAM_HEALTH_OBSERVATIONS")
	require.NotContains(t, sql, "ttft_ms = NULL")
	require.NotContains(t, sql, "http_status = NULL")
	require.NotContains(t, sql, "observed_at = NULL")
}
