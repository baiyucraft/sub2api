package dto

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountFromServiceShallowRedactsLegacyUpstreamRateMetadata(t *testing.T) {
	account := &service.Account{Extra: map[string]any{
		"keep":                               "value",
		"upstream_rate_multiplier":           1.2,
		"upstream_source_rate_multiplier":    8,
		"upstream_recharge_rate":             0.1,
		"upstream_effective_cost_multiplier": 0.8,
		"sub2api_upstream_rate_multiplier":   1.2,
	}}

	got := AccountFromServiceShallow(account)
	require.Equal(t, "value", got.Extra["keep"])
	for _, key := range []string{
		"upstream_rate_multiplier",
		"upstream_source_rate_multiplier",
		"upstream_recharge_rate",
		"upstream_effective_cost_multiplier",
		"sub2api_upstream_rate_multiplier",
	} {
		require.NotContains(t, got.Extra, key)
	}
	require.Contains(t, account.Extra, "upstream_rate_multiplier", "mapping must not mutate the service entity")
}
