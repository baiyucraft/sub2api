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

func TestAccountsToServiceProjectsEffectiveUpstreamAPIURL(t *testing.T) {
	db, err := sql.Open("sqlite", "file:account_upstream_url?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	separateAPIURL := "https://api.example.com/v1"
	configs := []*dbent.UpstreamConfig{
		mustCreateUpstreamConfigForURLTest(t, ctx, client, "separate", "https://site.example.com", &separateAPIURL),
		mustCreateUpstreamConfigForURLTest(t, ctx, client, "fallback", "https://same.example.com", nil),
	}
	accounts := make([]*dbent.Account, 0, len(configs))
	for _, config := range configs {
		account, createErr := client.Account.Create().
			SetName(config.Name).
			SetType(service.AccountTypeAPIKey).
			SetPlatform(service.PlatformOpenAI).
			SetCredentials(map[string]any{"base_url": "https://stale.example.com"}).
			SetExtra(map[string]any{}).
			SetUpstreamConfigID(config.ID).
			Save(ctx)
		require.NoError(t, createErr)
		accounts = append(accounts, account)
	}

	repo := newAccountRepositoryWithSQL(client, nil, nil)
	got, err := repo.accountsToService(ctx, accounts)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, separateAPIURL, got[0].Credentials["base_url"])
	require.Equal(t, "https://same.example.com", got[1].Credentials["base_url"])
}

func mustCreateUpstreamConfigForURLTest(t *testing.T, ctx context.Context, client *dbent.Client, name, siteURL string, apiURL *string) *dbent.UpstreamConfig {
	t.Helper()
	builder := client.UpstreamConfig.Create().
		SetName(name).
		SetProvider(service.UpstreamProviderOther).
		SetSiteURL(siteURL).
		SetAuthMode(service.UpstreamAuthModeUserLogin)
	if apiURL != nil {
		builder.SetAPIURL(*apiURL)
	}
	config, err := builder.Save(ctx)
	require.NoError(t, err)
	return config
}
