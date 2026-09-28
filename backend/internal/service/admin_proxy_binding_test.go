package service

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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

func (r *nativeProxyListRepoStub) GetByID(_ context.Context, id int64) (*Proxy, error) {
	for i := range r.proxies {
		if r.proxies[i].ID == id {
			return &r.proxies[i], nil
		}
	}
	return nil, ErrProxyNotFound
}

func newAdminProxyBindingTestClient(t *testing.T, ids ...int64) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE proxy_bindings (id INTEGER PRIMARY KEY, binding_type TEXT NOT NULL, proxy_id INTEGER, proxy_ip_group_id INTEGER, created_at TIMESTAMP NOT NULL)`)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	for _, id := range ids {
		_, err := db.Exec(`INSERT INTO proxy_bindings (id, binding_type, proxy_id, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP)`, id, proxyBindingTypeProxy, id)
		require.NoError(t, err)
	}
	return client
}

func lookupTestProxyBinding(id int64) (adminProxyBinding, error) {
	switch id {
	case 1, 11:
		return adminProxyBinding{id: id, kind: proxyBindingTypeProxy}, nil
	case 6:
		return adminProxyBinding{id: 6, kind: proxyBindingTypeProxyIPGroup, groupID: 12}, nil
	case 7:
		return adminProxyBinding{id: 7, kind: proxyBindingTypeProxyIPGroup, groupID: 13}, nil
	default:
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
}

func lookupTestGroupBinding(id int64) (adminProxyBinding, error) {
	if id == 12 {
		return lookupTestProxyBinding(6)
	}
	if id == 13 {
		return lookupTestProxyBinding(7)
	}
	return adminProxyBinding{}, ErrProxyIPGroupNotFound
}

func TestNormalizeAdminProxyBindingDecodesVirtualGroupID(t *testing.T) {
	rawProxyID := int64(-12)
	var proxyID *int64 = &rawProxyID
	var groupID *int64

	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &groupID, lookupTestProxyBinding, lookupTestGroupBinding))
	require.Equal(t, int64(6), *proxyID)
	require.Nil(t, groupID)
}

func TestNormalizeAdminProxyBindingRejectsConflictingGroup(t *testing.T) {
	rawProxyID := int64(-12)
	proxyID := &rawProxyID
	rawGroupID := int64(13)
	groupID := &rawGroupID

	err := normalizeAdminProxyBinding(&proxyID, &groupID, lookupTestProxyBinding, lookupTestGroupBinding)
	require.Equal(t, "ACCOUNT_PROXY_BINDING_CONFLICT", infraerrors.Reason(err))
}

func TestNormalizeAdminProxyBindingRejectsVirtualIDWithExplicitClear(t *testing.T) {
	rawProxyID := int64(-12)
	proxyID := &rawProxyID
	rawGroupID := int64(0)
	groupID := &rawGroupID

	err := normalizeAdminProxyBinding(&proxyID, &groupID, lookupTestProxyBinding, lookupTestGroupBinding)
	require.Equal(t, "ACCOUNT_PROXY_BINDING_CONFLICT", infraerrors.Reason(err))
}

func TestNormalizeAdminProxyBindingKeepsRealProxyBinding(t *testing.T) {
	rawProxyID := int64(11)
	proxyID := &rawProxyID
	var groupID *int64

	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &groupID, lookupTestProxyBinding, lookupTestGroupBinding))
	require.Equal(t, int64(11), *proxyID)
	require.Nil(t, groupID)
}

func TestNormalizeAdminProxyBindingAcceptsPositiveGroupBinding(t *testing.T) {
	id := int64(6)
	groupID := int64(12)
	proxyID := &id
	legacyID := &groupID
	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &legacyID, lookupTestProxyBinding, lookupTestGroupBinding))
	require.Equal(t, int64(6), *proxyID)
	require.Nil(t, legacyID)
}

func TestNormalizeAdminProxyBindingConvertsLegacyGroupFieldAndClear(t *testing.T) {
	groupID := int64(12)
	var proxyID *int64
	legacyID := &groupID
	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &legacyID, lookupTestProxyBinding, lookupTestGroupBinding))
	require.Equal(t, int64(6), *proxyID)
	require.Nil(t, legacyID)

	clear := int64(0)
	legacyID = &clear
	proxyID = nil
	require.NoError(t, normalizeAdminProxyBinding(&proxyID, &legacyID, lookupTestProxyBinding, lookupTestGroupBinding))
	require.Equal(t, int64(0), *proxyID)
	require.Nil(t, legacyID)
}

func TestNormalizeAdminProxyBindingRejectsMissingPositiveID(t *testing.T) {
	id := int64(999)
	proxyID := &id
	var legacyID *int64
	err := normalizeAdminProxyBinding(&proxyID, &legacyID, lookupTestProxyBinding, lookupTestGroupBinding)
	require.Equal(t, "PROXY_BINDING_NOT_FOUND", infraerrors.Reason(err))
}

func TestOpenAIPrivacyProxyURLUsesGroupMemberAndFailsClosed(t *testing.T) {
	groupID, bindingID := int64(12), int64(6)
	group := ProxyIPGroup{ID: groupID, BindingID: bindingID, ProxyIDs: []int64{1}}
	proxies := &nativeProxyListRepoStub{members: []Proxy{{ID: 1, Protocol: "http", Host: "member.example.invalid", Port: 8080, Status: StatusActive}}}
	svc := &adminServiceImpl{proxyRepo: proxies}
	account := &Account{ProxyID: &bindingID, ProxyIPGroupID: &groupID, ProxyIPGroup: &group}
	url, ok := svc.openAIPrivacyProxyURL(context.Background(), account)
	require.True(t, ok)
	require.Equal(t, "http://member.example.invalid:8080", url)
	proxies.members = nil
	url, ok = svc.openAIPrivacyProxyURL(context.Background(), account)
	require.False(t, ok)
	require.Empty(t, url)
}

func TestAdminProxyBindingRegistryRejectsGroupAsRealProxy(t *testing.T) {
	client := newAdminProxyBindingTestClient(t, 1)
	groupID := int64(12)
	groupBinding, err := client.ProxyBinding.Create().SetBindingType(proxyBindingTypeProxyIPGroup).SetProxyIPGroupID(groupID).Save(context.Background())
	require.NoError(t, err)
	groups := newProxyIPGroupRepoStub()
	groups.groups[groupID] = ProxyIPGroup{ID: groupID, BindingID: groupBinding.ID}
	svc := &adminServiceImpl{entClient: client, proxyIPGroupRepo: groups, proxyRepo: &nativeProxyListRepoStub{proxies: []Proxy{{ID: 1}}}}

	require.NoError(t, svc.requireRealProxyBinding(context.Background(), 1))
	err = svc.requireRealProxyBinding(context.Background(), groupBinding.ID)
	require.Equal(t, "PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", infraerrors.Reason(err))
	_, err = svc.GetProxy(context.Background(), groupBinding.ID)
	require.Equal(t, "PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", infraerrors.Reason(err))
	_, err = svc.TestProxy(context.Background(), groupBinding.ID)
	require.Equal(t, "PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", infraerrors.Reason(err))
	err = svc.DeleteProxy(context.Background(), groupBinding.ID)
	require.Equal(t, "PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", infraerrors.Reason(err))
}

func TestAdminServiceNativeProxyListIncludesPositiveProxyGroup(t *testing.T) {
	groupRepo := newProxyIPGroupRepoStub()
	groupRepo.groups[12] = ProxyIPGroup{ID: 12, BindingID: 6, Name: "美国轮换组", PerIPConcurrency: 10, ProxyIDs: []int64{1, 2}}
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
	require.Equal(t, int64(6), groupItem.ID)
	require.Equal(t, int64(12), *groupItem.ProxyIPGroupID)
	require.Equal(t, 2, groupItem.MemberCount)
	require.Equal(t, 1, groupItem.AvailableMemberCount)
	require.Equal(t, proxyGroupStatusAvailable, groupItem.Status)
	require.NotNil(t, realItem)
	require.Equal(t, int64(1), *realItem.ProxyID)
}
