ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS rpm_limit INTEGER NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'accounts_rpm_limit_nonnegative'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_rpm_limit_nonnegative CHECK (rpm_limit >= 0);
    END IF;
END $$;
