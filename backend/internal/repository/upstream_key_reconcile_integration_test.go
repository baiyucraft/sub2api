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
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUpstreamNullRateLifecycleMigrationApplied(t *testing.T) {
	var applied bool
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = '285_upstream_null_rate_lifecycle.sql')").Scan(&applied))
	require.True(t, applied, "the PostgreSQL fixture must load the lifecycle replacement migration")
}

func TestApplySyncSnapshotNullRateMissingLifecycleKeepsHealthyUpdates(t *testing.T) {
	for _, owner := range []string{service.AccountUpstreamLifecycleOwnerSyncManaged, service.AccountUpstreamLifecycleOwnerManual} {
		t.Run(owner, func(t *testing.T) {
			ctx := context.Background()
			f := newUpstreamNullRateFixture(t, owner)
			service.GlobalUpstreamHealthRegistry().Hydrate(service.UpstreamHealthSnapshot{
				KeyID: f.missingKey.ID, Status: service.UpstreamHealthSuspended, ObservationEnabled: true, ConsecutiveFails: 3,
			})
			for _, elapsed := range []time.Duration{time.Minute, 16 * time.Minute, 31*time.Minute - time.Second} {
				reconciled, _, err := f.applyHealthy(t, f.now.Add(elapsed), 0.2)
				require.NoError(t, err)
				require.Equal(t, 1, reconciled.Missing)
				require.Zero(t, reconciled.Stale)
				require.Zero(t, reconciled.Deleted)
				require.Zero(t, reconciled.ArchivedAccountCount)
				account := getNullRateAccount(t, f)
				require.Nil(t, account.DeletedAt)
				require.True(t, account.Schedulable)
				f.assertHistoricalRate(t, account)
			}
			checkedAt := f.now.Add(31 * time.Minute)
			var outboxID int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox").Scan(&outboxID))
			reconciled, updated, err := f.applyHealthy(t, checkedAt, 0.35)
			require.NoError(t, err)
			require.Equal(t, 1, reconciled.Missing)
			require.Equal(t, 2, updated)
			account := getNullRateAccount(t, f)
			key, err := f.client.UpstreamKey.Get(mixins.SkipSoftDelete(ctx), f.missingKey.ID)
			require.NoError(t, err)
			require.Nil(t, key.RateMultiplier)
			require.Nil(t, key.SourceRateMultiplier)
			f.assertHistoricalRate(t, account)
			f.assertDependencies(t)
			f.assertEvent(t, "key_missing_detected")
			if owner == service.AccountUpstreamLifecycleOwnerSyncManaged {
				require.Equal(t, 1, reconciled.Deleted)
				require.Equal(t, 1, reconciled.ArchivedAccountCount)
				require.Zero(t, reconciled.Stale)
				require.NotNil(t, account.DeletedAt)
				require.NotNil(t, key.DeletedAt)
				require.NotNil(t, account.UpstreamArchiveReason)
				require.Equal(t, service.AccountUpstreamArchiveReasonKeyMissing, *account.UpstreamArchiveReason)
				f.assertEvent(t, "derived_accounts_archived")
				require.Equal(t, service.UpstreamHealthObserving, service.GlobalUpstreamHealthRegistry().Snapshot(key.ID).Status)
			} else {
				require.Equal(t, 1, reconciled.Stale)
				require.Zero(t, reconciled.Deleted)
				require.Nil(t, account.DeletedAt)
				require.Nil(t, key.DeletedAt)
				require.Equal(t, service.UpstreamKeyStatusStale, key.Status)
				require.False(t, account.Schedulable)
				require.Equal(t, key.ID, *account.UpstreamStalePauseKeyID)
				require.NotNil(t, account.UpstreamStalePausedAt)
				f.assertEvent(t, "key_marked_stale")
			}
			healthy, err := f.client.Account.Get(ctx, f.healthyAccount.ID)
			require.NoError(t, err)
			require.InDelta(t, 0.35, healthy.RateMultiplier, 1e-10)
			require.Equal(t, 35, healthy.Priority)
			require.InDelta(t, 0.35, *healthy.UpstreamSourceRateMultiplier, 1e-10)
			require.Equal(t, 7777, *healthy.LoadFactor)
			require.True(t, healthy.Schedulable)
			var payload []byte
			require.NoError(t, integrationDB.QueryRowContext(ctx, `
				SELECT payload FROM scheduler_outbox WHERE id > $1 AND event_type = 'account_bulk_changed'
				AND payload->'account_ids' @> to_jsonb(ARRAY[$2::bigint, $3::bigint]) ORDER BY id DESC LIMIT 1
			`, outboxID, account.ID, healthy.ID).Scan(&payload))
			if owner == service.AccountUpstreamLifecycleOwnerSyncManaged {
				require.JSONEq(t, fmt.Sprintf(`{"account_ids":[%d,%d],"group_ids":[%d]}`, account.ID, healthy.ID, f.groupID), string(payload))
			} else {
				require.JSONEq(t, fmt.Sprintf(`{"account_ids":[%d,%d]}`, account.ID, healthy.ID), string(payload))
			}
			config, err := f.client.UpstreamConfig.Get(ctx, f.config.ID)
			require.NoError(t, err)
			require.True(t, checkedAt.Equal(*config.LastSuccessAt))

			// A reappearing NULL-rate key cannot restore either lifecycle path.
			beforeRestore := f.state(t)
			restoreAt := checkedAt.Add(time.Minute)
			healthyRate := 0.4
			_, failed, changed, err := f.repo.ApplySyncSnapshot(ctx, f.config.ID, 0,
				[]service.UpstreamKey{f.incoming(f.healthyKey, &healthyRate, restoreAt), f.incoming(f.missingKey, nil, restoreAt)}, nil, restoreAt, true)
			require.ErrorContains(t, err, "without an actual rate")
			require.Equal(t, service.UpstreamKeyReconcileResult{}, failed)
			require.Zero(t, changed)
			require.Equal(t, beforeRestore, f.state(t), "failed restoration must roll back the whole snapshot")

			// Zero is an authoritative rate, so the same identities can recover.
			zero := 0.0
			restoreAt = restoreAt.Add(time.Minute)
			_, restored, _, err := f.repo.ApplySyncSnapshot(ctx, f.config.ID, 0,
				[]service.UpstreamKey{f.incoming(f.healthyKey, &healthyRate, restoreAt), f.incoming(f.missingKey, &zero, restoreAt)}, nil, restoreAt, true)
			require.NoError(t, err)
			require.Equal(t, 1, restored.Restored)
			if owner == service.AccountUpstreamLifecycleOwnerSyncManaged {
				require.Equal(t, 1, restored.RestoredAccountCount)
				f.assertEvent(t, "derived_account_restored")
			}
			account = getNullRateAccount(t, f)
			require.Nil(t, account.DeletedAt)
			require.Nil(t, account.UpstreamArchiveReason)
			require.Nil(t, account.UpstreamStalePauseKeyID)
			require.Nil(t, account.UpstreamStalePausedAt)
			require.True(t, account.Schedulable)
			require.Zero(t, account.RateMultiplier)
			require.NotNil(t, account.UpstreamSourceRateMultiplier)
			require.Zero(t, *account.UpstreamSourceRateMultiplier)
			require.Zero(t, account.Priority)
			require.Equal(t, 7777, *account.LoadFactor)
			require.Equal(t, f.missingKey.ID, *account.UpstreamKeyID)
			f.assertDependencies(t)
			f.assertEvent(t, "key_restored")
		})
	}
}

