package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type nativeProxyListRepoStub struct {
	ProxyRepository
	proxies []Proxy
	members []Proxy
}

func (r *nativeProxyListRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]Proxy, *pagination.PaginationResult, error) {
	return r.proxies, &pagination.PaginationResult{Total: int64(len(r.proxies))}, nil
}

func (r *nativeProxyListRepoStub) ListWithFiltersAndAccountCount(context.Context, pagination.PaginationParams, string, string, string) ([]ProxyWithAccountCount, *pagination.PaginationResult, error) {
	out := make([]ProxyWithAccountCount, 0, len(r.proxies))
	for _, proxy := range r.proxies {
		out = append(out, ProxyWithAccountCount{Proxy: proxy})
	}
	return out, &pagination.PaginationResult{Total: int64(len(out))}, nil
}

func (r *nativeProxyListRepoStub) ListByIDs(context.Context, []int64) ([]Proxy, error) {
	return r.members, nil
}

func TestNormalizeAdminProxyBindingDecodesVirtualGroupID(t *testing.T) {
	rawProxyID := int64(-12)
	var proxyID *int64 = &rawProxyID
	var groupID *int64

	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &groupID))
	require.Nil(t, proxyID)
	require.NotNil(t, groupID)
	require.Equal(t, int64(12), *groupID)
}

func TestNormalizeAdminProxyBindingRejectsConflictingGroup(t *testing.T) {
	rawProxyID := int64(-12)
	proxyID := &rawProxyID
	rawGroupID := int64(13)
	groupID := &rawGroupID

	err := normalizeAdminProxyBinding(&proxyID, &groupID)
	require.Equal(t, "ACCOUNT_PROXY_BINDING_CONFLICT", infraerrors.Reason(err))
}

func TestNormalizeAdminProxyBindingRejectsVirtualIDWithExplicitClear(t *testing.T) {
	rawProxyID := int64(-12)
	proxyID := &rawProxyID
	rawGroupID := int64(0)
	groupID := &rawGroupID

	err := normalizeAdminProxyBinding(&proxyID, &groupID)
	require.Equal(t, "ACCOUNT_PROXY_BINDING_CONFLICT", infraerrors.Reason(err))
}

func TestNormalizeAdminProxyBindingKeepsRealProxyBinding(t *testing.T) {
	rawProxyID := int64(11)
	proxyID := &rawProxyID
	var groupID *int64

	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &groupID))
	require.Equal(t, int64(11), *proxyID)
	require.Nil(t, groupID)
}

func TestAdminServiceNativeProxyListIncludesVirtualProxyGroup(t *testing.T) {
	groupRepo := newProxyIPGroupRepoStub()
	groupRepo.groups[12] = ProxyIPGroup{ID: 12, Name: "美国轮换组", PerIPConcurrency: 10, ProxyIDs: []int64{1, 2}}
	proxyRepo := &nativeProxyListRepoStub{
		proxies: []Proxy{{ID: 1, Name: "东京", Protocol: "socks5", Status: StatusActive}},
		members: []Proxy{
			{ID: 1, Status: StatusActive},
			{ID: 2, Status: StatusDisabled},
		},
	}
	svc := &adminServiceImpl{proxyRepo: proxyRepo, proxyIPGroupRepo: groupRepo}

	items, total, err := svc.ListProxies(context.Background(), 1, 20, "", "", "", "id", "desc")
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, items, 2)

	var groupItem, realItem *Proxy
	for i := range items {
		if items[i].BindingType == proxyBindingTypeProxyIPGroup {
			groupItem = &items[i]
		} else {
			realItem = &items[i]
		}
	}
	require.NotNil(t, groupItem)
	require.Equal(t, int64(-12), groupItem.ID)
	require.Equal(t, int64(12), *groupItem.ProxyIPGroupID)
	require.Equal(t, 2, groupItem.MemberCount)
	require.Equal(t, 1, groupItem.AvailableMemberCount)
	require.Equal(t, proxyGroupStatusAvailable, groupItem.Status)
	require.NotNil(t, realItem)
	require.Equal(t, int64(1), *realItem.ProxyID)
}
