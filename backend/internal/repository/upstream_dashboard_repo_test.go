package repository

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
