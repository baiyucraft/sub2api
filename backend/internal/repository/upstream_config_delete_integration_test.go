//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/accountgroup"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	"github.com/Wei-Shaw/sub2api/ent/upstreamauthsession"
	"github.com/Wei-Shaw/sub2api/ent/upstreamevent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamConfigCascadeDeleteSoftDeletesResourcesAndEmitsInvalidation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	cache := &schedulerCacheRecorder{accounts: make(map[int64]*service.Account)}
	repo := &upstreamConfigRepository{client: client, schedulerCache: cache}
	suffix := time.Now().UnixNano()

	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("cascade-success-%d", suffix)).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://cascade.example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).
		SetName("cascade-key").
		SetKey("sk-cascade-success").
		SetKeyHash(service.HashUpstreamKey("sk-cascade-success")).
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName("cascade-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetConcurrency(3).
		SetPriority(50).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(key.ID).
		SetUpstreamLifecycleOwner(service.AccountUpstreamLifecycleOwnerSyncManaged).
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName(fmt.Sprintf("cascade-group-%d", suffix)).Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	_, err = client.UpstreamAuthSession.Create().
		SetUpstreamConfigID(config.ID).
		SetProvider(config.Provider).
		SetAuthMode(config.AuthMode).
		SetCredentialFingerprint("fingerprint").
		SetSecretCiphertext("ciphertext").
		Save(ctx)
	require.NoError(t, err)
	cache.accounts[account.ID] = &service.Account{ID: account.ID}
	service.GlobalUpstreamHealthRegistry().SetObservationTransition(key.ID, false, time.Now().UTC())

	result, err := repo.DeleteWithOptions(ctx, config.ID, service.UpstreamConfigDeleteOptions{DeleteSyncManagedAccounts: true})
	require.NoError(t, err)
	require.Equal(t, service.UpstreamConfigDeleteResult{DeletedAccountCount: 1, DeletedKeyCount: 1}, result)
	require.Equal(t, []int64{account.ID}, cache.deleteIDs)
	require.True(t, service.GlobalUpstreamHealthRegistry().Snapshot(key.ID).ObservationEnabled)

	deletedAccount, err := client.Account.Get(mixins.SkipSoftDelete(ctx), account.ID)
	require.NoError(t, err)
	require.NotNil(t, deletedAccount.DeletedAt)
	deletedKey, err := client.UpstreamKey.Get(mixins.SkipSoftDelete(ctx), key.ID)
	require.NoError(t, err)
	require.NotNil(t, deletedKey.DeletedAt)
	deletedConfig, err := client.UpstreamConfig.Get(mixins.SkipSoftDelete(ctx), config.ID)
	require.NoError(t, err)
	require.NotNil(t, deletedConfig.DeletedAt)
	_, err = client.UpstreamAuthSession.Query().Where(upstreamauthsession.UpstreamConfigIDEQ(config.ID)).Only(ctx)
	require.Error(t, err)
	_, err = client.AccountGroup.Query().Where(accountgroup.AccountIDEQ(account.ID), accountgroup.GroupIDEQ(group.ID)).Only(ctx)
	require.NoError(t, err)

	event, err := client.UpstreamEvent.Query().Where(
		upstreamevent.UpstreamConfigIDEQ(config.ID),
		upstreamevent.EventTypeEQ("config_cascade_deleted"),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"config_id":     float64(config.ID),
		"account_count": float64(1),
		"key_count":     float64(1),
	}, decodeJSONMap(t, event.Payload))

	var outboxPayload []byte
	err = integrationDB.QueryRowContext(ctx, `
		SELECT payload
		FROM scheduler_outbox
		WHERE event_type = $1 AND payload->'account_ids' @> $2::jsonb
		ORDER BY id DESC LIMIT 1`, service.SchedulerOutboxEventAccountBulkChanged, fmt.Sprintf(`[%d]`, account.ID)).Scan(&outboxPayload)
	require.NoError(t, err)
	var outbox map[string]any
	require.NoError(t, json.Unmarshal(outboxPayload, &outbox))
	require.Equal(t, []any{float64(account.ID)}, outbox["account_ids"])
	require.Equal(t, []any{float64(group.ID)}, outbox["group_ids"])

	cleanupCascadeDeleteFixture(t, config.ID, key.ID, account.ID, group.ID)
}

func TestUpstreamConfigCascadeDeleteRejectsManualWithoutWrites(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	suffix := time.Now().UnixNano()
	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("cascade-manual-%d", suffix)).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://cascade-manual.example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).
		SetName("manual-key").
		SetKey("sk-cascade-manual").
		SetKeyHash(service.HashUpstreamKey("sk-cascade-manual")).
		SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName("manual-account").
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{}).
		SetExtra(map[string]any{}).
		SetConcurrency(3).
		SetPriority(50).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(key.ID).
		SetUpstreamLifecycleOwner(service.AccountUpstreamLifecycleOwnerManual).
		Save(ctx)
	require.NoError(t, err)

	_, err = repo.DeleteWithOptions(ctx, config.ID, service.UpstreamConfigDeleteOptions{DeleteSyncManagedAccounts: true})
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "UPSTREAM_CONFIG_CASCADE_REJECTED", appErr.Reason)
	require.Equal(t, map[string]string{
		"bound_account_count":        "1",
		"manual_account_count":       "1",
		"missing_key_account_count":  "0",
		"sync_managed_account_count": "0",
	}, appErr.Metadata)
	activeAccount, err := client.Account.Get(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, activeAccount.DeletedAt)
	activeKey, err := client.UpstreamKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Nil(t, activeKey.DeletedAt)
	_, err = client.UpstreamEvent.Query().Where(upstreamevent.UpstreamConfigIDEQ(config.ID), upstreamevent.EventTypeEQ("config_cascade_deleted")).Count(ctx)
	require.NoError(t, err)
	cleanupCascadeDeleteFixture(t, config.ID, key.ID, account.ID, 0)
}

func decodeJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	return decoded
}

func cleanupCascadeDeleteFixture(t *testing.T, configID, keyID, accountID, groupID int64) {
	t.Helper()
	ctx := context.Background()
	if configID > 0 {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM upstream_events WHERE upstream_config_id = $1", configID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM upstream_auth_sessions WHERE upstream_config_id = $1", configID)
	}
	if accountID > 0 {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM account_groups WHERE account_id = $1", accountID)
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE payload->'account_ids' @> $1::jsonb", fmt.Sprintf(`[%d]`, accountID))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = $1", accountID)
	}
	if keyID > 0 {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM upstream_keys WHERE id = $1", keyID)
	}
	if configID > 0 {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM upstream_configs WHERE id = $1", configID)
	}
	if groupID > 0 {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", groupID)
	}
}
