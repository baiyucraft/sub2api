//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	dbupstreamevent "github.com/Wei-Shaw/sub2api/ent/upstreamevent"
	dbupstreamkeyratesnapshot "github.com/Wei-Shaw/sub2api/ent/upstreamkeyratesnapshot"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestApplySyncSnapshotPersistsRateBaselineAndChangesWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	remoteID := int64(42)
	secret := "sk-secret-value-never-persist-in-rate-history"
	start := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)

	apply := func(runID int64, rate float64, at time.Time, complete bool) []service.UpstreamKey {
		runID = createRateTrendRun(t, ctx, client, runID, at)
		keys := []service.UpstreamKey{{Name: "primary", Key: secret, KeyHash: service.HashUpstreamKey(secret), RemoteKeyID: &remoteID, Platform: repoStringPtr(service.PlatformOpenAI), SourceRateMultiplier: &rate, Status: service.StatusActive, LastSeenAt: &at}}
		local, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, runID, keys, nil, at, complete)
		require.NoError(t, err)
		return local
	}

	local := apply(1001, 1, start, true)
	require.Len(t, local, 1)
	keyID := local[0].ID
	require.Equal(t, 1, countRateSnapshots(t, ctx, client, keyID))
	require.Equal(t, 0, countRateEvents(t, ctx, client, keyID))

	apply(1002, 1, start.Add(time.Hour), true)
	require.Equal(t, 2, countRateSnapshots(t, ctx, client, keyID))
	require.Equal(t, 0, countRateEvents(t, ctx, client, keyID))

	apply(1003, 1.25, start.Add(2*time.Hour), true)
	require.Equal(t, 3, countRateSnapshots(t, ctx, client, keyID))
	require.Equal(t, 1, countRateEvents(t, ctx, client, keyID))

	_, err := client.UpstreamConfig.UpdateOneID(config.ID).SetRechargeRate(0.5).Save(ctx)
	require.NoError(t, err)
	apply(1004, 1.25, start.Add(3*time.Hour), true)
	require.Equal(t, 4, countRateSnapshots(t, ctx, client, keyID))
	require.Equal(t, 2, countRateEvents(t, ctx, client, keyID))

	snapshots, err := client.UpstreamKeyRateSnapshot.Query().Where(dbupstreamkeyratesnapshot.UpstreamKeyIDEQ(keyID)).All(ctx)
	require.NoError(t, err)
	for _, snapshot := range snapshots {
		encoded, marshalErr := json.Marshal(snapshot)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(encoded), secret)
		require.Equal(t, service.HashUpstreamKey(secret), snapshot.KeyHashSnapshot)
	}
	events, err := client.UpstreamEvent.Query().Where(dbupstreamevent.UpstreamKeyIDEQ(keyID)).All(ctx)
	require.NoError(t, err)
	for _, event := range events {
		encoded, marshalErr := json.Marshal(event.Payload)
		require.NoError(t, marshalErr)
		require.NotContains(t, string(encoded), secret)
	}
}

