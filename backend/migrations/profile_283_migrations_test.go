package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile283OfficialMCPOAuthMigrationContract(t *testing.T) {
	raw, err := FS.ReadFile("308_mcp_oauth.sql")
	require.NoError(t, err)
	require.Equal(t, "881ff2e8ecf430202c593c699b1441db1d2196ac2c312d8a3849acb6c97609e5", fmt.Sprintf("%x", sha256.Sum256(raw)))
	require.Equal(t, "751a1dcee6108afcecbd580b6e3140cca3480853fecd2174861b02f3a71e54a1", fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(string(raw))))))
	_, err = FS.ReadFile("249_mcp_oauth.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = FS.ReadFile("249_upstream_confidence_juice_reset.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(raw)), " ")
	for _, marker := range []string{
		"CREATE TABLE IF NOT EXISTS mcp_oauth_grants",
		"id VARCHAR(43) PRIMARY KEY",
		"user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE",
		"scope VARCHAR(16) NOT NULL CHECK (scope IN ('user', 'admin'))",
		"CREATE TABLE IF NOT EXISTS mcp_oauth_codes",
		"code_hash VARCHAR(64) PRIMARY KEY",
		"code_challenge VARCHAR(43) NOT NULL",
		"CREATE TABLE IF NOT EXISTS mcp_oauth_credentials",
		"key_hash VARCHAR(64) PRIMARY KEY",
		"kind VARCHAR(8) NOT NULL CHECK (kind IN ('access', 'refresh'))",
	} {
		require.Contains(t, sql, marker)
	}
}
