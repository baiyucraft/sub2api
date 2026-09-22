package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"
)

const pluginUpgradeDrainTimeout = 60 * time.Minute
const pluginUpgradeLeaseTTL = time.Minute

// Upgrade preserves bindings and scope. A database-time lease owns the private
// rollback journal; another instance can recover it only after lease expiry.
func (m *PluginManager) Upgrade(ctx context.Context, id int64, reader io.Reader, installedBy *int64) (*PluginInstallation, error) {
	m.operationMu.Lock()
	defer m.operationMu.Unlock()
	repo, ok := m.repo.(PluginMaintenanceRepository)
	guards, guardOK := m.repo.(PluginRequestGuardRepository)
	if !ok || !guardOK {
		return nil, errors.New("plugin maintenance storage unavailable")
	}
	previous, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if previous.State == PluginStateStarting || previous.State == PluginStateUpgrading {
		return nil, ErrPluginStateChanged
	}
	// Legacy v1 plugins have no scoped admission/maintenance contract while
	// running, so an enabled legacy installation must still be disabled and
	// reinstalled explicitly. A disabled legacy installation with no enabled
	// binding has no in-flight scoped requests to drain and can be upgraded
	// through the normal maintenance transaction, preserving its config and
	// installation identity.
	if !legacyPluginUpgradeAllowed(previous) {
		return nil, errors.New("legacy plugin upgrades require explicit disable and install")
	}
	replacement, err := m.installer.Install(ctx, reader, installedBy)
	if err != nil {
		return nil, err
	}
	// Failed and previous packages are retained; rollback never depends on a
	// mutable remote artifact or a file being downloaded again.
	if replacement.PluginKey != previous.PluginKey || replacement.SignatureStatus != PluginSignatureTrusted || !replacement.Compatibility.Compatible || !pluginRequiresFeature(replacement.Manifest, "scoped-routing.v1") {
		return nil, errors.New("upgrade requires a trusted compatible package with the same plugin ID and scoped capability")
	}
	if !reflect.DeepEqual(previous.Manifest.SortedCapabilities(), replacement.Manifest.SortedCapabilities()) {
		return nil, errors.New("upgrade cannot change granted plugin capabilities")
	}
	for _, secret := range previous.Manifest.ConfigSecrets {
		found := false
		for _, field := range replacement.Manifest.ConfigSecrets {
			found = found || field == secret
		}
		if !found {
			return nil, errors.New("upgrade cannot remove existing secret field protections")
		}
	}
	configJSON, err := m.decryptConfig(previous)
	if err != nil {
		return nil, err
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
		return nil, err
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
		return nil, err
	}
	if !reflect.DeepEqual(scope, previous.ManagedScope) && !(len(scope) == 0 && len(previous.ManagedScope) == 0) {
		return nil, errors.New("upgrade configuration changes managed scope; save it explicitly first")
	}
	if !bytes.Equal(canonical, configJSON) {
		replacement.ConfigEncrypted, err = m.encryptor.Encrypt(string(canonical))
		if err != nil {
			return nil, err
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
		return nil, err
	}
	owner := rand.Text() + rand.Text()
	if err = repo.BeginPluginMaintenance(ctx, previous, owner, pluginUpgradeLeaseTTL); err != nil {
		return nil, err
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
			return nil, restore(err)
		}
	}
	drainCtx, stop := context.WithTimeout(maintCtx, pluginUpgradeDrainTimeout)
	err = waitPluginRequestsDrained(drainCtx, guards, id)
	stop()
	if err != nil {
		return nil, restore(err)
	}
	if err = repo.PublishPluginMaintenance(maintCtx, &locked, replacement, owner); err != nil {
		return nil, restore(err)
	}
	activateCtx, stop := context.WithTimeout(maintCtx, 30*time.Second)
	err = candidate.applyScopedConfig(activateCtx, canonical, replacement.ConfigRevision, hasEnabledOpenAIBinding(previous.Bindings))
	if err == nil {
		err = candidate.checkHealth(activateCtx)
	}
	stop()
	if err != nil {
		return nil, restore(err)
	}
	installed := *replacement
	installed.State = previous.State
	if err = repo.FinishPluginMaintenance(maintCtx, &installed, owner); err != nil {
		return nil, restore(err)
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
	return m.Get(ctx, id)
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
