-- Immutable per-key upstream rate observations. Secret key material is never stored here.
CREATE TABLE IF NOT EXISTS upstream_key_rate_snapshots (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    upstream_key_id BIGINT REFERENCES upstream_keys(id) ON DELETE SET NULL,
    remote_key_id BIGINT,
    key_name_snapshot VARCHAR(100) NOT NULL DEFAULT '',
    key_hash_snapshot VARCHAR(128) NOT NULL DEFAULT '',
    sync_run_id BIGINT REFERENCES upstream_sync_runs(id) ON DELETE SET NULL,
    provider VARCHAR(32) NOT NULL,
    raw_rate_multiplier DECIMAL(20,10) NOT NULL,
    recharge_rate DECIMAL(20,10) NOT NULL,
    effective_cost_multiplier DECIMAL(20,10) NOT NULL,
    source VARCHAR(32) NOT NULL DEFAULT 'sync',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_key_rate_snapshots_key_run
    ON upstream_key_rate_snapshots(upstream_key_id, sync_run_id)
    WHERE upstream_key_id IS NOT NULL AND sync_run_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_upstream_key_rate_snapshots_config_observed
    ON upstream_key_rate_snapshots(upstream_config_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_upstream_key_rate_snapshots_key_observed
    ON upstream_key_rate_snapshots(upstream_key_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_upstream_key_rate_snapshots_run_id
    ON upstream_key_rate_snapshots(sync_run_id);
