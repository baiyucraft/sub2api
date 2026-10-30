package migrations

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile282UserDefaultRateMigrationContract(t *testing.T) {
	raw, err := FS.ReadFile("307_user_default_group_rate_percent.sql")
	require.NoError(t, err)
	require.Equal(t, "43cc403117cc8971ee5e3e3b5eee60c1c927016e959f1cfb802658d644ba9237", fmt.Sprintf("%x", sha256.Sum256(raw)))
	sql := strings.Join(strings.Fields(string(raw)), " ")
	for _, marker := range []string{
		"ADD COLUMN IF NOT EXISTS default_group_rate_percent DECIMAL(20,10) NULL",
		"users_default_group_rate_percent_nonnegative_finite",
		"default_group_rate_percent >= 0",
		"'NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric",
		"CREATE TABLE IF NOT EXISTS user_group_rate_invalidation_outbox",
		"user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE",
		"revision BIGINT GENERATED ALWAYS AS IDENTITY NOT NULL CHECK (revision > 0)",
		"SET revision = DEFAULT",
		"SELECT id FROM users WHERE id = p_user_id",
		"AFTER INSERT OR UPDATE OF default_group_rate_percent ON users",
		"AFTER INSERT OR UPDATE OF user_id, group_id, rate_percent, rate_multiplier OR DELETE",
		"NEW.default_group_rate_percent IS DISTINCT FROM OLD.default_group_rate_percent",
	} {
		require.Contains(t, sql, marker)
	}
	require.NotContains(t, sql, "UPDATE users SET default_group_rate_percent")
	require.NotContains(t, sql, "CREATE TABLE IF NOT EXISTS auth_cache_invalidation_outbox")
	require.NotContains(t, sql, "UPDATE OF rpm_override")
	require.NotContains(t, sql, "revision + 1")
	require.NotContains(t, sql, "revision = EXCLUDED.revision")
}
