\set ON_ERROR_STOP on

BEGIN;

CREATE TEMP TABLE scheduler_outbox_verification_input (
    validation_event_id BIGINT NOT NULL,
    redis_watermark BIGINT NOT NULL
) ON COMMIT DROP;

INSERT INTO scheduler_outbox_verification_input (validation_event_id, redis_watermark)
VALUES (:validation_event_id, :redis_watermark);

DO $$
DECLARE
    validation_event_id BIGINT;
    redis_watermark BIGINT;
BEGIN
    SELECT i.validation_event_id, i.redis_watermark
      INTO validation_event_id, redis_watermark
      FROM scheduler_outbox_verification_input i;
    IF redis_watermark < validation_event_id THEN
        RAISE EXCEPTION 'scheduler verification failed: Redis watermark % is behind validation event %', redis_watermark, validation_event_id;
    END IF;
END
$$;

SELECT 'scheduler_outbox_verified' AS status;

ROLLBACK;
