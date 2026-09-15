-- Upstream account minimum input token threshold.
-- 0 means unlimited. Health probes pad their input; ordinary requests skip
-- accounts whose threshold is larger than the estimated request input.
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS probe_min_input_tokens INTEGER NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'accounts_probe_min_input_tokens_nonnegative'
    ) THEN
        ALTER TABLE accounts
            ADD CONSTRAINT accounts_probe_min_input_tokens_nonnegative
            CHECK (probe_min_input_tokens >= 0);
    END IF;
END $$;

COMMENT ON COLUMN accounts.probe_min_input_tokens IS
    'Minimum estimated input token count required by the upstream account; 0 means unlimited.';