func TestApplySyncSnapshotNullRateLifecycleRollsBackWithMigration241(t *testing.T) {
	for _, owner := range []string{service.AccountUpstreamLifecycleOwnerSyncManaged, service.AccountUpstreamLifecycleOwnerManual} {
		t.Run(owner, func(t *testing.T) {
			f := newUpstreamNullRateFixture(t, owner)
			for _, elapsed := range []time.Duration{time.Minute, 16 * time.Minute} {
				_, _, err := f.applyHealthy(t, f.now.Add(elapsed), 0.2)
				require.NoError(t, err)
			}
			useMigration241BindingFunction(t)
			before := f.state(t)
			_, reconciled, updated, err := f.repo.ApplySyncSnapshot(context.Background(), f.config.ID, 0,
				[]service.UpstreamKey{f.incoming(f.healthyKey, repoTestRatePtr(0.35), f.now.Add(31*time.Minute))},
				map[string]any{"must_rollback": true}, f.now.Add(31*time.Minute), true)
			require.ErrorContains(t, err, "without an actual rate")
			var pgErr *pq.Error
			require.ErrorAs(t, err, &pgErr)
			require.Equal(t, pq.ErrorCode("23514"), pgErr.Code)
			require.Equal(t, service.UpstreamKeyReconcileResult{}, reconciled)
			require.Zero(t, updated)
			require.Equal(t, before, f.state(t), "migration 241 rejects the lifecycle write and rolls back healthy updates, events and outbox")
		})
	}
}

