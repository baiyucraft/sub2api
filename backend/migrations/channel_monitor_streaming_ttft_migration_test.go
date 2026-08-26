package migrations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorStreamingTTFTMigrationIsIdempotentAndNullable(t *testing.T) {
	path := filepath.Join("252_channel_monitor_streaming_ttft.sql")
	sql, err := os.ReadFile(path)
	require.NoError(t, err)

	text := string(sql)
	require.Contains(t, text, "ALTER TABLE channel_monitor_histories")
	require.Contains(t, text, "ADD COLUMN IF NOT EXISTS ttft_ms BIGINT")
	// No NOT NULL/default is intentional: historical non-streaming rows remain NULL.
	require.NotContains(t, text, "ttft_ms BIGINT NOT NULL")
	require.NotContains(t, text, "DEFAULT 0")
}
