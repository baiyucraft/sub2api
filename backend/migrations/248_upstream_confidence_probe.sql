ALTER TABLE upstream_health_observations
    ADD COLUMN IF NOT EXISTS confidence_score INTEGER NULL,
    ADD COLUMN IF NOT EXISTS confidence_prompt_version VARCHAR(64) NULL,
    ADD COLUMN IF NOT EXISTS requested_effort VARCHAR(32) NULL,
    ADD COLUMN IF NOT EXISTS reasoning_tokens BIGINT NULL,
    ADD COLUMN IF NOT EXISTS confidence_checks JSONB NULL,
    ADD COLUMN IF NOT EXISTS confidence_status VARCHAR(32) NULL;

CREATE INDEX IF NOT EXISTS idx_upstream_health_confidence_window
    ON upstream_health_observations(upstream_key_id, observed_at)
    WHERE confidence_score IS NOT NULL;
