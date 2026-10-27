-- Keep cross-platform fingerprint series and probe rotation outside account
-- health JSON so concurrent health writes cannot overwrite leases or samples.
CREATE TABLE IF NOT EXISTS upstream_confidence_fingerprint_states (
    upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    state_json JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
