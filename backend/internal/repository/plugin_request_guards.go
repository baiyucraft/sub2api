package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *pluginRepository) BeginPluginRequest(ctx context.Context, id int64, sha string, revision uint64, requestID string) error {
	result, err := r.db.ExecContext(ctx, `WITH ready AS (
		SELECT id FROM sub2api_plugin_installations p
		WHERE id=$1 AND binary_sha256=$2 AND config_revision=$3 AND state='enabled'
		AND EXISTS (SELECT 1 FROM sub2api_plugin_bindings b WHERE b.plugin_id=p.id AND b.enabled)
		FOR SHARE
	) INSERT INTO sub2api_plugin_runtime_requests(plugin_id,request_id)
	SELECT id,$4 FROM ready`, id, sha, revision, requestID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return service.ErrPluginStateChanged
	}
	return nil
}

func (r *pluginRepository) EndPluginRequest(ctx context.Context, id int64, requestID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1 AND request_id=$2`, id, requestID)
	return err
}

func (r *pluginRepository) PluginRequestsInFlight(ctx context.Context, id int64) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1`, id).Scan(&count)
	return count, err
}
