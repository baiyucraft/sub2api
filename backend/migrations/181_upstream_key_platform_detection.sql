ALTER TABLE upstream_keys
    ALTER COLUMN platform DROP DEFAULT,
    ALTER COLUMN platform DROP NOT NULL;

ALTER TABLE upstream_keys
    ADD COLUMN IF NOT EXISTS platform_source VARCHAR(16) NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS detected_platform VARCHAR(50),
    ADD COLUMN IF NOT EXISTS platform_detection_status VARCHAR(16) NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS platform_detected_at TIMESTAMPTZ;

ALTER TABLE upstream_keys
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_detected_platform_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_source_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_detection_status_valid;

ALTER TABLE upstream_keys
    ADD CONSTRAINT upstream_keys_platform_valid
        CHECK (platform IS NULL OR platform IN ('anthropic', 'openai', 'gemini', 'grok')),
    ADD CONSTRAINT upstream_keys_detected_platform_valid
        CHECK (detected_platform IS NULL OR detected_platform IN ('anthropic', 'openai', 'gemini', 'grok')),
    ADD CONSTRAINT upstream_keys_platform_source_valid
        CHECK (platform_source IN ('legacy', 'auto', 'manual', 'unassigned')),
    ADD CONSTRAINT upstream_keys_platform_detection_status_valid
        CHECK (platform_detection_status IN ('legacy', 'detected', 'unresolved', 'ambiguous', 'conflict'));

CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_platform VARCHAR(50);
    key_deleted_at TIMESTAMPTZ;
BEGIN
    IF NEW.upstream_key_id IS NULL THEN
        NEW.upstream_stale_pause_key_id := NULL;
        NEW.upstream_stale_paused_at := NULL;
        RETURN NEW;
    END IF;

    SELECT upstream_config_id, status, platform, deleted_at
      INTO key_config_id, key_status, key_platform, key_deleted_at
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

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, platform, schedulable
ON accounts
FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
