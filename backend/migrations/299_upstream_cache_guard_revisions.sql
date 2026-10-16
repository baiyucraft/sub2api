-- Revisions include pending inserts, facts-only completions and retention.
-- Triggers keep older compatible application versions visible during rollback.
CREATE TABLE IF NOT EXISTS upstream_cache_guard_fact_versions (
    account_id BIGINT NOT NULL,
    upstream_key_id BIGINT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (account_id, upstream_key_id)
);
INSERT INTO upstream_cache_guard_fact_versions(account_id,upstream_key_id,version)
SELECT account_id,upstream_key_id,1 FROM upstream_cache_guard_facts GROUP BY account_id,upstream_key_id
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION upstream_cache_guard_advance_fact_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        INSERT INTO upstream_cache_guard_fact_versions(account_id,upstream_key_id,version)
        VALUES(OLD.account_id,OLD.upstream_key_id,1)
        ON CONFLICT(account_id,upstream_key_id) DO UPDATE SET version=upstream_cache_guard_fact_versions.version+1;
        RETURN OLD;
    END IF;
    INSERT INTO upstream_cache_guard_fact_versions(account_id,upstream_key_id,version)
    VALUES(NEW.account_id,NEW.upstream_key_id,1)
    ON CONFLICT(account_id,upstream_key_id) DO UPDATE SET version=upstream_cache_guard_fact_versions.version+1;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS upstream_cache_guard_fact_revision ON upstream_cache_guard_facts;
CREATE TRIGGER upstream_cache_guard_fact_revision AFTER INSERT OR UPDATE OF processed OR DELETE
ON upstream_cache_guard_facts FOR EACH ROW EXECUTE FUNCTION upstream_cache_guard_advance_fact_version();

CREATE INDEX IF NOT EXISTS idx_cache_guard_runtime_active_sessions
ON upstream_cache_guard_runtime(updated_at,group_id,state_key) WHERE state_kind='session';
CREATE INDEX IF NOT EXISTS idx_cache_guard_processed_account_time
ON upstream_cache_guard_facts(account_id,upstream_key_id,completed_at) WHERE processed=TRUE;

ALTER TABLE upstream_cache_guard_runtime ADD COLUMN IF NOT EXISTS has_pending_admissions BOOLEAN NOT NULL DEFAULT FALSE;
CREATE OR REPLACE FUNCTION upstream_cache_guard_pending_admissions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.has_pending_admissions := NEW.state_kind='session' AND EXISTS (
        SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(NEW.state_json->'admissions')='object' THEN NEW.state_json->'admissions' ELSE '{}'::jsonb END) AS admission
        WHERE COALESCE((admission.value->>'completed')::boolean,FALSE)=FALSE
    );
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS upstream_cache_guard_pending_sessions ON upstream_cache_guard_runtime;
CREATE TRIGGER upstream_cache_guard_pending_sessions BEFORE INSERT OR UPDATE OF state_json
ON upstream_cache_guard_runtime FOR EACH ROW EXECUTE FUNCTION upstream_cache_guard_pending_admissions();
UPDATE upstream_cache_guard_runtime SET state_json=state_json WHERE state_kind='session';
CREATE INDEX IF NOT EXISTS idx_cache_guard_runtime_pending_sessions
ON upstream_cache_guard_runtime(group_id,state_key) WHERE state_kind='session' AND has_pending_admissions=TRUE;
