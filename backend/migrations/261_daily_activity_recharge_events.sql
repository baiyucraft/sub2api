-- 261_daily_activity_recharge_events.sql
-- 记录无法从既有订单/兑换码可靠还原的每日活动充值来源。
-- 当前仅管理员 add 余额使用；set/subtract 不写入，避免把调账误算为充值。
CREATE TABLE IF NOT EXISTS activity_recharge_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL,
    source_key VARCHAR(64) NOT NULL,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(source_type, source_key)
);

CREATE INDEX IF NOT EXISTS idx_activity_recharge_events_user_time
    ON activity_recharge_events(user_id, occurred_at);
