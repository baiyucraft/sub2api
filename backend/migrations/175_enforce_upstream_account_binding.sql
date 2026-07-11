-- Canonical upstream-bound account representation:
--   type = 'apikey' with both upstream_config_id and upstream_key_id populated.
-- The preflight covers soft-deleted rows because the database constraint does too.
DO $$
DECLARE
    invalid_ids TEXT;
BEGIN
    SELECT string_agg(id::text, ', ' ORDER BY id)
    INTO invalid_ids
    FROM accounts
    WHERE (upstream_config_id IS NULL) <> (upstream_key_id IS NULL);

    IF invalid_ids IS NOT NULL THEN
        RAISE EXCEPTION 'cannot enforce upstream account binding: partially bound account ids: %', invalid_ids;
    END IF;

    SELECT string_agg(id::text, ', ' ORDER BY id)
    INTO invalid_ids
    FROM accounts
    WHERE upstream_config_id IS NOT NULL
      AND upstream_key_id IS NOT NULL
      AND type NOT IN ('apikey', 'upstream');

    IF invalid_ids IS NOT NULL THEN
        RAISE EXCEPTION 'cannot enforce upstream account binding: invalid bound account types for ids: %', invalid_ids;
    END IF;
END
$$;

UPDATE accounts
SET type = 'apikey', updated_at = NOW()
WHERE upstream_config_id IS NOT NULL
  AND upstream_key_id IS NOT NULL
  AND type = 'upstream';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'accounts_upstream_binding_complete'
          AND conrelid = 'accounts'::regclass
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_upstream_binding_complete
            CHECK (
                (upstream_config_id IS NULL AND upstream_key_id IS NULL)
                OR (
                    upstream_config_id IS NOT NULL
                    AND upstream_key_id IS NOT NULL
                    AND type = 'apikey'
                )
            ) NOT VALID;
    END IF;
END
$$;

ALTER TABLE accounts VALIDATE CONSTRAINT accounts_upstream_binding_complete;
