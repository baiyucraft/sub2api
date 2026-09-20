package service

import (
	"bytes"
	"context"
	"errors"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

// Published snapshots are immutable and private to this process. raw may contain
// secrets: never expose it through status, diagnostics, logs, or the UI bridge.
type pluginScopedConfig struct {
	raw      []byte
	revision uint64
	active   bool
}

func (r *pluginRuntime) applyAndRememberScopedConfig(ctx context.Context, raw []byte, revision uint64, active bool) error {
	if r == nil {
		return errors.New("plugin runtime unavailable")
	}
	r.scopedConfigMu.Lock()
	defer r.scopedConfigMu.Unlock()
	return r.applyScopedConfigLocked(ctx, raw, revision, active)
}

// pauseScopedConfig never uses a newer installation record to configure an old
// binary. It leaves Forward receipts and completion registrations untouched.
func (r *pluginRuntime) pauseScopedConfig(ctx context.Context) error {
	if r == nil {
		return errors.New("plugin runtime unavailable")
	}
	r.scopedConfigMu.Lock()
	defer r.scopedConfigMu.Unlock()
	current := r.scopedConfig.Load()
	if current == nil {
		return errors.New("plugin applied configuration unavailable")
	}
	if !current.active {
		return nil
	}
	return r.applyScopedConfigLocked(ctx, current.raw, current.revision, false)
}

func (r *pluginRuntime) applyScopedConfigLocked(ctx context.Context, raw []byte, revision uint64, active bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.api == nil || r.exited.Load() {
		return errors.New("plugin runtime unavailable")
	}
	snapshot := &pluginScopedConfig{raw: bytes.Clone(raw), revision: revision, active: active}
	response, err := r.api.ApplyConfig(ctx, &pluginv1.ApplyConfigRequest{
		ConfigJson: bytes.Clone(snapshot.raw), ConfigRevision: revision, RuntimeActive: active,
	})
	if err != nil || response == nil || !response.Applied {
		return errors.New("plugin configuration activation failed")
	}
	r.scopedConfig.Store(snapshot)
	return nil
}
