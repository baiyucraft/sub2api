-- Align upstream key platform constraints with the registered concrete platform catalog.
-- Unknown provider values remain in the sync evidence JSON and are not assigned.
ALTER TABLE upstream_keys
    DROP CONSTRAINT IF EXISTS upstream_keys_platform_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_detected_platform_valid;

ALTER TABLE upstream_keys
    ADD CONSTRAINT upstream_keys_platform_valid
        CHECK (platform IS NULL OR platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek')),
    ADD CONSTRAINT upstream_keys_detected_platform_valid
        CHECK (detected_platform IS NULL OR detected_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'kimi', 'zhipu', 'deepseek'));
