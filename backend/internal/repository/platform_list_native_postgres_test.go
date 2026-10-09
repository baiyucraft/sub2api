//go:build distribution_postgres && !integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/service"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// This opt-in suite executes the original 287/290 SQL and real repositories in
// a disposable database on a literal-loopback, non-default PostgreSQL port.
// Its minimum schema tests the platform boundary only; it does not replace the
// complete migration chain, integration suite or VM release Gate.
func TestPlatformListNativePostgres(t *testing.T) {
	ctx := context.Background()
	db := newNativePostgresTestDatabase(t, "platform_catalog")
	for _, ddl := range []string{
		`CREATE TABLE users (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE groups (id BIGINT PRIMARY KEY)`,
		`INSERT INTO users VALUES (1), (2), (3)`,
		`INSERT INTO groups VALUES (1), (2)`,
		`CREATE TABLE user_platform_quotas (
			id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id), platform VARCHAR(32) NOT NULL,
			daily_limit_usd DECIMAL(20,10), weekly_limit_usd DECIMAL(20,10), monthly_limit_usd DECIMAL(20,10),
			daily_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0, weekly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
			monthly_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0, daily_window_start TIMESTAMPTZ,
			weekly_window_start TIMESTAMPTZ, monthly_window_start TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), deleted_at TIMESTAMPTZ)`,
		`CREATE UNIQUE INDEX userplatformquota_user_id_platform_uq ON user_platform_quotas(user_id, platform) WHERE deleted_at IS NULL`,
		`CREATE TABLE composite_model_routes (
			id BIGSERIAL PRIMARY KEY, group_id BIGINT NOT NULL REFERENCES groups(id), public_model VARCHAR(200) NOT NULL,
			match_type VARCHAR(20) NOT NULL DEFAULT 'exact', target_platform VARCHAR(50) NOT NULL,
			upstream_model VARCHAR(200) NOT NULL DEFAULT '', endpoint VARCHAR(50) NOT NULL DEFAULT 'any',
			priority INTEGER NOT NULL DEFAULT 100, enabled BOOLEAN NOT NULL DEFAULT TRUE, notes TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE channel_monitors (
			id BIGSERIAL PRIMARY KEY, provider TEXT NOT NULL,
			CONSTRAINT channel_monitors_provider_check CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'zhipu')))`,
		`CREATE TABLE channel_monitor_request_templates (
			id BIGSERIAL PRIMARY KEY, provider TEXT NOT NULL,
			CONSTRAINT channel_monitor_request_templates_provider_check CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'zhipu')))`,
	} {
		_, err := db.ExecContext(ctx, ddl)
		require.NoError(t, err)
	}
	legacy, err := dbmigrations.FS.ReadFile("287_add_typesafe_platform.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(legacy))
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	quotaRepo := NewUserPlatformQuotaRepository(client)
	routeRepo := NewCompositeModelRouteRepository(client)

	t.Run("old_287_checks_to_repeatable_290", func(t *testing.T) {
		require.True(t, nativePlatformConstraintExists(t, db, "user_platform_quotas", "user_platform_quotas_platform_check"))
		require.True(t, nativePlatformConstraintExists(t, db, "composite_model_routes", "composite_model_routes_target_platform_check"))
		_, err := db.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id, platform, daily_limit_usd) VALUES (1, 'openai', 4)`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO composite_model_routes(group_id, public_model, target_platform) VALUES (1, 'old-model', 'openai')`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id, platform) VALUES (1, 'cline')`)
		require.ErrorContains(t, err, "user_platform_quotas_platform_check")
		_, err = db.ExecContext(ctx, `INSERT INTO composite_model_routes(group_id, public_model, target_platform) VALUES (1, 'new-model', 'command_code')`)
		require.ErrorContains(t, err, "composite_model_routes_target_platform_check")
		before := nativePlatformRowsSnapshot(t, db)
		migration, err := dbmigrations.FS.ReadFile("290_drop_platform_check_constraints.sql")
		require.NoError(t, err)
		for range 2 {
			_, err = db.ExecContext(ctx, string(migration))
			require.NoError(t, err)
		}
		require.Equal(t, before, nativePlatformRowsSnapshot(t, db), "dropping checks must not change observations or quota/route rows")
		require.False(t, nativePlatformConstraintExists(t, db, "user_platform_quotas", "user_platform_quotas_platform_check"))
		require.False(t, nativePlatformConstraintExists(t, db, "composite_model_routes", "composite_model_routes_target_platform_check"))
		for _, table := range []string{"channel_monitors", "channel_monitor_request_templates"} {
			require.True(t, nativePlatformConstraintExists(t, db, table, table+"_provider_check"))
			_, err = db.ExecContext(ctx, `INSERT INTO `+table+`(provider) VALUES ('cline')`)
			require.ErrorContains(t, err, table+"_provider_check")
		}
	})

	t.Run("registered_platforms_real_repository_writes", func(t *testing.T) {
		daily := 3.0
		require.NoError(t, quotaRepo.BulkInsertInitial(ctx, []UserPlatformQuotaRecord{
			{UserID: 2, Platform: service.PlatformCline, DailyLimitUSD: &daily},
		}))
		require.NoError(t, quotaRepo.UpsertForUser(ctx, 2, []UserPlatformQuotaRecord{
			{UserID: 2, Platform: service.PlatformCline, DailyLimitUSD: &daily},
			{UserID: 2, Platform: service.PlatformCommandCode, DailyLimitUSD: &daily},
		}))
		for _, platform := range []string{service.PlatformCline, service.PlatformCommandCode} {
			record, err := quotaRepo.GetByUserPlatform(ctx, 2, platform)
			require.NoError(t, err)
			require.NotNil(t, record)
			require.Equal(t, daily, *record.DailyLimitUSD)
			route := nativePlatformTestRoute(platform)
			require.NoError(t, routeRepo.Create(ctx, route))
			route.Notes = "updated through real repository"
			require.NoError(t, routeRepo.Update(ctx, route))
		}
		routes, err := routeRepo.ListByGroup(ctx, 2, true)
		require.NoError(t, err)
		require.Len(t, routes, 2)
	})

	t.Run("invalid_platforms_rejected_without_batch_side_effects", func(t *testing.T) {
		daily := 9.0
		for _, platform := range []string{"bogus", "moonshot", "Kimi", service.PlatformComposite} {
			before := nativePlatformRowsSnapshot(t, db)
			batch := []UserPlatformQuotaRecord{
				{UserID: 2, Platform: service.PlatformAnthropic, DailyLimitUSD: &daily},
				{UserID: 2, Platform: platform, DailyLimitUSD: &daily},
			}
			require.ErrorContains(t, quotaRepo.BulkInsertInitial(ctx, batch), "is not allowed", platform)
			require.ErrorContains(t, quotaRepo.UpsertForUser(ctx, 2, batch), "is not allowed", platform)
			require.Error(t, routeRepo.Create(ctx, nativePlatformTestRoute(platform)), platform)
			routes, err := routeRepo.ListByGroup(ctx, 2, true)
			require.NoError(t, err)
			require.Len(t, routes, 2)
			routes[0].TargetPlatform = platform
			require.Error(t, routeRepo.Update(ctx, &routes[0]), platform)
			require.Equal(t, before, nativePlatformRowsSnapshot(t, db), platform)
		}
		require.NoError(t, quotaRepo.BulkInsertInitial(ctx, []UserPlatformQuotaRecord{{UserID: 2, Platform: "bogus"}}))
	})

	t.Run("direct_sql_accepts_future_platform_after_290", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id, platform, daily_limit_usd) VALUES (3, 'future_platform', 1)`)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `INSERT INTO composite_model_routes(group_id, public_model, target_platform) VALUES (2, 'future-model', 'future_platform')`)
		require.NoError(t, err)
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_platform_quotas WHERE platform = 'future_platform'`).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM composite_model_routes WHERE target_platform = 'future_platform'`).Scan(&count))
		require.Equal(t, 1, count)
	})
}

func nativePlatformTestRoute(platform string) *service.CompositeModelRoute {
	return &service.CompositeModelRoute{
		GroupID: 2, PublicModel: "model-" + platform, MatchType: "exact", TargetPlatform: platform,
		UpstreamModel: "provider-model", Endpoint: "any", Priority: 100, Enabled: true,
	}
}

func nativePlatformConstraintExists(t *testing.T, db *sql.DB, table, constraint string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRowContext(context.Background(), `SELECT EXISTS(
		SELECT 1 FROM pg_constraint c JOIN pg_class tbl ON tbl.oid = c.conrelid
		JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
		WHERE ns.nspname = 'public' AND tbl.relname = $1 AND c.conname = $2)`, table, constraint).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func nativePlatformRowsSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var quotas, routes string
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(q) ORDER BY id), '[]'::jsonb)::text FROM user_platform_quotas q`).Scan(&quotas))
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY id), '[]'::jsonb)::text FROM composite_model_routes r`).Scan(&routes))
	return []string{quotas, routes}
}
