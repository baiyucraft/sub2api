-- Keep the administrator's health-observation toggle outside the provider
-- synchronisation payload. The historical backfill is deliberately
-- fail-closed when the latest explicit administrator state is ambiguous.

ALTER TABLE upstream_keys
    ADD COLUMN IF NOT EXISTS observation_enabled BOOLEAN NOT NULL DEFAULT TRUE;

DO $$
DECLARE
    ambiguous_count BIGINT;
BEGIN
    WITH candidates AS (
        SELECT
            e.upstream_key_id,
            e.occurred_at,
            e.id,
            CASE e.payload->>'observation_enabled'
                WHEN 'true' THEN TRUE
                WHEN 'false' THEN FALSE
                ELSE NULL
            END AS enabled
        FROM upstream_events e
        WHERE e.upstream_key_id IS NOT NULL
          AND e.event_type = 'key_health_state_changed'
          AND e.source = 'health'
          AND e.payload ? 'observation_enabled'
          AND e.payload->>'observation_enabled' IN ('true', 'false')
          AND e.payload ? 'previous_observation_enabled'
          AND e.payload->>'previous_observation_enabled' IN ('true', 'false')
          AND e.payload->>'observation_enabled' IS DISTINCT FROM e.payload->>'previous_observation_enabled'
    ), latest_times AS (
        SELECT upstream_key_id, MAX(occurred_at) AS occurred_at
        FROM candidates
        GROUP BY upstream_key_id
    ), latest AS (
        SELECT c.upstream_key_id, c.enabled
        FROM candidates c
        JOIN latest_times t
          ON t.upstream_key_id = c.upstream_key_id
         AND t.occurred_at = c.occurred_at
    ), ambiguous AS (
        SELECT upstream_key_id
        FROM latest
        GROUP BY upstream_key_id
        HAVING COUNT(DISTINCT enabled) > 1
    )
    SELECT COUNT(*) INTO ambiguous_count FROM ambiguous;

    IF ambiguous_count > 0 THEN
        RAISE EXCEPTION
            'migration 240 cannot determine observation preference for % upstream key(s)',
            ambiguous_count
            USING ERRCODE = 'check_violation';
    END IF;
END
$$;

WITH candidates AS (
    SELECT
        e.upstream_key_id,
        e.occurred_at,
        e.id,
        CASE e.payload->>'observation_enabled'
            WHEN 'true' THEN TRUE
            WHEN 'false' THEN FALSE
            ELSE NULL
        END AS enabled
    FROM upstream_events e
    WHERE e.upstream_key_id IS NOT NULL
      AND e.event_type = 'key_health_state_changed'
      AND e.source = 'health'
      AND e.payload ? 'observation_enabled'
      AND e.payload->>'observation_enabled' IN ('true', 'false')
      AND e.payload ? 'previous_observation_enabled'
      AND e.payload->>'previous_observation_enabled' IN ('true', 'false')
      AND e.payload->>'observation_enabled' IS DISTINCT FROM e.payload->>'previous_observation_enabled'
), latest AS (
    SELECT DISTINCT ON (upstream_key_id)
        upstream_key_id,
        enabled
    FROM candidates
    ORDER BY upstream_key_id, occurred_at DESC, id DESC
)
UPDATE upstream_keys k
SET observation_enabled = latest.enabled
FROM latest
WHERE latest.upstream_key_id = k.id;

COMMENT ON COLUMN upstream_keys.observation_enabled IS
    'Administrator preference for scheduled upstream health observation; provider sync must preserve it.';

CREATE INDEX IF NOT EXISTS idx_upstream_keys_observation_enabled
    ON upstream_keys(observation_enabled)
    WHERE deleted_at IS NULL;
