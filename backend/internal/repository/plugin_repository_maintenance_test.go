package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

const maintenanceTestOwner = "0123456789abcdef0123456789abcdef"

func newMaintenanceTestRepo(t *testing.T) (*pluginRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return &pluginRepository{db: db}, mock
}

func maintenanceTestInstallation() *service.PluginInstallation {
	return &service.PluginInstallation{ID: 42, PluginKey: "test.maintenance", BinarySHA256: strings.Repeat("a", 64), ConfigRevision: 7, State: service.PluginStateEnabled}
}

func expectMaintenanceLock(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLockSQL)).WithArgs(id).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id))
}

func expectMaintenanceLease(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLeaseSQL)).WithArgs(id, maintenanceTestOwner).
		WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}).AddRow(id))
}

func TestPluginMaintenanceBeginSnapshotCAS(t *testing.T) {
	for _, affected := range []int64{0, 1} {
		t.Run(map[int64]string{0: "stale_or_already_owned", 1: "saved"}[affected], func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			previous := maintenanceTestInstallation()
			expectMaintenanceLock(mock, previous.ID)
			mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceBeginSQL)).
				WithArgs(previous.ID, maintenanceTestOwner, int64(30000), previous.PluginKey, previous.BinarySHA256, previous.ConfigRevision, previous.State).
				WillReturnResult(sqlmock.NewResult(0, affected))
			if affected == 1 {
				expectMaintenanceLease(mock, previous.ID)
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := repo.BeginPluginMaintenance(context.Background(), previous, maintenanceTestOwner, 30*time.Second)
			if affected == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, service.ErrPluginStateChanged)
			}
		})
	}
	for _, marker := range []string{"to_jsonb(p)", "p.config_revision=$6", "p.state=$7", "ON CONFLICT (plugin_id) DO NOTHING", "state='upgrading'"} {
		require.Contains(t, pluginMaintenanceBeginSQL, marker)
	}
}

func TestPluginMaintenanceRejectsInvalidInputBeforeSQL(t *testing.T) {
	repo, _ := newMaintenanceTestRepo(t)
	ctx := context.Background()
	previous := maintenanceTestInstallation()
	for _, ttl := range []time.Duration{0, -time.Second, time.Millisecond, service.PluginMaintenanceMaxTTL + time.Second} {
		require.Error(t, repo.BeginPluginMaintenance(ctx, previous, maintenanceTestOwner, ttl))
		require.Error(t, repo.RenewPluginMaintenance(ctx, previous.ID, maintenanceTestOwner, ttl))
	}
	for _, owner := range []string{"", "guessable", strings.Repeat("a", 129), maintenanceTestOwner + "\x00"} {
		require.Error(t, repo.BeginPluginMaintenance(ctx, previous, owner, time.Minute))
	}
	require.ErrorIs(t, repo.BeginPluginMaintenance(ctx, nil, maintenanceTestOwner, time.Minute), service.ErrPluginStateChanged)
	invalid := *previous
	invalid.ConfigRevision = math.MaxUint64
	require.ErrorIs(t, repo.BeginPluginMaintenance(ctx, &invalid, maintenanceTestOwner, time.Minute), service.ErrPluginStateChanged)
	for _, state := range []string{service.PluginStateUpgrading, service.PluginStateStarting} {
		invalid = *previous
		invalid.State = state
		require.ErrorIs(t, repo.FinishPluginMaintenance(ctx, &invalid, maintenanceTestOwner), service.ErrPluginStateChanged)
	}
	invalid = *previous
	invalid.ConfigRevision--
	require.ErrorIs(t, repo.PublishPluginMaintenance(ctx, previous, &invalid, maintenanceTestOwner), service.ErrPluginStateChanged)
	invalid = *previous
	invalid.PluginKey = "other.plugin"
	require.ErrorIs(t, repo.PublishPluginMaintenance(ctx, previous, &invalid, maintenanceTestOwner), service.ErrPluginStateChanged)
}

func TestPluginMaintenanceOwnerOrExpiryLoss(t *testing.T) {
	for _, action := range []string{"renew", "publish", "finish", "rollback"} {
		t.Run(action, func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			previous := maintenanceTestInstallation()
			expectMaintenanceLock(mock, previous.ID)
			mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLeaseSQL)).WithArgs(previous.ID, maintenanceTestOwner).WillReturnError(sql.ErrNoRows)
			mock.ExpectRollback()
			var err error
			switch action {
			case "renew":
				err = repo.RenewPluginMaintenance(context.Background(), previous.ID, maintenanceTestOwner, time.Minute)
			case "publish":
				err = repo.PublishPluginMaintenance(context.Background(), previous, previous, maintenanceTestOwner)
			case "finish":
				err = repo.FinishPluginMaintenance(context.Background(), previous, maintenanceTestOwner)
			case "rollback":
				err = repo.RollbackPluginMaintenance(context.Background(), previous.ID, maintenanceTestOwner)
			}
			require.ErrorIs(t, err, service.ErrPluginLeaseLost)
		})
	}
}

