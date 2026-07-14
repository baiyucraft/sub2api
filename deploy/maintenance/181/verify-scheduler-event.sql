\set ON_ERROR_STOP on

BEGIN;

CREATE TEMP TABLE scheduler_event_verification_input (
    pre_sync_max_id BIGINT NOT NULL,
    validation_event_id BIGINT NOT NULL
) ON COMMIT DROP;

INSERT INTO scheduler_event_verification_input (pre_sync_max_id, validation_event_id)
VALUES (:pre_sync_max_id, :validation_event_id);

DO $$
DECLARE
    pre_sync_max_id BIGINT;
    validation_event_id BIGINT;
    expected_ids JSONB;
    actual_ids JSONB;
BEGIN
    SELECT i.pre_sync_max_id, i.validation_event_id
      INTO pre_sync_max_id, validation_event_id
      FROM scheduler_event_verification_input i;
    IF validation_event_id <= pre_sync_max_id THEN
        RAISE EXCEPTION 'scheduler verification failed: validation event % did not advance pre-sync maximum %', validation_event_id, pre_sync_max_id;
    END IF;

    SELECT jsonb_agg(a.id ORDER BY a.id)
      INTO expected_ids
      FROM accounts a
      JOIN upstream_configs c ON c.id = a.upstream_config_id
     WHERE a.deleted_at IS NULL
       AND c.deleted_at IS NULL
       AND lower(trim(trailing '/' FROM c.site_url)) = 'https://www.codexapis.com'
       AND a.name IN ('刀哥-pro', '刀哥-plus')
       AND a.platform = 'openai'
       AND a.status = 'active'
       AND a.schedulable;

    SELECT o.payload->'account_ids'
      INTO actual_ids
      FROM scheduler_outbox o
     WHERE o.id = validation_event_id
       AND o.event_type = 'account_bulk_changed';

    IF expected_ids IS NULL OR jsonb_array_length(expected_ids) <> 2 OR actual_ids IS DISTINCT FROM expected_ids THEN
        RAISE EXCEPTION 'scheduler verification failed: validation event does not exactly target both Dao accounts';
    END IF;
END
$$;

SELECT 'scheduler_event_verified' AS status;

ROLLBACK;
