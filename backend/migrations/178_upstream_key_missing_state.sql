ALTER TABLE upstream_keys
    ADD COLUMN IF NOT EXISTS missing_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS missing_since TIMESTAMPTZ;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS upstream_stale_pause_key_id BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_stale_paused_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_upstream_keys_config_missing
    ON upstream_keys(upstream_config_id, missing_count, missing_since)
    WHERE deleted_at IS NULL AND missing_count > 0;

CREATE INDEX IF NOT EXISTS idx_accounts_upstream_stale_pause_key
    ON accounts(upstream_stale_pause_key_id)
    WHERE deleted_at IS NULL AND upstream_stale_pause_key_id IS NOT NULL;

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

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, schedulable
ON accounts
FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
