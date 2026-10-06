package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile262ConfidenceDistributionMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("288_upstream_confidence_distribution.sql")
	require.NoError(t, err)
	require.Equal(t, "18d33b7a9d64abab88d4cda73fded0e2ac3619daa11467704a4f2565b2c46835", fmt.Sprintf("%x", sha256.Sum256(content)))
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_confidence_distribution_states")
	require.Contains(t, sql, "PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE")
	require.Contains(t, sql, "revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)")
	require.Contains(t, sql, "state_json JSONB NOT NULL")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE UPSTREAM_KEYS")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE ACCOUNTS")
}
