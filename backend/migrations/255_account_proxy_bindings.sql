-- Store ordered proxy routes per account while preserving accounts.proxy_id as
-- the compatibility pointer to the first route.
CREATE TABLE IF NOT EXISTS account_proxy_bindings (
    account_id BIGINT NOT NULL,
    proxy_id BIGINT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, proxy_id),
    CONSTRAINT account_proxy_bindings_account_fk
        FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE,
    CONSTRAINT account_proxy_bindings_proxy_fk
        FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS account_proxy_bindings_account_position_idx
    ON account_proxy_bindings (account_id, position);

CREATE INDEX IF NOT EXISTS account_proxy_bindings_proxy_id_idx
    ON account_proxy_bindings (proxy_id);

INSERT INTO account_proxy_bindings (account_id, proxy_id, position)
SELECT a.id, a.proxy_id, 0
FROM accounts a
JOIN proxies p ON p.id = a.proxy_id
WHERE a.proxy_id IS NOT NULL
ON CONFLICT (account_id, proxy_id) DO NOTHING;
