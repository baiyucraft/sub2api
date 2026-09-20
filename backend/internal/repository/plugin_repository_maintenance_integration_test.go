//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newMaintenancePostgresFixture(t *testing.T, state string, emptyScope bool) (pluginRequestGuardFixture, *service.PluginInstallation) {
	t.Helper()
	f := newPluginRequestGuardFixture(t)
	var scope any
	if emptyScope {
		scope = "[]"
	}
	_, err := integrationDB.ExecContext(f.ctx, `UPDATE sub2api_plugin_installations SET
		state=$2,artifact_data=$3,config_encrypted='test-ciphertext',managed_scope=$4::jsonb,
		artifact_path='/old/package',install_path='/old/install',binary_path='/old/bin',
		description='old description',author='old author',last_error='original diagnostic',enabled_at=clock_timestamp()
		WHERE id=$1`, f.id, state, []byte{0, 1, 255, 0, 128}, scope)
	require.NoError(t, err)
	p, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	p.ArtifactData, err = f.repo.GetArtifact(f.ctx, f.id)
	require.NoError(t, err)
	return f, p
}

func maintenancePostgresRow(t *testing.T, f pluginRequestGuardFixture) string {
	t.Helper()
	var row string
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `SELECT (to_jsonb(p)-'updated_at')::text FROM sub2api_plugin_installations p WHERE id=$1`, f.id).Scan(&row))
	return row
}

func maintenancePostgresReplacement(p *service.PluginInstallation) *service.PluginInstallation {
	r := *p
	r.Name, r.Version, r.Description, r.Author = "replacement", "2", "new description", "new author"
	r.Manifest = service.PluginManifest{ID: p.PluginKey, Version: "2"}
	r.ArtifactData = []byte{255, 0, 3, 4}
	r.ArtifactPath, r.InstallPath, r.BinaryPath = "/new/package", "/new/install", "/new/bin"
	r.BinarySHA256 = strings.Repeat("b", 64)
	r.ConfigEncrypted = "replacement-ciphertext"
	r.ConfigRevision++
	r.ManagedScope = []service.PluginManagedTarget{{AccountID: 9, Models: []string{"model"}}}
	r.SignatureStatus = service.PluginSignatureTrusted
	r.State = service.PluginStateUpgrading
	return &r
}

