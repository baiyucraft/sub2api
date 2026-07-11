-- Non-transactional indexes for large existing tables and partial uniqueness.

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_upstream_keys_config_remote_key_id_active
    ON upstream_keys(upstream_config_id, remote_key_id)
    WHERE remote_key_id IS NOT NULL AND deleted_at IS NULL;

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_upstream_config_created_at;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_upstream_config_created_at
    ON usage_logs(upstream_config_id, created_at);

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_upstream_key_created_at;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_upstream_key_created_at
    ON usage_logs(upstream_key_id, created_at);
