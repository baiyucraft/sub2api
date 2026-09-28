package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProxyFromServicePreservesPositiveGroupBindingID(t *testing.T) {
	groupID := int64(12)
	got := ProxyFromService(&service.Proxy{
		ID: 36, Name: "US pool", BindingType: "proxy_ip_group",
		ProxyIPGroupID: &groupID, MemberCount: 3, PerIPConcurrency: 10,
	})
	require.Equal(t, int64(36), got.ID)
	require.Equal(t, "proxy_ip_group", got.BindingType)
	require.Equal(t, &groupID, got.ProxyIPGroupID)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"id":36`)
	require.Contains(t, string(raw), `"proxy_ip_group_id":12`)
}
