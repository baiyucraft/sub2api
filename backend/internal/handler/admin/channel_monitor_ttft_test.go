//go:build unit

package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestChannelMonitorResponseMappersNormalizeNonPositiveTTFT(t *testing.T) {
	zero := 0
	negative := -2
	positive := 17

	check := checkResultToResponse(&service.CheckResult{TTFTMs: &zero})
	require.Nil(t, check.TTFTMs)
	history := historyEntryToResponse(&service.ChannelMonitorHistoryEntry{TTFTMs: &negative})
	require.Nil(t, history.TTFTMs)

	check = checkResultToResponse(&service.CheckResult{TTFTMs: &positive})
	require.NotNil(t, check.TTFTMs)
	require.Equal(t, 17, *check.TTFTMs)
}
