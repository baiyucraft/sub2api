package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildListItemResponsePreserves24hAvailability(t *testing.T) {
	value := 99.61
	monitor := &service.ChannelMonitor{ID: 1, PrimaryModel: "gpt"}
	row := buildListItemResponse(monitor, service.MonitorStatusSummary{Availability24h: &value, Availability7d: 98.0})
	require.NotNil(t, row.Availability24h)
	require.Equal(t, value, *row.Availability24h)
	require.Equal(t, 98.0, row.Availability7d)
	require.Nil(t, buildListItemResponse(monitor, service.MonitorStatusSummary{}).Availability24h)
}
