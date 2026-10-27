package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile277ConfidenceFingerprintMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("304_upstream_confidence_fingerprint.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Equal(t, "fd7e5c4e93e07a41af57f5cd171236e32b869b588a51c33e5959a740334f4d5b", fmt.Sprintf("%x", sha256.Sum256(content)))
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_confidence_fingerprint_states")
	require.Contains(t, sql, "upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE")
	require.Contains(t, sql, "revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0)")
	require.Contains(t, sql, "state_json JSONB NOT NULL")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE UPSTREAM_KEYS")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE ACCOUNTS")
}