func TestPluginMaintenanceRenew(t *testing.T) {
	for _, affected := range []int64{0, 1} {
		t.Run(map[int64]string{0: "expired_during_renew", 1: "renewed"}[affected], func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			expectMaintenanceLock(mock, 42)
			expectMaintenanceLease(mock, 42)
			mock.ExpectExec(`UPDATE sub2api_plugin_maintenance j .*expires_at=clock_timestamp\(\).*j.expires_at > clock_timestamp\(\).*p.state='upgrading'.*p.config_revision=j.installed_config_revision`).
				WithArgs(int64(42), maintenanceTestOwner, int64(60000)).WillReturnResult(sqlmock.NewResult(0, affected))
			if affected == 1 {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err := repo.RenewPluginMaintenance(context.Background(), 42, maintenanceTestOwner, time.Minute)
			if affected == 1 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, service.ErrPluginLeaseLost)
			}
		})
	}
}

func TestPluginMaintenancePublishAndJournalAdvanceAtomic(t *testing.T) {
	for _, failJournal := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "journal_error_rolls_back_package"}[failJournal], func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			previous := maintenanceTestInstallation()
			replacement := *previous
			replacement.Name, replacement.Version = "new", "2"
			replacement.BinarySHA256 = strings.Repeat("b", 64)
			replacement.ArtifactData = []byte{0, 0xff, 1}
			replacement.ConfigEncrypted = "encrypted-only"
			replacement.ConfigRevision++
			replacement.ManagedScope = []service.PluginManagedTarget{}
			expectMaintenanceLock(mock, previous.ID)
			expectMaintenanceLease(mock, previous.ID)
			mock.ExpectExec(`UPDATE sub2api_plugin_installations p SET .*p.binary_sha256=\$17 AND p.config_revision=\$18 AND p.state='upgrading'.*j.owner_token=\$19.*NOT EXISTS .*sub2api_plugin_runtime_requests`).
				WithArgs(previous.ID, replacement.Name, replacement.Version, replacement.Description, replacement.Author, sqlmock.AnyArg(), replacement.ArtifactData,
					replacement.ArtifactPath, replacement.InstallPath, replacement.BinaryPath, replacement.BinarySHA256, replacement.SignatureStatus,
					replacement.ConfigEncrypted, replacement.ConfigRevision, "[]", previous.PluginKey, previous.BinarySHA256, previous.ConfigRevision, maintenanceTestOwner).
				WillReturnResult(sqlmock.NewResult(0, 1))
			journal := mock.ExpectExec(`UPDATE sub2api_plugin_maintenance SET installed_sha256=\$3,installed_config_revision=\$4 WHERE plugin_id=\$1 AND owner_token=\$2 AND expires_at > clock_timestamp\(\)`).
				WithArgs(previous.ID, maintenanceTestOwner, replacement.BinarySHA256, replacement.ConfigRevision)
			failure := errors.New("journal write failed")
			if failJournal {
				journal.WillReturnError(failure)
				mock.ExpectRollback()
			} else {
				journal.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			err := repo.PublishPluginMaintenance(context.Background(), previous, &replacement, maintenanceTestOwner)
			if failJournal {
				require.ErrorIs(t, err, failure)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPluginMaintenanceFinishPreservesOriginalState(t *testing.T) {
	for _, state := range []string{service.PluginStateEnabled, service.PluginStateDisabled, service.PluginStateError, service.PluginStateIncompatible} {
		t.Run(state, func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			installed := maintenanceTestInstallation()
			installed.State = state
			expectMaintenanceLock(mock, installed.ID)
			expectMaintenanceLease(mock, installed.ID)
			mock.ExpectExec(`UPDATE sub2api_plugin_installations p SET state=\$5.*p.binary_sha256=\$3 AND p.config_revision=\$4 AND p.state='upgrading'.*j.previous_installation->>'state'=\$5.*NOT EXISTS .*sub2api_plugin_runtime_requests`).
				WithArgs(installed.ID, installed.PluginKey, installed.BinarySHA256, installed.ConfigRevision, state, installed.LastError, installed.EnabledAt, maintenanceTestOwner).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp()`)).
				WithArgs(installed.ID, maintenanceTestOwner).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			require.NoError(t, repo.FinishPluginMaintenance(context.Background(), installed, maintenanceTestOwner))
		})
	}
}

func TestPluginMaintenanceRollbackRestoresCompletePrivateSnapshot(t *testing.T) {
	repo, mock := newMaintenanceTestRepo(t)
	expectMaintenanceLock(mock, 42)
	expectMaintenanceLease(mock, 42)
	mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceRestoreSQL+` AND j.owner_token=$2 AND j.expires_at > clock_timestamp()`)).
		WithArgs(int64(42), maintenanceTestOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp()`)).
		WithArgs(int64(42), maintenanceTestOwner).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	require.NoError(t, repo.RollbackPluginMaintenance(context.Background(), 42, maintenanceTestOwner))
	for _, field := range []string{"artifact_data", "config_encrypted", "config_revision", "managed_scope", "enabled_at", "jsonb_populate_record", "p.config_revision=j.installed_config_revision"} {
		require.Contains(t, pluginMaintenanceRestoreSQL, field)
	}
	require.NotContains(t, pluginMaintenanceRestoreSQL, "sub2api_plugin_bindings")
	require.NotContains(t, pluginMaintenanceRestoreSQL, "sub2api_plugin_runtime_requests")
}

func TestPluginMaintenanceCASConflictDistinctFromMidWriteLeaseExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "cas_conflict", true: "lease_expired"}[expired], func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			expectMaintenanceLock(mock, 42)
			expectMaintenanceLease(mock, 42)
			mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceRestoreSQL+` AND j.owner_token=$2 AND j.expires_at > clock_timestamp()`)).
				WithArgs(int64(42), maintenanceTestOwner).WillReturnResult(sqlmock.NewResult(0, 0))
			if expired {
				mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLeaseSQL)).WithArgs(int64(42), maintenanceTestOwner).WillReturnError(sql.ErrNoRows)
			} else {
				expectMaintenanceLease(mock, 42)
			}
			mock.ExpectRollback()
			err := repo.RollbackPluginMaintenance(context.Background(), 42, maintenanceTestOwner)
			if expired {
				require.ErrorIs(t, err, service.ErrPluginLeaseLost)
			} else {
				require.ErrorIs(t, err, service.ErrPluginStateChanged)
			}
		})
	}
}

