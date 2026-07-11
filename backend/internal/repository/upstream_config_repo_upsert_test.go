package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestUpsertKeyBackfillsRemoteIDOnExistingHash(t *testing.T) {
	db, err := sql.Open("sqlite", "file:upstream_key_upsert?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	config, err := client.UpstreamConfig.Create().
		SetName("test").SetProvider(service.UpstreamProviderNewAPI).
		SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeUserLogin).
		Save(ctx)
	require.NoError(t, err)
	existing, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).SetName("old").SetKey("sk-secret").
		SetKeyHash(service.HashUpstreamKey("sk-secret")).Save(ctx)
	require.NoError(t, err)

	remoteID := int64(42)
	repo := &upstreamConfigRepository{client: client}
	incoming := &service.UpstreamKey{
		UpstreamConfigID: config.ID, Name: "new", Key: "sk-secret",
		KeyHash: service.HashUpstreamKey("sk-secret"), RemoteKeyID: &remoteID,
		Platform: service.PlatformOpenAI, Status: service.StatusActive,
	}
	require.NoError(t, repo.UpsertKey(ctx, incoming))
	require.Equal(t, existing.ID, incoming.ID)
	require.Equal(t, 1, mustCountUpstreamKeys(t, ctx, client, config.ID))
	updated, err := client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(existing.ID)).Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, updated.RemoteKeyID)
	require.Equal(t, remoteID, *updated.RemoteKeyID)
}

func TestUpstreamKeyMissingEligible(t *testing.T) {
	since := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	require.False(t, upstreamKeyMissingEligible(2, &since, since.Add(time.Hour)))
	require.False(t, upstreamKeyMissingEligible(3, &since, since.Add(29*time.Minute+59*time.Second)))
	require.True(t, upstreamKeyMissingEligible(3, &since, since.Add(30*time.Minute)))
	require.False(t, upstreamKeyMissingEligible(10, nil, since.Add(time.Hour)))
}

func mustCountUpstreamKeys(t *testing.T, ctx context.Context, client *dbent.Client, configID int64) int {
	t.Helper()
	count, err := client.UpstreamKey.Query().Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).Count(ctx)
	require.NoError(t, err)
	return count
}
