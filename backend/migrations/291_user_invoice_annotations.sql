-- 用户级开票标注仅记录站内金额，不参与充值、退款、消费或余额计算。
-- 不引用 users 外键，以便用户删除后仍保留登记人与审计身份。
CREATE TABLE IF NOT EXISTS user_invoice_annotations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    amount DECIMAL(20, 8) NOT NULL CHECK (amount > 0 AND amount < 1000000000000),
    invoice_date DATE NOT NULL,
    invoice_number VARCHAR(100) NOT NULL DEFAULT '',
    notes VARCHAR(1000) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'voided')),
    operation_id VARCHAR(128) NOT NULL CHECK (operation_id ~ '^[A-Za-z0-9_-]{8,128}$'),
    request_hash VARCHAR(64) NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    created_by BIGINT NOT NULL CHECK (created_by > 0),
    updated_by BIGINT NOT NULL CHECK (updated_by > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    void_reason VARCHAR(500) NOT NULL DEFAULT '',
    voided_by BIGINT,
    voided_at TIMESTAMPTZ,
    CONSTRAINT user_invoice_annotations_void_state CHECK (
        (status = 'active' AND void_reason = '' AND voided_by IS NULL AND voided_at IS NULL)
        OR (status = 'voided' AND char_length(void_reason) > 0 AND voided_by IS NOT NULL AND voided_by > 0 AND voided_at IS NOT NULL)
    ),
    CONSTRAINT user_invoice_annotations_operation_unique UNIQUE (user_id, created_by, operation_id)
);

CREATE INDEX IF NOT EXISTS idx_user_invoice_annotations_user_created
    ON user_invoice_annotations (user_id, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS user_invoice_annotation_events (
    id BIGSERIAL PRIMARY KEY,
    invoice_id BIGINT NOT NULL REFERENCES user_invoice_annotations(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL CHECK (user_id > 0),
    action VARCHAR(16) NOT NULL CHECK (action IN ('create', 'update', 'void')),
    actor_id BIGINT NOT NULL CHECK (actor_id > 0),
    before_value JSONB,
    after_value JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_invoice_annotation_events_before CHECK (
        (action = 'create' AND before_value IS NULL)
        OR (action IN ('update', 'void') AND before_value IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_user_invoice_annotation_events_invoice_created
    ON user_invoice_annotation_events (invoice_id, created_at DESC, id DESC);
