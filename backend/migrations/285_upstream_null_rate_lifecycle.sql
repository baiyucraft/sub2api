-- Missing keys can outlive an older sync that cleared their rate. Allow only
-- fail-closed lifecycle updates of that existing binding, never a guessed rate.
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
    IF NOT FOUND
       OR key_config_id IS DISTINCT FROM NEW.upstream_config_id
       OR (key_deleted_at IS NOT NULL AND NEW.deleted_at IS NULL) THEN
        RAISE EXCEPTION 'invalid upstream key binding' USING ERRCODE = '23514';
    END IF;
    IF key_actual_rate IS NULL THEN
        IF TG_OP = 'UPDATE' THEN
            IF OLD.upstream_config_id IS NOT NULL
               AND OLD.upstream_key_id IS NOT NULL
               AND NEW.upstream_config_id IS NOT DISTINCT FROM OLD.upstream_config_id
               AND NEW.upstream_key_id IS NOT DISTINCT FROM OLD.upstream_key_id
               AND NEW.platform IS NOT DISTINCT FROM OLD.platform
               AND NEW.rate_multiplier IS NOT DISTINCT FROM OLD.rate_multiplier
               AND NEW.upstream_source_rate_multiplier IS NOT DISTINCT FROM OLD.upstream_source_rate_multiplier
               AND NEW.priority IS NOT DISTINCT FROM OLD.priority
               AND (OLD.deleted_at IS NULL OR NEW.deleted_at IS NOT NULL)
               AND (
                   NEW.schedulable IS FALSE
                   OR (
                       NEW.deleted_at IS NOT NULL
                       AND NEW.schedulable IS NOT DISTINCT FROM OLD.schedulable
                   )
               ) THEN
                RETURN NEW;
            END IF;
        END IF;
        RAISE EXCEPTION 'cannot bind an upstream key without an actual rate' USING ERRCODE = '23514';
    END IF;
    IF key_deleted_at IS NOT NULL AND NEW.deleted_at IS NOT NULL THEN
        NEW.rate_multiplier := key_actual_rate;
        NEW.upstream_source_rate_multiplier := key_source_rate;
        NEW.priority := CEIL(key_actual_rate * 100)::INTEGER;
        RETURN NEW;
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id
        OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL)) AND key_status <> 'active' THEN
        RAISE EXCEPTION 'cannot bind an inactive upstream key' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id
        OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL))
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
    rate_multiplier, upstream_source_rate_multiplier, priority, schedulable, deleted_at
ON accounts FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
