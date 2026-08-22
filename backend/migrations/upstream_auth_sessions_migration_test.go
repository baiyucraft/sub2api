package migrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamAuthSessionsMigrationContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("246_upstream_auth_sessions.sql"))
	require.NoError(t, err)
	sql := strings.ToUpper(string(content))
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS UPSTREAM_AUTH_SESSIONS",
		"UPSTREAM_CONFIG_ID BIGINT NOT NULL UNIQUE REFERENCES UPSTREAM_CONFIGS(ID) ON DELETE CASCADE",
		"CREDENTIAL_FINGERPRINT VARCHAR(64) NOT NULL",
		"SECRET_CIPHERTEXT TEXT NOT NULL",
		"COOLDOWN_UNTIL TIMESTAMPTZ NULL",
		"CONSECUTIVE_AUTH_FAILURES INTEGER NOT NULL DEFAULT 0",
		"LOGIN_COUNT BIGINT NOT NULL DEFAULT 0",
		"REUSE_COUNT BIGINT NOT NULL DEFAULT 0",
		"REFRESH_COUNT BIGINT NOT NULL DEFAULT 0",
		"RELOGIN_COUNT BIGINT NOT NULL DEFAULT 0",
		"COOLDOWN_COUNT BIGINT NOT NULL DEFAULT 0",
	} {
		require.Contains(t, sql, fragment)
	}
}
