-- Shared upstream relay configurations and key bindings.

CREATE TABLE IF NOT EXISTS upstream_configs (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    provider VARCHAR(32) NOT NULL,
    base_url VARCHAR(512) NOT NULL,
    auth_mode VARCHAR(32) NOT NULL DEFAULT 'user_login',
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
    extra JSONB NOT NULL DEFAULT '{}'::jsonb,
    proxy_id BIGINT REFERENCES proxies(id) ON DELETE SET NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    last_error TEXT,
    last_checked_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_upstream_configs_provider ON upstream_configs(provider);
CREATE INDEX IF NOT EXISTS idx_upstream_configs_proxy_id ON upstream_configs(proxy_id);
CREATE INDEX IF NOT EXISTS idx_upstream_configs_deleted_at ON upstream_configs(deleted_at);

CREATE TABLE IF NOT EXISTS upstream_keys (
    id BIGSERIAL PRIMARY KEY,
    upstream_config_id BIGINT NOT NULL REFERENCES upstream_configs(id) ON DELETE CASCADE,
    name VARCHAR(100) NOT NULL DEFAULT '',
    key TEXT NOT NULL,
    key_hash VARCHAR(128) NOT NULL,
    remote_key_id BIGINT,
    upstream_group_id BIGINT,
    upstream_group_name VARCHAR(100) NOT NULL DEFAULT '',
    platform VARCHAR(50) NOT NULL DEFAULT 'openai',
    rate_multiplier DECIMAL(10,4),
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    last_seen_at TIMESTAMPTZ,
    extra JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_upstream_keys_config_id ON upstream_keys(upstream_config_id);
CREATE INDEX IF NOT EXISTS idx_upstream_keys_deleted_at ON upstream_keys(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_keys_config_key_hash_active
    ON upstream_keys(upstream_config_id, key_hash)
    WHERE deleted_at IS NULL;

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS upstream_config_id BIGINT REFERENCES upstream_configs(id) ON DELETE SET NULL;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS upstream_key_id BIGINT REFERENCES upstream_keys(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_accounts_upstream_config_id ON accounts(upstream_config_id);
CREATE INDEX IF NOT EXISTS idx_accounts_upstream_key_id ON accounts(upstream_key_id);

WITH source AS (
    SELECT
        a.id AS account_id,
        a.name,
        COALESCE(NULLIF(TRIM(a.credentials->>'base_url'), ''), '') AS base_url,
        a.proxy_id,
        COALESCE(NULLIF(TRIM(a.extra->>'sub2api_rate_sync_adapter'), ''), 'user_login') AS auth_mode,
        jsonb_strip_nulls(jsonb_build_object(
            'sub2api_login_email', NULLIF(a.credentials->>'sub2api_login_email', ''),
            'sub2api_login_password', NULLIF(a.credentials->>'sub2api_login_password', ''),
            'sub2api_access_token', NULLIF(a.credentials->>'sub2api_access_token', ''),
            'sub2api_refresh_token', NULLIF(a.credentials->>'sub2api_refresh_token', '')
        )) AS credentials,
        NULLIF(a.credentials->>'api_key', '') AS api_key
    FROM accounts a
    WHERE a.deleted_at IS NULL
      AND a.type = 'apikey'
      AND LOWER(COALESCE(a.extra->>'upstream_provider', '')) = 'sub2api'
      AND a.upstream_config_id IS NULL
),
config_source AS (
    SELECT DISTINCT ON (base_url, COALESCE(proxy_id, 0), auth_mode, credentials::text)
        base_url,
        proxy_id,
        auth_mode,
        credentials,
        'Sub2API ' || ROW_NUMBER() OVER (ORDER BY base_url, COALESCE(proxy_id, 0), auth_mode) AS generated_name
    FROM source
    WHERE base_url <> ''
),
inserted_configs AS (
    INSERT INTO upstream_configs (name, provider, base_url, proxy_id, auth_mode, credentials, status, created_at, updated_at)
    SELECT generated_name, 'sub2api', base_url, proxy_id, auth_mode, credentials, 'active', NOW(), NOW()
    FROM config_source
    RETURNING id, base_url, proxy_id, auth_mode, credentials
),
matched AS (
    SELECT s.*, c.id AS upstream_config_id
    FROM source s
    JOIN inserted_configs c
      ON c.base_url = s.base_url
     AND COALESCE(c.proxy_id, 0) = COALESCE(s.proxy_id, 0)
     AND c.auth_mode = s.auth_mode
     AND c.credentials::text = s.credentials::text
)
INSERT INTO upstream_keys (upstream_config_id, name, key, key_hash, platform, status, created_at, updated_at)
SELECT
    upstream_config_id,
    name,
    api_key,
    md5(api_key),
    'openai',
    'active',
    NOW(),
    NOW()
FROM matched
WHERE api_key IS NOT NULL
ON CONFLICT DO NOTHING;

WITH source AS (
    SELECT
        a.id AS account_id,
        COALESCE(NULLIF(TRIM(a.credentials->>'base_url'), ''), '') AS base_url,
        a.proxy_id,
        COALESCE(NULLIF(TRIM(a.extra->>'sub2api_rate_sync_adapter'), ''), 'user_login') AS auth_mode,
        jsonb_strip_nulls(jsonb_build_object(
            'sub2api_login_email', NULLIF(a.credentials->>'sub2api_login_email', ''),
            'sub2api_login_password', NULLIF(a.credentials->>'sub2api_login_password', ''),
            'sub2api_access_token', NULLIF(a.credentials->>'sub2api_access_token', ''),
            'sub2api_refresh_token', NULLIF(a.credentials->>'sub2api_refresh_token', '')
        )) AS credentials,
        NULLIF(a.credentials->>'api_key', '') AS api_key
    FROM accounts a
    WHERE a.deleted_at IS NULL
      AND a.type = 'apikey'
      AND LOWER(COALESCE(a.extra->>'upstream_provider', '')) = 'sub2api'
      AND a.upstream_config_id IS NULL
)
UPDATE accounts a
SET
    upstream_config_id = c.id,
    upstream_key_id = uk.id,
    updated_at = NOW()
FROM source s
JOIN upstream_configs c
  ON c.provider = 'sub2api'
 AND c.deleted_at IS NULL
 AND c.base_url = s.base_url
 AND COALESCE(c.proxy_id, 0) = COALESCE(s.proxy_id, 0)
 AND c.auth_mode = s.auth_mode
 AND c.credentials::text = s.credentials::text
LEFT JOIN upstream_keys uk
  ON uk.upstream_config_id = c.id
 AND uk.deleted_at IS NULL
 AND uk.key_hash = md5(s.api_key)
WHERE a.id = s.account_id;
