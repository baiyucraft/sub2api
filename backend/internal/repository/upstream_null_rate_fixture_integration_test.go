//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

type upstreamNullRateFixture struct {
	client         *dbent.Client
	repo           *upstreamConfigRepository
	config         *dbent.UpstreamConfig
	missingKey     *dbent.UpstreamKey
	healthyKey     *dbent.UpstreamKey
	missingAccount *dbent.Account
	healthyAccount *dbent.Account
	groupID        int64
	planID         int64
	now            time.Time
}

func newUpstreamNullRateFixture(t *testing.T, owner string) *upstreamNullRateFixture {
	t.Helper()
	ctx := context.Background()
	f := &upstreamNullRateFixture{client: testEntClient(t), now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	f.repo = &upstreamConfigRepository{client: f.client}
	var err error
	f.config, err = f.client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("null-rate-%d", time.Now().UnixNano())).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		SetRechargeRate(1).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, query := range []string{
			`DELETE FROM scheduler_outbox WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)
			 OR EXISTS (SELECT 1 FROM accounts a WHERE a.upstream_config_id = $1 AND payload->'account_ids' @> to_jsonb(ARRAY[a.id]))`,
			"DELETE FROM scheduled_test_results WHERE plan_id IN (SELECT id FROM scheduled_test_plans WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1))",
			"DELETE FROM scheduled_test_plans WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)",
			"DELETE FROM account_groups WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)",
			"DELETE FROM upstream_events WHERE upstream_config_id = $1",
			"DELETE FROM accounts WHERE upstream_config_id = $1",
			"DELETE FROM upstream_keys WHERE upstream_config_id = $1",
			"DELETE FROM upstream_configs WHERE id = $1",
		} {
			_, cleanupErr := integrationDB.ExecContext(cleanupCtx, query, f.config.ID)
			require.NoError(t, cleanupErr)
		}
		if f.groupID != 0 {
			_, cleanupErr := integrationDB.ExecContext(cleanupCtx, "DELETE FROM groups WHERE id = $1", f.groupID)
			require.NoError(t, cleanupErr)
		}
		for _, key := range []*dbent.UpstreamKey{f.missingKey, f.healthyKey} {
			if key != nil {
				service.GlobalUpstreamHealthRegistry().Forget(key.ID)
			}
		}
	})
	createKey := func(name string, remoteID int64, rate float64) *dbent.UpstreamKey {
		key, createErr := f.client.UpstreamKey.Create().SetUpstreamConfigID(f.config.ID).
			SetName(name).SetRemoteKeyID(remoteID).SetKey("sk-test-" + name).
			SetKeyHash(service.HashUpstreamKey("sk-test-" + name)).
			SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceManual).
			SetStatus(service.StatusActive).SetSourceRateMultiplier(rate).SetRateMultiplier(rate).
			SetLastSeenAt(f.now).Save(ctx)
		require.NoError(t, createErr)
		return key
	}
	f.missingKey = createKey("missing", 98501, 0.1)
	f.healthyKey = createKey("healthy", 98502, 0.2)
	createAccount := func(key *dbent.UpstreamKey, lifecycleOwner string) *dbent.Account {
		name, nameErr := service.BuildUpstreamAccountName(f.config.Name, key.Name)
		require.NoError(t, nameErr)
		account, createErr := f.client.Account.Create().SetName(name).
			SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{"base_url": "https://example.com"}).SetExtra(map[string]any{}).
			SetConcurrency(100).SetPriority(99).SetLoadFactor(7777).
			SetStatus(service.StatusActive).SetSchedulable(true).
			SetUpstreamConfigID(f.config.ID).SetUpstreamKeyID(key.ID).
			SetUpstreamLifecycleOwner(lifecycleOwner).Save(ctx)
		require.NoError(t, createErr)
		account, createErr = f.client.Account.Get(ctx, account.ID)
		require.NoError(t, createErr)
		return account
	}
	f.missingAccount = createAccount(f.missingKey, owner)
	f.healthyAccount = createAccount(f.healthyKey, service.AccountUpstreamLifecycleOwnerManual)
	group, err := f.client.Group.Create().SetName(f.config.Name + "-group").SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	f.groupID = group.ID
	_, err = f.client.AccountGroup.Create().SetAccountID(f.missingAccount.ID).SetGroupID(group.ID).SetPriority(7).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, created_at, updated_at)
		VALUES ($1, 'gpt-test', '*/30 * * * *', true, 10, false, $2, NOW(), NOW()) RETURNING id
	`, f.missingAccount.ID, f.now.Add(time.Hour)).Scan(&f.planID))
	// Start with a valid binding, then reproduce a later authoritative NULL rate.
	_, err = f.client.UpstreamKey.UpdateOneID(f.missingKey.ID).ClearRateMultiplier().ClearSourceRateMultiplier().Save(ctx)
	require.NoError(t, err)
	f.assertHistoricalRate(t, f.missingAccount)
	return f
}

func (f *upstreamNullRateFixture) incoming(key *dbent.UpstreamKey, rate *float64, at time.Time) service.UpstreamKey {
	return service.UpstreamKey{
		UpstreamConfigID: f.config.ID, Name: key.Name, Key: key.Key, KeyHash: key.KeyHash,
		RemoteKeyID: key.RemoteKeyID, Platform: key.Platform, PlatformSource: key.PlatformSource,
		SourceRateMultiplier: rate, Status: service.StatusActive, LastSeenAt: &at,
	}
}

func (f *upstreamNullRateFixture) applyHealthy(t *testing.T, at time.Time, rate float64) (service.UpstreamKeyReconcileResult, int, error) {
	t.Helper()
	_, reconciled, updated, err := f.repo.ApplySyncSnapshot(context.Background(), f.config.ID, 0,
		[]service.UpstreamKey{f.incoming(f.healthyKey, &rate, at)}, map[string]any{"fixture_snapshot": at.Format(time.RFC3339)}, at, true)
	return reconciled, updated, err
}

func (f *upstreamNullRateFixture) assertHistoricalRate(t *testing.T, account *dbent.Account) {
	t.Helper()
	require.InDelta(t, 0.1, account.RateMultiplier, 1e-10)
	require.NotNil(t, account.UpstreamSourceRateMultiplier)
	require.InDelta(t, 0.1, *account.UpstreamSourceRateMultiplier, 1e-10)
	require.Equal(t, 10, account.Priority)
	require.NotNil(t, account.LoadFactor)
	require.Equal(t, 7777, *account.LoadFactor)
	require.Equal(t, f.missingKey.ID, *account.UpstreamKeyID)
	require.Equal(t, f.config.ID, *account.UpstreamConfigID)
}

func (f *upstreamNullRateFixture) assertDependencies(t *testing.T) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM account_groups WHERE account_id = $1 AND group_id = $2 AND priority = 7", f.missingAccount.ID, f.groupID).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM scheduled_test_plans WHERE id = $1 AND account_id = $2 AND enabled", f.planID, f.missingAccount.ID).Scan(&count))
	require.Equal(t, 1, count)
}

func (f *upstreamNullRateFixture) assertEvent(t *testing.T, eventType string) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM upstream_events WHERE upstream_config_id = $1 AND upstream_key_id = $2 AND event_type = $3",
		f.config.ID, f.missingKey.ID, eventType).Scan(&count))
	require.Equal(t, 1, count, eventType)
}

func (f *upstreamNullRateFixture) state(t *testing.T) any {
	t.Helper()
	var raw []byte
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		SELECT jsonb_build_object(
		 'config', (SELECT to_jsonb(c) FROM upstream_configs c WHERE id = $1),
		 'keys', (SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM upstream_keys k WHERE upstream_config_id = $1),
		 'accounts', (SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM accounts a WHERE upstream_config_id = $1),
		 'events', (SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM upstream_events e WHERE upstream_config_id = $1),
		 'outbox', (SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM scheduler_outbox o
		   WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)
		   OR EXISTS (SELECT 1 FROM accounts a WHERE a.upstream_config_id = $1 AND o.payload->'account_ids' @> to_jsonb(ARRAY[a.id]))),
		 'groups', (SELECT jsonb_agg(to_jsonb(g) ORDER BY account_id, group_id) FROM account_groups g WHERE account_id = $2),
		 'plans', (SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM scheduled_test_plans p WHERE account_id = $2))
	`, f.config.ID, f.missingAccount.ID).Scan(&raw))
	var state any
	require.NoError(t, json.Unmarshal(raw, &state))
	return state
}

func useMigration241BindingFunction(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var current string
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT pg_get_functiondef('validate_account_upstream_key_binding()'::regprocedure)").Scan(&current))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), current)
		require.NoError(t, err)
	})
	raw, err := migrations.FS.ReadFile("241_precise_upstream_effective_rate.sql")
	require.NoError(t, err)
	sql := string(raw)
	start := strings.Index(sql, "CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()")
	require.NotEqual(t, -1, start)
	end := strings.Index(sql[start:], "$$ LANGUAGE plpgsql;")
	require.NotEqual(t, -1, end)
	_, err = integrationDB.ExecContext(ctx, sql[start:start+end+len("$$ LANGUAGE plpgsql;")])
	require.NoError(t, err)
}

func getNullRateAccount(t *testing.T, f *upstreamNullRateFixture) *dbent.Account {
	t.Helper()
	account, err := f.client.Account.Get(mixins.SkipSoftDelete(context.Background()), f.missingAccount.ID)
	require.NoError(t, err)
	return account
}
