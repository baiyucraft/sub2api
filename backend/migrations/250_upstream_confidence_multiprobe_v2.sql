ALTER TABLE upstream_health_observations
    ADD COLUMN IF NOT EXISTS confidence_evidence JSONB NULL;

CREATE INDEX IF NOT EXISTS idx_upstream_health_confidence_v2_window
    ON upstream_health_observations(upstream_key_id, observed_at)
    WHERE confidence_prompt_version = 'openai-juice-multiprobe-v2';
