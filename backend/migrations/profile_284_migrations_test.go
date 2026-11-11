package migrations

import (
	"crypto/sha256"
	"fmt"
	"github.com/stretchr/testify/require"
	"io/fs"
	"strings"
	"testing"
)

func TestProfile284OfficialIndexAndCacheTTLMigrationContract(t *testing.T) {
	raw, err := FS.ReadFile("309_add_usage_logs_api_key_cost_index_notx.sql")
	require.NoError(t, err)
	require.Equal(t, "789d4d5a5c186531cb6edd65ea2ecc07f5e488272f7876c6f46240e2fed0c30b", fmt.Sprintf("%x", sha256.Sum256(raw)))
	require.Contains(t, strings.Join(strings.Fields(string(raw)), " "), "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_api_key_created_cost ON usage_logs (api_key_id, created_at) INCLUDE (actual_cost)")
	raw, err = FS.ReadFile("310_openai_cache_write_ttl.sql")
	require.NoError(t, err)
	require.Equal(t, "c470116c49042425f5663c421f0aea2a6aa6eae75b7e489dc95c12fc303562b2", fmt.Sprintf("%x", sha256.Sum256(raw)))
	sql := strings.Join(strings.Fields(string(raw)), " ")
	for _, marker := range []string{"cache_creation_30m_tokens INTEGER NOT NULL DEFAULT 0", "cache_creation_12h_tokens INTEGER NOT NULL DEFAULT 0", "channel_model_pricing", "channel_pricing_intervals", "channel_account_stats_model_pricing", "channel_account_stats_pricing_intervals", "cache_write_12h_price NUMERIC(20,12)"} {
		require.Contains(t, sql, marker)
	}
	_, err = FS.ReadFile("249_add_usage_logs_api_key_cost_index_notx.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = FS.ReadFile("250_openai_cache_write_ttl.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)
}
