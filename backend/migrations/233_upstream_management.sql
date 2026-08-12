-- Consolidated upstream-management schema for the first post-profile-232
-- release.  The previously split feature branches were never deployed, so
-- their contracts intentionally ship as one immutable migration.

-- One upstream key owns at most one non-deleted derived account.  Historical
-- duplicates are a hard preflight failure; the migration never unbinds or
-- deletes user data implicitly.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM accounts
        WHERE upstream_key_id IS NOT NULL
          AND deleted_at IS NULL
        GROUP BY upstream_key_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'upstream account key duplicate preflight failed';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_upstream_key_id_active
    ON accounts(upstream_key_id)
    WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL;

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

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE upstream_health_observations TO CURRENT_USER;
GRANT USAGE, SELECT ON SEQUENCE upstream_health_observations_id_seq TO CURRENT_USER;

-- Best-effort import of the bounded legacy JSON history.  A malformed item is
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

-- Restore the fork's original LoadFactor contract.  Upstream identity still
-- derives billing multiplier, unrounded source multiplier and Priority from
-- the bound Key, while account LoadFactor and Concurrency remain untouched.
CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_platform VARCHAR(50);
    key_deleted_at TIMESTAMPTZ;
    key_actual_rate NUMERIC(10,4);
    key_source_rate NUMERIC(20,10);
BEGIN
    IF NEW.upstream_key_id IS NULL THEN
        NEW.upstream_stale_pause_key_id := NULL;
        NEW.upstream_stale_paused_at := NULL;
        NEW.upstream_source_rate_multiplier := NULL;
        RETURN NEW;
    END IF;

    SELECT upstream_config_id, status, platform, deleted_at, rate_multiplier, source_rate_multiplier
      INTO key_config_id, key_status, key_platform, key_deleted_at, key_actual_rate, key_source_rate
      FROM upstream_keys WHERE id = NEW.upstream_key_id;
    IF NOT FOUND OR key_deleted_at IS NOT NULL OR key_config_id IS DISTINCT FROM NEW.upstream_config_id THEN
        RAISE EXCEPTION 'invalid upstream key binding' USING ERRCODE = '23514';
    END IF;
    IF key_actual_rate IS NULL THEN
        RAISE EXCEPTION 'cannot bind an upstream key without an actual rate' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id) AND key_status <> 'active' THEN
        RAISE EXCEPTION 'cannot bind an inactive upstream key' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id)
       AND (key_platform IS NULL OR key_platform IS DISTINCT FROM NEW.platform) THEN
        RAISE EXCEPTION 'cannot bind an unassigned or mismatched upstream key platform' USING ERRCODE = '23514';
    END IF;
    IF NEW.schedulable AND key_status = 'stale' THEN
        RAISE EXCEPTION 'cannot schedule an account bound to a stale upstream key' USING ERRCODE = '23514';
    END IF;
    IF NEW.schedulable AND (key_platform IS NULL OR key_platform IS DISTINCT FROM NEW.platform) THEN
        RAISE EXCEPTION 'cannot schedule an account with a mismatched upstream key platform' USING ERRCODE = '23514';
    END IF;

    NEW.rate_multiplier := key_actual_rate;
    NEW.upstream_source_rate_multiplier := key_source_rate;
    NEW.priority := CEIL(key_actual_rate * 100)::INTEGER;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, platform,
    rate_multiplier, priority, schedulable, deleted_at
ON accounts FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