func TestPluginMaintenanceExpiryAfterMutationRollsBackTransaction(t *testing.T) {
	for _, action := range []string{"begin", "publish", "finish", "rollback"} {
		t.Run(action, func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			p := maintenanceTestInstallation()
			expectMaintenanceLock(mock, p.ID)
			if action != "begin" {
				expectMaintenanceLease(mock, p.ID)
			}
			// A trigger or expensive artifact update may finish after its deadline.
			// The final journal check must roll back even a successful row write.
			if action == "begin" {
				mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceBeginSQL)).WillReturnResult(sqlmock.NewResult(0, 1))
			} else {
				mock.ExpectExec(`UPDATE sub2api_plugin_installations p`).WillReturnResult(sqlmock.NewResult(0, 1))
				if action == "publish" {
					mock.ExpectExec(`UPDATE sub2api_plugin_maintenance .*expires_at > clock_timestamp\(\)`).WillReturnResult(sqlmock.NewResult(0, 0))
				} else {
					mock.ExpectExec(`DELETE FROM sub2api_plugin_maintenance .*expires_at > clock_timestamp\(\)`).WillReturnResult(sqlmock.NewResult(0, 0))
				}
			}
			mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLeaseSQL)).WithArgs(p.ID, maintenanceTestOwner).WillReturnError(sql.ErrNoRows)
			mock.ExpectRollback()
			var err error
			switch action {
			case "begin":
				err = repo.BeginPluginMaintenance(context.Background(), p, maintenanceTestOwner, time.Minute)
			case "publish":
				err = repo.PublishPluginMaintenance(context.Background(), p, p, maintenanceTestOwner)
			case "finish":
				err = repo.FinishPluginMaintenance(context.Background(), p, maintenanceTestOwner)
			case "rollback":
				err = repo.RollbackPluginMaintenance(context.Background(), p.ID, maintenanceTestOwner)
			}
			require.ErrorIs(t, err, service.ErrPluginLeaseLost)
		})
	}
}

