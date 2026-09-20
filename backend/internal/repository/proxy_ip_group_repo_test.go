package repository

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestProxyIPGroupRepositoryGetByIDPreservesMemberOrder(t *testing.T) {
	client := newProxyIPGroupTestClient(t)
	ctx := context.Background()

	first := createProxyIPGroupTestProxy(t, ctx, client, "first")
	second := createProxyIPGroupTestProxy(t, ctx, client, "second")
	group, err := client.ProxyIPGroup.Create().
		SetName("codex-pool").
		SetPerIPConcurrency(4).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ProxyIPGroupMember.Create().
		SetProxyIPGroupID(group.ID).
		SetProxyID(second.ID).
		SetPosition(0).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ProxyIPGroupMember.Create().
		SetProxyIPGroupID(group.ID).
		SetProxyID(first.ID).
		SetPosition(1).
		Save(ctx)
	require.NoError(t, err)

	repo := NewProxyIPGroupRepository(client)
	got, err := repo.GetByID(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, group.ID, got.ID)
	require.Equal(t, "codex-pool", got.Name)
	require.Equal(t, 4, got.PerIPConcurrency)
	require.Equal(t, []int64{second.ID, first.ID}, got.ProxyIDs)
}

func TestAccountsToServiceHydratesProxyIPGroupSummary(t *testing.T) {
	client := newProxyIPGroupTestClient(t)
	ctx := context.Background()

	proxy := createProxyIPGroupTestProxy(t, ctx, client, "member")
	group, err := client.ProxyIPGroup.Create().
		SetName("account-pool").
		SetPerIPConcurrency(2).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.ProxyIPGroupMember.Create().
		SetProxyIPGroupID(group.ID).
		SetProxyID(proxy.ID).
		SetPosition(0).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName("oauth-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeOAuth).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetProxyIPGroupID(group.ID).
		Save(ctx)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.accountsToService(ctx, []*dbent.Account{account})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].ProxyIPGroupID)
	require.Equal(t, group.ID, *got[0].ProxyIPGroupID)
	require.NotNil(t, got[0].ProxyIPGroup)
	require.Equal(t, []int64{proxy.ID}, got[0].ProxyIPGroup.ProxyIDs)
}

func TestProxyIPGroupRepositoryGetByIDNotFound(t *testing.T) {
	client := newProxyIPGroupTestClient(t)
	repo := NewProxyIPGroupRepository(client)
	_, err := repo.GetByID(context.Background(), 999)
	require.ErrorIs(t, err, service.ErrProxyIPGroupNotFound)
}

func newProxyIPGroupTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func createProxyIPGroupTestProxy(t *testing.T, ctx context.Context, client *dbent.Client, name string) *dbent.Proxy {
	t.Helper()
	proxy, err := client.Proxy.Create().
		SetName(name).
		SetProtocol("http").
		SetHost(name + ".example.invalid").
		SetPort(8080).
		Save(ctx)
	require.NoError(t, err)
	return proxy
}
