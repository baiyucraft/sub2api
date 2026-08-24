DO $$
DECLARE
    old_confidence_rows BIGINT;
BEGIN
    SELECT COUNT(*) INTO old_confidence_rows
    FROM upstream_health_observations
    WHERE confidence_prompt_version IS DISTINCT FROM 'openai-juice-high-v1'
      AND (
        confidence_score IS NOT NULL OR confidence_prompt_version IS NOT NULL
        OR requested_effort IS NOT NULL OR reasoning_tokens IS NOT NULL
        OR (confidence_checks IS NOT NULL AND confidence_checks NOT IN ('null'::jsonb, '{}'::jsonb))
        OR confidence_status IS NOT NULL
      );
    RAISE NOTICE 'upstream confidence reset before: old_confidence_rows=%', old_confidence_rows;
END $$;

UPDATE upstream_health_observations
SET confidence_score = NULL,
    confidence_prompt_version = NULL,
    requested_effort = NULL,
    reasoning_tokens = NULL,
    confidence_checks = NULL,
    confidence_status = NULL
WHERE confidence_prompt_version IS DISTINCT FROM 'openai-juice-high-v1'
  AND (
    confidence_score IS NOT NULL OR confidence_prompt_version IS NOT NULL
    OR requested_effort IS NOT NULL OR reasoning_tokens IS NOT NULL
    OR (confidence_checks IS NOT NULL AND confidence_checks NOT IN ('null'::jsonb, '{}'::jsonb))
    OR confidence_status IS NOT NULL
  );

DO $$
DECLARE
    old_confidence_rows BIGINT;
BEGIN
    SELECT COUNT(*) INTO old_confidence_rows
    FROM upstream_health_observations
    WHERE confidence_prompt_version IS DISTINCT FROM 'openai-juice-high-v1'
      AND (
        confidence_score IS NOT NULL OR confidence_prompt_version IS NOT NULL
        OR requested_effort IS NOT NULL OR reasoning_tokens IS NOT NULL
        OR (confidence_checks IS NOT NULL AND confidence_checks NOT IN ('null'::jsonb, '{}'::jsonb))
        OR confidence_status IS NOT NULL
      );
    RAISE NOTICE 'upstream confidence reset after: old_confidence_rows=%', old_confidence_rows;
END $$;
