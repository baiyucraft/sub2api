\set ON_ERROR_STOP on

BEGIN;

WITH target AS (
    SELECT id
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com'
     FOR UPDATE
), guard AS (
    SELECT CASE WHEN COUNT(*) = 1 THEN 1 ELSE 1 / 0 END AS ok
      FROM target
), updated AS (
    UPDATE upstream_configs c
       SET name = '刀哥', updated_at = NOW()
      FROM target, guard
     WHERE c.id = target.id
       AND c.name IS DISTINCT FROM '刀哥'
    RETURNING c.id
)
SELECT COUNT(*) AS renamed_configs FROM updated;

COMMIT;

-- Account names are intentionally not updated here. Run the normal sync endpoint
-- for this config after migration; syncUpstreamAccount applies the shared
-- "upstream name-key name" rule and emits scheduler outbox events atomically.
