\set ON_ERROR_STOP on

BEGIN;

DO $$
DECLARE
    null_platforms INTEGER;
    mismatched_schedulable_accounts INTEGER;
BEGIN
    SELECT COUNT(*) INTO null_platforms
      FROM upstream_keys
     WHERE platform IS NULL;
    IF null_platforms <> 0 THEN
        RAISE EXCEPTION 'rollback refused: % upstream keys still have NULL platform; restore the coordinated data snapshot first', null_platforms;
    END IF;

    SELECT COUNT(*) INTO mismatched_schedulable_accounts
      FROM accounts a
      JOIN upstream_keys k ON k.id = a.upstream_key_id
     WHERE a.deleted_at IS NULL
       AND a.schedulable
       AND k.platform IS DISTINCT FROM a.platform;
    IF mismatched_schedulable_accounts <> 0 THEN
        RAISE EXCEPTION 'rollback refused: % schedulable accounts have platform mismatches', mismatched_schedulable_accounts;
    END IF;
END
$$;

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;

CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_deleted_at TIMESTAMPTZ;
BEGIN
    IF NEW.upstream_key_id IS NULL THEN
        NEW.upstream_stale_pause_key_id := NULL;
        NEW.upstream_stale_paused_at := NULL;
        RETURN NEW;
    END IF;

    SELECT upstream_config_id, status, deleted_at
      INTO key_config_id, key_status, key_deleted_at
      FROM upstream_keys
     WHERE id = NEW.upstream_key_id
     FOR KEY SHARE;

    IF NOT FOUND OR key_deleted_at IS NOT NULL OR key_config_id IS DISTINCT FROM NEW.upstream_config_id THEN
        RAISE EXCEPTION 'invalid upstream key binding' USING ERRCODE = '23514';
    END IF;

    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id)
       AND key_status <> 'active' THEN
        RAISE EXCEPTION 'cannot bind an inactive upstream key' USING ERRCODE = '23514';
    END IF;

    IF NEW.schedulable AND key_status = 'stale' THEN
        RAISE EXCEPTION 'cannot schedule an account bound to a stale upstream key' USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, schedulable
ON accounts
FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();

ALTER TABLE upstream_keys
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_detected_platform_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_source_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_detection_status_valid;

ALTER TABLE upstream_keys
    ALTER COLUMN platform SET DEFAULT 'openai',
    ALTER COLUMN platform SET NOT NULL,
    DROP COLUMN IF EXISTS platform_source,
    DROP COLUMN IF EXISTS detected_platform,
    DROP COLUMN IF EXISTS platform_detection_status,
    DROP COLUMN IF EXISTS platform_detected_at;

DELETE FROM schema_migrations
 WHERE filename = '181_upstream_key_platform_detection.sql';

INSERT INTO scheduler_outbox (event_type, payload)
VALUES ('full_rebuild', NULL);

COMMIT;
