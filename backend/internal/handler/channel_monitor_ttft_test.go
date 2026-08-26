//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserMonitorViewMapperNormalizesNonPositiveTTFT(t *testing.T) {
	zero := 0
	positive := 21
	view := &service.UserMonitorView{
		PrimaryTTFTMs: &zero,
		Timeline: []service.UserMonitorTimelinePoint{{
			Status: "operational",
			TTFTMs: &positive,
		}},
	}

	got := userMonitorViewToItem(view, false)
	require.Nil(t, got.PrimaryTTFTMs)
	require.Len(t, got.Timeline, 1)
	require.NotNil(t, got.Timeline[0].TTFTMs)
	require.Equal(t, 21, *got.Timeline[0].TTFTMs)
}
