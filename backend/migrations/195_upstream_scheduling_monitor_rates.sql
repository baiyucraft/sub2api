-- Upstream scheduling gates, probe retry configuration, managed monitor keys,
-- and immutable public group-rate snapshots.

ALTER TABLE upstream_configs
    ADD COLUMN IF NOT EXISTS scheduling_enabled BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS upstream_source_rate_multiplier DECIMAL(20,10);

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_upstream_source_rate_valid,
    ADD CONSTRAINT accounts_upstream_source_rate_valid
        CHECK (upstream_source_rate_multiplier IS NULL OR upstream_source_rate_multiplier >= 0);

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS purpose VARCHAR(32) NOT NULL DEFAULT 'general',
    ADD COLUMN IF NOT EXISTS managed_monitor_id BIGINT;

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS api_keys_purpose_check,
    ADD CONSTRAINT api_keys_purpose_check
        CHECK (purpose IN ('general', 'managed_monitor'));
CREATE INDEX IF NOT EXISTS idx_api_keys_purpose ON api_keys(purpose);
CREATE INDEX IF NOT EXISTS idx_api_keys_managed_monitor_id ON api_keys(managed_monitor_id);

ALTER TABLE channel_monitors
    ADD COLUMN IF NOT EXISTS credential_mode VARCHAR(32) NOT NULL DEFAULT 'manual',
    ADD COLUMN IF NOT EXISTS group_id BIGINT,
    ADD COLUMN IF NOT EXISTS show_group_rate BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS managed_api_key_id BIGINT,
    ADD COLUMN IF NOT EXISTS max_probe_attempts INT NOT NULL DEFAULT 3;

ALTER TABLE channel_monitors
    DROP CONSTRAINT IF EXISTS channel_monitors_credential_mode_check,
    ADD CONSTRAINT channel_monitors_credential_mode_check
        CHECK (credential_mode IN ('manual', 'managed_local')),
    DROP CONSTRAINT IF EXISTS channel_monitors_max_probe_attempts_check,
    ADD CONSTRAINT channel_monitors_max_probe_attempts_check
        CHECK (max_probe_attempts BETWEEN 1 AND 5);
CREATE INDEX IF NOT EXISTS idx_channel_monitors_group_id ON channel_monitors(group_id);
CREATE INDEX IF NOT EXISTS idx_channel_monitors_managed_api_key_id ON channel_monitors(managed_api_key_id);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'channel_monitors_group_id_fkey'
           AND conrelid = 'channel_monitors'::regclass
           AND contype = 'f'
    ) THEN
        ALTER TABLE channel_monitors
            ADD CONSTRAINT channel_monitors_group_id_fkey
            FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conname = 'channel_monitors_managed_api_key_id_fkey'
           AND conrelid = 'channel_monitors'::regclass
           AND contype = 'f'
    ) THEN
        ALTER TABLE channel_monitors
            ADD CONSTRAINT channel_monitors_managed_api_key_id_fkey
            FOREIGN KEY (managed_api_key_id) REFERENCES api_keys(id) ON DELETE SET NULL;
    END IF;
END $$;

ALTER TABLE channel_monitor_histories
    DROP CONSTRAINT IF EXISTS channel_monitor_histories_status_check,
    ADD CONSTRAINT channel_monitor_histories_status_check
        CHECK (status IN ('operational', 'degraded', 'failed', 'error', 'unknown'));

CREATE TABLE IF NOT EXISTS group_rate_snapshots (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    rate_multiplier DECIMAL(10,4) NOT NULL,
    peak_rate_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    peak_start VARCHAR(5) NOT NULL DEFAULT '',
    peak_end VARCHAR(5) NOT NULL DEFAULT '',
    peak_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1,
    timezone VARCHAR(64) NOT NULL DEFAULT 'UTC',
    effective_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_group_rate_snapshots_group_effective
    ON group_rate_snapshots(group_id, effective_at);

-- Existing groups receive one current snapshot at deployment time only.
INSERT INTO group_rate_snapshots
    (group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
     peak_rate_multiplier, timezone, effective_at)
SELECT id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
       peak_rate_multiplier,
       COALESCE(NULLIF(current_setting('TIMEZONE', TRUE), ''), 'UTC'), NOW()
  FROM groups g
 WHERE deleted_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM group_rate_snapshots s WHERE s.group_id = g.id);

