\set ON_ERROR_STOP on

BEGIN;

WITH dao_accounts AS (
    SELECT a.id
      FROM accounts a
      JOIN upstream_configs c ON c.id = a.upstream_config_id
     WHERE a.deleted_at IS NULL
       AND c.deleted_at IS NULL
       AND lower(trim(trailing '/' FROM c.site_url)) = 'https://www.codexapis.com'
       AND a.name IN ('刀哥-pro', '刀哥-plus')
       AND a.platform = 'openai'
       AND a.status = 'active'
       AND a.schedulable
), guard AS (
    SELECT CASE WHEN COUNT(*) = 2 THEN 1 ELSE 1 / 0 END AS ok
      FROM dao_accounts
), inserted AS (
    INSERT INTO scheduler_outbox (event_type, payload)
    SELECT 'account_bulk_changed',
           jsonb_build_object('account_ids', (SELECT jsonb_agg(id ORDER BY id) FROM dao_accounts))
      FROM guard
    RETURNING id
)
SELECT id AS scheduler_validation_event_id FROM inserted;

COMMIT;
