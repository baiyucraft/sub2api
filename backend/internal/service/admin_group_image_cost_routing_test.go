package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeImageCostRoutingDefaultsAndValidation(t *testing.T) {
	enabled, mode, tolerance, staleAfter, err := normalizeImageCostRouting(true, "", nil, nil)
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, "prefer_lowest", mode)
	require.Equal(t, 5.0, tolerance)
	require.Equal(t, 86400, staleAfter)

	badTolerance := 101.0
	_, _, _, _, err = normalizeImageCostRouting(true, "prefer_lowest", &badTolerance, nil)
	require.ErrorContains(t, err, "image_cost_tolerance_percent")

	badStale := 299
	_, _, _, _, err = normalizeImageCostRouting(true, "strict_lowest", nil, &badStale)
	require.ErrorContains(t, err, "image_cost_stale_after_seconds")

	_, _, _, _, err = normalizeImageCostRouting(true, "unknown", nil, nil)
	require.ErrorContains(t, err, "image_cost_routing_mode")
}
