CREATE TABLE IF NOT EXISTS upstream_health_observations (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL,
    upstream_key_id BIGINT NOT NULL,
    account_id BIGINT NULL,
    platform VARCHAR(50) NOT NULL DEFAULT '',
    model VARCHAR(255) NOT NULL DEFAULT '',
    protocol VARCHAR(50) NOT NULL DEFAULT '',
    source VARCHAR(20) NOT NULL DEFAULT 'probe',
    state VARCHAR(20) NOT NULL,
    result VARCHAR(100) NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    http_status INTEGER NULL,
    ttft_ms BIGINT NULL,
    duration_ms BIGINT NULL,
    input_tokens BIGINT NULL,
    output_tokens BIGINT NULL,
    output_tps DOUBLE PRECISION NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_upstream_health_observations_key_observed
    ON upstream_health_observations(upstream_key_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_upstream_health_observations_config_observed
    ON upstream_health_observations(upstream_config_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_upstream_health_observations_observed
    ON upstream_health_observations(observed_at);

-- Best-effort import of the bounded legacy JSON history. A malformed item is
-- skipped without blocking the schema migration.
DO $$
DECLARE
    key_row RECORD;
    item JSONB;
BEGIN
    FOR key_row IN
        SELECT id, upstream_config_id, platform, extra
        FROM upstream_keys
        WHERE jsonb_typeof(extra->'health_history') = 'array'
    LOOP
        FOR item IN SELECT value FROM jsonb_array_elements(key_row.extra->'health_history')
        LOOP
            BEGIN
                IF COALESCE(item->>'observed_at', '') = '' OR
                   COALESCE(item->>'state', '') NOT IN ('healthy', 'degraded', 'suspended', 'observing', 'recovering', 'disabled') THEN
                    CONTINUE;
                END IF;
                INSERT INTO upstream_health_observations (
                    upstream_config_id, upstream_key_id, account_id, platform,
                    source, state, result, reason, observed_at
                ) VALUES (
                    key_row.upstream_config_id,
                    key_row.id,
                    (SELECT a.id FROM accounts a WHERE a.upstream_key_id = key_row.id AND a.deleted_at IS NULL ORDER BY a.id LIMIT 1),
                    COALESCE(key_row.platform, ''),
                    'legacy',
                    item->>'state',
                    COALESCE(item->>'result', ''),
                    COALESCE(item->>'reason', ''),
                    (item->>'observed_at')::timestamptz
                );
            EXCEPTION WHEN OTHERS THEN
                CONTINUE;
            END;
        END LOOP;
    END LOOP;
END $$;

DELETE FROM upstream_health_observations
WHERE observed_at < NOW() - INTERVAL '35 days';
