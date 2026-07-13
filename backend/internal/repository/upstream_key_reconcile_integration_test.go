//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

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
