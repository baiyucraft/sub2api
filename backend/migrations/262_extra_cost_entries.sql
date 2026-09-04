-- 管理员维护的额外成本审计流水（金额沿用账号成本的美元口径）。
CREATE TABLE IF NOT EXISTS extra_cost_entries (
    id BIGSERIAL PRIMARY KEY,
    cost_date DATE NOT NULL,
    amount NUMERIC(20, 8) NOT NULL,
    category VARCHAR(32) NOT NULL,
    notes VARCHAR(500) NOT NULL DEFAULT '',
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reversal_of BIGINT REFERENCES extra_cost_entries(id) ON DELETE RESTRICT,
    idempotency_key VARCHAR(128),
    rule_version VARCHAR(64) NOT NULL DEFAULT 'extra-cost-v1'
);

CREATE INDEX IF NOT EXISTS extra_cost_entries_cost_date_idx ON extra_cost_entries (cost_date);
CREATE INDEX IF NOT EXISTS extra_cost_entries_category_idx ON extra_cost_entries (category);
CREATE UNIQUE INDEX IF NOT EXISTS extra_cost_entries_idempotency_key_uq
    ON extra_cost_entries (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
