package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const pluginMaintenanceLockSQL = `SELECT id FROM sub2api_plugin_installations WHERE id=$1 FOR UPDATE`
const pluginMaintenanceLeaseSQL = `SELECT plugin_id FROM sub2api_plugin_maintenance
	WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp() FOR UPDATE`
const pluginMaintenanceBeginSQL = `WITH saved AS (
	INSERT INTO sub2api_plugin_maintenance
	(plugin_id,owner_token,previous_installation,installed_sha256,installed_config_revision,expires_at)
	SELECT p.id,$2,to_jsonb(p),p.binary_sha256,p.config_revision,clock_timestamp()+($3 * interval '1 millisecond')
	FROM sub2api_plugin_installations p
	WHERE p.id=$1 AND p.plugin_key=$4 AND p.binary_sha256=$5 AND p.config_revision=$6 AND p.state=$7
	AND p.state NOT IN ('starting','upgrading')
	ON CONFLICT (plugin_id) DO NOTHING RETURNING plugin_id
) UPDATE sub2api_plugin_installations p SET state='upgrading',updated_at=clock_timestamp()
	FROM saved WHERE p.id=saved.plugin_id`

func validatePluginMaintenanceOwner(owner string) error {
	if len(owner) < 32 || len(owner) > 128 || strings.ContainsRune(owner, 0) {
		return errors.New("invalid plugin maintenance owner")
	}
	return nil
}

func validatePluginMaintenanceTTL(ttl time.Duration) error {
	if ttl < service.PluginMaintenanceMinTTL || ttl > service.PluginMaintenanceMaxTTL {
		return errors.New("invalid plugin maintenance TTL")
	}
	return nil
}

func validPluginMaintenanceInstallation(p *service.PluginInstallation) bool {
	return p != nil && p.ID > 0 && p.PluginKey != "" && p.BinarySHA256 != "" && p.ConfigRevision <= math.MaxInt64
}

func pluginMaintenanceRows(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrPluginStateChanged
	}
	return nil
}

// Every mutation locks the installation before the journal, then evaluates the
// deadline in a new statement. A lease cannot pass validation before a lock wait.
func (r *pluginRepository) pluginMaintenanceTx(ctx context.Context, id int64, owner string, requireLease bool, fn func(*sql.Tx) error) error {
	if id <= 0 {
		return service.ErrPluginStateChanged
	}
	if err := validatePluginMaintenanceOwner(owner); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	if err := tx.QueryRowContext(ctx, pluginMaintenanceLockSQL, id).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrPluginStateChanged
		}
		return err
	}
	if requireLease {
		if err := tx.QueryRowContext(ctx, pluginMaintenanceLeaseSQL, id, owner).Scan(&locked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return service.ErrPluginLeaseLost
			}
			return err
		}
	}
	if err := fn(tx); err != nil {
		if requireLease && errors.Is(err, service.ErrPluginStateChanged) {
			// Large package writes can consume the remaining TTL after the first
			// check. Keep expiration distinguishable from a genuine CAS conflict.
			leaseErr := tx.QueryRowContext(ctx, pluginMaintenanceLeaseSQL, id, owner).Scan(&locked)
			if errors.Is(leaseErr, sql.ErrNoRows) {
				return service.ErrPluginLeaseLost
			}
			if leaseErr != nil {
				return leaseErr
			}
		}
		return err
	}
	return tx.Commit()
}

func (r *pluginRepository) BeginPluginMaintenance(ctx context.Context, previous *service.PluginInstallation, owner string, ttl time.Duration) error {
	if !validPluginMaintenanceInstallation(previous) {
		return service.ErrPluginStateChanged
	}
	if err := validatePluginMaintenanceTTL(ttl); err != nil {
		return err
	}
	return r.pluginMaintenanceTx(ctx, previous.ID, owner, false, func(tx *sql.Tx) error {
		if err := pluginMaintenanceRows(tx.ExecContext(ctx, pluginMaintenanceBeginSQL,
			previous.ID, owner, ttl.Milliseconds(), previous.PluginKey, previous.BinarySHA256, previous.ConfigRevision, previous.State)); err != nil {
			return err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, pluginMaintenanceLeaseSQL, previous.ID, owner).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return service.ErrPluginLeaseLost
			}
			return err
		}
		return nil
	})
}

