package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDashboardWindowTodayUsesConfiguredTimezoneStartOfDay(t *testing.T) {
	previousTimezone := timezone.Location().String()
	require.NoError(t, timezone.Init("Asia/Shanghai"))
	t.Cleanup(func() { require.NoError(t, timezone.Init(previousTimezone)) })

	now := time.Date(2026, 9, 13, 18, 30, 0, 0, time.UTC)
	start, end, normalized := dashboardWindow(service.UpstreamDashboardWindowToday, now)

	require.Equal(t, "today", normalized)
	require.Equal(t, now, end)
	require.Equal(t, time.Date(2026, 9, 14, 0, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)).UTC(), start)
}

func TestDashboardProbeNullableSelectIsScanSafeForMissingObservations(t *testing.T) {
	projection := strings.Join(strings.Fields(dashboardProbeNullableSelect), " ")
	require.Contains(t, projection, "COALESCE(p.latest_state,'')")
	require.Contains(t, projection, "COALESCE(p.latest_reason,'')")
	require.Contains(t, projection, "COALESCE(p.confidence_status,'')")
	// These values intentionally remain nullable and are scanned into nullable
	// destinations because a config may have no observation in the window.
	require.Contains(t, projection, "p.latest_observed_at")
	require.Contains(t, projection, "p.avg_ttft")
	require.Contains(t, projection, "p.avg_duration")
}

func TestDashboardUpstreamCostIncludesAccountRateMultiplier(t *testing.T) {
	// account_stats_cost is the base model cost. The request-time account rate
	// must be applied before the CNY conversion, matching the usage trend query.
	expression := strings.Join(strings.Fields(dashboardUpstreamCostExpression), " ")
	require.Contains(t, expression, "COALESCE(ul.account_stats_cost, ul.total_cost)")
	require.Contains(t, expression, "COALESCE(ul.account_rate_multiplier, 1)")
	require.Contains(t, expression, "ul.upstream_cost_to_cny_rate")
}
