-- 用户默认分组倍率仍使用百分比；NULL 不改变既有分组倍率语义，0 为明确免费。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS default_group_rate_percent DECIMAL(20,10) NULL;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_default_group_rate_percent_nonnegative_finite;

ALTER TABLE users
    ADD CONSTRAINT users_default_group_rate_percent_nonnegative_finite
    CHECK (
        default_group_rate_percent IS NULL OR (
            default_group_rate_percent >= 0
            AND default_group_rate_percent NOT IN ('NaN'::numeric, 'Infinity'::numeric, '-Infinity'::numeric)
        )
    );

-- 独立于旧认证失效消息，避免旧实例消费不认识的用户倍率事件。
-- 全局 identity 序列不随 ack 删除而重置，防止旧 revision 确认误删新事件（ABA）。
CREATE TABLE IF NOT EXISTS user_group_rate_invalidation_outbox (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    revision BIGINT GENERATED ALWAYS AS IDENTITY NOT NULL CHECK (revision > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION enqueue_user_group_rate_invalidation(p_user_id BIGINT)
RETURNS VOID AS $$
    INSERT INTO user_group_rate_invalidation_outbox (user_id)
    SELECT id FROM users WHERE id = p_user_id
    ON CONFLICT (user_id) DO UPDATE
    SET revision = DEFAULT,
        updated_at = NOW();
$$ LANGUAGE SQL;

CREATE OR REPLACE FUNCTION invalidate_user_default_group_rate()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.default_group_rate_percent IS NOT NULL THEN
            PERFORM enqueue_user_group_rate_invalidation(NEW.id);
        END IF;
    ELSIF NEW.default_group_rate_percent IS DISTINCT FROM OLD.default_group_rate_percent THEN
        PERFORM enqueue_user_group_rate_invalidation(NEW.id);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_invalidate_user_default_group_rate ON users;
CREATE TRIGGER trg_invalidate_user_default_group_rate
AFTER INSERT OR UPDATE OF default_group_rate_percent ON users
FOR EACH ROW
EXECUTE FUNCTION invalidate_user_default_group_rate();

CREATE OR REPLACE FUNCTION invalidate_user_group_rate_configuration()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.rate_percent IS NOT NULL OR OLD.rate_multiplier IS NOT NULL THEN
            PERFORM enqueue_user_group_rate_invalidation(OLD.user_id);
        END IF;
        RETURN OLD;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.rate_percent IS NOT NULL OR NEW.rate_multiplier IS NOT NULL THEN
            PERFORM enqueue_user_group_rate_invalidation(NEW.user_id);
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.user_id IS DISTINCT FROM OLD.user_id
        OR NEW.group_id IS DISTINCT FROM OLD.group_id
        OR NEW.rate_percent IS DISTINCT FROM OLD.rate_percent
        OR NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier THEN
        IF OLD.rate_percent IS NOT NULL OR OLD.rate_multiplier IS NOT NULL THEN
            PERFORM enqueue_user_group_rate_invalidation(OLD.user_id);
        END IF;
        IF (NEW.rate_percent IS NOT NULL OR NEW.rate_multiplier IS NOT NULL)
            AND (NEW.user_id IS DISTINCT FROM OLD.user_id
                OR (OLD.rate_percent IS NULL AND OLD.rate_multiplier IS NULL)) THEN
            PERFORM enqueue_user_group_rate_invalidation(NEW.user_id);
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_invalidate_user_group_rate_configuration ON user_group_rate_multipliers;
CREATE TRIGGER trg_invalidate_user_group_rate_configuration
AFTER INSERT OR UPDATE OF user_id, group_id, rate_percent, rate_multiplier OR DELETE
ON user_group_rate_multipliers
FOR EACH ROW
EXECUTE FUNCTION invalidate_user_group_rate_configuration();

COMMENT ON COLUMN users.default_group_rate_percent IS
    '用户默认分组倍率百分比；单组覆盖优先，NULL 无用户默认，0 为明确免费。';
COMMENT ON TABLE user_group_rate_invalidation_outbox IS
    '用户倍率事务性失效账本；专用topic发布成功后仅按user_id与revision一致时删除。';
COMMENT ON COLUMN user_group_rate_invalidation_outbox.revision IS
    '全局序列生成，事件删除重建不复用；阻止多实例旧ack误删新事件。';
