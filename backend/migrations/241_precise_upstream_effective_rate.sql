-- Preserve the full effective upstream multiplier instead of the historical
-- two-decimal ceiling.  The application computes source_rate * recharge_rate
-- and rounds only to the ten decimal places supported by these columns.
-- The migration contract is numeric(20,10) precision for every effective-rate
-- snapshot; PostgreSQL DECIMAL is its equivalent spelling.

BEGIN;

ALTER TABLE upstream_keys
    ALTER COLUMN rate_multiplier TYPE DECIMAL(20,10)
        USING rate_multiplier::DECIMAL(20,10);

ALTER TABLE accounts
    ALTER COLUMN rate_multiplier TYPE DECIMAL(20,10)
        USING rate_multiplier::DECIMAL(20,10);

ALTER TABLE usage_logs
    ALTER COLUMN account_rate_multiplier TYPE DECIMAL(20,10)
        USING account_rate_multiplier::DECIMAL(20,10);

-- Batch image jobs keep the account multiplier used by settlement as a
-- request-time snapshot.
ALTER TABLE batch_image_jobs
    ALTER COLUMN account_rate_multiplier TYPE DECIMAL(20,10)
        USING account_rate_multiplier::DECIMAL(20,10);

ALTER TABLE upstream_keys
    DROP CONSTRAINT IF EXISTS upstream_keys_actual_rate_valid,
    ADD CONSTRAINT upstream_keys_actual_rate_valid
        CHECK (rate_multiplier IS NULL OR (rate_multiplier >= 0 AND rate_multiplier <= 999999.9999));

COMMENT ON COLUMN upstream_keys.rate_multiplier IS
    'Exact effective cost multiplier: round(source_rate_multiplier * recharge_rate, 10)';
COMMENT ON COLUMN accounts.rate_multiplier IS
    'Exact effective upstream cost multiplier used for billing and scheduling';
COMMENT ON COLUMN usage_logs.account_rate_multiplier IS
    'Request-time snapshot of the exact account effective multiplier';
COMMENT ON COLUMN batch_image_jobs.account_rate_multiplier IS
    'Submission-time snapshot of the exact account effective multiplier';

-- Keep the account-binding trigger numerically lossless.  Earlier migration
-- versions declared key_actual_rate as NUMERIC(10,4), which silently rounded
-- values such as 0.045 back to 0.0500 during account updates.
CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_platform VARCHAR(50);
    key_deleted_at TIMESTAMPTZ;
    key_actual_rate NUMERIC(20,10);
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

-- Recompute current key values from their source and recharge rates, then
-- force every bound account through the lossless trigger.  Historical usage
-- snapshots are intentionally not rewritten.
UPDATE upstream_keys k
   SET rate_multiplier = ROUND(k.source_rate_multiplier * COALESCE(c.recharge_rate, 1), 10)
  FROM upstream_configs c
 WHERE c.id = k.upstream_config_id
   AND k.source_rate_multiplier IS NOT NULL;

UPDATE accounts
   SET rate_multiplier = rate_multiplier
 WHERE upstream_key_id IS NOT NULL;

INSERT INTO scheduler_outbox (event_type, payload)
SELECT 'account_bulk_changed', jsonb_build_object('account_ids', jsonb_agg(id ORDER BY id))
  FROM accounts
 WHERE upstream_key_id IS NOT NULL
HAVING COUNT(*) > 0;

COMMIT;
