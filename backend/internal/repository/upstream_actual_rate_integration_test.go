//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamActualRateTriggerDerivesAccountFields(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("actual-rate-%d", time.Now().UnixNano())).
		SetProvider(service.UpstreamProviderNewAPI).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeCookie).
		SetRechargeRate(0.1).
		Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).
		SetName("pro").
		SetKey("sk-actual-rate-trigger").
		SetKeyHash(service.HashUpstreamKey("sk-actual-rate-trigger")).
		SetPlatform(service.PlatformOpenAI).
		SetPlatformSource(service.UpstreamKeyPlatformSourceManual).
		SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).
		SetSourceRateMultiplier(8).
		SetRateMultiplier(0.8).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	account, err := client.Account.Create().
		SetName("actual-rate-trigger").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetConcurrency(200).
		SetRateMultiplier(99).
		SetPriority(9999).
		SetLoadFactor(9999).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(key.ID).
		Save(ctx)
	require.NoError(t, err)
	require.InDelta(t, 0.8, account.RateMultiplier, 0.00001)
	require.Equal(t, 80, account.Priority)
	require.NotNil(t, account.LoadFactor)
	require.Equal(t, 100, *account.LoadFactor)

	updated, err := client.Account.UpdateOneID(account.ID).
		SetConcurrency(100).
		SetRateMultiplier(77).
		SetPriority(7777).
		SetLoadFactor(7777).
		Save(ctx)
	require.NoError(t, err)
	require.InDelta(t, 0.8, updated.RateMultiplier, 0.00001)
	require.Equal(t, 80, updated.Priority)
	require.NotNil(t, updated.LoadFactor)
	require.Equal(t, 50, *updated.LoadFactor)
}

func TestUpstreamActualRateTriggerRejectsKeyWithoutActualRate(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("missing-actual-rate-%d", time.Now().UnixNano())).
		SetProvider(service.UpstreamProviderNewAPI).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeCookie).
		Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).
		SetName("unresolved").
		SetKey("sk-missing-actual-rate").
		SetKeyHash(service.HashUpstreamKey("sk-missing-actual-rate")).
		SetPlatform(service.PlatformOpenAI).
		SetPlatformSource(service.UpstreamKeyPlatformSourceManual).
		SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	_, err = client.Account.Create().
		SetName("missing-actual-rate").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetConcurrency(100).
		SetPriority(1).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(key.ID).
		Save(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "without an actual rate")

	count, countErr := client.Account.Query().Where(dbaccount.UpstreamConfigIDEQ(config.ID)).Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, count)
}

func TestUpstreamActualRateTriggerRecalculatesRestoredAccountAndRejectsConcurrencyOverflow(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("restore-actual-rate-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderNewAPI).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeCookie).Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetName("restore").SetKey("sk-restore-actual-rate").SetKeyHash(service.HashUpstreamKey("sk-restore-actual-rate")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceManual).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetSourceRateMultiplier(0.8).SetRateMultiplier(0.8).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().SetName("restore-actual-rate").SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetConcurrency(100).SetPriority(80).SetStatus(service.StatusActive).SetSchedulable(true).SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NOW() WHERE id = $1", account.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE upstream_keys SET rate_multiplier = 0.2 WHERE id = $1", key.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET deleted_at = NULL WHERE id = $1", account.ID)
	require.NoError(t, err)
	restored, err := client.Account.Get(ctx, account.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.2, restored.RateMultiplier, 0.00001)
	require.Equal(t, 20, restored.Priority)
	require.NotNil(t, restored.LoadFactor)
	require.Equal(t, 100, *restored.LoadFactor)

	_, err = client.Account.UpdateOneID(account.ID).SetConcurrency(1073741824).Save(ctx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot derive a safe load factor")
}
