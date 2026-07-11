-- Upstream synchronization, audit, incident, and balance operation models.

ALTER TABLE upstream_configs
    ADD COLUMN IF NOT EXISTS recharge_rate DECIMAL(20,10) NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS balance_to_cny_rate DECIMAL(20,10);

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_config_id BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_key_id BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_cost_currency VARCHAR(8),
    ADD COLUMN IF NOT EXISTS upstream_cost_to_cny_rate DECIMAL(20,10);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'fk_usage_logs_upstream_config_id'
          AND conrelid = 'usage_logs'::regclass
    ) THEN
        ALTER TABLE usage_logs
            ADD CONSTRAINT fk_usage_logs_upstream_config_id
            FOREIGN KEY (upstream_config_id) REFERENCES upstream_configs(id)
            ON DELETE SET NULL NOT VALID;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'fk_usage_logs_upstream_key_id'
          AND conrelid = 'usage_logs'::regclass
    ) THEN
        ALTER TABLE usage_logs
            ADD CONSTRAINT fk_usage_logs_upstream_key_id
            FOREIGN KEY (upstream_key_id) REFERENCES upstream_keys(id)
            ON DELETE SET NULL NOT VALID;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS upstream_sync_runs (
    id BIGSERIAL PRIMARY KEY,
    trigger VARCHAR(32) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    total_configs INTEGER NOT NULL DEFAULT 0,
    success_configs INTEGER NOT NULL DEFAULT 0,
    partial_configs INTEGER NOT NULL DEFAULT 0,
    failed_configs INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS upstream_sync_results (
    id BIGSERIAL PRIMARY KEY,
    sync_run_id BIGINT NOT NULL REFERENCES upstream_sync_runs(id) ON DELETE CASCADE,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    config_name VARCHAR(100) NOT NULL,
    provider VARCHAR(32) NOT NULL,
    status VARCHAR(20) NOT NULL,
    stage VARCHAR(32) NOT NULL DEFAULT '',
    error_code VARCHAR(32) NOT NULL DEFAULT '',
    safe_message TEXT,
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    http_status INTEGER,
    remote_key_count INTEGER NOT NULL DEFAULT 0,
    persisted_key_count INTEGER NOT NULL DEFAULT 0,
    fallback_key_count INTEGER NOT NULL DEFAULT 0,
    unresolved_key_count INTEGER NOT NULL DEFAULT 0,
    updated_account_count INTEGER NOT NULL DEFAULT 0,
    warnings JSONB NOT NULL DEFAULT '[]'::jsonb,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS upstream_events (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    upstream_key_id BIGINT REFERENCES upstream_keys(id) ON DELETE SET NULL,
    sync_run_id BIGINT REFERENCES upstream_sync_runs(id) ON DELETE SET NULL,
    account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    event_key VARCHAR(128),
    event_type VARCHAR(64) NOT NULL,
    severity VARCHAR(20) NOT NULL,
    source VARCHAR(32) NOT NULL,
    message TEXT,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS upstream_incidents (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    upstream_key_id BIGINT REFERENCES upstream_keys(id) ON DELETE SET NULL,
    source_event_id BIGINT REFERENCES upstream_events(id) ON DELETE SET NULL,
    incident_key VARCHAR(128) NOT NULL,
    incident_type VARCHAR(64) NOT NULL,
    severity VARCHAR(20) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'open',
    title VARCHAR(255) NOT NULL,
    description TEXT,
    metric_value DECIMAL(20,10),
    threshold_value DECIMAL(20,10),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    acknowledged_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS upstream_balance_snapshots (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    sync_run_id BIGINT REFERENCES upstream_sync_runs(id) ON DELETE SET NULL,
    provider VARCHAR(32) NOT NULL,
    balance_raw DECIMAL(20,10),
    used_raw DECIMAL(20,10),
    total_raw DECIMAL(20,10),
    balance_cny DECIMAL(20,10),
    used_cny DECIMAL(20,10),
    total_recharged_cny DECIMAL(20,10),
    currency_source VARCHAR(16) NOT NULL DEFAULT '',
    currency_to_cny_rate DECIMAL(20,10),
    currency_rate_source VARCHAR(32) NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_status_started_at
    ON upstream_sync_runs(status, started_at);
CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_started_at
    ON upstream_sync_runs(started_at);

CREATE INDEX IF NOT EXISTS idx_upstream_sync_results_run_id
    ON upstream_sync_results(sync_run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_sync_results_run_config
    ON upstream_sync_results(sync_run_id, upstream_config_id);
CREATE INDEX IF NOT EXISTS idx_upstream_sync_results_config_created_at
    ON upstream_sync_results(upstream_config_id, created_at);
CREATE INDEX IF NOT EXISTS idx_upstream_sync_results_status_created_at
    ON upstream_sync_results(status, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_events_config_event_key
    ON upstream_events(upstream_config_id, event_key)
    WHERE event_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_upstream_events_config_occurred_at
    ON upstream_events(upstream_config_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_upstream_events_key_occurred_at
    ON upstream_events(upstream_key_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_upstream_events_sync_run_id
    ON upstream_events(sync_run_id);
CREATE INDEX IF NOT EXISTS idx_upstream_events_type_occurred_at
    ON upstream_events(event_type, occurred_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_incidents_config_incident_key
    ON upstream_incidents(upstream_config_id, incident_key);
CREATE INDEX IF NOT EXISTS idx_upstream_incidents_status_severity_last_seen
    ON upstream_incidents(status, severity, last_seen_at);
CREATE INDEX IF NOT EXISTS idx_upstream_incidents_upstream_key_id
    ON upstream_incidents(upstream_key_id);
CREATE INDEX IF NOT EXISTS idx_upstream_incidents_source_event_id
    ON upstream_incidents(source_event_id);

CREATE INDEX IF NOT EXISTS idx_upstream_balance_snapshots_config_captured_at
    ON upstream_balance_snapshots(upstream_config_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_upstream_balance_snapshots_run_id
    ON upstream_balance_snapshots(sync_run_id);

WITH account_refs AS (
    SELECT upstream_key_id, COUNT(*) AS ref_count
    FROM accounts
    WHERE upstream_key_id IS NOT NULL
    GROUP BY upstream_key_id
), ranked AS (
    SELECT
        uk.id,
        uk.upstream_config_id,
        uk.remote_key_id,
        FIRST_VALUE(uk.id) OVER (
            PARTITION BY uk.upstream_config_id, uk.remote_key_id
            ORDER BY COALESCE(ar.ref_count, 0) DESC,
                     uk.last_seen_at DESC NULLS LAST,
                     uk.id ASC
        ) AS keeper_key_id,
        ROW_NUMBER() OVER (
            PARTITION BY uk.upstream_config_id, uk.remote_key_id
            ORDER BY COALESCE(ar.ref_count, 0) DESC,
                     uk.last_seen_at DESC NULLS LAST,
                     uk.id ASC
        ) AS duplicate_rank
    FROM upstream_keys uk
    LEFT JOIN account_refs ar ON ar.upstream_key_id = uk.id
    WHERE uk.remote_key_id IS NOT NULL
      AND uk.deleted_at IS NULL
)
INSERT INTO upstream_events (
    upstream_config_id,
    upstream_key_id,
    event_key,
    event_type,
    severity,
    source,
    message,
    payload,
    occurred_at,
    created_at
)
SELECT
    upstream_config_id,
    id,
    'remote_key_id_duplicate_cleared:' || upstream_config_id || ':' || id || ':' || remote_key_id,
    'remote_key_id_duplicate_cleared',
    'warning',
    'migration',
    'Cleared a duplicate active upstream remote key identifier before enforcing uniqueness.',
    jsonb_build_object(
        'keeper_key_id', keeper_key_id,
        'cleared_key_id', id,
        'remote_key_id', remote_key_id
    ),
    NOW(),
    NOW()
FROM ranked
WHERE duplicate_rank > 1
ON CONFLICT (upstream_config_id, event_key) WHERE event_key IS NOT NULL DO NOTHING;

WITH account_refs AS (
    SELECT upstream_key_id, COUNT(*) AS ref_count
    FROM accounts
    WHERE upstream_key_id IS NOT NULL
    GROUP BY upstream_key_id
), ranked AS (
    SELECT
        uk.id,
        FIRST_VALUE(uk.id) OVER (
            PARTITION BY uk.upstream_config_id, uk.remote_key_id
            ORDER BY COALESCE(ar.ref_count, 0) DESC,
                     uk.last_seen_at DESC NULLS LAST,
                     uk.id ASC
        ) AS keeper_key_id,
        ROW_NUMBER() OVER (
            PARTITION BY uk.upstream_config_id, uk.remote_key_id
            ORDER BY COALESCE(ar.ref_count, 0) DESC,
                     uk.last_seen_at DESC NULLS LAST,
                     uk.id ASC
        ) AS duplicate_rank
    FROM upstream_keys uk
    LEFT JOIN account_refs ar ON ar.upstream_key_id = uk.id
    WHERE uk.remote_key_id IS NOT NULL
      AND uk.deleted_at IS NULL
)
UPDATE accounts a
SET upstream_key_id = ranked.keeper_key_id,
    updated_at = NOW()
FROM ranked
WHERE a.upstream_key_id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH account_refs AS (
    SELECT upstream_key_id, COUNT(*) AS ref_count
    FROM accounts
    WHERE upstream_key_id IS NOT NULL
    GROUP BY upstream_key_id
), ranked AS (
    SELECT
        uk.id,
        ROW_NUMBER() OVER (
            PARTITION BY uk.upstream_config_id, uk.remote_key_id
            ORDER BY COALESCE(ar.ref_count, 0) DESC,
                     uk.last_seen_at DESC NULLS LAST,
                     uk.id ASC
        ) AS duplicate_rank
    FROM upstream_keys uk
    LEFT JOIN account_refs ar ON ar.upstream_key_id = uk.id
    WHERE uk.remote_key_id IS NOT NULL
      AND uk.deleted_at IS NULL
)
UPDATE upstream_keys uk
SET deleted_at = NOW(),
    updated_at = NOW()
FROM ranked
WHERE ranked.id = uk.id
  AND ranked.duplicate_rank > 1;
