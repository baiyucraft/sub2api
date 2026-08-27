-- Per-group scheduler preference is independent from account_groups.priority.
ALTER TABLE account_groups
    ADD COLUMN IF NOT EXISTS scheduler_preferred BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_account_groups_preferred
    ON account_groups(group_id, scheduler_preferred)
    WHERE scheduler_preferred = TRUE;
