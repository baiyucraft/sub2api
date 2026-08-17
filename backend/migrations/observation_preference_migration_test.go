package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamObservationPreferenceMigrationContract(t *testing.T) {
	sql, err := FS.ReadFile("240_upstream_observation_preference.sql")
	require.NoError(t, err)
	content := strings.ToUpper(string(sql))
	for _, fragment := range []string{
		"ADD COLUMN IF NOT EXISTS OBSERVATION_ENABLED BOOLEAN NOT NULL DEFAULT TRUE",
		"KEY_HEALTH_STATE_CHANGED",
		"PREVIOUS_OBSERVATION_ENABLED",
		"AMBIGUOUS_COUNT",
		"RAISE EXCEPTION",
		"IDX_UPSTREAM_KEYS_OBSERVATION_ENABLED",
		"WHERE DELETED_AT IS NULL",
	} {
		require.Contains(t, content, fragment)
	}
	require.NotContains(t, content, "DELETE FROM UPSTREAM_KEYS")
}

func TestProfile240MigrationContract(t *testing.T) {
	for _, name := range []string{"240_upstream_observation_preference.sql", "241_precise_upstream_effective_rate.sql"} {
		_, err := FS.ReadFile(name)
		require.NoError(t, err, name)
	}
}
