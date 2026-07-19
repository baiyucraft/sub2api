//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorCreateRequestPreservesShowGroupRatePresence(t *testing.T) {
	var omitted channelMonitorCreateRequest
	require.NoError(t, json.Unmarshal([]byte(`{"credential_mode":"managed_local"}`), &omitted))
	require.Nil(t, omitted.ShowGroupRate)

	var disabled channelMonitorCreateRequest
	require.NoError(t, json.Unmarshal([]byte(`{"credential_mode":"managed_local","show_group_rate":false}`), &disabled))
	require.NotNil(t, disabled.ShowGroupRate)
	require.False(t, *disabled.ShowGroupRate)
}