func TestApplySyncSnapshotOnlySnapshotsTrustedReturnedKeys(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	runID := createRateTrendRun(t, ctx, client, 2001, now)
	rate := 1.1
	keys := []service.UpstreamKey{
		{Name: "valid", Key: "sk-valid", KeyHash: service.HashUpstreamKey("sk-valid"), Platform: repoStringPtr(service.PlatformOpenAI), SourceRateMultiplier: &rate, Status: service.StatusActive, LastSeenAt: &now},
		{Name: "missing-rate", Key: "sk-missing", KeyHash: service.HashUpstreamKey("sk-missing"), Platform: repoStringPtr(service.PlatformOpenAI), Status: service.StatusActive, LastSeenAt: &now},
	}
	local, reconciled, _, err := repo.ApplySyncSnapshot(ctx, config.ID, runID, keys, nil, now, false)
	require.NoError(t, err)
	require.Len(t, local, 2)
	require.Equal(t, service.UpstreamKeyReconcileResult{}, reconciled)

	count, err := client.UpstreamKeyRateSnapshot.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	runID = createRateTrendRun(t, ctx, client, 2002, now.Add(time.Hour))
	_, reconciled, _, err = repo.ApplySyncSnapshot(ctx, config.ID, runID, nil, nil, now.Add(time.Hour), false)
	require.NoError(t, err)
	require.Equal(t, service.UpstreamKeyReconcileResult{}, reconciled)
	count, err = client.UpstreamKeyRateSnapshot.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestGetUpstreamKeyRateTrendUsesLastObservationPerBucketAndSupportsSoftDeletedKey(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	var keyID int64
	for index, item := range []struct {
		rate float64
		at   time.Time
	}{{1, now.Add(-2*time.Hour + 5*time.Minute)}, {1.2, now.Add(-2*time.Hour + 50*time.Minute)}, {1.3, now.Add(-30 * time.Minute)}} {
		runID := int64(3001 + index)
		runID = createRateTrendRun(t, ctx, client, runID, item.at)
		keys := []service.UpstreamKey{{Name: "trend", Key: "sk-trend", KeyHash: service.HashUpstreamKey("sk-trend"), Platform: repoStringPtr(service.PlatformOpenAI), SourceRateMultiplier: &item.rate, Status: service.StatusActive, LastSeenAt: &item.at}}
		local, _, _, err := repo.ApplySyncSnapshot(ctx, config.ID, runID, keys, nil, item.at, true)
		require.NoError(t, err)
		keyID = local[0].ID
	}
	require.NoError(t, client.UpstreamKey.DeleteOneID(keyID).Exec(ctx))

	trend, err := repo.GetUpstreamKeyRateTrend(ctx, config.ID, keyID, "24h", now)
	require.NoError(t, err)
	require.Equal(t, "trend", trend.KeyName)
	require.Len(t, trend.Points, 2)
	require.InDelta(t, 1.2, trend.Points[0].RateMultiplier, 1e-12)
	require.InDelta(t, 1.3, *trend.CurrentRate, 1e-12)
	require.InDelta(t, 1.2, *trend.PreviousRate, 1e-12)
	require.NotNil(t, trend.FirstObservedAt)
	require.NotNil(t, trend.LastChangedAt)
	require.Len(t, trend.Changes, 2)

	_, err = repo.GetUpstreamKeyRateTrend(ctx, config.ID+1, keyID, "24h", now)
	require.ErrorIs(t, err, service.ErrUpstreamKeyNotFound)
}

func TestGetUpstreamKeyRateTrendProjectsEffectiveSnapshotsAndCompatibleEvents(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetName("effective").SetKey("sk-effective").SetKeyHash(service.HashUpstreamKey("sk-effective")).Save(ctx)
	require.NoError(t, err)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	for index, snapshot := range []struct{ raw, effective float64 }{{2, 0.5}, {3, 0.75}} {
		_, err = client.UpstreamKeyRateSnapshot.Create().
			SetUpstreamConfigID(config.ID).
			SetUpstreamKeyID(key.ID).
			SetProvider(config.Provider).
			SetRawRateMultiplier(snapshot.raw).
			SetRechargeRate(0.25).
			SetEffectiveCostMultiplier(snapshot.effective).
			SetObservedAt(now.Add(time.Duration(index-2) * time.Hour)).
			Save(ctx)
		require.NoError(t, err)
	}
	for index, event := range []struct {
		payload map[string]any
	}{{map[string]any{"old_effective_rate": 0.4, "new_effective_rate": 0.5, "old_raw_rate": 1.6, "new_raw_rate": 2.0}}, {map[string]any{"old_rate": 0.5, "new_rate": 0.75}}} {
		_, err = client.UpstreamEvent.Create().
			SetUpstreamConfigID(config.ID).
			SetUpstreamKeyID(key.ID).
			SetEventType("key_actual_rate_changed").
			SetSeverity("info").
			SetPayload(event.payload).
			SetOccurredAt(now.Add(time.Duration(index-2) * time.Hour)).
			Save(ctx)
		require.NoError(t, err)
	}

	trend, err := repo.GetUpstreamKeyRateTrend(ctx, config.ID, key.ID, "24h", now)
	require.NoError(t, err)
	require.InDelta(t, 0.75, *trend.CurrentRate, 1e-12)
	require.InDelta(t, 0.5, *trend.PreviousRate, 1e-12)
	require.InDelta(t, 0.5, trend.Points[0].RateMultiplier, 1e-12)
	require.InDelta(t, 0.75, trend.Points[1].RateMultiplier, 1e-12)
	require.InDelta(t, 0.5, *trend.Changes[0].OldRate, 1e-12)
	require.InDelta(t, 0.75, *trend.Changes[0].NewRate, 1e-12)
	require.InDelta(t, 0.4, *trend.Changes[1].OldRate, 1e-12)
	require.InDelta(t, 0.5, *trend.Changes[1].NewRate, 1e-12)
}

func TestListUpstreamKeyRateTrendKeysUsesPersistedActualRateWithoutReapplyingRechargeRate(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	_, err := client.UpstreamConfig.UpdateOneID(config.ID).SetRechargeRate(0.5).Save(ctx)
	require.NoError(t, err)
	key, err := client.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).
		SetName("catalog").
		SetKey("sk-catalog").
		SetKeyHash(service.HashUpstreamKey("sk-catalog")).
		SetSourceRateMultiplier(0.8).
		SetRateMultiplier(0.4).
		Save(ctx)
	require.NoError(t, err)

	items, err := repo.ListUpstreamKeyRateTrendKeys(ctx, config.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, key.ID, items[0].KeyID)
	require.InDelta(t, 0.4, *items[0].CurrentRate, 1e-12)
}

