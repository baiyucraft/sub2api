//go:build unit

package handler

import (
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestIsExpectedGrokRealtimeClose(t *testing.T) {
	for _, status := range []coderws.StatusCode{
		coderws.StatusNormalClosure,
		coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd,
		coderws.StatusAbnormalClosure,
	} {
		if !isExpectedGrokRealtimeClose(coderws.CloseError{Code: status}) {
			t.Fatalf("status %v should be treated as an expected session close", status)
		}
	}
	if isExpectedGrokRealtimeClose(coderws.CloseError{Code: coderws.StatusPolicyViolation}) {
		t.Fatal("policy violations must not be treated as normal closes")
	}
}

func TestGrokRealtimeUsageResultBillsAbnormalExitDuration(t *testing.T) {
	started := time.Unix(100, 0)
	result := grokRealtimeUsageResult("grok-voice-latest", started, started.Add(90*time.Second))
	require.NotNil(t, result)
	require.NotEmpty(t, result.RequestID)
	require.Equal(t, 90*time.Second, result.Duration)
	require.NotNil(t, result.AudioUsage)
	require.Equal(t, "realtime", result.AudioUsage.Mode)
	require.InDelta(t, 1.5, result.AudioUsage.DurationOrUnits, 1e-12)
	require.Nil(t, grokRealtimeUsageResult("grok-voice-latest", started, started))
}