CREATE OR REPLACE FUNCTION record_group_rate_snapshot()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT'
       OR NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier
       OR NEW.peak_rate_enabled IS DISTINCT FROM OLD.peak_rate_enabled
       OR NEW.peak_start IS DISTINCT FROM OLD.peak_start
       OR NEW.peak_end IS DISTINCT FROM OLD.peak_end
       OR NEW.peak_rate_multiplier IS DISTINCT FROM OLD.peak_rate_multiplier THEN
        INSERT INTO group_rate_snapshots
            (group_id, rate_multiplier, peak_rate_enabled, peak_start, peak_end,
             peak_rate_multiplier, timezone, effective_at)
        VALUES
            (NEW.id, NEW.rate_multiplier, NEW.peak_rate_enabled, NEW.peak_start,
             NEW.peak_end, NEW.peak_rate_multiplier,
             COALESCE(NULLIF(current_setting('TIMEZONE', TRUE), ''), 'UTC'), NOW());
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_record_group_rate_snapshot ON groups;
CREATE TRIGGER trg_record_group_rate_snapshot
AFTER INSERT OR UPDATE OF rate_multiplier, peak_rate_enabled, peak_start,
    peak_end, peak_rate_multiplier ON groups
FOR EACH ROW EXECUTE FUNCTION record_group_rate_snapshot();

-- Persist the provider's unrounded value on bound accounts and use a decimal
-- ceiling for the public/billing multiplier and scheduler priority.
UPDATE accounts a
   SET upstream_source_rate_multiplier = k.source_rate_multiplier,
       updated_at = NOW()
  FROM upstream_keys k
 WHERE a.upstream_key_id = k.id
   AND a.upstream_source_rate_multiplier IS DISTINCT FROM k.source_rate_multiplier;

DO $$
DECLARE
    invalid_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO invalid_count
      FROM upstream_keys k
      JOIN upstream_configs c ON c.id = k.upstream_config_id
     WHERE k.source_rate_multiplier IS NOT NULL
       AND CEIL((k.source_rate_multiplier * c.recharge_rate) * 100) / 100 > 999999.9999;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 195: rounded-up upstream rates cannot be represented safely: %', invalid_count;
    END IF;
END $$;

CREATE TEMP TABLE migration_195_preserved_upstream_key_ids (
    id BIGINT PRIMARY KEY
) ON COMMIT DROP;

INSERT INTO migration_195_preserved_upstream_key_ids (id)
SELECT id
  FROM upstream_keys
 WHERE source_rate_multiplier IS NULL
   AND rate_multiplier IS NOT NULL;

UPDATE upstream_keys
   SET source_rate_multiplier = rate_multiplier,
       updated_at = NOW()
 WHERE id IN (SELECT id FROM migration_195_preserved_upstream_key_ids);

UPDATE upstream_keys
   SET rate_multiplier = CASE
       WHEN COALESCE(source_rate_multiplier, 0) = 0 THEN 0
       ELSE CEIL((source_rate_multiplier * c.recharge_rate) * 100) / 100
   END,
       updated_at = NOW()
 FROM upstream_configs c
 WHERE c.id = upstream_keys.upstream_config_id
   AND source_rate_multiplier IS NOT NULL
   AND upstream_keys.id NOT IN (SELECT id FROM migration_195_preserved_upstream_key_ids);

COMMENT ON COLUMN upstream_keys.source_rate_multiplier IS 'Internal provider multiplier; never expose through product APIs';
COMMENT ON COLUMN upstream_keys.rate_multiplier IS 'Public and billing multiplier: ceil(source_rate_multiplier * recharge_rate, 2 decimals)';
COMMENT ON COLUMN accounts.upstream_source_rate_multiplier IS 'Internal unrounded upstream multiplier for scheduler tie-breaking';

CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_platform VARCHAR(50);
    key_deleted_at TIMESTAMPTZ;
    key_actual_rate NUMERIC(10,4);
    key_source_rate NUMERIC(20,10);
    derived_priority INTEGER;
    base_concurrency BIGINT;
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
    IF NEW.concurrency > 1073741823 THEN
        RAISE EXCEPTION 'upstream account concurrency cannot derive a safe load factor' USING ERRCODE = '23514';
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
    derived_priority := CEIL(key_actual_rate * 100)::INTEGER;
    NEW.priority := derived_priority;
    base_concurrency := GREATEST(NEW.concurrency::BIGINT, 1::BIGINT);
    NEW.load_factor := LEAST(base_concurrency * 2, GREATEST(1, ROUND(base_concurrency * CASE
        WHEN derived_priority <= 5 THEN 2.0 WHEN derived_priority <= 10 THEN 1.5
        WHEN derived_priority <= 20 THEN 1.0 WHEN derived_priority <= 50 THEN 0.75 ELSE 0.5
    END)::INTEGER));
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, platform,
    rate_multiplier, priority, load_factor, concurrency, schedulable, deleted_at
ON accounts FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();

-- Re-run every existing binding through the new trigger so account billing and
-- scheduler fields cannot retain migration 182's four-decimal ROUND values.
UPDATE accounts
   SET rate_multiplier = rate_multiplier,
       updated_at = NOW()
 WHERE upstream_key_id IS NOT NULL;

INSERT INTO scheduler_outbox (event_type, payload)
SELECT 'account_bulk_changed', jsonb_build_object('account_ids', jsonb_agg(id ORDER BY id))
  FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL
HAVING COUNT(*) > 0;
