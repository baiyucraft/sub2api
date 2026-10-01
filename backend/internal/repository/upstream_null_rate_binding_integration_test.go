//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUpstreamNullRateBindingAllowsOnlyPreservedLifecycleWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare string
		update  string
		allowed bool
	}{
		{name: "pause", update: "schedulable = false", allowed: true},
		{name: "pause_explicit_preserved_billing", update: "schedulable = false, rate_multiplier = 0.1, upstream_source_rate_multiplier = 0.1, priority = 10", allowed: true},
		{name: "archive", update: "deleted_at = NOW()", allowed: true},
		{name: "archive_and_pause", update: "deleted_at = NOW(), schedulable = false", allowed: true},
		{name: "archive_already_paused", prepare: "schedulable = false", update: "deleted_at = NOW()", allowed: true},
		{name: "archived_pause", prepare: "deleted_at = NOW()", update: "schedulable = false", allowed: true},
		{name: "repeat_pause", prepare: "schedulable = false", update: "schedulable = false", allowed: true},
		{name: "repeat_archive", prepare: "deleted_at = NOW()", update: "deleted_at = deleted_at", allowed: true},
		{name: "refresh_archive_timestamp", prepare: "deleted_at = NOW()", update: "deleted_at = NOW()", allowed: true},
		{name: "active_rate_write", update: "rate_multiplier = rate_multiplier"},
		{name: "reenable", prepare: "schedulable = false", update: "schedulable = true"},
		{name: "restore", prepare: "deleted_at = NOW()", update: "deleted_at = NULL"},
		{name: "restore_paused", prepare: "deleted_at = NOW(), schedulable = false", update: "deleted_at = NULL"},
		{name: "archive_cannot_reenable", prepare: "schedulable = false", update: "deleted_at = NOW(), schedulable = true"},
		{name: "archived_cannot_reenable", prepare: "deleted_at = NOW(), schedulable = false", update: "schedulable = true"},
		{name: "pause_rate_change", update: "schedulable = false, rate_multiplier = 0.5"},
		{name: "pause_priority_change", update: "schedulable = false, priority = 50"},
		{name: "pause_source_change", update: "schedulable = false, upstream_source_rate_multiplier = 0.5"},
		{name: "pause_source_clear", update: "schedulable = false, upstream_source_rate_multiplier = NULL"},
		{name: "source_only_change", prepare: "schedulable = false", update: "upstream_source_rate_multiplier = 0.5"},
		{name: "pause_platform_change", update: "schedulable = false, platform = 'anthropic'"},
		{name: "pause_config_change", update: "schedulable = false, upstream_config_id = 0"},
		{name: "archive_config_change", update: "deleted_at = NOW(), upstream_config_id = 0"},
		{name: "archive_platform_change", update: "deleted_at = NOW(), platform = 'anthropic'"},
		{name: "archive_rate_change", update: "deleted_at = NOW(), rate_multiplier = 0.5"},
		{name: "archive_priority_change", update: "deleted_at = NOW(), priority = 50"},
		{name: "archive_source_change", update: "deleted_at = NOW(), upstream_source_rate_multiplier = 0.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpstreamNullRateFixture(t, service.AccountUpstreamLifecycleOwnerSyncManaged)
			ctx := context.Background()
			if tc.prepare != "" {
				_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET "+tc.prepare+" WHERE id = $1", f.missingAccount.ID)
				require.NoError(t, err)
			}
			before := f.state(t)
			_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET "+tc.update+" WHERE id = $1", f.missingAccount.ID)
			if tc.allowed {
				require.NoError(t, err)
				f.assertHistoricalRate(t, getNullRateAccount(t, f))
			} else {
				var pgErr *pq.Error
				require.ErrorAs(t, err, &pgErr)
				require.Equal(t, pq.ErrorCode("23514"), pgErr.Code)
				require.Equal(t, before, f.state(t))
			}
		})
	}
}

