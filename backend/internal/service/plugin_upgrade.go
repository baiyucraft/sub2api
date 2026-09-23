package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"time"
)

const pluginUpgradeDrainTimeout = 60 * time.Minute
const pluginUpgradeLeaseTTL = time.Minute

// PluginUpgradeError records the maintenance phase without exposing the
// wrapped package/config/runtime error to the HTTP client.
type PluginUpgradeError struct {
	Stage string
	Err   error
}

func (e *PluginUpgradeError) Error() string {
	if e == nil {
		return "plugin upgrade failed"
	}
	if e.Err == nil {
		return fmt.Sprintf("plugin upgrade failed at %s", e.Stage)
	}
	return fmt.Sprintf("plugin upgrade failed at %s: %v", e.Stage, e.Err)
}

func (e *PluginUpgradeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// PluginUpgradeFailureDetails returns only a stable phase classification.
func PluginUpgradeFailureDetails(err error) (stage string, ok bool) {
	var upgradeErr *PluginUpgradeError
	if !errors.As(err, &upgradeErr) || upgradeErr == nil || upgradeErr.Stage == "" {
		return "unknown", false
	}
	return upgradeErr.Stage, true
}

// PluginUpgradeFailureReason is a fixed, non-sensitive classification for
// operator diagnostics. Never return the plugin response or RPC error text.
func PluginUpgradeFailureReason(err error) string {
	for _, candidate := range []struct {
		target error
		name   string
	}{
		{errPluginValidationRPC, "validation_rpc"},
		{errPluginValidationEmpty, "validation_empty"},
		{errPluginValidationBlank, "validation_rejected_empty_object"},
		{errPluginValidationRejected, "validation_rejected"},
		{errPluginValidationCapability, "scoped_routing_missing"},
		{errPluginValidationNormalized, "normalized_config_invalid"},
		{errPluginValidationScope, "managed_scope_invalid"},
	} {
		if errors.Is(err, candidate.target) {
			return candidate.name
		}
	}
	return "unclassified"
}

func pluginUpgradeFailure(stage string, err error) error {
	if err == nil {
		return nil
	}
	var upgradeErr *PluginUpgradeError
	if errors.As(err, &upgradeErr) {
		return err
	}
	return &PluginUpgradeError{Stage: stage, Err: err}
}

// Upgrade preserves bindings and scope. A database-time lease owns the private
// rollback journal; another instance can recover it only after lease expiry.
func (m *PluginManager) Upgrade(ctx context.Context, id int64, reader io.Reader, installedBy *int64) (*PluginInstallation, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	repo, ok := m.repo.(PluginMaintenanceRepository)
	guards, guardOK := m.repo.(PluginRequestGuardRepository)
	if !ok || !guardOK {
		return nil, pluginUpgradeFailure("maintenance_contract", errors.New("plugin maintenance storage unavailable"))
	}
	previous, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, pluginUpgradeFailure("load_previous", err)
	}
	if previous.State == PluginStateStarting || previous.State == PluginStateUpgrading {
		return nil, pluginUpgradeFailure("validate_previous_state", ErrPluginStateChanged)
	}
	// Legacy v1 plugins have no scoped admission/maintenance contract while
	// running, so an enabled legacy installation must still be disabled and
	// reinstalled explicitly. A disabled legacy installation with no enabled
	// binding has no in-flight scoped requests to drain and can be upgraded
	// through the normal maintenance transaction, preserving its config and
	// installation identity.
	if !legacyPluginUpgradeAllowed(previous) {
		return nil, pluginUpgradeFailure("validate_previous_state", errors.New("legacy plugin upgrades require explicit disable and install"))
	}
	replacement, err := m.installer.Install(ctx, reader, installedBy)
	if err != nil {
		return nil, pluginUpgradeFailure("install_package", err)
	}
	// Failed and previous packages are retained; rollback never depends on a
	// mutable remote artifact or a file being downloaded again.
	if replacement.PluginKey != previous.PluginKey || replacement.SignatureStatus != PluginSignatureTrusted || !replacement.Compatibility.Compatible || !pluginRequiresFeature(replacement.Manifest, "scoped-routing.v1") {
		return nil, pluginUpgradeFailure("validate_package", errors.New("upgrade requires a trusted compatible package with the same plugin ID and scoped capability"))
	}
	if !reflect.DeepEqual(previous.Manifest.SortedCapabilities(), replacement.Manifest.SortedCapabilities()) {
		return nil, pluginUpgradeFailure("validate_package", errors.New("upgrade cannot change granted plugin capabilities"))
	}
	for _, secret := range previous.Manifest.ConfigSecrets {
		found := false
		for _, field := range replacement.Manifest.ConfigSecrets {
			found = found || field == secret
		}
		if !found {
			return nil, pluginUpgradeFailure("validate_package", errors.New("upgrade cannot remove existing secret field protections"))
		}
	}
	configJSON, err := m.decryptConfig(previous)
	if err != nil {
		return nil, pluginUpgradeFailure("decrypt_config", err)
	}
	replacement.ID = id
	replacement.ConfigEncrypted = previous.ConfigEncrypted
	replacement.ConfigRevision = previous.ConfigRevision
	replacement.ManagedScope = previous.ManagedScope
	replacement.Bindings = append([]PluginBinding(nil), previous.Bindings...)
	replacement.EnabledAt = previous.EnabledAt
	replacement.State = PluginStateUpgrading
	candidate, err := m.newRuntime(ctx, replacement)
	if err != nil {
		return nil, pluginUpgradeFailure("start_runtime", err)
	}
	// Validation/health may run, but this candidate cannot obtain credentials,
	// acquire leases or mutate shared state until the maintenance commit wins.
	candidate.staged.Store(true)
	adopted := false
	defer func() {
		if !adopted {
			candidate.kill()
		}
	}()
	validateCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	canonical, scope, err := candidate.validateScopedConfig(validateCtx, configJSON)
	cancel()
	if err != nil {
		return nil, pluginUpgradeFailure("validate_config", err)
	}
	if !reflect.DeepEqual(scope, previous.ManagedScope) && !(len(scope) == 0 && len(previous.ManagedScope) == 0) {
		return nil, pluginUpgradeFailure("validate_scope", errors.New("upgrade configuration changes managed scope; save it explicitly first"))
	}
	if !bytes.Equal(canonical, configJSON) {
		replacement.ConfigEncrypted, err = m.encryptor.Encrypt(string(canonical))
		if err != nil {
			return nil, pluginUpgradeFailure("persist_normalized_config", err)
		}
		var before, after any
		if json.Unmarshal(configJSON, &before) != nil || json.Unmarshal(canonical, &after) != nil || !reflect.DeepEqual(before, after) {
			replacement.ConfigRevision++
		}
	}
	validateCtx, cancel = context.WithTimeout(ctx, 15*time.Second)
	err = candidate.applyScopedConfig(validateCtx, canonical, replacement.ConfigRevision, false)
	cancel()
	if err != nil {
		return nil, pluginUpgradeFailure("apply_config", err)
	}
	owner := rand.Text() + rand.Text()
	if err = repo.BeginPluginMaintenance(ctx, previous, owner, pluginUpgradeLeaseTTL); err != nil {
		return nil, pluginUpgradeFailure("acquire_maintenance", err)
	}
	maintCtx, stopMaintenance := context.WithCancel(ctx)
	defer stopMaintenance()
	stopLease := keepPluginMaintenanceLease(maintCtx, repo, id, owner, stopMaintenance)
	defer stopLease()
	locked := *previous
	locked.State = PluginStateUpgrading
	m.mu.Lock()
	oldRuntime := m.runtimes[id]
	if oldRuntime != nil {
		oldRuntime.draining.Store(true)
	}
	if hasEnabledOpenAIBinding(previous.Bindings) {
		m.route.Store(scopedPluginRoute(previous, nil, "plugin maintenance in progress"))
	}
	m.mu.Unlock()
	restore := func(cause error) error {
		rollbackCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if rollbackErr := repo.RollbackPluginMaintenance(rollbackCtx, id, owner); rollbackErr != nil {
			return errors.Join(cause, rollbackErr)
		}
		resumable := pluginRuntimeMatchesInstallation(oldRuntime, previous)
		if resumable {
			if applyErr := oldRuntime.applyScopedConfig(rollbackCtx, configJSON, previous.ConfigRevision, hasEnabledOpenAIBinding(previous.Bindings)); applyErr != nil {
				oldRuntime.kill()
				m.mu.Lock()
				delete(m.runtimes, id)
				m.mu.Unlock()
				return errors.Join(cause, applyErr)
			}
			oldRuntime.draining.Store(false)
		}
		m.mu.Lock()
		if resumable {
			m.runtimes[id] = oldRuntime
		} else {
			delete(m.runtimes, id)
		}
		if hasEnabledOpenAIBinding(previous.Bindings) {
			var restoredRuntime *pluginRuntime
			if resumable {
				restoredRuntime = oldRuntime
			}
			m.route.Store(scopedPluginRoute(previous, restoredRuntime, "plugin recovery pending"))
		}
		m.mu.Unlock()
		if oldRuntime != nil && !resumable {
			go oldRuntime.drain(pluginUpgradeDrainTimeout)
		}
		return cause
	}
	if oldRuntime != nil {
		pauseCtx, stop := context.WithTimeout(maintCtx, 15*time.Second)
		err = oldRuntime.pauseScopedConfig(pauseCtx)
		stop()
		if err != nil {
			return nil, pluginUpgradeFailure("pause_runtime", restore(err))
		}
	}
	drainCtx, stop := context.WithTimeout(maintCtx, pluginUpgradeDrainTimeout)
	err = waitPluginRequestsDrained(drainCtx, guards, id)
	stop()
	if err != nil {
		return nil, pluginUpgradeFailure("drain_requests", restore(err))
	}
	if err = repo.PublishPluginMaintenance(maintCtx, &locked, replacement, owner); err != nil {
		return nil, pluginUpgradeFailure("publish_installation", restore(err))
	}
	activateCtx, stop := context.WithTimeout(maintCtx, 30*time.Second)
	err = candidate.applyScopedConfig(activateCtx, canonical, replacement.ConfigRevision, hasEnabledOpenAIBinding(previous.Bindings))
	if err == nil {
		err = candidate.checkHealth(activateCtx)
	}
	stop()
	if err != nil {
		return nil, pluginUpgradeFailure("activate_runtime", restore(err))
	}
	installed := *replacement
	installed.State = previous.State
	if err = repo.FinishPluginMaintenance(maintCtx, &installed, owner); err != nil {
		return nil, pluginUpgradeFailure("finish_maintenance", restore(err))
	}
	candidate.staged.Store(false)
	m.mu.Lock()
	m.localInstallations[id] = &installed
	m.invalidateAdminUICacheLocked(id)
	delete(m.runtimes, id)
	if hasEnabledOpenAIBinding(previous.Bindings) {
		m.publishRuntimeLocked(&installed, candidate)
		adopted = true
	}
	m.mu.Unlock()
	if oldRuntime != nil {
		oldRuntime.drain(10 * time.Second)
	}
	result, err := m.Get(ctx, id)
	if err != nil {
		// FinishPluginMaintenance has already committed the replacement. Do not
		// turn a post-commit readback outage into a false upgrade failure that
		// would invite an unsafe retry of the same package.
		slog.Warn("plugin_upgrade_post_commit_readback_failed", "plugin_id", id)
		return &installed, nil
	}
	return result, nil
}