func TestCleanupUpstreamOperationHistoryRemovesRateSnapshotsAfterNinetyDays(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	repo := &upstreamConfigRepository{client: client}
	config := createRateTrendConfig(t, ctx, client)
	key, err := client.UpstreamKey.Create().SetUpstreamConfigID(config.ID).SetName("cleanup").SetKey("sk-cleanup").SetKeyHash(service.HashUpstreamKey("sk-cleanup")).Save(ctx)
	require.NoError(t, err)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{now.AddDate(0, 0, -91), now.AddDate(0, 0, -89)} {
		_, err = client.UpstreamKeyRateSnapshot.Create().SetUpstreamConfigID(config.ID).SetUpstreamKeyID(key.ID).SetProvider(config.Provider).SetRawRateMultiplier(1).SetRechargeRate(1).SetEffectiveCostMultiplier(1).SetObservedAt(at).Save(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, repo.CleanupUpstreamOperationHistory(ctx, now))
	count, err := client.UpstreamKeyRateSnapshot.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestCreateNewAPIGroupStructureEventsRequiresCompleteSnapshots(t *testing.T) {
	ctx := context.Background()
	client := newRateTrendTestClient(t)
	config := createRateTrendConfig(t, ctx, client)
	oldSnapshot := map[string]any{
		"groups_complete": true,
		"groups":          map[string]any{"default": map[string]any{"ratio": 1.0}},
	}
	config, err := client.UpstreamConfig.UpdateOneID(config.ID).SetExtra(map[string]any{"upstream_provider_snapshot": oldSnapshot}).Save(ctx)
	require.NoError(t, err)
	at := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	err = createNewAPIGroupStructureEvents(ctx, client, config, 0, map[string]any{
		"upstream_provider_snapshot": map[string]any{"groups": map[string]any{}},
	}, at)
	require.NoError(t, err)
	count, err := client.UpstreamEvent.Query().Where(dbupstreamevent.EventTypeEQ("group_removed")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)

	err = createNewAPIGroupStructureEvents(ctx, client, config, 0, map[string]any{
		"upstream_provider_snapshot": map[string]any{"groups_complete": true, "groups": map[string]any{}},
	}, at.Add(time.Minute))
	require.NoError(t, err)
	count, err = client.UpstreamEvent.Query().Where(dbupstreamevent.EventTypeEQ("group_removed")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func newRateTrendTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:rate_trend_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func createRateTrendConfig(t *testing.T, ctx context.Context, client *dbent.Client) *dbent.UpstreamConfig {
	t.Helper()
	config, err := client.UpstreamConfig.Create().SetName("rate-trend").SetProvider(service.UpstreamProviderNewAPI).SetSiteURL("https://example.com").SetAuthMode(service.UpstreamAuthModeUserLogin).SetRechargeRate(1).Save(ctx)
	require.NoError(t, err)
	return config
}

func createRateTrendRun(t *testing.T, ctx context.Context, client *dbent.Client, id int64, at time.Time) int64 {
	t.Helper()
	row, err := client.UpstreamSyncRun.Create().SetTrigger(service.UpstreamSyncTriggerManualSingle).SetStatus("running").SetStartedAt(at).Save(ctx)
	require.NoError(t, err)
	return row.ID
}

func countRateSnapshots(t *testing.T, ctx context.Context, client *dbent.Client, keyID int64) int {
	t.Helper()
	count, err := client.UpstreamKeyRateSnapshot.Query().Where(dbupstreamkeyratesnapshot.UpstreamKeyIDEQ(keyID)).Count(ctx)
	require.NoError(t, err)
	return count
}

func countRateEvents(t *testing.T, ctx context.Context, client *dbent.Client, keyID int64) int {
	t.Helper()
	count, err := client.UpstreamEvent.Query().Where(dbupstreamevent.UpstreamKeyIDEQ(keyID), dbupstreamevent.EventTypeIn("key_rate_changed", "key_effective_rate_changed", "key_actual_rate_changed")).Count(ctx)
	require.NoError(t, err)
	return count
}

func assertNoSecret(t *testing.T, encoded []byte, secret string) {
	t.Helper()
	require.False(t, strings.Contains(string(encoded), secret))
}
