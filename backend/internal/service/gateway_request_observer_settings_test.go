package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeGatewayRequestObserverSettingsPreservesLegacyAccountTargets(t *testing.T) {
	settings, err := decodeGatewayRequestObserverSettings(`{"enabled":true,"account_ids":[789]}`)
	require.NoError(t, err)
	require.True(t, settings.Enabled)
	require.Equal(t, []int64{789}, settings.LegacyAccountIDs)
	require.Empty(t, settings.UserIDs)
	require.Empty(t, settings.UserEmails)
}
