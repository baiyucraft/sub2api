CREATE TABLE IF NOT EXISTS proxy_ip_groups (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    per_ip_concurrency INTEGER NOT NULL DEFAULT 10,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	deleted_at TIMESTAMPTZ,
    CONSTRAINT proxy_ip_groups_name_not_blank CHECK (BTRIM(name) <> ''),
	CONSTRAINT proxy_ip_groups_per_ip_concurrency_range CHECK (per_ip_concurrency BETWEEN 1 AND 1000)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_proxy_ip_groups_name_active
    ON proxy_ip_groups(name)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS proxy_ip_group_members (
    proxy_ip_group_id BIGINT NOT NULL REFERENCES proxy_ip_groups(id) ON DELETE CASCADE,
    proxy_id BIGINT NOT NULL REFERENCES proxies(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (proxy_ip_group_id, proxy_id),
    CONSTRAINT proxy_ip_group_members_position_non_negative CHECK (position >= 0)
);

CREATE INDEX IF NOT EXISTS idx_proxy_ip_group_members_proxy_id
    ON proxy_ip_group_members(proxy_id);
CREATE INDEX IF NOT EXISTS idx_proxy_ip_group_members_group_position
    ON proxy_ip_group_members(proxy_ip_group_id, position, proxy_id);

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS proxy_ip_group_id BIGINT;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_proxy_ip_group_id_fkey;
ALTER TABLE accounts
    ADD CONSTRAINT accounts_proxy_ip_group_id_fkey
        FOREIGN KEY (proxy_ip_group_id) REFERENCES proxy_ip_groups(id) ON DELETE RESTRICT;

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_proxy_source_exclusive;
ALTER TABLE accounts
    ADD CONSTRAINT accounts_proxy_source_exclusive
        CHECK (proxy_id IS NULL OR proxy_ip_group_id IS NULL);

CREATE INDEX IF NOT EXISTS idx_accounts_proxy_ip_group_id
    ON accounts(proxy_ip_group_id)
    WHERE proxy_ip_group_id IS NOT NULL;

COMMENT ON TABLE proxy_ip_groups IS
    'Named proxy pools for OpenAI OAuth and setup-token accounts.';
COMMENT ON COLUMN proxy_ip_groups.per_ip_concurrency IS
    'Maximum account concurrency assigned to each member proxy.';
COMMENT ON COLUMN accounts.proxy_ip_group_id IS
    'Optional proxy IP group binding; mutually exclusive with proxy_id.';
