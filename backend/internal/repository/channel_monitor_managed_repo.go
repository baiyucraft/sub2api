package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/apikey"
	"github.com/Wei-Shaw/sub2api/ent/channelmonitor"
	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *channelMonitorRepository) ValidateManagedRuntime(ctx context.Context, monitor *service.ChannelMonitor) error {
	if monitor == nil || monitor.GroupID == nil || monitor.ManagedAPIKeyID == nil {
		return service.ErrChannelMonitorManagedConfig
	}
	if _, err := r.client.Group.Query().Where(
		group.IDEQ(*monitor.GroupID),
		group.DeletedAtIsNil(),
		group.StatusEQ(service.StatusActive),
	).Only(ctx); err != nil {
		return service.ErrChannelMonitorManagedGroupInactive
	}
	if _, err := r.client.APIKey.Query().Where(
		apikey.IDEQ(*monitor.ManagedAPIKeyID),
		apikey.DeletedAtIsNil(),
		apikey.PurposeEQ(apikey.PurposeManagedMonitor),
		apikey.ManagedMonitorIDEQ(monitor.ID),
		apikey.UserIDEQ(service.ManagedMonitorOwnerUserID),
	).Only(ctx); err != nil {
		return service.ErrChannelMonitorManagedKeyInvalid
	}
	return nil
}

