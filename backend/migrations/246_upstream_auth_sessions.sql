-- Persisted, encrypted authentication sessions for upstream configurations.
-- The upstream config entity is soft-deleted by Ent, so the application also
-- removes the row in the same transaction when a config is deleted.
CREATE TABLE IF NOT EXISTS upstream_auth_sessions (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL UNIQUE REFERENCES upstream_configs(id) ON DELETE CASCADE,
    provider VARCHAR(32) NOT NULL,
    auth_mode VARCHAR(32) NOT NULL,
    credential_fingerprint VARCHAR(64) NOT NULL,
    secret_ciphertext TEXT NOT NULL,
    expires_at TIMESTAMPTZ NULL,
    last_authenticated_at TIMESTAMPTZ NULL,
    last_refreshed_at TIMESTAMPTZ NULL,
    last_used_at TIMESTAMPTZ NULL,
    cooldown_until TIMESTAMPTZ NULL,
    consecutive_auth_failures INTEGER NOT NULL DEFAULT 0,
    last_error_category VARCHAR(64) NULL,
    last_error_at TIMESTAMPTZ NULL,
    login_count BIGINT NOT NULL DEFAULT 0,
    reuse_count BIGINT NOT NULL DEFAULT 0,
    refresh_count BIGINT NOT NULL DEFAULT 0,
    relogin_count BIGINT NOT NULL DEFAULT 0,
    cooldown_count BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_upstream_auth_sessions_cooldown_until
    ON upstream_auth_sessions(cooldown_until);
CREATE INDEX IF NOT EXISTS idx_upstream_auth_sessions_provider
    ON upstream_auth_sessions(provider);

COMMENT ON TABLE upstream_auth_sessions IS
    'Encrypted, provider-neutral upstream authentication sessions; never expose secret_ciphertext.';
