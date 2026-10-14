-- Cache evidence remains account/Key scoped. Groups own policy projections only.
CREATE TABLE IF NOT EXISTS fork_group_scheduling_protection_policies (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
    cache_mode VARCHAR(16) NOT NULL DEFAULT 'inherit' CHECK (cache_mode IN ('inherit', 'enabled', 'disabled')),
    threshold_percent DOUBLE PRECISION NOT NULL DEFAULT 75 CHECK (threshold_percent >= 0 AND threshold_percent <= 100),
    recovery_threshold_percent DOUBLE PRECISION NOT NULL DEFAULT 80 CHECK (recovery_threshold_percent > threshold_percent AND recovery_threshold_percent <= 100),
    min_valid_requests INTEGER NOT NULL DEFAULT 20 CHECK (min_valid_requests BETWEEN 1 AND 100000),
    priority VARCHAR(16) NOT NULL DEFAULT 'inherit' CHECK (priority IN ('inherit', 'cache_first', 'ttft_first')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Preserve the former explicit execution whitelist exactly once. Newly created
-- groups inherit the global policy after this migration.
WITH legacy AS (
    SELECT value::jsonb AS config FROM settings WHERE key = 'upstream_cache_guard_settings'
), explicit_groups AS (
    SELECT config FROM legacy WHERE jsonb_typeof(config->'group_ids') = 'array' AND jsonb_array_length(config->'group_ids') > 0
)
INSERT INTO fork_group_scheduling_protection_policies (group_id, cache_mode)
SELECT g.id, CASE WHEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(p.config->'group_ids') AS selected(group_id_text) WHERE selected.group_id_text::bigint = g.id) THEN 'inherit' ELSE 'disabled' END
FROM groups g CROSS JOIN explicit_groups p WHERE g.deleted_at IS NULL
ON CONFLICT (group_id) DO NOTHING;

-- Keep legacy group_ids intact for an old application image during rollback.