// CreateManaged creates the monitor and its real admin-owned API key in one
// database transaction. The plaintext key never leaves this repository call.
func (r *channelMonitorRepository) CreateManaged(ctx context.Context, monitor *service.ChannelMonitor, plaintextKey string) error {
	if monitor == nil || monitor.GroupID == nil || strings.TrimSpace(plaintextKey) == "" {
		return service.ErrChannelMonitorManagedConfig
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	rollback := func(err error) error { _ = tx.Rollback(); return err }
	if exists, err := tx.User.Query().Where(user.IDEQ(service.ManagedMonitorOwnerUserID)).Exist(ctx); err != nil {
		return rollback(err)
	} else if !exists {
		return rollback(service.ErrChannelMonitorManagedOwnerMissing)
	}
	g, err := tx.Group.Query().Where(group.IDEQ(*monitor.GroupID), group.DeletedAtIsNil()).Only(ctx)
	if err != nil {
		return rollback(fmt.Errorf("managed monitor group: %w", err))
	}
	if g.Status != service.StatusActive {
		return rollback(service.ErrChannelMonitorManagedGroupInactive)
	}
	monitor.GroupName = g.Name
	monitor.CredentialMode = service.ChannelMonitorCredentialManagedLocal
	builder := tx.ChannelMonitor.Create().
		SetName(monitor.Name).
		SetProvider(channelmonitor.Provider(monitor.Provider)).
		SetAPIMode(defaultAPIModeRepo(monitor.APIMode)).
		SetEndpoint(monitor.Endpoint).
		SetAPIKeyEncrypted(monitor.APIKey).
		SetPrimaryModel(monitor.PrimaryModel).
		SetExtraModels(emptySliceIfNil(monitor.ExtraModels)).
		SetGroupName(monitor.GroupName).
		SetGroupID(*monitor.GroupID).
		SetShowGroupRate(monitor.ShowGroupRate).
		SetCredentialMode(channelmonitor.CredentialModeManagedLocal).
		SetEnabled(monitor.Enabled).
		SetIntervalSeconds(monitor.IntervalSeconds).
		SetJitterSeconds(monitor.JitterSeconds).
		SetMaxProbeAttempts(monitor.MaxProbeAttempts).
		SetCreatedBy(monitor.CreatedBy).
		SetExtraHeaders(channelMonitorHeadersForPersistence(monitor)).
		SetBodyOverrideMode(defaultBodyModeRepo(monitor.BodyOverrideMode))
	if monitor.TemplateID != nil {
		builder.SetTemplateID(*monitor.TemplateID)
	}
	if monitor.BodyOverride != nil {
		builder.SetBodyOverride(monitor.BodyOverride)
	}
	created, err := builder.Save(ctx)
	if err != nil {
		return rollback(translatePersistenceError(err, service.ErrChannelMonitorNotFound, nil))
	}
	monitor.ID, monitor.CreatedAt, monitor.UpdatedAt = created.ID, created.CreatedAt, created.UpdatedAt
	key, err := tx.APIKey.Create().
		SetUserID(service.ManagedMonitorOwnerUserID).
		SetKey(plaintextKey).
		SetName(managedMonitorKeyName(monitor)).
		SetGroupID(*monitor.GroupID).
		SetPurpose(apikey.PurposeManagedMonitor).
		SetManagedMonitorID(monitor.ID).
		SetStatus(apiKeyStatusForMonitor(monitor.Enabled)).
		Save(ctx)
	if err != nil {
		return rollback(fmt.Errorf("create managed monitor key: %w", err))
	}
	monitor.ManagedAPIKeyID = &key.ID
	if _, err = tx.ChannelMonitor.UpdateOneID(monitor.ID).SetManagedAPIKeyID(key.ID).Save(ctx); err != nil {
		return rollback(fmt.Errorf("link managed monitor key: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

// UpdateManaged updates monitor data and keeps its real key's group/status in
// the same transaction. It returns the previous plaintext key for auth-cache
// invalidation after commit.
func (r *channelMonitorRepository) UpdateManaged(ctx context.Context, monitor *service.ChannelMonitor) (string, error) {
	if monitor == nil || monitor.GroupID == nil {
		return "", service.ErrChannelMonitorManagedConfig
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return "", err
	}
	rollback := func(err error) (string, error) { _ = tx.Rollback(); return "", err }
	row, err := tx.ChannelMonitor.Query().Where(channelmonitor.IDEQ(monitor.ID)).Only(ctx)
	if err != nil {
		return rollback(translatePersistenceError(err, service.ErrChannelMonitorNotFound, nil))
	}
	if row.ManagedAPIKeyID == nil {
		return rollback(service.ErrChannelMonitorManagedConfig)
	}
	key, err := tx.APIKey.Query().Where(
		apikey.IDEQ(*row.ManagedAPIKeyID),
		apikey.DeletedAtIsNil(),
		apikey.PurposeEQ(apikey.PurposeManagedMonitor),
		apikey.ManagedMonitorIDEQ(monitor.ID),
		apikey.UserIDEQ(service.ManagedMonitorOwnerUserID),
	).Only(ctx)
	if err != nil {
		return rollback(service.ErrChannelMonitorManagedKeyInvalid)
	}
	g, err := tx.Group.Query().Where(group.IDEQ(*monitor.GroupID), group.DeletedAtIsNil()).Only(ctx)
	if err != nil {
		return rollback(fmt.Errorf("managed monitor group: %w", err))
	}
	if g.Status != service.StatusActive {
		return rollback(service.ErrChannelMonitorManagedGroupInactive)
	}
	monitor.GroupName = g.Name
	updater := tx.ChannelMonitor.UpdateOneID(monitor.ID).
		SetName(monitor.Name).SetProvider(channelmonitor.Provider(monitor.Provider)).
		SetAPIMode(defaultAPIModeRepo(monitor.APIMode)).SetEndpoint(monitor.Endpoint).
		SetAPIKeyEncrypted(monitor.APIKey).SetPrimaryModel(monitor.PrimaryModel).
		SetExtraModels(emptySliceIfNil(monitor.ExtraModels)).SetGroupName(monitor.GroupName).
		SetGroupID(*monitor.GroupID).SetShowGroupRate(monitor.ShowGroupRate).
		SetEnabled(monitor.Enabled).SetIntervalSeconds(monitor.IntervalSeconds).
		SetJitterSeconds(monitor.JitterSeconds).SetMaxProbeAttempts(monitor.MaxProbeAttempts).
		SetExtraHeaders(channelMonitorHeadersForPersistence(monitor)).
		SetBodyOverrideMode(defaultBodyModeRepo(monitor.BodyOverrideMode))
	if monitor.TemplateID != nil {
		updater.SetTemplateID(*monitor.TemplateID)
	} else {
		updater.ClearTemplateID()
	}
	if monitor.BodyOverride != nil {
		updater.SetBodyOverride(monitor.BodyOverride)
	} else {
		updater.ClearBodyOverride()
	}
	if _, err = updater.Save(ctx); err != nil {
		return rollback(translatePersistenceError(err, service.ErrChannelMonitorNotFound, nil))
	}
	if _, err = tx.APIKey.UpdateOneID(key.ID).SetName(managedMonitorKeyName(monitor)).SetGroupID(*monitor.GroupID).
		SetStatus(apiKeyStatusForMonitor(monitor.Enabled)).SetUpdatedAt(time.Now()).Save(ctx); err != nil {
		return rollback(fmt.Errorf("update managed monitor key: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	monitor.GroupName = g.Name
	return key.Key, nil
}

// DeleteManaged tombstones but retains the real key row and its usage history.
func (r *channelMonitorRepository) DeleteManaged(ctx context.Context, monitorID int64) (string, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return "", err
	}
	rollback := func(err error) (string, error) { _ = tx.Rollback(); return "", err }
	row, err := tx.ChannelMonitor.Query().Where(channelmonitor.IDEQ(monitorID)).Only(ctx)
	if err != nil {
		return rollback(translatePersistenceError(err, service.ErrChannelMonitorNotFound, nil))
	}
	var oldKey string
	if row.ManagedAPIKeyID != nil {
		key, keyErr := tx.APIKey.Query().Where(
			apikey.IDEQ(*row.ManagedAPIKeyID),
			apikey.DeletedAtIsNil(),
			apikey.PurposeEQ(apikey.PurposeManagedMonitor),
			apikey.ManagedMonitorIDEQ(monitorID),
			apikey.UserIDEQ(service.ManagedMonitorOwnerUserID),
		).Only(ctx)
		if keyErr != nil {
			return rollback(service.ErrChannelMonitorManagedKeyInvalid)
		}
		oldKey = key.Key
		tombstone := fmt.Sprintf("managed-monitor-%d-%d", monitorID, time.Now().UnixNano())
		if _, err = tx.APIKey.UpdateOneID(key.ID).SetKey(tombstone).
			SetStatus(service.StatusAPIKeyDisabled).ClearGroupID().SetUpdatedAt(time.Now()).Save(ctx); err != nil {
			return rollback(fmt.Errorf("tombstone managed monitor key: %w", err))
		}
	}
	if err = tx.ChannelMonitor.DeleteOneID(monitorID).Exec(ctx); err != nil {
		return rollback(err)
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return oldKey, nil
}

func managedMonitorKeyName(monitor *service.ChannelMonitor) string {
	return "监控-" + strings.TrimSpace(monitor.Name)
}

func apiKeyStatusForMonitor(enabled bool) string {
	if enabled {
		return service.StatusAPIKeyActive
	}
	return service.StatusAPIKeyDisabled
}
