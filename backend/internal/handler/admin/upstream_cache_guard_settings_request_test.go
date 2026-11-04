package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeUpstreamCacheGuardSettingsDistinguishesOmissionAndRejectsInvalidRecoverySessions(t *testing.T) {
	var omitted json.RawMessage
	settings, err := decodeUpstreamCacheGuardSettings(omitted)
	require.NoError(t, err)
	require.Nil(t, settings)

	for _, raw := range []string{
		`{"recovery_min_sessions":0}`,
		`{"recovery_min_sessions":-1}`,
		`{"recovery_min_sessions":101}`,
		`{"recovery_min_sessions":2.5}`,
		`{"recovery_min_sessions":null}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeUpstreamCacheGuardSettings(json.RawMessage(raw))
			require.Error(t, err)
		})
	}

	settings, err = decodeUpstreamCacheGuardSettings(json.RawMessage(`{"recovery_min_sessions":1,"session_window_migration_threshold_percent":0}`))
	require.NoError(t, err)
	require.NotNil(t, settings)
	require.Equal(t, 1, settings.RecoveryMinSessions)
	require.NotNil(t, settings.SessionWindowMigrationThresholdPercent)
	require.Zero(t, *settings.SessionWindowMigrationThresholdPercent)

	settings, err = decodeUpstreamCacheGuardSettings(json.RawMessage(`{"session_window_migration_threshold_percent":null}`))
	require.NoError(t, err)
	require.Nil(t, settings.SessionWindowMigrationThresholdPercent)
	require.Zero(t, settings.RecoveryMinSessions, "legacy omission is normalized by the settings service")
}
