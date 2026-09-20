package service

import (
	"context"
	"time"
)

const (
	PluginMaintenanceMinTTL = time.Second
	PluginMaintenanceMaxTTL = 10 * time.Minute
)

// PluginMaintenanceRepository is an optional host-private upgrade journal.
// Owners must be unique cryptographically random tokens (32-128 bytes) for each
// Begin, never reused. All lease deadlines use database time. Lease expiry or
// ownership loss returns ErrPluginLeaseLost; stale installation identity returns
// ErrPluginStateChanged. Bindings and runtime request registrations are untouched.
type PluginMaintenanceRepository interface {
	// Begin compares ID, key, SHA, revision and state, snapshots the database row
	// (including encrypted config and artifact bytes), and marks it upgrading.
	BeginPluginMaintenance(ctx context.Context, previous *PluginInstallation, owner string, ttl time.Duration) error
	RenewPluginMaintenance(ctx context.Context, id int64, owner string, ttl time.Duration) error
	// Publish keeps upgrading and the rollback snapshot. previous identifies the
	// currently installed package; replacement supplies the new package/config.
	PublishPluginMaintenance(ctx context.Context, previous, replacement *PluginInstallation, owner string) error
	// Finish compares installed ID/key/SHA/revision and restores installed.State,
	// which must equal the pre-maintenance state (disabled/error stay that way).
	// It does not install package bytes; call Publish before candidate activation.
	FinishPluginMaintenance(ctx context.Context, installed *PluginInstallation, owner string) error
	RollbackPluginMaintenance(ctx context.Context, id int64, owner string) error
	// Recover restores expired snapshots individually and atomically, never a live
	// owner's journal or an installation whose identity no longer matches it.
	// The count reports committed recoveries even if another row returns an error.
	RecoverExpiredPluginMaintenance(ctx context.Context) (int64, error)
}