func legacyPluginUpgradeAllowed(previous *PluginInstallation) bool {
	if previous == nil {
		return false
	}
	if pluginRequiresFeature(previous.Manifest, "scoped-routing.v1") {
		return true
	}
	return previous.State == PluginStateDisabled && !hasEnabledOpenAIBinding(previous.Bindings)
}

func pluginRuntimeMatchesInstallation(runtime *pluginRuntime, installation *PluginInstallation) bool {
	if runtime == nil || runtime.installation == nil || installation == nil || runtime.exited.Load() {
		return false
	}
	local := runtime.installation
	return local.ID == installation.ID && local.PluginKey == installation.PluginKey &&
		local.BinarySHA256 == installation.BinarySHA256 && local.Version == installation.Version
}

func keepPluginMaintenanceLease(ctx context.Context, repo PluginMaintenanceRepository, id int64, owner string, lost context.CancelFunc) func() {
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(pluginUpgradeLeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				renewCtx, stop := context.WithTimeout(leaseCtx, 5*time.Second)
				err := repo.RenewPluginMaintenance(renewCtx, id, owner, pluginUpgradeLeaseTTL)
				stop()
				if err != nil {
					lost()
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func waitPluginRequestsDrained(ctx context.Context, repo PluginRequestGuardRepository, id int64) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		count, err := repo.PluginRequestsInFlight(ctx, id)
		if err != nil {
			return errors.New("cannot confirm plugin request drain")
		}
		if count == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("plugin request drain incomplete; upgrade aborted")
		case <-ticker.C:
		}
	}
}