func maintenancePostgresJournalCount(t *testing.T, f pluginRequestGuardFixture, expected int) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM sub2api_plugin_maintenance WHERE plugin_id=$1`, f.id).Scan(&count))
	require.Equal(t, expected, count)
}

func TestPluginMaintenancePostgresRollbackPreservesCompleteDatabaseSnapshot(t *testing.T) {
	for _, state := range []string{service.PluginStateEnabled, service.PluginStateDisabled, service.PluginStateError, service.PluginStateIncompatible} {
		for _, emptyScope := range []bool{false, true} {
			t.Run(state+map[bool]string{false: "/null_scope", true: "/empty_scope"}[emptyScope], func(t *testing.T) {
				f, previous := newMaintenancePostgresFixture(t, state, emptyScope)
				before := maintenancePostgresRow(t, f)
				// The public installation JSON deliberately omits these fields. The
				// rollback snapshot must come from the locked DB row, not the caller.
				previous.ArtifactData = nil
				previous.ConfigEncrypted = "caller-is-not-snapshot"
				require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
				var snapshot string
				require.NoError(t, integrationDB.QueryRowContext(f.ctx, `SELECT (previous_installation-'updated_at')::text FROM sub2api_plugin_maintenance WHERE plugin_id=$1`, f.id).Scan(&snapshot))
				require.JSONEq(t, before, snapshot)
				replacement := maintenancePostgresReplacement(previous)
				require.NoError(t, f.repo.PublishPluginMaintenance(f.ctx, previous, replacement, maintenanceTestOwner))
				published, err := f.repo.GetByID(f.ctx, f.id)
				require.NoError(t, err)
				require.Equal(t, service.PluginStateUpgrading, published.State)
				require.Equal(t, replacement.BinarySHA256, published.BinarySHA256)
				require.Equal(t, previous.Bindings, published.Bindings)
				require.NoError(t, f.repo.RollbackPluginMaintenance(f.ctx, f.id, maintenanceTestOwner))
				require.JSONEq(t, before, maintenancePostgresRow(t, f))
				restored, err := f.repo.GetByID(f.ctx, f.id)
				require.NoError(t, err)
				require.Equal(t, previous.Bindings, restored.Bindings)
				maintenancePostgresJournalCount(t, f, 0)
			})
		}
	}
}

func TestPluginMaintenancePostgresFinishCASAndOriginalState(t *testing.T) {
	for _, state := range []string{service.PluginStateEnabled, service.PluginStateDisabled, service.PluginStateError} {
		t.Run(state, func(t *testing.T) {
			f, previous := newMaintenancePostgresFixture(t, state, false)
			require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
			replacement := maintenancePostgresReplacement(previous)
			require.NoError(t, f.repo.PublishPluginMaintenance(f.ctx, previous, replacement, maintenanceTestOwner))
			replacement.State = previous.State
			stale := *replacement
			stale.ConfigRevision--
			require.ErrorIs(t, f.repo.FinishPluginMaintenance(f.ctx, &stale, maintenanceTestOwner), service.ErrPluginStateChanged)
			stale = *replacement
			stale.State = service.PluginStateEnabled
			if state == service.PluginStateEnabled {
				stale.State = service.PluginStateDisabled
			}
			require.ErrorIs(t, f.repo.FinishPluginMaintenance(f.ctx, &stale, maintenanceTestOwner), service.ErrPluginStateChanged)
			maintenancePostgresJournalCount(t, f, 1)
			require.NoError(t, f.repo.FinishPluginMaintenance(f.ctx, replacement, maintenanceTestOwner))
			installed, err := f.repo.GetByID(f.ctx, f.id)
			require.NoError(t, err)
			require.Equal(t, state, installed.State)
			require.Equal(t, replacement.ConfigRevision, installed.ConfigRevision)
			require.Equal(t, replacement.ConfigEncrypted, installed.ConfigEncrypted)
			require.Equal(t, replacement.ManagedScope, installed.ManagedScope)
			require.Equal(t, previous.Bindings, installed.Bindings)
			artifact, err := f.repo.GetArtifact(f.ctx, f.id)
			require.NoError(t, err)
			require.Equal(t, replacement.ArtifactData, artifact)
			maintenancePostgresJournalCount(t, f, 0)
			require.ErrorIs(t, f.repo.RollbackPluginMaintenance(f.ctx, f.id, maintenanceTestOwner), service.ErrPluginLeaseLost)
		})
	}
}

func TestPluginMaintenancePostgresExpiredRecoveryDoesNotOverrideLiveOwner(t *testing.T) {
	expired, previous := newMaintenancePostgresFixture(t, service.PluginStateError, true)
	live, livePrevious := newMaintenancePostgresFixture(t, service.PluginStateDisabled, false)
	before := maintenancePostgresRow(t, expired)
	require.NoError(t, expired.repo.BeginPluginMaintenance(expired.ctx, previous, maintenanceTestOwner, time.Minute))
	replacement := maintenancePostgresReplacement(previous)
	require.NoError(t, expired.repo.PublishPluginMaintenance(expired.ctx, previous, replacement, maintenanceTestOwner))
	require.NoError(t, live.repo.BeginPluginMaintenance(live.ctx, livePrevious, maintenanceTestOwner, time.Minute))
	liveRow := maintenancePostgresRow(t, live)
	require.ErrorIs(t, expired.repo.RollbackPluginMaintenance(expired.ctx, expired.id, strings.Repeat("x", 32)), service.ErrPluginLeaseLost)
	_, err := integrationDB.ExecContext(expired.ctx, `UPDATE sub2api_plugin_maintenance SET expires_at=clock_timestamp()-interval '1 second' WHERE plugin_id=$1`, expired.id)
	require.NoError(t, err)
	require.ErrorIs(t, expired.repo.RenewPluginMaintenance(expired.ctx, expired.id, maintenanceTestOwner, time.Minute), service.ErrPluginLeaseLost)
	require.ErrorIs(t, expired.repo.PublishPluginMaintenance(expired.ctx, replacement, replacement, maintenanceTestOwner), service.ErrPluginLeaseLost)
	replacement.State = previous.State
	require.ErrorIs(t, expired.repo.FinishPluginMaintenance(expired.ctx, replacement, maintenanceTestOwner), service.ErrPluginLeaseLost)
	require.ErrorIs(t, expired.repo.RollbackPluginMaintenance(expired.ctx, expired.id, maintenanceTestOwner), service.ErrPluginLeaseLost)
	n, err := expired.repo.RecoverExpiredPluginMaintenance(expired.ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.JSONEq(t, before, maintenancePostgresRow(t, expired))
	require.JSONEq(t, liveRow, maintenancePostgresRow(t, live))
	maintenancePostgresJournalCount(t, expired, 0)
	maintenancePostgresJournalCount(t, live, 1)
	n, err = expired.repo.RecoverExpiredPluginMaintenance(expired.ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestPluginMaintenancePostgresPendingRequestsBlockPublishNotRollback(t *testing.T) {
	f, previous := newMaintenancePostgresFixture(t, service.PluginStateEnabled, false)
	require.NoError(t, f.repo.BeginPluginRequest(f.ctx, f.id, f.sha, f.revision, "in-flight"))
	require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
	require.ErrorIs(t, f.repo.BeginPluginRequest(f.ctx, f.id, f.sha, f.revision, "late"), service.ErrPluginStateChanged)
	require.ErrorIs(t, f.repo.PublishPluginMaintenance(f.ctx, previous, maintenancePostgresReplacement(previous), maintenanceTestOwner), service.ErrPluginStateChanged)
	require.ErrorIs(t, f.repo.FinishPluginMaintenance(f.ctx, previous, maintenanceTestOwner), service.ErrPluginStateChanged)
	require.NoError(t, f.repo.RollbackPluginMaintenance(f.ctx, f.id, maintenanceTestOwner))
	requirePluginRequestCount(t, f, 1)
	require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, strings.Repeat("b", 32), time.Minute))
	_, err := integrationDB.ExecContext(f.ctx, `UPDATE sub2api_plugin_maintenance SET expires_at=clock_timestamp()-interval '1 second' WHERE plugin_id=$1`, f.id)
	require.NoError(t, err)
	n, err := f.repo.RecoverExpiredPluginMaintenance(f.ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	requirePluginRequestCount(t, f, 1)
}

func TestPluginMaintenancePostgresOrdinaryWritesAndStaleConfigCannotBypass(t *testing.T) {
	f, previous := newMaintenancePostgresFixture(t, service.PluginStateEnabled, false)
	require.NoError(t, f.repo.UpdateConfig(f.ctx, f.id, "new-cipher", f.sha))
	require.ErrorIs(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute), service.ErrPluginStateChanged)
	previous, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
	before := maintenancePostgresRow(t, f)
	operations := []func() error{
		func() error { return f.repo.BeginEnable(f.ctx, f.id, f.sha, service.PluginStateUpgrading) },
		func() error { return f.repo.MarkRuntimeHealthy(f.ctx, f.id, f.sha, "new-cipher") },
		func() error {
			return f.repo.UpdateState(f.ctx, f.id, service.PluginStateEnabled, "", nil, f.sha, service.PluginStateUpgrading)
		},
		func() error { return f.repo.UpdateConfig(f.ctx, f.id, "bypass", f.sha) },
		func() error {
			return f.repo.UpdateBindingsAndState(f.ctx, f.id, nil, service.PluginStateDisabled, "", nil, "", f.sha)
		},
		func() error { return f.repo.UpgradePackage(f.ctx, previous, maintenancePostgresReplacement(previous)) },
		func() error {
			_, err := f.repo.UpdateScopedConfig(f.ctx, f.id, "bypass", f.sha, previous.ConfigRevision, nil)
			return err
		},
		func() error { return f.repo.Delete(f.ctx, f.id, f.sha) },
		func() error { _, err := f.repo.Install(f.ctx, previous, nil); return err },
	}
	for _, operation := range operations {
		require.ErrorIs(t, operation(), service.ErrPluginStateChanged)
		require.JSONEq(t, before, maintenancePostgresRow(t, f))
	}
	current, err := f.repo.GetByID(f.ctx, f.id)
	require.NoError(t, err)
	require.Equal(t, previous.Bindings, current.Bindings)
}

func TestPluginMaintenancePostgresLeaseRecheckedAfterInstallationLockWait(t *testing.T) {
	for _, action := range []string{"renew", "publish", "finish", "rollback"} {
		t.Run(action, func(t *testing.T) {
			f, previous := newMaintenancePostgresFixture(t, service.PluginStateEnabled, false)
			require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
			tx, err := integrationDB.BeginTx(f.ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			var id int64
			require.NoError(t, tx.QueryRowContext(f.ctx, pluginMaintenanceLockSQL, f.id).Scan(&id))
			var pid int
			require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			result := make(chan error, 1)
			go func() {
				switch action {
				case "renew":
					result <- f.repo.RenewPluginMaintenance(f.ctx, f.id, maintenanceTestOwner, time.Minute)
				case "publish":
					result <- f.repo.PublishPluginMaintenance(f.ctx, previous, maintenancePostgresReplacement(previous), maintenanceTestOwner)
				case "finish":
					result <- f.repo.FinishPluginMaintenance(f.ctx, previous, maintenanceTestOwner)
				case "rollback":
					result <- f.repo.RollbackPluginMaintenance(f.ctx, f.id, maintenanceTestOwner)
				}
			}()
			waitPluginGuardBlockedPID(t, f.ctx, pid)
			_, err = tx.ExecContext(f.ctx, `UPDATE sub2api_plugin_maintenance SET expires_at=clock_timestamp()-interval '1 second' WHERE plugin_id=$1`, f.id)
			require.NoError(t, err)
			require.NoError(t, tx.Commit())
			require.ErrorIs(t, awaitPluginGuardResult(t, f.ctx, result), service.ErrPluginLeaseLost)
			maintenancePostgresJournalCount(t, f, 1)
		})
	}
}

func TestPluginMaintenancePostgresRecoveryRechecksConcurrentRenewal(t *testing.T) {
	f, previous := newMaintenancePostgresFixture(t, service.PluginStateDisabled, false)
	require.NoError(t, f.repo.BeginPluginMaintenance(f.ctx, previous, maintenanceTestOwner, time.Minute))
	_, err := integrationDB.ExecContext(f.ctx, `UPDATE sub2api_plugin_maintenance SET expires_at=clock_timestamp()-interval '1 second' WHERE plugin_id=$1`, f.id)
	require.NoError(t, err)
	tx, err := integrationDB.BeginTx(f.ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var id int64
	require.NoError(t, tx.QueryRowContext(f.ctx, pluginMaintenanceLockSQL, f.id).Scan(&id))
	var pid int
	require.NoError(t, tx.QueryRowContext(f.ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	result := make(chan error, 1)
	var recovered int64
	go func() { var err error; recovered, err = f.repo.RecoverExpiredPluginMaintenance(f.ctx); result <- err }()
	waitPluginGuardBlockedPID(t, f.ctx, pid)
	// Simulate a winning renewal/new live journal while discovery was waiting.
	_, err = tx.ExecContext(f.ctx, `UPDATE sub2api_plugin_maintenance SET expires_at=clock_timestamp()+interval '1 minute' WHERE plugin_id=$1`, f.id)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, awaitPluginGuardResult(t, f.ctx, result))
	require.Zero(t, recovered)
	maintenancePostgresJournalCount(t, f, 1)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(maintenancePostgresRow(t, f)), &row))
	require.Equal(t, service.PluginStateUpgrading, row["state"])
}

func TestPluginMaintenancePostgresBeginRechecksRevisionAfterLockWait(t *testing.T) {
	f, previous := newMaintenancePostgresFixture(t, service.PluginStateEnabled, false)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE sub2api_plugin_installations SET config_revision=config_revision+1 WHERE id=$1`, f.id)
	require.NoError(t, err)
	var pid int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	result := make(chan error, 1)
	go func() { result <- f.repo.BeginPluginMaintenance(ctx, previous, maintenanceTestOwner, time.Minute) }()
	waitPluginGuardBlockedPID(t, ctx, pid)
	require.NoError(t, tx.Commit())
	require.ErrorIs(t, awaitPluginGuardResult(t, ctx, result), service.ErrPluginStateChanged)
	maintenancePostgresJournalCount(t, f, 0)
}
