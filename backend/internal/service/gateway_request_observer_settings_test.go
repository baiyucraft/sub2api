package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeGatewayRequestObserverSettingsIgnoresLegacyAccountTargets(t *testing.T) {
	settings, err := decodeGatewayRequestObserverSettings(`{"enabled":true,"api_key_names":["maibon-gpt"],"account_ids":[789]}`)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, []string{"maibon-gpt"}, settings.APIKeyNames)
	require.Empty(t, settings.UserIDs)
	require.Empty(t, settings.UserEmails)
}