func TestPluginMaintenanceRecoveryRechecksAfterLockAndNeverOverridesActive(t *testing.T) {
	repo, mock := newMaintenanceTestRepo(t)
	mock.ExpectQuery(`SELECT plugin_id FROM sub2api_plugin_maintenance WHERE expires_at <= clock_timestamp\(\) ORDER BY plugin_id`).
		WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}).AddRow(41).AddRow(42).AddRow(43))
	for _, id := range []int64{41, 42, 43} {
		expectMaintenanceLock(mock, id)
		query := mock.ExpectQuery(`SELECT plugin_id FROM sub2api_plugin_maintenance WHERE plugin_id=\$1 AND expires_at <= clock_timestamp\(\) FOR UPDATE`).WithArgs(id)
		if id == 41 { // A renewal or new owner won after discovery.
			query.WillReturnError(sql.ErrNoRows)
			mock.ExpectRollback()
			continue
		}
		query.WillReturnRows(sqlmock.NewRows([]string{"plugin_id"}).AddRow(id))
		restore := mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceRestoreSQL + ` AND j.expires_at <= clock_timestamp()`)).WithArgs(id)
		if id == 42 { // Unexpected installation identity must not be overwritten.
			restore.WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectRollback()
			continue
		}
		restore.WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1`)).WithArgs(id).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
	n, err := repo.RecoverExpiredPluginMaintenance(context.Background())
	require.EqualValues(t, 1, n)
	require.ErrorIs(t, err, service.ErrPluginStateChanged)
}

func TestPluginMaintenanceTransactionFailuresDoNotSucceed(t *testing.T) {
	for _, phase := range []string{"begin", "lock", "statement", "rows", "commit"} {
		t.Run(phase, func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			p := maintenanceTestInstallation()
			failure := errors.New("database failure")
			if phase == "begin" {
				mock.ExpectBegin().WillReturnError(failure)
			} else if phase == "lock" {
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(pluginMaintenanceLockSQL)).WithArgs(p.ID).WillReturnError(failure)
				mock.ExpectRollback()
			} else {
				expectMaintenanceLock(mock, p.ID)
				query := mock.ExpectExec(regexp.QuoteMeta(pluginMaintenanceBeginSQL))
				switch phase {
				case "statement":
					query.WillReturnError(failure)
				case "rows":
					query.WillReturnResult(sqlmock.NewErrorResult(failure))
				case "commit":
					query.WillReturnResult(sqlmock.NewResult(0, 1))
				}
				if phase == "commit" {
					expectMaintenanceLease(mock, p.ID)
					mock.ExpectCommit().WillReturnError(failure)
				} else {
					mock.ExpectRollback()
				}
			}
			require.ErrorIs(t, repo.BeginPluginMaintenance(context.Background(), p, maintenanceTestOwner, time.Minute), failure)
		})
	}
}

func TestPluginMaintenanceOrdinaryWritesCannotBypassUpgrading(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"delete", "enable", "healthy", "state", "config", "bindings", "upgrade", "scoped_config"} {
		t.Run(action, func(t *testing.T) {
			repo, mock := newMaintenanceTestRepo(t)
			p := maintenanceTestInstallation()
			if action == "bindings" {
				mock.ExpectBegin()
			}
			mock.ExpectExec(`(?i)(DELETE|UPDATE).*state (NOT IN \([^)]*'upgrading'|<> 'upgrading')`).WillReturnResult(sqlmock.NewResult(0, 0))
			if action == "bindings" {
				mock.ExpectRollback()
			}
			var err error
			switch action {
			case "delete":
				err = repo.Delete(ctx, p.ID, p.BinarySHA256)
			case "enable":
				err = repo.BeginEnable(ctx, p.ID, p.BinarySHA256, "upgrading")
			case "healthy":
				err = repo.MarkRuntimeHealthy(ctx, p.ID, p.BinarySHA256, "cipher")
			case "state":
				err = repo.UpdateState(ctx, p.ID, "enabled", "", nil, p.BinarySHA256, "upgrading")
			case "config":
				err = repo.UpdateConfig(ctx, p.ID, "cipher", p.BinarySHA256)
			case "bindings":
				err = repo.UpdateBindingsAndState(ctx, p.ID, nil, "disabled", "", nil, "", p.BinarySHA256)
			case "upgrade":
				err = repo.UpgradePackage(ctx, p, p)
			case "scoped_config":
				_, err = repo.UpdateScopedConfig(ctx, p.ID, "cipher", p.BinarySHA256, p.ConfigRevision, nil)
			}
			require.ErrorIs(t, err, service.ErrPluginStateChanged)
		})
	}
	repo, _ := newMaintenanceTestRepo(t)
	p := maintenanceTestInstallation()
	p.State = service.PluginStateUpgrading
	_, err := repo.Install(ctx, p, nil)
	require.ErrorIs(t, err, service.ErrPluginStateChanged)
}
