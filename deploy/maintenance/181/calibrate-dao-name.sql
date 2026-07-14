\set ON_ERROR_STOP on

BEGIN;

LOCK TABLE upstream_configs IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    target_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO target_count
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com';
    IF target_count <> 1 THEN
        RAISE EXCEPTION 'Dao calibration failed: expected exactly one config, found %', target_count;
    END IF;
END
$$;

WITH target AS (
    SELECT id
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com'
     FOR UPDATE
), updated AS (
    UPDATE upstream_configs c
       SET name = '刀哥', updated_at = NOW()
      FROM target
     WHERE c.id = target.id
       AND c.name IS DISTINCT FROM '刀哥'
    RETURNING c.id
)
SELECT COUNT(*) AS renamed_configs FROM updated;

COMMIT;

-- Account names are intentionally not updated here. Run the normal sync endpoint
-- for this config after migration; syncUpstreamAccount applies the shared
-- "upstream name-key name" rule and emits scheduler outbox events atomically.
