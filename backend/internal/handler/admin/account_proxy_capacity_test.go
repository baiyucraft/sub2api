package admin

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildProxyCapacitiesReturnsOneRowPerBinding(t *testing.T) {
	account := &service.Account{
		ID:          42,
		Concurrency: 5,
		ProxyIDs:    []int64{11, 12},
		Proxies: []*service.Proxy{
			{ID: 11, Name: "Hong Kong", Status: service.StatusActive},
			{ID: 12, Name: "Expired", Status: service.StatusDisabled},
		},
	}
	firstTarget := service.AccountProxyConcurrencyTarget(account, 11)
	loads := map[string]*service.AccountLoadInfo{
		firstTarget.Key(): {CurrentConcurrency: 2, WaitingCount: 1},
	}

	items := buildProxyCapacities(account, loads)

	require.Len(t, items, 2)
	require.Equal(t, int64(11), items[0].ProxyID)
	require.Equal(t, "Hong Kong", items[0].Name)
	require.Equal(t, 2, *items[0].CurrentConcurrency)
	require.Equal(t, 1, *items[0].Waiting)
	require.Equal(t, 5, *items[0].Limit)
	require.True(t, items[0].Available)
	require.Equal(t, int64(12), items[1].ProxyID)
	require.Equal(t, "Expired", items[1].Name)
	require.Nil(t, items[1].CurrentConcurrency)
	require.Nil(t, items[1].Waiting)
	require.False(t, items[1].Available)
}

func TestBuildProxyCapacitiesOmitsDirectAndUpstreamAccounts(t *testing.T) {
	require.Nil(t, buildProxyCapacities(&service.Account{ID: 1, Concurrency: 5}, nil))

	upstreamID := int64(9)
	require.Nil(t, buildProxyCapacities(&service.Account{
		ID:               2,
		Concurrency:      5,
		ProxyIDs:         []int64{11},
		UpstreamConfigID: &upstreamID,
		UpstreamKeyID:    &upstreamID,
	}, nil))
}
