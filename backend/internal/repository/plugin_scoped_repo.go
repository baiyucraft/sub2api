package repository

import (
	"context"
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *pluginRepository) UpdateScopedConfig(ctx context.Context, id int64, encrypted, expectedSHA string, expectedRevision uint64, scope []service.PluginManagedTarget) (uint64, error) {
	if scope == nil {
		scope = []service.PluginManagedTarget{}
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE sub2api_plugin_installations
		SET config_encrypted=$2, config_revision=config_revision+1, managed_scope=$3::jsonb, updated_at=NOW()
		WHERE id=$1 AND binary_sha256=$4 AND config_revision=$5 AND state <> 'upgrading'`, id, encrypted, raw, expectedSHA, expectedRevision)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rows != 1 {
		return 0, service.ErrPluginStateChanged
	}
	return expectedRevision + 1, nil
}

func (r *pluginRepository) UpgradePackage(ctx context.Context, previous, replacement *service.PluginInstallation) error {
	manifest, err := json.Marshal(replacement.Manifest)
	if err != nil {
		return err
	}
	var scope any
	if replacement.ManagedScope != nil {
		raw, encodeErr := json.Marshal(replacement.ManagedScope)
		if encodeErr != nil {
			return encodeErr
		}
		scope = string(raw)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE sub2api_plugin_installations SET
		name=$2,version=$3,description=$4,author=$5,manifest=$6::jsonb,artifact_data=$7,
		artifact_path=$8,install_path=$9,binary_path=$10,binary_sha256=$11,signature_status=$12,
		config_encrypted=$13,config_revision=$14,managed_scope=$15::jsonb,last_error='',updated_at=NOW()
		WHERE id=$1 AND plugin_key=$16 AND binary_sha256=$17 AND config_revision=$18 AND state=$19 AND state <> 'upgrading'`,
		previous.ID, replacement.Name, replacement.Version, replacement.Description, replacement.Author, manifest, replacement.ArtifactData,
		replacement.ArtifactPath, replacement.InstallPath, replacement.BinaryPath, replacement.BinarySHA256, replacement.SignatureStatus,
		replacement.ConfigEncrypted, replacement.ConfigRevision, scope, previous.PluginKey, previous.BinarySHA256, previous.ConfigRevision, previous.State)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return service.ErrPluginStateChanged
	}
	return nil
}
