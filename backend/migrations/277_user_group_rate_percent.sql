-- 用户专属分组倍率以百分比为持久化真值。
-- rate_multiplier 保留为兼容影子列，便于旧客户端和旧二进制继续读写。
ALTER TABLE user_group_rate_multipliers
    ADD COLUMN IF NOT EXISTS rate_percent DECIMAL(20,10) NULL;

ALTER TABLE user_group_rate_multipliers
    DROP CONSTRAINT IF EXISTS user_group_rate_multipliers_rate_percent_nonnegative;

ALTER TABLE user_group_rate_multipliers
    ADD CONSTRAINT user_group_rate_multipliers_rate_percent_nonnegative
    CHECK (rate_percent IS NULL OR rate_percent >= 0);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM user_group_rate_multipliers AS ugr
        JOIN groups AS g ON g.id = ugr.group_id
        WHERE ugr.rate_multiplier IS NOT NULL
          AND ugr.rate_percent IS NULL
          AND g.rate_multiplier <= 0
    ) THEN
        RAISE EXCEPTION 'cannot migrate user group rates: groups with non-positive rate_multiplier have legacy overrides';
    END IF;
END;
$$;

UPDATE user_group_rate_multipliers AS ugr
SET rate_percent = ROUND((ugr.rate_multiplier / g.rate_multiplier) * 100.0, 10)
FROM groups AS g
WHERE g.id = ugr.group_id
  AND ugr.rate_multiplier IS NOT NULL
  AND ugr.rate_percent IS NULL
  AND g.rate_multiplier > 0;

CREATE OR REPLACE FUNCTION sync_user_group_rate_percent_fields()
RETURNS TRIGGER AS $$
DECLARE
    group_rate DECIMAL(20,10);
BEGIN
    SELECT rate_multiplier::DECIMAL(20,10)
    INTO group_rate
    FROM groups
    WHERE id = NEW.group_id;

    IF group_rate IS NULL OR group_rate <= 0 THEN
        RAISE EXCEPTION 'group % has invalid rate_multiplier', NEW.group_id;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF NEW.rate_percent IS NOT NULL THEN
            NEW.rate_multiplier := ROUND(group_rate * NEW.rate_percent / 100.0, 10);
        ELSIF NEW.rate_multiplier IS NOT NULL THEN
            NEW.rate_percent := ROUND(NEW.rate_multiplier / group_rate * 100.0, 10);
        END IF;
        RETURN NEW;
    END IF;

    -- The groups trigger below performs a nested shadow refresh. In that path
    -- rate_percent is already authoritative and must not be recalculated from
    -- the lower-precision compatibility column.
    IF pg_trigger_depth() > 1 THEN
        RETURN NEW;
    END IF;

    IF NEW.rate_percent IS DISTINCT FROM OLD.rate_percent THEN
        IF NEW.rate_percent IS NULL THEN
            NEW.rate_multiplier := NULL;
        ELSE
            NEW.rate_multiplier := ROUND(group_rate * NEW.rate_percent / 100.0, 10);
        END IF;
    ELSIF NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier THEN
        IF NEW.rate_multiplier IS NULL THEN
            NEW.rate_percent := NULL;
        ELSE
            NEW.rate_percent := ROUND(NEW.rate_multiplier / group_rate * 100.0, 10);
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sync_user_group_rate_percent_fields
    ON user_group_rate_multipliers;

CREATE TRIGGER trg_sync_user_group_rate_percent_fields
BEFORE INSERT OR UPDATE OF rate_percent, rate_multiplier
ON user_group_rate_multipliers
FOR EACH ROW
EXECUTE FUNCTION sync_user_group_rate_percent_fields();

CREATE OR REPLACE FUNCTION refresh_user_group_rate_multiplier_shadow()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.rate_multiplier IS DISTINCT FROM OLD.rate_multiplier THEN
        UPDATE user_group_rate_multipliers
        SET rate_multiplier = ROUND(NEW.rate_multiplier::DECIMAL(20,10) * rate_percent / 100.0, 10),
            updated_at = NOW()
        WHERE group_id = NEW.id
          AND rate_percent IS NOT NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_refresh_user_group_rate_multiplier_shadow ON groups;

CREATE TRIGGER trg_refresh_user_group_rate_multiplier_shadow
AFTER UPDATE OF rate_multiplier
ON groups
FOR EACH ROW
EXECUTE FUNCTION refresh_user_group_rate_multiplier_shadow();

COMMENT ON COLUMN user_group_rate_multipliers.rate_percent IS
    '用户专属倍率占分组普通倍率的百分比；NULL 表示沿用分组默认倍率。';
COMMENT ON COLUMN user_group_rate_multipliers.rate_multiplier IS
    '兼容影子值，由 rate_percent 和 groups.rate_multiplier 自动同步；不再作为业务真值。';