func repoTestRatePtr(rate float64) *float64 { return &rate }

func repoTestSetDetectedKeyRates(keys []service.UpstreamKey) {
	for i := range keys {
		keys[i].SourceRateMultiplier = repoTestRatePtr(1)
		keys[i].DetectedPlatform = keys[i].Platform
		keys[i].PlatformDetectionStatus = service.UpstreamKeyPlatformDetectionDetected
	}
}

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
	remoteManual := int64(90003)
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	keys := []service.UpstreamKey{
		{UpstreamConfigID: config.ID, Name: "unbound", Key: "sk-unbound", KeyHash: service.HashUpstreamKey("sk-unbound"), RemoteKeyID: &remoteUnbound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
		{UpstreamConfigID: config.ID, Name: "bound", Key: "sk-bound", KeyHash: service.HashUpstreamKey("sk-bound"), RemoteKeyID: &remoteBound, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
		{UpstreamConfigID: config.ID, Name: "manual", Key: "sk-manual", KeyHash: service.HashUpstreamKey("sk-manual"), RemoteKeyID: &remoteManual, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
	}
	repoTestSetDetectedKeyRates(keys)
	localKeys, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, keys, nil, now, true)
	require.NoError(t, err)
	require.Len(t, localKeys, 3)
	keyByRemote := map[int64]service.UpstreamKey{}
	for _, key := range localKeys {
		keyByRemote[*key.RemoteKeyID] = key
	}
	unboundID := keyByRemote[remoteUnbound].ID
	boundID := keyByRemote[remoteBound].ID
	manualID := keyByRemote[remoteManual].ID

	createAccount := func(suffix string, keyID int64) int64 {
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
			SetUpstreamKeyID(keyID).
			Save(ctx)
		require.NoError(t, createErr)
		return account.ID
	}
	autoRestoreAccountID := createAccount("auto", boundID)
	manualPauseAccountID := createAccount("manual", manualID)

	for _, checkedAt := range []time.Time{now.Add(10 * time.Minute), now.Add(20 * time.Minute), now.Add(29*time.Minute + 59*time.Second)} {
		_, reconciled, _, applyErr := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, checkedAt, true)
		require.NoError(t, applyErr)
		require.Zero(t, reconciled.Deleted)
		require.Zero(t, reconciled.Stale)
	}

	_, reconciled, updated, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, nil, nil, now.Add(40*time.Minute), true)
	require.NoError(t, err)
	require.Equal(t, 1, reconciled.Deleted)
	require.Equal(t, 2, reconciled.Stale)
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
		{UpstreamConfigID: config.ID, Name: "manual", Key: "sk-manual", KeyHash: service.HashUpstreamKey("sk-manual"), RemoteKeyID: &remoteManual, Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &restoreAt},
	}
	repoTestSetDetectedKeyRates(restoredKeys)
	localKeys, reconciled, updated, err = repo.ApplySyncSnapshot(ctx, config.ID, 0, restoredKeys, nil, restoreAt, true)
	require.NoError(t, err)
	require.Equal(t, 3, reconciled.Restored)
	require.Equal(t, 2, updated)
	require.Len(t, localKeys, 3)
	for _, key := range localKeys {
		if *key.RemoteKeyID == remoteUnbound {
			require.Equal(t, unboundID, key.ID)
		}
		if *key.RemoteKeyID == remoteBound {
			require.Equal(t, boundID, key.ID)
		}
		if *key.RemoteKeyID == remoteManual {
			require.Equal(t, manualID, key.ID)
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
	repoTestSetDetectedKeyRates(keys)
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
	repoTestSetDetectedKeyRates(incoming)
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
	repoTestSetDetectedKeyRates(incoming)
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

func TestApplySyncSnapshotAutoConflictDisablesMismatchedAccount(t *testing.T) {
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
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("auto").SetKey("sk-auto").SetKeyHash(service.HashUpstreamKey("sk-auto")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceAuto).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetSourceRateMultiplier(1).SetRateMultiplier(1).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	_, err = client.Account.Create().SetName("auto-openai").SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetConcurrency(100).SetPriority(1).SetStatus(service.StatusActive).SetSchedulable(true).SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).Save(ctx)
	require.NoError(t, err)
	detectedAt := time.Now().UTC()
	incoming := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "auto", Key: "sk-auto", KeyHash: service.HashUpstreamKey("sk-auto"), RemoteKeyID: &remoteID, DetectedPlatform: repoStringPtr(service.PlatformAnthropic), PlatformDetectionStatus: service.UpstreamKeyPlatformDetectionDetected, PlatformDetectedAt: &detectedAt, Status: service.StatusActive, LastSeenAt: &detectedAt}}
	incoming[0].SourceRateMultiplier = repoTestRatePtr(1)

	_, _, changed, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, detectedAt, true)
	require.NoError(t, err)
	require.Equal(t, 1, changed)
	updated, err := client.UpstreamKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, service.PlatformOpenAI, *updated.Platform)
	require.Equal(t, service.UpstreamKeyPlatformDetectionConflict, updated.PlatformDetectionStatus)
	accounts, err := client.Account.Query().Where(dbaccount.UpstreamKeyIDEQ(key.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
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
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetRemoteKeyID(remoteID).SetName("partial").SetKey("sk-partial").SetKeyHash(service.HashUpstreamKey("sk-partial")).SetPlatform(service.PlatformOpenAI).SetPlatformSource(service.UpstreamKeyPlatformSourceAuto).SetDetectedPlatform(service.PlatformOpenAI).SetPlatformDetectionStatus(service.UpstreamKeyPlatformDetectionDetected).SetSourceRateMultiplier(1).SetRateMultiplier(1).SetStatus(service.StatusActive).Save(ctx)
	require.NoError(t, err)
	accountName, err := service.BuildUpstreamAccountName(config.Name, key.Name)
	require.NoError(t, err)
	account, err := client.Account.Create().SetName(accountName).SetPlatform(service.PlatformOpenAI).SetType(service.AccountTypeAPIKey).SetCredentials(map[string]any{}).SetExtra(map[string]any{}).SetConcurrency(100).SetPriority(1).SetStatus(service.StatusActive).SetSchedulable(true).SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).Save(ctx)
	require.NoError(t, err)
	detectedAt := time.Now().UTC()
	incoming := []service.UpstreamKey{{UpstreamConfigID: config.ID, Name: "partial", Key: "sk-partial", KeyHash: service.HashUpstreamKey("sk-partial"), RemoteKeyID: &remoteID, DetectedPlatform: repoStringPtr(service.PlatformAnthropic), PlatformDetectionStatus: service.UpstreamKeyPlatformDetectionDetected, PlatformDetectedAt: &detectedAt, Status: service.StatusActive, LastSeenAt: &detectedAt, Extra: map[string]any{"newapi_platform_evidence": map[string]any{"status": "unique", "candidates": []string{service.PlatformAnthropic}}}}}
	incoming[0].SourceRateMultiplier = repoTestRatePtr(1)

	_, _, changed, err := repo.ApplySyncSnapshot(ctx, config.ID, 0, incoming, nil, detectedAt, false)
	require.NoError(t, err)
	require.Equal(t, 1, changed)
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
