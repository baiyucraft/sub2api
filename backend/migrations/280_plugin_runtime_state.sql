ALTER TABLE sub2api_plugin_installations
    ADD COLUMN IF NOT EXISTS config_revision BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS managed_scope JSONB NULL;

COMMENT ON COLUMN sub2api_plugin_installations.managed_scope IS
    'NULL preserves legacy full scope; [] means no managed targets for a new plugin.';

ALTER TABLE sub2api_plugin_installations
    DROP CONSTRAINT IF EXISTS sub2api_plugin_installations_state_check;
ALTER TABLE sub2api_plugin_installations
    ADD CONSTRAINT sub2api_plugin_installations_state_check
    CHECK (state IN ('disabled', 'starting', 'enabled', 'error', 'incompatible', 'upgrading'));

-- Host-private crash recovery. The snapshot is the complete database row;
-- config remains ciphertext and bytea artifacts use PostgreSQL JSON encoding.
-- Expiration permits recovery, never deletion of the snapshot without restore.
CREATE TABLE IF NOT EXISTS sub2api_plugin_maintenance (
    plugin_id BIGINT PRIMARY KEY REFERENCES sub2api_plugin_installations(id) ON DELETE CASCADE,
    owner_token VARCHAR(128) NOT NULL CHECK (octet_length(owner_token) BETWEEN 32 AND 128),
    previous_installation JSONB NOT NULL CHECK (jsonb_typeof(previous_installation) = 'object'),
    installed_sha256 VARCHAR(64) NOT NULL,
    installed_config_revision BIGINT NOT NULL CHECK (installed_config_revision >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX IF NOT EXISTS idx_sub2api_plugin_maintenance_expiry
    ON sub2api_plugin_maintenance(expires_at);

-- Host-private request registrations have no expiry or automatic cleanup.
-- Upgrade timeout must roll back, never infer completion from record age.
CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_requests (
    plugin_id BIGINT NOT NULL REFERENCES sub2api_plugin_installations(id) ON DELETE CASCADE,
    request_id VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (plugin_id, request_id)
);

-- Tombstones retain revisions so delete/recreate cannot reset CAS versions.
CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_state (
    plugin_key VARCHAR(160) NOT NULL REFERENCES sub2api_plugin_installations(plugin_key) ON DELETE CASCADE,
    namespace VARCHAR(128) COLLATE "C" NOT NULL,
    key VARCHAR(512) COLLATE "C" NOT NULL,
    value_encrypted TEXT,
    version BIGINT NOT NULL CHECK (version > 0),
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (plugin_key, namespace, key),
    CHECK ((deleted AND value_encrypted IS NULL) OR (NOT deleted AND value_encrypted IS NOT NULL))
);

-- Inactive rows remain as the per-slot mutex and persistent fencing counter.
-- Fence zero represents a slot that has never had an acquired lease.
CREATE TABLE IF NOT EXISTS sub2api_plugin_runtime_leases (
    plugin_key VARCHAR(160) NOT NULL REFERENCES sub2api_plugin_installations(plugin_key) ON DELETE CASCADE,
    namespace VARCHAR(128) COLLATE "C" NOT NULL,
    key VARCHAR(512) COLLATE "C" NOT NULL,
    owner_token TEXT NOT NULL DEFAULT '',
    fence BIGINT NOT NULL DEFAULT 0 CHECK (fence >= 0),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT '-infinity',
    PRIMARY KEY (plugin_key, namespace, key)
);
