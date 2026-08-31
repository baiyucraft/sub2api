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

func TestAccountsToServiceLoadsOrderedProxyBindings(t *testing.T) {
	db, err := sql.Open("sqlite", "file:account_multi_proxy?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	first := createProxyForMultiProxyTest(t, ctx, client, "first", 10001)
	second := createProxyForMultiProxyTest(t, ctx, client, "second", 10002)
	account, err := client.Account.Create().
		SetName("multi-proxy").
		SetType(service.AccountTypeOAuth).
		SetPlatform(service.PlatformOpenAI).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetProxyID(first.ID).
		Save(ctx)
	require.NoError(t, err)

	_, err = client.AccountProxyBinding.Create().
		SetAccountID(account.ID).
		SetProxyID(second.ID).
		SetPosition(1).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountProxyBinding.Create().
		SetAccountID(account.ID).
		SetProxyID(first.ID).
		SetPosition(0).
		Save(ctx)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.accountsToService(ctx, []*dbent.Account{account})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []int64{first.ID, second.ID}, got[0].ProxyIDs)
	require.Len(t, got[0].Proxies, 2)
	require.Equal(t, []string{"first", "second"}, []string{got[0].Proxies[0].Name, got[0].Proxies[1].Name})
	require.NotNil(t, got[0].ProxyID)
	require.Equal(t, first.ID, *got[0].ProxyID)
	require.NotNil(t, got[0].Proxy)
	require.Equal(t, first.ID, got[0].Proxy.ID)
}

func TestAccountsToServiceFallsBackToLegacyProxyID(t *testing.T) {
	db, err := sql.Open("sqlite", "file:account_legacy_proxy?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	proxy := createProxyForMultiProxyTest(t, ctx, client, "legacy", 10003)
	account, err := client.Account.Create().
		SetName("legacy-proxy").
		SetType(service.AccountTypeOAuth).
		SetPlatform(service.PlatformOpenAI).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetProxyID(proxy.ID).
		Save(ctx)
	require.NoError(t, err)

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.accountsToService(ctx, []*dbent.Account{account})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []int64{proxy.ID}, got[0].ProxyIDs)
	require.Len(t, got[0].Proxies, 1)
	require.Equal(t, proxy.ID, got[0].Proxies[0].ID)
}

func TestAccountsToServiceKeepsSoftDeletedBoundProxyVisibleButUnavailable(t *testing.T) {
	db, err := sql.Open("sqlite", "file:account_deleted_proxy?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	proxy := createProxyForMultiProxyTest(t, ctx, client, "deleted-route", 10004)
	account, err := client.Account.Create().
		SetName("deleted-proxy-account").
		SetType(service.AccountTypeOAuth).
		SetPlatform(service.PlatformOpenAI).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetProxyID(proxy.ID).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountProxyBinding.Create().
		SetAccountID(account.ID).
		SetProxyID(proxy.ID).
		SetPosition(0).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.Proxy.DeleteOneID(proxy.ID).Exec(ctx))

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.accountsToService(ctx, []*dbent.Account{account})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, []int64{proxy.ID}, got[0].ProxyIDs)
	require.Len(t, got[0].Proxies, 1)
	require.Equal(t, "deleted-route", got[0].Proxies[0].Name)
	require.Equal(t, service.StatusDisabled, got[0].Proxies[0].Status)
}

func createProxyForMultiProxyTest(t *testing.T, ctx context.Context, client *dbent.Client, name string, port int) *dbent.Proxy {
	t.Helper()
	proxy, err := client.Proxy.Create().
		SetName(name).
		SetProtocol("http").
		SetHost("127.0.0.1").
		SetPort(port).
		Save(ctx)
	require.NoError(t, err)
	return proxy
}
