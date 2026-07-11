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

func TestUpstreamSettingsRoundTripAllFields(t *testing.T) {
	db, err := sql.Open("sqlite", "file:upstream_settings?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	repo := &upstreamConfigRepository{client: client}
	ctx := context.Background()
	require.NoError(t, repo.UpdateUpstreamSettings(ctx, service.UpstreamSettings{
		BalanceLowThresholdCNY:  12.5,
		Sub2APINotInCNConfirmed: true,
	}))

	settings, err := repo.GetUpstreamSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 12.5, settings.BalanceLowThresholdCNY)
	require.True(t, settings.Sub2APINotInCNConfirmed)

	require.NoError(t, repo.UpdateUpstreamSettings(ctx, service.UpstreamSettings{
		BalanceLowThresholdCNY:  8,
		Sub2APINotInCNConfirmed: false,
	}))
	settings, err = repo.GetUpstreamSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, 8.0, settings.BalanceLowThresholdCNY)
	require.False(t, settings.Sub2APINotInCNConfirmed)
}
