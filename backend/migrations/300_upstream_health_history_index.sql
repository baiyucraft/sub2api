-- Bound latest-history reads by key and preserve observed_at/id tie ordering.
-- Additive non-unique index: legacy readers and writers remain compatible.
CREATE INDEX IF NOT EXISTS idx_upstream_health_observations_key_recent
    ON upstream_health_observations (upstream_key_id, observed_at DESC, id DESC);
