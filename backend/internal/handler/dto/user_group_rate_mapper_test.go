package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserFromServiceAdmin_MapsEffectiveRatesAndPercentTruth(t *testing.T) {
	user := &service.User{
		ID:                42,
		Email:             "user@example.com",
		GroupRates:        map[int64]float64{7: 0.4},
		GroupRatePercents: map[int64]float64{7: 50},
	}

	got := UserFromServiceAdmin(user)
	require.NotNil(t, got)
	require.Equal(t, map[int64]float64{7: 0.4}, got.GroupRates)
	require.Equal(t, map[int64]float64{7: 50}, got.GroupRatePercents)
}
