-- Model-level provider capability routes for a single upstream key.
-- A key still maps to one physical account; these rows only describe the
-- public model, concrete provider and optional protocol used for dispatch.
CREATE TABLE IF NOT EXISTS upstream_key_model_routes (
    id BIGSERIAL PRIMARY KEY,
    upstream_key_id BIGINT NOT NULL REFERENCES upstream_keys(id) ON DELETE CASCADE,
    public_model VARCHAR(200) NOT NULL,
    upstream_model VARCHAR(200) NOT NULL DEFAULT '',
    target_platform VARCHAR(50) NOT NULL DEFAULT '',
    api_protocol VARCHAR(50) NOT NULL DEFAULT '',
    source VARCHAR(16) NOT NULL DEFAULT 'auto',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INTEGER NOT NULL DEFAULT 100,
    status VARCHAR(20) NOT NULL DEFAULT 'available',
    last_seen_at TIMESTAMPTZ NULL,
    last_error TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS upstream_key_model_routes_key_model_active_idx
    ON upstream_key_model_routes (upstream_key_id, public_model)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS upstream_key_model_routes_key_enabled_idx
    ON upstream_key_model_routes (upstream_key_id, enabled);

CREATE INDEX IF NOT EXISTS upstream_key_model_routes_key_platform_enabled_idx
    ON upstream_key_model_routes (upstream_key_id, target_platform, enabled);

CREATE INDEX IF NOT EXISTS upstream_key_model_routes_status_idx
    ON upstream_key_model_routes (status);
