-- Durable single-request sampling and a cross-instance lease are kept outside
-- upstream_keys.extra so concurrent health observation updates cannot erase
-- a consumed attempt or its fixed 128-question cycle.
CREATE TABLE IF NOT EXISTS upstream_confidence_distribution_states (
    upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    state_json JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
