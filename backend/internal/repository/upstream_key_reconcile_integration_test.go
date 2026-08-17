//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestApplySyncSnapshotReconcilesMissingKeysAndRespectsManualPause(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	name := fmt.Sprintf("upstream-reconcile-%d", time.Now().UnixNano())
	config, err := client.UpstreamConfig.Create().
		SetName(name).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	remoteUnbound := int64(90001)
	remoteBound := int64(90002)
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	keys := []service.UpstreamKey{
		{UpstreamConfigID: config.ID, Name: "unbound", Key: "sk-unbound", KeyHash: service.HashUpstreamKey("sk-unbound"), RemoteKeyID: &remoteUnbound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
		{UpstreamConfigID: config.ID, Name: "bound", Key: "sk-bound", KeyHash: service.HashUpstreamKey("sk-bound"), RemoteKeyID: &remoteBound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
	}
	localKeys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, keys, nil, now, true)
	require.NoError(t, err)
	require.Len(t, localKeys, 2)
	keyByRemote := map[int64]service.UpstreamKey{}
	for _, key := range localKeys {
		keyByRemote[*key.RemoteKeyID] = key
	}
	unboundID := keyByRemote[remoteUnbound].ID
	boundID := keyByRemote[remoteBound].ID

	createAccount := func(suffix string) int64 {
		account, createErr := client.Account.Create().
			SetName(name + "-" + suffix).
			SetPlatform(service.PlatformOpenAI).
			SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{}).
			SetExtra(map[string]any{}).
			SetConcurrency(100).
			SetPriority(5).
			SetStatus(service.StatusActive).
			SetSchedulable(true).
			SetUpstreamConfigID(config.ID).
			SetUpstreamKeyID(boundID).
			Save(ctx)
		require.NoError(t, createErr)
		return account.ID
	}
	autoRestoreAccountID := createAccount("auto")
	manualPauseAccountID := createAccount("manual")

	for _, checkedAt := range []time.Time{now.Add(10 * time.Minute), now.Add(20 * time.Minute), now.Add(29*time.Minute + 59*time.Second)} {
		_, reconciled, _, applyErr := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, checkedAt, true)
		require.NoError(t, applyErr)
		require.Zero(t, reconciled.Deleted)
		require.Zero(t, reconciled.Stale)
	}

	_, reconciled, updated, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, now.Add(40*time.Minute), true)
	require.NoError(t, err)
	require.Equal(t, 1, reconciled.Deleted)
	require.Equal(t, 1, reconciled.Stale)
	require.Equal(t, 2, updated)

	deletedKey, err := client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(unboundID)).Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err)
	require.NotNil(t, deletedKey.DeletedAt)
	staleKey, err := client.UpstreamKey.Get(ctx, boundID)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamKeyStatusStale, staleKey.Status)
	for _, accountID := range []int64{autoRestoreAccountID, manualPauseAccountID} {
		account, getErr := client.Account.Get(ctx, accountID)
		require.NoError(t, getErr)
		require.False(t, account.Schedulable)
		require.NotNil(t, account.UpstreamStalePauseKeyID)
	}
	_, err = client.Account.UpdateOneID(autoRestoreAccountID).SetSchedulable(true).Save(ctx)
	require.ErrorContains(t, err, "cannot schedule an account bound to a stale upstream key")

	accountRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	require.NoError(t, accountRepo.SetSchedulable(ctx, manualPauseAccountID, false))
	manualAccount, err := client.Account.Get(ctx, manualPauseAccountID)
	require.NoError(t, err)
	require.Nil(t, manualAccount.UpstreamStalePauseKeyID)

	restoreAt := now.Add(50 * time.Minute)
	restoredKeys := []service.UpstreamKey{
		{UpstreamConfigID: config.ID, Name: "unbound", Key: "sk-unbound", KeyHash: service.HashUpstreamKey("sk-unbound"), RemoteKeyID: &remoteUnbound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &restoreAt},
		{UpstreamConfigID: config.ID, Name: "bound", Key: "sk-bound", KeyHash: service.HashUpstreamKey("sk-bound"), RemoteKeyID: &remoteBound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &restoreAt},
	}
	localKeys, reconciled, updated, err = repo.ApplySyncSnapshot(ctx, config.ID, 0, restoredKeys, nil, restoreAt, true)
	require.NoError(t, err)
	require.Equal(t, 2, reconciled.Restored)
	require.Equal(t, 1, updated)
	require.Len(t, localKeys, 2)
	for _, key := range localKeys {
		if *key.RemoteKeyID == remoteUnbound {
			require.Equal(t, unboundID, key.ID)
		}
		if *key.RemoteKeyID == remoteBound {
			require.Equal(t, boundID, key.ID)
		}
	}
	autoAccount, err := client.Account.Get(ctx, autoRestoreAccountID)
	require.NoError(t, err)
	require.True(t, autoAccount.Schedulable)
	require.Nil(t, autoAccount.UpstreamStalePauseKeyID)
	manualAccount, err = client.Account.Get(ctx, manualPauseAccountID)
	require.NoError(t, err)
	require.False(t, manualAccount.Schedulable)
	require.Nil(t, manualAccount.UpstreamStalePauseKeyID)

}

