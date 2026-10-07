-- 活动成本来源为不可变审计快照；不设置外键，避免删除用户或奖励时丢失成本。
ALTER TABLE extra_cost_entries
    ADD COLUMN IF NOT EXISTS activity_reward_id BIGINT,
    ADD COLUMN IF NOT EXISTS related_user_id BIGINT,
    ADD COLUMN IF NOT EXISTS activity_type VARCHAR(32);

CREATE UNIQUE INDEX IF NOT EXISTS extra_cost_entries_activity_reward_uq
    ON extra_cost_entries (activity_reward_id);

-- 在同一事务内补齐并校验；活动表的共享锁提供固定事实集合，
-- 防止并发旧实例发奖恰好落在补录与校验之间。发布完成后可再次调用。
CREATE OR REPLACE FUNCTION backfill_activity_reward_costs() RETURNS BIGINT
LANGUAGE plpgsql AS $$
DECLARE
    inserted_count BIGINT;
BEGIN
    LOCK TABLE activity_reward_records IN SHARE MODE;
    INSERT INTO extra_cost_entries
        (cost_date, amount, category, notes, created_by, created_at, reversal_of,
         idempotency_key, rule_version, activity_reward_id, related_user_id, activity_type)
    SELECT (r.created_at AT TIME ZONE 'Asia/Shanghai')::date, r.amount,
           'activity_reward', '', NULL, r.created_at, NULL,
           'activity-reward:' || r.id::text, 'activity-reward-cost-v1', r.id, r.user_id, r.activity_type
    FROM activity_reward_records r
    WHERE r.status = 'credited'
    ORDER BY r.id
    ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS inserted_count = ROW_COUNT;

    IF EXISTS (
        SELECT 1
        FROM activity_reward_records r
        LEFT JOIN extra_cost_entries c ON c.activity_reward_id = r.id
        WHERE r.status = 'credited' AND (
            c.id IS NULL OR c.amount IS DISTINCT FROM r.amount
            OR c.related_user_id IS DISTINCT FROM r.user_id
            OR c.activity_type IS DISTINCT FROM r.activity_type
            OR c.cost_date IS DISTINCT FROM (r.created_at AT TIME ZONE 'Asia/Shanghai')::date
            OR c.created_at IS DISTINCT FROM r.created_at
            OR c.category IS DISTINCT FROM 'activity_reward'
            OR c.rule_version IS DISTINCT FROM 'activity-reward-cost-v1'
            OR c.idempotency_key IS DISTINCT FROM ('activity-reward:' || r.id::text)
            OR c.created_by IS NOT NULL OR c.reversal_of IS NOT NULL
        )
    ) THEN
        RAISE EXCEPTION 'activity reward cost audit snapshot conflict';
    END IF;
    RETURN inserted_count;
END;
$$;

SELECT backfill_activity_reward_costs();