func TestUpstreamNullRateBindingRejectsNewBindingsIncludingPausedAndArchived(t *testing.T) {
	f := newUpstreamNullRateFixture(t, service.AccountUpstreamLifecycleOwnerManual)
	ctx := context.Background()
	for _, schedulable := range []bool{true, false} {
		before := f.state(t)
		_, err := f.client.Account.Create().SetName("null-rate-new-binding").
			SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{}).SetExtra(map[string]any{}).
			SetConcurrency(100).SetPriority(10).SetStatus(service.StatusActive).
			SetSchedulable(schedulable).SetUpstreamConfigID(f.config.ID).SetUpstreamKeyID(f.missingKey.ID).Save(ctx)
		require.ErrorContains(t, err, "without an actual rate")
		require.Equal(t, before, f.state(t))
	}
	for _, update := range []string{
		"upstream_key_id = $2",
		"upstream_key_id = $2, schedulable = false",
		"upstream_key_id = $2, deleted_at = NOW()",
	} {
		before := f.state(t)
		_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET "+update+" WHERE id = $1", f.healthyAccount.ID, f.missingKey.ID)
		require.ErrorContains(t, err, "without an actual rate")
		require.Equal(t, before, f.state(t))
	}
	unbound, err := f.client.Account.Create().SetName("null-rate-unbound").
		SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{}).SetExtra(map[string]any{}).
		SetConcurrency(100).SetPriority(10).SetStatus(service.StatusActive).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", unbound.ID)
		require.NoError(t, cleanupErr)
	})
	_, err = integrationDB.ExecContext(ctx,
		"UPDATE accounts SET upstream_config_id = $2, upstream_key_id = $3 WHERE id = $1", unbound.ID, f.config.ID, f.missingKey.ID)
	require.ErrorContains(t, err, "without an actual rate")
	unbound, err = f.client.Account.Get(ctx, unbound.ID)
	require.NoError(t, err)
	require.Nil(t, unbound.UpstreamConfigID)
	require.Nil(t, unbound.UpstreamKeyID)
	before := f.state(t)
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO accounts (name, platform, type, credentials, extra, concurrency, priority, status, schedulable, upstream_config_id, upstream_key_id, deleted_at)
		VALUES ('null-rate-new-archive', 'openai', 'apikey', '{}'::jsonb, '{}'::jsonb, 100, 10, 'active', false, $1, $2, NOW())
	`, f.config.ID, f.missingKey.ID)
	require.ErrorContains(t, err, "without an actual rate")
	require.Equal(t, before, f.state(t))
}

func TestUpstreamNullRateBindingRestorationRequiresActiveMatchingKey(t *testing.T) {
	for _, tc := range []struct {
		name        string
		keyChange   string
		schedulable bool
		allowed     bool
	}{
		{name: "zero_active_schedulable", schedulable: true, allowed: true},
		{name: "zero_active_paused", allowed: true},
		{name: "inactive_paused", keyChange: "status = 'disabled'"},
		{name: "inactive_schedulable", keyChange: "status = 'disabled'", schedulable: true},
		{name: "stale_paused", keyChange: "status = 'stale'"},
		{name: "mismatched_paused", keyChange: "platform = 'anthropic'"},
		{name: "mismatched_schedulable", keyChange: "platform = 'anthropic'", schedulable: true},
		{name: "unassigned_paused", keyChange: "platform = NULL"},
		{name: "deleted_key_paused", keyChange: "deleted_at = NOW()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newUpstreamNullRateFixture(t, service.AccountUpstreamLifecycleOwnerSyncManaged)
			_, err := integrationDB.ExecContext(ctx,
				"UPDATE accounts SET deleted_at = NOW(), schedulable = false, status = 'disabled' WHERE id = $1", f.missingAccount.ID)
			require.NoError(t, err)
			_, err = integrationDB.ExecContext(ctx,
				"UPDATE upstream_keys SET rate_multiplier = 0, source_rate_multiplier = 0 WHERE id = $1", f.missingKey.ID)
			require.NoError(t, err)
			if tc.keyChange != "" {
				_, err = integrationDB.ExecContext(ctx, "UPDATE upstream_keys SET "+tc.keyChange+" WHERE id = $1", f.missingKey.ID)
				require.NoError(t, err)
			}
			before := f.state(t)
			_, err = integrationDB.ExecContext(ctx,
				"UPDATE accounts SET deleted_at = NULL, schedulable = $2 WHERE id = $1", f.missingAccount.ID, tc.schedulable)
			if !tc.allowed {
				var pgErr *pq.Error
				require.ErrorAs(t, err, &pgErr)
				require.Equal(t, pq.ErrorCode("23514"), pgErr.Code)
				require.Equal(t, before, f.state(t))
				return
			}
			require.NoError(t, err)
			account := getNullRateAccount(t, f)
			require.Nil(t, account.DeletedAt)
			require.Zero(t, account.RateMultiplier)
			require.Zero(t, account.Priority)
			require.NotNil(t, account.UpstreamSourceRateMultiplier)
			require.Zero(t, *account.UpstreamSourceRateMultiplier)
			require.Equal(t, tc.schedulable, account.Schedulable)
			require.Equal(t, service.StatusDisabled, account.Status)
			require.Equal(t, 7777, *account.LoadFactor)
			require.Equal(t, f.missingKey.ID, *account.UpstreamKeyID)
			f.assertDependencies(t)
		})
	}
}
