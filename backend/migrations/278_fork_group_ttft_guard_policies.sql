-- Fork-owned OpenAI TTFT Guard policy overrides, isolated from the upstream
-- groups table so upstream schema changes remain easy to merge.
CREATE TABLE IF NOT EXISTS fork_group_ttft_guard_policies (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
    mode VARCHAR(16) NOT NULL DEFAULT 'inherit',
    degradation_ttft_seconds INTEGER NOT NULL DEFAULT 20,
    min_samples INTEGER NOT NULL DEFAULT 5,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fork_group_ttft_guard_policies_mode_check
        CHECK (mode IN ('inherit', 'enabled', 'disabled')),
    CONSTRAINT fork_group_ttft_guard_policies_threshold_check
        CHECK (degradation_ttft_seconds BETWEEN 5 AND 300),
    CONSTRAINT fork_group_ttft_guard_policies_min_samples_check
        CHECK (min_samples BETWEEN 2 AND 20)
);

COMMENT ON TABLE fork_group_ttft_guard_policies IS
    'Fork-owned per-group OpenAI TTFT Guard policy; runtime applies it only to OpenAI scheduling.';
COMMENT ON COLUMN fork_group_ttft_guard_policies.mode IS
    'inherit uses global settings, enabled uses group values, disabled bypasses TTFT Guard for the group.';
