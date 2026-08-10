-- One upstream key owns at most one non-deleted derived account.
-- The explicit preflight intentionally blocks migration on historical
-- duplicates instead of silently unbinding or deleting data.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM accounts
        WHERE upstream_key_id IS NOT NULL
          AND deleted_at IS NULL
        GROUP BY upstream_key_id
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'upstream account key duplicate preflight failed';
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_accounts_upstream_key_id_active
    ON accounts(upstream_key_id)
    WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL;
