package service

import (
	"context"
	"errors"
	"time"
)

var (
	ErrPluginStateConflict = errors.New("plugin state conflict")
	ErrPluginLeaseLost     = errors.New("plugin lease lost")
)

const (
	PluginStateDefaultListLimit = 100
	PluginStateMaxListLimit     = 200
)

// PluginStateRecord preserves Version even when Found is false after deletion.
// Version is zero only for a slot that has never contained state.
type PluginStateRecord struct {
	Key     string
	Value   []byte
	Version int64
	Found   bool
}

type PluginStateMutation struct {
	Value           []byte
	ExpectedVersion int64
	Lease           *PluginLease
}

// PluginLease is valid only for the exact plugin/namespace/key it was acquired
// for. Owner is an opaque secret token; ExpiresAt is informational DB time.
type PluginLease struct {
	Owner     string
	Fence     int64
	ExpiresAt time.Time
}

// PluginStateStore is independent of the plugin wire protocol and Redis KV.
// StateCAS with ExpectedVersion=0 creates a never-used slot only. Updating or
// recreating a tombstone requires its current positive Version. Successful
// writes and deletes advance Version; deleting an absent value conflicts.
// An active lease excludes writes without a lease (ErrPluginStateConflict).
// Writes with a stale, expired or mismatched lease return ErrPluginLeaseLost.
// LeaseAcquire conflicts while occupied; renew/release require a live matching
// Owner and Fence. Release retains the fence counter for the next acquisition.
type PluginStateStore interface {
	StateGet(ctx context.Context, pluginKey, namespace, key string) (PluginStateRecord, error)
	StateCAS(ctx context.Context, pluginKey, namespace, key string, mutation PluginStateMutation) (PluginStateRecord, error)
	StateDelete(ctx context.Context, pluginKey, namespace, key string, mutation PluginStateMutation) (PluginStateRecord, error)
	// StateList excludes tombstones, sorts keys bytewise, and uses an exclusive
	// afterKey cursor. A nonempty next cursor means more rows were observed.
	// Limits <=0 use the default; larger limits are capped at the maximum.
	// Pages reflect separate reads, not a snapshot across concurrent mutations.
	StateList(ctx context.Context, pluginKey, namespace, keyPrefix, afterKey string, limit int) ([]PluginStateRecord, string, error)
	LeaseAcquire(ctx context.Context, pluginKey, namespace, key string, ttl time.Duration) (PluginLease, error)
	LeaseRenew(ctx context.Context, pluginKey, namespace, key string, lease PluginLease, ttl time.Duration) (PluginLease, error)
	LeaseRelease(ctx context.Context, pluginKey, namespace, key string, lease PluginLease) error
}