func (r *pluginRepository) RenewPluginMaintenance(ctx context.Context, id int64, owner string, ttl time.Duration) error {
	if err := validatePluginMaintenanceTTL(ttl); err != nil {
		return err
	}
	return r.pluginMaintenanceTx(ctx, id, owner, true, func(tx *sql.Tx) error {
		err := pluginMaintenanceRows(tx.ExecContext(ctx, `UPDATE sub2api_plugin_maintenance j
			SET expires_at=clock_timestamp()+($3 * interval '1 millisecond')
			FROM sub2api_plugin_installations p
			WHERE j.plugin_id=$1 AND j.owner_token=$2 AND j.expires_at > clock_timestamp()
			AND p.id=j.plugin_id AND p.state='upgrading'
			AND p.binary_sha256=j.installed_sha256 AND p.config_revision=j.installed_config_revision`, id, owner, ttl.Milliseconds()))
		if errors.Is(err, service.ErrPluginStateChanged) {
			return service.ErrPluginLeaseLost
		}
		return err
	})
}

func (r *pluginRepository) PublishPluginMaintenance(ctx context.Context, previous, replacement *service.PluginInstallation, owner string) error {
	if !validPluginMaintenanceInstallation(previous) || !validPluginMaintenanceInstallation(replacement) ||
		previous.ID != replacement.ID || previous.PluginKey != replacement.PluginKey || replacement.ConfigRevision < previous.ConfigRevision {
		return service.ErrPluginStateChanged
	}
	manifest, err := json.Marshal(replacement.Manifest)
	if err != nil {
		return err
	}
	var scope any
	if replacement.ManagedScope != nil {
		raw, err := json.Marshal(replacement.ManagedScope)
		if err != nil {
			return err
		}
		scope = string(raw)
	}
	return r.pluginMaintenanceTx(ctx, previous.ID, owner, true, func(tx *sql.Tx) error {
		if err := pluginMaintenanceRows(tx.ExecContext(ctx, `UPDATE sub2api_plugin_installations p SET
			name=$2,version=$3,description=$4,author=$5,manifest=$6::jsonb,artifact_data=$7,
			artifact_path=$8,install_path=$9,binary_path=$10,binary_sha256=$11,signature_status=$12,
			config_encrypted=$13,config_revision=$14,managed_scope=$15::jsonb,last_error='',updated_at=clock_timestamp()
			FROM sub2api_plugin_maintenance j
			WHERE p.id=$1 AND p.plugin_key=$16 AND p.binary_sha256=$17 AND p.config_revision=$18 AND p.state='upgrading'
			AND j.plugin_id=p.id AND j.owner_token=$19 AND j.expires_at > clock_timestamp()
			AND j.installed_sha256=p.binary_sha256 AND j.installed_config_revision=p.config_revision
			AND NOT EXISTS (SELECT 1 FROM sub2api_plugin_runtime_requests q WHERE q.plugin_id=p.id)`,
			previous.ID, replacement.Name, replacement.Version, replacement.Description, replacement.Author, manifest, replacement.ArtifactData,
			replacement.ArtifactPath, replacement.InstallPath, replacement.BinaryPath, replacement.BinarySHA256, replacement.SignatureStatus,
			replacement.ConfigEncrypted, replacement.ConfigRevision, scope, previous.PluginKey, previous.BinarySHA256, previous.ConfigRevision, owner)); err != nil {
			return err
		}
		return pluginMaintenanceRows(tx.ExecContext(ctx, `UPDATE sub2api_plugin_maintenance
			SET installed_sha256=$3,installed_config_revision=$4 WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp()`,
			previous.ID, owner, replacement.BinarySHA256, replacement.ConfigRevision))
	})
}

func (r *pluginRepository) FinishPluginMaintenance(ctx context.Context, installed *service.PluginInstallation, owner string) error {
	if !validPluginMaintenanceInstallation(installed) || installed.State == service.PluginStateUpgrading || installed.State == service.PluginStateStarting {
		return service.ErrPluginStateChanged
	}
	return r.pluginMaintenanceTx(ctx, installed.ID, owner, true, func(tx *sql.Tx) error {
		if err := pluginMaintenanceRows(tx.ExecContext(ctx, `UPDATE sub2api_plugin_installations p
			SET state=$5,last_error=$6,enabled_at=$7,updated_at=clock_timestamp()
			FROM sub2api_plugin_maintenance j
			WHERE p.id=$1 AND p.plugin_key=$2 AND p.binary_sha256=$3 AND p.config_revision=$4 AND p.state='upgrading'
			AND j.plugin_id=p.id AND j.owner_token=$8 AND j.expires_at > clock_timestamp()
			AND j.installed_sha256=p.binary_sha256 AND j.installed_config_revision=p.config_revision
			AND j.previous_installation->>'state'=$5
			AND NOT EXISTS (SELECT 1 FROM sub2api_plugin_runtime_requests q WHERE q.plugin_id=p.id)`,
			installed.ID, installed.PluginKey, installed.BinarySHA256, installed.ConfigRevision, installed.State, installed.LastError, installed.EnabledAt, owner)); err != nil {
			return err
		}
		return pluginMaintenanceRows(tx.ExecContext(ctx, `DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp()`, installed.ID, owner))
	})
}

