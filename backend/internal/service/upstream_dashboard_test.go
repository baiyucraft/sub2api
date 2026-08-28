package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamDashboardSummaryAggregatesVisibleCards(t *testing.T) {
	result := &UpstreamDashboardResponse{
		Items: []UpstreamDashboardCard{
			{Requests: 3, OverallStatus: "degraded", SchedulableAccountCount: 2, OpenIncidentCount: 1},
			{Requests: 0, OverallStatus: "operational", SchedulableAccountCount: 4, OpenIncidentCount: 2, BalanceLow: true},
		},
	}
	populateUpstreamDashboardSummary(result)
	require.Equal(t, UpstreamDashboardSummary{
		TotalConfigurations:      2,
		TrafficConfigurations:    1,
		AttentionConfigurations:  1,
		SchedulableAccounts:      6,
		OpenIncidents:            3,
		BalanceLowConfigurations: 1,
	}, result.Summary)
}