func TestApplySyncSnapshotDoesNotCountMissingKeysForIncompleteSnapshot(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("incomplete-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderSub2API).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeManualJWT).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	remoteID := int64(91001)
	now := time.Now().UTC()
	keys := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "key", Key: "sk-key", KeyHash: service.HashUpstreamKey("sk-key"), RemoteKeyID: &remoteID, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now}}
	localKeys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, keys, nil, now, true)
	require.NoError(t, err)
	require.Len(t, localKeys, 1)
	_, reconciled, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, now.Add(time.Hour), false)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamKeyReconcileResult{}, reconciled)
	key, err := client.UpstreamKey.Get(ctx, localKeys[0].ID)
	require.NoError(t, err)
	require.Zero(t, key.MissingCount)
	require.Nil(t, key.MissingSince)
}

func TestApplySyncSnapshotArchivesSyncManagedAccountAndRestoresSameIdentity(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	suffix := time.Now().UnixNano()
	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("lifecycle-%d", suffix)).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName(fmt.Sprintf("lifecycle-group-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduled_test_results WHERE plan_id IN (SELECT id FROM scheduled_test_plans WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1))", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduled_test_plans WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM account_groups WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	remoteID := int64(95001)
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	incoming := []service.UpstreamKey{{
		UpstreamConfigID: config.ID,
		Name:             "image-key",
		Key:              "sk-lifecycle",
		KeyHash:          service.HashUpstreamKey("sk-lifecycle"),
		RemoteKeyID:      &remoteID,
		Platform:         repoStringPtr(service.PlatformOpenAI),
		Status:           service.StatusActive,
		LastSeenAt:       &now,
	}}
	keys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, now, true)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	keyID := keys[0].ID
	accountName, err := service.BuildUpstreamAccountName(config.Name, keys[0].Name)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName(accountName).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"pool_mode": true}).
		SetExtra(map[string]any{
			service.AccountUpstreamProviderKey:       config.Provider,
			service.AccountSub2APIRateSyncAdapterKey: config.AuthMode,
		}).
		SetConcurrency(100).
		SetPriority(5).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(keyID).
		SetUpstreamLifecycleOwner(service.AccountUpstreamLifecycleOwnerSyncManaged).
		Save(ctx)
	require.NoError(t, err)
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).SetPriority(7).Save(ctx)
	require.NoError(t, err)
	var planID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO scheduled_test_plans (account_id, model_id, cron_expression, enabled, max_results, auto_recover, next_run_at, created_at, updated_at)
		VALUES ($1, 'gpt-test', '*/30 * * * *', true, 10, false, $2, NOW(), NOW())
		RETURNING id
	`, account.ID, now.Add(time.Hour)).Scan(&planID))
	service.GlobalUpstreamHealthRegistry().Hydrate(service.UpstreamHealthSnapshot{
		KeyID: keyID, Status: service.UpstreamHealthSuspended, ObservationEnabled: true, ConsecutiveFails: 3,
	})

	for index, checkedAt := range []time.Time{now.Add(time.Minute), now.Add(16 * time.Minute), now.Add(31 * time.Minute)} {
		_, reconciled, _, applyErr := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, checkedAt, true)
		require.NoError(t, applyErr)
		if index < 2 {
			require.Zero(t, reconciled.Deleted)
			require.Zero(t, reconciled.ArchivedAccountCount)
		} else {
			require.Equal(t, 1, reconciled.Deleted)
			require.Equal(t, 1, reconciled.ArchivedAccountCount)
		}
	}

	_, err = client.Account.Get(ctx, account.ID)
	require.True(t, dbent.IsNotFound(err))
	archivedAccount, err := client.Account.Get(mixins.SkipSoftDelete(ctx), account.ID)
	require.NoError(t, err)
	require.NotNil(t, archivedAccount.DeletedAt)
	require.Equal(t, service.AccountUpstreamLifecycleOwnerSyncManaged, archivedAccount.UpstreamLifecycleOwner)
	require.NotNil(t, archivedAccount.UpstreamArchiveReason)
	require.Equal(t, service.AccountUpstreamArchiveReasonKeyMissing, *archivedAccount.UpstreamArchiveReason)
	archivedKey, err := client.UpstreamKey.Get(mixins.SkipSoftDelete(ctx), keyID)
	require.NoError(t, err)
	require.NotNil(t, archivedKey.DeletedAt)
	var groupLinkCount, planCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_groups WHERE account_id = $1 AND group_id = $2", account.ID, group.ID).Scan(&groupLinkCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduled_test_plans WHERE id = $1 AND account_id = $2", planID, account.ID).Scan(&planCount))
	require.Equal(t, 1, groupLinkCount)
	require.Equal(t, 1, planCount)
	require.Equal(t, service.UpstreamHealthObserving, service.GlobalUpstreamHealthRegistry().Snapshot(keyID).Status)

	restoreAt := now.Add(40 * time.Minute)
	incoming[0].LastSeenAt = &restoreAt
	restoredKeys, reconciled, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, restoreAt, true)
	require.NoError(t, err)
	require.Len(t, restoredKeys, 1)
	require.Equal(t, keyID, restoredKeys[0].ID)
	require.Equal(t, 1, reconciled.RestoredAccountCount)
	restoredAccount, err := client.Account.Get(ctx, account.ID)
	require.NoError(t, err)
	require.Nil(t, restoredAccount.DeletedAt)
	require.Nil(t, restoredAccount.UpstreamArchiveReason)
	require.Equal(t, keyID, *restoredAccount.UpstreamKeyID)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_groups WHERE account_id = $1 AND group_id = $2", account.ID, group.ID).Scan(&groupLinkCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduled_test_plans WHERE id = $1 AND account_id = $2", planID, account.ID).Scan(&planCount))
	require.Equal(t, 1, groupLinkCount)
	require.Equal(t, 1, planCount)
}

func TestApplySyncSnapshotKeepsManualAccountAndDoesNotRestoreManualDeletion(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	suffix := time.Now().UnixNano()
	config, err := client.UpstreamConfig.Create().
		SetName(fmt.Sprintf("manual-lifecycle-%d", suffix)).
		SetProvider(service.UpstreamProviderSub2API).
		SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id IN (SELECT id FROM accounts WHERE upstream_config_id = $1)", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})

	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	remoteManual := int64(95101)
	remoteDeleted := int64(95102)
	incoming := []service.UpstreamKey{
		{UpstreamConfigID: config.ID, Name: "manual", Key: "sk-manual", KeyHash: service.HashUpstreamKey("sk-manual"), RemoteKeyID: &remoteManual, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
		{UpstreamConfigID: config.ID, Name: "deleted", Key: "sk-deleted", KeyHash: service.HashUpstreamKey("sk-deleted"), RemoteKeyID: &remoteDeleted, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
	}
	keys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, now, true)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	keyByRemote := make(map[int64]service.UpstreamKey, len(keys))
	for _, key := range keys {
		keyByRemote[*key.RemoteKeyID] = key
	}
	manualAccount, err := client.Account.Create().
		SetName(fmt.Sprintf("manual-account-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "operator-managed"}).
		SetExtra(map[string]any{}).
		SetConcurrency(100).
		SetPriority(5).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(keyByRemote[remoteManual].ID).
		Save(ctx)
	require.NoError(t, err)
	deletedAccount, err := client.Account.Create().
		SetName(fmt.Sprintf("deleted-account-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"pool_mode": true}).
		SetExtra(map[string]any{service.AccountUpstreamProviderKey: config.Provider, service.AccountSub2APIRateSyncAdapterKey: config.AuthMode}).
		SetConcurrency(100).
		SetPriority(5).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetUpstreamConfigID(config.ID).
		SetUpstreamKeyID(keyByRemote[remoteDeleted].ID).
		SetUpstreamLifecycleOwner(service.AccountUpstreamLifecycleOwnerSyncManaged).
		Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.Account.DeleteOneID(deletedAccount.ID).Exec(ctx))

	for _, checkedAt := range []time.Time{now.Add(time.Minute), now.Add(16 * time.Minute), now.Add(31 * time.Minute)} {
		_, _, _, err = repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, checkedAt, true)
		require.NoError(t, err)
	}

	manualAfter, err := client.Account.Get(ctx, manualAccount.ID)
	require.NoError(t, err)
	require.Equal(t, service.AccountUpstreamLifecycleOwnerManual, manualAfter.UpstreamLifecycleOwner)
	require.Nil(t, manualAfter.DeletedAt)
	require.Nil(t, manualAfter.UpstreamArchiveReason)
	require.False(t, manualAfter.Schedulable)
	manualKey, err := client.UpstreamKey.Get(ctx, keyByRemote[remoteManual].ID)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamKeyStatusStale, manualKey.Status)

	restoreAt := now.Add(40 * time.Minute)
	for index := range incoming {
		incoming[index].LastSeenAt = &restoreAt
	}
	restoredKeys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, restoreAt, true)
	require.NoError(t, err)
	require.Len(t, restoredKeys, 2)
	_, err = client.Account.Get(ctx, deletedAccount.ID)
	require.True(t, dbent.IsNotFound(err))
	manuallyDeletedTombstone, err := client.Account.Get(mixins.SkipSoftDelete(ctx), deletedAccount.ID)
	require.NoError(t, err)
	require.NotNil(t, manuallyDeletedTombstone.DeletedAt)
	require.Nil(t, manuallyDeletedTombstone.UpstreamArchiveReason)
}

func TestApplySyncSnapshotPreservesManualPlatformAndFlagsDetectionConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("platform-manual-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderNewAPI).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeCookie).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	remoteID := int64(94001)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("manual").SetKey("sk-manual").SetKeyHash(service.HashUpstreamKey("sk-manual")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceManual).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	detectedAt := time.Now().UTC()
	incoming := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "manual", Key: "sk-manual", KeyHash: service.HashUpstreamKey("sk-manual"), RemoteKeyID: &remoteID, DetectedPlatform: repoStringPtr(service.PlatformAnthropic), PlatformDetectionStatus: service.UpstreamKeyPlatformDetectionDetected, PlatformDetectedAt: &detectedAt, Status: service.StatusActive, LastSeenAt: &detectedAt}}

	_, _, _, err = repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, detectedAt, true)
	require.NoError(t, err)
	updated, err := client.UpstreamKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, *updated.Platform)
	require.Equal(t, service.UpstreamKeyPlatformSourceManual, updated.PlatformSource)
	require.Equal(t, service.PlatformAnthropic, *updated.DetectedPlatform)
	require.Equal(t, service.UpstreamKeyPlatformDetectionConflict, updated.PlatformDetectionStatus)
}

func TestApplySyncSnapshotAutoConflictDisablesEveryMismatchedAccount(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("platform-auto-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderNewAPI).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeCookie).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	remoteID := int64(94002)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("auto").SetKey("sk-auto").SetKeyHash(service.HashUpstreamKey("sk-auto")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceAuto).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	for index := range 2 {
		_, err = client.Account.Create().SetName(fmt.Sprintf("auto-openai-%d", index)).SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetConcurrency(100).SetPriority(1).SetStatus(service.StatusActive).SetSchedulable(true).SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).Save(ctx)
		require.NoError(t, err)
	}
	detectedAt := time.Now().UTC()
	incoming := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "auto", Key: "sk-auto", KeyHash: service.HashUpstreamKey("sk-auto"), RemoteKeyID: &remoteID, DetectedPlatform: repoStringPtr(service.PlatformAnthropic), PlatformDetectionStatus: service.UpstreamKeyPlatformDetectionDetected, PlatformDetectedAt: &detectedAt, Status: service.StatusActive, LastSeenAt: &detectedAt}}

	_, _, changed, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, detectedAt, true)
	require.NoError(t, err)
	require.Equal(t, 2, changed)
	updated, err := client.UpstreamKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, *updated.Platform)
	require.Equal(t, service.UpstreamKeyPlatformDetectionConflict, updated.PlatformDetectionStatus)
	accounts, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(key.ID)).All(ctx)
	require.NoError(t, err)
	for _, account := range accounts {
		if account.UpstreamKeyID != nil && *account.UpstreamKeyID == key.ID {
			require.Equal(t, service.StatusDisabled, account.Status)
			require.False(t, account.Schedulable)
		}
	}
	var outboxCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE event_type = $1", service.SchedulerOutboxEventAccountBulkChanged).Scan(&outboxCount))
	require.GreaterOrEqual(t, outboxCount, 1)
}

func TestApplySyncSnapshotIncompletePlatformEvidencePreservesAssignmentAndAccounts(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("platform-partial-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderNewAPI).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeCookie).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	remoteID := int64(94003)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("partial").SetKey("sk-partial").SetKeyHash(service.HashUpstreamKey("sk-partial")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceAuto).SetDetectedPlatform(service.PlatformOpenAI).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	accountName, err := service.BuildUpstreamAccountName(config.Name, key.Name)
	require.NoError(t, err)
	account, err := client.Account.Create().SetName(accountName).SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetConcurrency(100).SetPriority(1).SetStatus(service.StatusActive).SetSchedulable(true).SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).Save(ctx)
	require.NoError(t, err)
	detectedAt := time.Now().UTC()
	incoming := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "partial", Key: "sk-partial", KeyHash: service.HashUpstreamKey("sk-partial"), RemoteKeyID: &remoteID, DetectedPlatform: repoStringPtr(service.PlatformAnthropic), PlatformDetectionStatus: service.UpstreamKeyPlatformDetectionDetected, PlatformDetectedAt: &detectedAt, Status: service.StatusActive, LastSeenAt: &detectedAt, Extra: map[string]any{"newapi_platform_evidence": map[string]any{"status": "unique", "candidates": []string{service.PlatformAnthropic}}}}}

	_, _, changed, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, detectedAt, false)
	require.NoError(t, err)
	require.Zero(t, changed)
	updatedKey, err := client.UpstreamKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, *updatedKey.Platform)
	require.Equal(t, service.UpstreamKeyPlatformSourceAuto, updatedKey.PlatformSource)
	require.Equal(t, service.PlatformOpenAI, *updatedKey.DetectedPlatform)
	require.Equal(t, service.UpstreamKeyPlatformDetectionDetected, updatedKey.PlatformDetectionStatus)
	updatedAccount, err := client.Account.Get(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusActive, updatedAccount.Status)
	require.True(t, updatedAccount.Schedulable)
}

func TestApplySyncSnapshotReconcilesLegacyKeyWithoutRemoteID(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("legacy-key-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderSub2API).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeManualJWT).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_events WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	legacy, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetName("legacy").SetKey("sk-legacy").SetKeyHash(service.HashUpstreamKey("sk-legacy")).SetPlatform(service.PlatformOpenAI).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()

	for _, checkedAt := range []time.Time{now, now.Add(15 * time.Minute), now.Add(31 * time.Minute)} {
		_, _, _, err = repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, checkedAt, true)
		require.NoError(t, err)
	}

	deleted, err := client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(legacy.ID)).Only(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err)
	require.NotNil(t, deleted.DeletedAt)
}

func TestListKeysForMaskedFallbackIncludesLatestTombstone(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := &upstreamConfigRepository{client: client}
	config, err := client.UpstreamConfig.Create().SetName(fmt.Sprintf("masked-tombstone-%d", time.Now().UnixNano())).SetProvider(service.UpstreamProviderSub2API).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeManualJWT).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_keys WHERE upstream_config_id = $1", config.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM upstream_configs WHERE id = $1", config.ID)
	})
	remoteID := int64(93001)
	older, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("masked-old").SetKey("sk-old").SetKeyHash(service.HashUpstreamKey("sk-old")).SetPlatform(service.PlatformOpenAI).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.UpstreamKey.DeleteOneID(older.ID).Exec(ctx))
	_, err = integrationDB.ExecContext(ctx, "UPDATE upstream_keys SET deleted_at = $1 WHERE id = $2", time.Now().UTC().Add(-time.Hour), older.ID)
	require.NoError(t, err)
	newer, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("masked-new").SetKey("sk-new").SetKeyHash(service.HashUpstreamKey("sk-new")).SetPlatform(service.PlatformOpenAI).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	require.NoError(t, client.UpstreamKey.DeleteOneID(newer.ID).Exec(ctx))

	keys, err := repo.ListKeysForMaskedFallback(ctx, config.ID, []int64{remoteID, 99999})

	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, newer.ID, keys[0].ID)
	require.Equal(t, "sk-new", keys[0].Key)
}