// The snapshot comes from to_jsonb(p), not PluginInstallation JSON (which omits
// ciphertext/artifacts). PostgreSQL restores bytea, NULL scope and timestamps.
const pluginMaintenanceRestoreSQL = `UPDATE sub2api_plugin_installations p SET
	(name,version,description,author,manifest,artifact_data,artifact_path,install_path,binary_path,binary_sha256,
	 signature_status,state,config_encrypted,config_revision,managed_scope,last_error,installed_by,installed_at,enabled_at,updated_at) =
	(s.name,s.version,s.description,s.author,s.manifest,s.artifact_data,s.artifact_path,s.install_path,s.binary_path,s.binary_sha256,
	 s.signature_status,s.state,s.config_encrypted,s.config_revision,s.managed_scope,s.last_error,
	 (SELECT id FROM users WHERE id=s.installed_by),s.installed_at,s.enabled_at,clock_timestamp())
	FROM sub2api_plugin_maintenance j,
	LATERAL jsonb_populate_record(NULL::sub2api_plugin_installations,j.previous_installation) s
	WHERE p.id=$1 AND j.plugin_id=p.id AND p.state='upgrading'
	AND p.id=s.id AND p.plugin_key=s.plugin_key AND s.state NOT IN ('starting','upgrading')
	AND p.binary_sha256=j.installed_sha256 AND p.config_revision=j.installed_config_revision`

func (r *pluginRepository) RollbackPluginMaintenance(ctx context.Context, id int64, owner string) error {
	return r.pluginMaintenanceTx(ctx, id, owner, true, func(tx *sql.Tx) error {
		if err := pluginMaintenanceRows(tx.ExecContext(ctx, pluginMaintenanceRestoreSQL+`
			AND j.owner_token=$2 AND j.expires_at > clock_timestamp()`, id, owner)); err != nil {
			return err
		}
		return pluginMaintenanceRows(tx.ExecContext(ctx, `DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1 AND owner_token=$2 AND expires_at > clock_timestamp()`, id, owner))
	})
}

func (r *pluginRepository) RecoverExpiredPluginMaintenance(ctx context.Context) (int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT plugin_id FROM sub2api_plugin_maintenance WHERE expires_at <= clock_timestamp() ORDER BY plugin_id`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return 0, err
	}
	var recovered int64
	var failures []error
	for _, id := range ids {
		ok, err := r.recoverExpiredPluginMaintenance(ctx, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("recover plugin maintenance %d: %w", id, err))
		}
		if ok {
			recovered++
		}
		if ctx.Err() != nil {
			break
		}
	}
	return recovered, errors.Join(failures...)
}

func (r *pluginRepository) recoverExpiredPluginMaintenance(ctx context.Context, id int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	if err := tx.QueryRowContext(ctx, pluginMaintenanceLockSQL, id).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	// Discovery is only a hint: a renew, finish or new Begin may have won while
	// waiting for the installation lock. Recheck the current journal now.
	if err := tx.QueryRowContext(ctx, `SELECT plugin_id FROM sub2api_plugin_maintenance
		WHERE plugin_id=$1 AND expires_at <= clock_timestamp() FOR UPDATE`, id).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := pluginMaintenanceRows(tx.ExecContext(ctx, pluginMaintenanceRestoreSQL+`
		AND j.expires_at <= clock_timestamp()`, id)); err != nil {
		return false, err
	}
	if err := pluginMaintenanceRows(tx.ExecContext(ctx, `DELETE FROM sub2api_plugin_maintenance WHERE plugin_id=$1`, id)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

var _ service.PluginMaintenanceRepository = (*pluginRepository)(nil)
