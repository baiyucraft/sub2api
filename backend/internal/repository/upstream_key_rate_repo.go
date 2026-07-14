package repository

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"
	dbupstreamevent "github.com/Wei-Shaw/sub2api/ent/upstreamevent"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	dbupstreamkeyratesnapshot "github.com/Wei-Shaw/sub2api/ent/upstreamkeyratesnapshot"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func persistUpstreamKeyRateSnapshots(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, runID int64, keys []service.UpstreamKey, observedAt time.Time) error {
	if config == nil || runID <= 0 || len(keys) == 0 {
		return nil
	}
	rechargeRate := config.RechargeRate
	if rechargeRate <= 0 || math.IsNaN(rechargeRate) || math.IsInf(rechargeRate, 0) {
		rechargeRate = 1
	}
	for _, key := range keys {
		if key.ID <= 0 || key.SourceRateMultiplier == nil || key.RateMultiplier == nil || !validUpstreamRateMultiplier(*key.RateMultiplier) {
			continue
		}
		rawRate := *key.SourceRateMultiplier
		effectiveRate := *key.RateMultiplier
		if !validUpstreamRateMultiplier(effectiveRate) {
			continue
		}
		exists, err := client.UpstreamKeyRateSnapshot.Query().Where(
			dbupstreamkeyratesnapshot.UpstreamKeyIDEQ(key.ID),
			dbupstreamkeyratesnapshot.SyncRunIDEQ(runID),
		).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		previous, err := client.UpstreamKeyRateSnapshot.Query().
			Where(dbupstreamkeyratesnapshot.UpstreamKeyIDEQ(key.ID)).
			Order(dbent.Desc(dbupstreamkeyratesnapshot.FieldObservedAt), dbent.Desc(dbupstreamkeyratesnapshot.FieldID)).
			First(ctx)
		if err != nil && !dbent.IsNotFound(err) {
			return err
		}
		builder := client.UpstreamKeyRateSnapshot.Create().
			SetUpstreamConfigID(config.ID).
			SetUpstreamKeyID(key.ID).
			SetKeyNameSnapshot(key.Name).
			SetKeyHashSnapshot(key.KeyHash).
			SetSyncRunID(runID).
			SetProvider(config.Provider).
			SetRawRateMultiplier(rawRate).
			SetRechargeRate(rechargeRate).
			SetEffectiveCostMultiplier(effectiveRate).
			SetSource("sync").
			SetObservedAt(observedAt)
		if key.RemoteKeyID != nil {
			builder.SetRemoteKeyID(*key.RemoteKeyID)
		}
		if err := builder.Exec(ctx); err != nil {
			return err
		}
		if previous == nil {
			continue
		}
		if !sameRate(previous.EffectiveCostMultiplier, effectiveRate) {
			if err := createActualRateChangeEvent(ctx, client, config.ID, key.ID, runID, previous.EffectiveCostMultiplier, effectiveRate, observedAt); err != nil {
				return err
			}
		}
	}
	return nil
}

func createActualRateChangeEvent(ctx context.Context, client *dbent.Client, configID, keyID, runID int64, oldRate, newRate float64, occurredAt time.Time) error {
	eventKey := fmt.Sprintf("key_actual_rate_changed:%d:%d:%d", keyID, runID, occurredAt.UnixNano())
	builder := client.UpstreamEvent.Create().
		SetUpstreamConfigID(configID).
		SetUpstreamKeyID(keyID).
		SetEventKey(eventKey).
		SetEventType("key_actual_rate_changed").
		SetSeverity("info").
		SetSource("sync").
		SetMessage("Upstream key actual rate multiplier changed").
		SetPayload(map[string]any{
			"old_rate": oldRate,
			"new_rate": newRate,
		}).
		SetOccurredAt(occurredAt)
	if runID > 0 {
		builder.SetSyncRunID(runID)
	}
	return builder.
		OnConflict(
			sql.ConflictColumns(dbupstreamevent.FieldUpstreamConfigID, dbupstreamevent.FieldEventKey),
			sql.ConflictWhere(sql.NotNull(dbupstreamevent.FieldEventKey)),
			sql.DoNothing(),
		).
		Exec(ctx)
}

