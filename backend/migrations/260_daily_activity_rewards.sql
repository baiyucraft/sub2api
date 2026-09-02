-- 260_daily_activity_rewards.sql
-- 每日活动独立奖励、抽奖机会和邀请达标里程碑。所有金额以元 Decimal 保存，奖励由服务端结算。
CREATE TABLE IF NOT EXISTS activity_reward_records (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    activity_type VARCHAR(32) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    period_date DATE NULL,
    source VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(20) NOT NULL DEFAULT 'credited',
    idempotency_key_hash VARCHAR(64) NULL,
    rule_version VARCHAR(32) NOT NULL DEFAULT 'v1',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_activity_reward_daily_gift_once
    ON activity_reward_records(user_id, activity_type, period_date)
    WHERE activity_type = 'daily_gift' AND status <> 'failed';
CREATE UNIQUE INDEX IF NOT EXISTS idx_activity_reward_idempotency
    ON activity_reward_records(user_id, idempotency_key_hash)
    WHERE idempotency_key_hash IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_activity_reward_user_created
    ON activity_reward_records(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS activity_draw_credits (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    activity_type VARCHAR(32) NOT NULL,
    credit_index BIGINT NOT NULL,
    source VARCHAR(64) NOT NULL DEFAULT '',
    consumed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, activity_type, credit_index)
);
CREATE INDEX IF NOT EXISTS idx_activity_draw_credits_available
    ON activity_draw_credits(user_id, activity_type, consumed_at, credit_index);

CREATE TABLE IF NOT EXISTS activity_invitation_milestones (
    id BIGSERIAL PRIMARY KEY,
    inviter_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invitee_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    qualifying_amount DECIMAL(20,8) NOT NULL,
    qualifying_order_id BIGINT NULL,
    qualified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(inviter_id, invitee_id)
);
CREATE INDEX IF NOT EXISTS idx_activity_invitation_milestones_inviter
    ON activity_invitation_milestones(inviter_id, id);

CREATE TABLE IF NOT EXISTS activity_draw_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    activity_type VARCHAR(32) NOT NULL,
    idempotency_key_hash VARCHAR(64) NOT NULL,
    count INTEGER NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(user_id, activity_type, idempotency_key_hash)
);

-- 固定首次启用边界，避免升级后扫描活动上线前的历史充值和消费补发机会。
INSERT INTO settings(key, value, updated_at)
VALUES ('daily_activity_started_at', EXTRACT(EPOCH FROM NOW())::TEXT, NOW())
ON CONFLICT (key) DO NOTHING;