func sameRate(a, b float64) bool {
	return math.Abs(a-b) < 0.0000000001
}

func (r *upstreamConfigRepository) GetUpstreamKeyRateTrend(ctx context.Context, configID, keyID int64, rangeName string, now time.Time) (*service.UpstreamKeyRateTrend, error) {
	key, err := r.client.UpstreamKey.Query().Where(func(selector *sql.Selector) {
		selector.Where(sql.And(sql.EQ(selector.C("id"), keyID), sql.EQ(selector.C("upstream_config_id"), configID)))
	}).Only(mixins.SkipSoftDelete(ctx))
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	rows, err := r.client.UpstreamKeyRateSnapshot.Query().
		Where(dbupstreamkeyratesnapshot.UpstreamConfigIDEQ(configID), dbupstreamkeyratesnapshot.UpstreamKeyIDEQ(keyID)).
		Order(dbent.Asc(dbupstreamkeyratesnapshot.FieldObservedAt), dbent.Asc(dbupstreamkeyratesnapshot.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if key == nil && len(rows) == 0 {
		return nil, service.ErrUpstreamKeyNotFound
	}
	if len(rows) == 0 {
		name := ""
		var remoteID *int64
		if key != nil {
			name, remoteID = key.Name, key.RemoteKeyID
		}
		return &service.UpstreamKeyRateTrend{Range: rangeName, ConfigID: configID, KeyID: keyID, KeyName: name, RemoteKeyID: remoteID, Points: []service.UpstreamKeyRateTrendPoint{}, Changes: []service.UpstreamKeyRateChange{}}, nil
	}
	name := rows[len(rows)-1].KeyNameSnapshot
	remoteID := rows[len(rows)-1].RemoteKeyID
	if key != nil {
		name = key.Name
		remoteID = key.RemoteKeyID
	}
	trend := &service.UpstreamKeyRateTrend{Range: rangeName, ConfigID: configID, KeyID: keyID, KeyName: name, RemoteKeyID: remoteID, Points: []service.UpstreamKeyRateTrendPoint{}, Changes: []service.UpstreamKeyRateChange{}}
	first := rows[0].ObservedAt
	trend.FirstObservedAt = &first
	current := rows[len(rows)-1]
	trend.CurrentRate = floatPtr(current.EffectiveCostMultiplier)
	if len(rows) > 1 {
		previous := rows[len(rows)-2]
		trend.PreviousRate = floatPtr(previous.EffectiveCostMultiplier)
	}
	start := now.Add(-rateTrendRange(rangeName))
	points := make(map[time.Time]service.UpstreamKeyRateTrendPoint)
	for _, row := range rows {
		if row.ObservedAt.Before(start) || row.ObservedAt.After(now) {
			continue
		}
		bucket := rateTrendBucket(row.ObservedAt, rangeName)
		points[bucket] = service.UpstreamKeyRateTrendPoint{Bucket: bucket.UTC().Format(time.RFC3339), RateMultiplier: row.EffectiveCostMultiplier}
	}
	for _, point := range points {
		trend.Points = append(trend.Points, point)
	}
	sort.Slice(trend.Points, func(i, j int) bool { return trend.Points[i].Bucket < trend.Points[j].Bucket })
	events, err := r.client.UpstreamEvent.Query().
		Where(dbupstreamevent.UpstreamConfigIDEQ(configID), dbupstreamevent.UpstreamKeyIDEQ(keyID), dbupstreamevent.EventTypeIn("key_rate_changed", "key_effective_rate_changed", "key_actual_rate_changed")).
		Order(dbent.Desc(dbupstreamevent.FieldOccurredAt), dbent.Desc(dbupstreamevent.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.OccurredAt.Before(start) || event.OccurredAt.After(now) {
			continue
		}
		change := service.UpstreamKeyRateChange{Type: event.EventType, OccurredAt: event.OccurredAt}
		change.OldRate = rateFromEventPayload(event.Payload, "old_rate", "old_effective_rate")
		change.NewRate = rateFromEventPayload(event.Payload, "new_rate", "new_effective_rate")
		trend.Changes = append(trend.Changes, change)
		if trend.LastChangedAt == nil {
			at := event.OccurredAt
			trend.LastChangedAt = &at
		}
	}
	return trend, nil
}

func (r *upstreamConfigRepository) ListUpstreamKeyRateTrendKeys(ctx context.Context, configID int64) ([]service.UpstreamKeyRateCatalogItem, error) {
	_, err := r.client.UpstreamConfig.Get(ctx, configID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, service.ErrUpstreamConfigNotFound
		}
		return nil, err
	}
	keys, err := r.client.UpstreamKey.Query().Where(dbupstreamkey.UpstreamConfigIDEQ(configID)).Order(dbent.Asc(dbupstreamkey.FieldID)).All(mixins.SkipSoftDelete(ctx))
	if err != nil {
		return nil, err
	}
	rows, err := r.client.UpstreamKeyRateSnapshot.Query().Where(dbupstreamkeyratesnapshot.UpstreamConfigIDEQ(configID)).Order(dbent.Desc(dbupstreamkeyratesnapshot.FieldObservedAt), dbent.Desc(dbupstreamkeyratesnapshot.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	latest := make(map[int64]*dbent.UpstreamKeyRateSnapshot, len(rows))
	for _, row := range rows {
		if row.UpstreamKeyID == nil {
			continue
		}
		if _, exists := latest[*row.UpstreamKeyID]; !exists {
			latest[*row.UpstreamKeyID] = row
		}
	}
	lastChanged := make(map[int64]time.Time)
	events, err := r.client.UpstreamEvent.Query().Where(dbupstreamevent.UpstreamConfigIDEQ(configID), dbupstreamevent.UpstreamKeyIDNotNil(), dbupstreamevent.EventTypeIn("key_rate_changed", "key_effective_rate_changed", "key_actual_rate_changed")).Order(dbent.Desc(dbupstreamevent.FieldOccurredAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.UpstreamKeyID != nil {
			if _, exists := lastChanged[*event.UpstreamKeyID]; !exists {
				lastChanged[*event.UpstreamKeyID] = event.OccurredAt
			}
		}
	}
	items := make([]service.UpstreamKeyRateCatalogItem, 0, len(keys))
	for _, key := range keys {
		item := service.UpstreamKeyRateCatalogItem{KeyID: key.ID, Name: key.Name, RemoteKeyID: key.RemoteKeyID, Status: key.Status, DeletedAt: key.DeletedAt}
		if key.DeletedAt != nil {
			item.Status = "deleted"
		}
		if snapshot := latest[key.ID]; snapshot != nil {
			item.CurrentRate = floatPtr(snapshot.EffectiveCostMultiplier)
			observed := snapshot.ObservedAt
			item.LastObservedAt = &observed
		} else if key.RateMultiplier != nil && validUpstreamRateMultiplier(*key.RateMultiplier) {
			item.CurrentRate = floatPtr(*key.RateMultiplier)
		}
		if changed, exists := lastChanged[key.ID]; exists {
			item.LastChangedAt = &changed
		}
		items = append(items, item)
	}
	return items, nil
}

func rateFromEventPayload(payload map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if value, ok := numberFromMap(payload, key); ok {
			return floatPtr(value)
		}
	}
	return nil
}

func rateTrendRange(name string) time.Duration {
	switch name {
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func rateTrendBucket(at time.Time, name string) time.Time {
	at = at.UTC()
	switch name {
	case "7d":
		return at.Truncate(6 * time.Hour)
	case "30d":
		return at.Truncate(24 * time.Hour)
	default:
		return at.Truncate(time.Hour)
	}
}

func floatPtr(value float64) *float64 { return &value }
