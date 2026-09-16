ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS scheduling_mode VARCHAR(20) NOT NULL DEFAULT 'cache_first';

ALTER TABLE api_keys
    DROP CONSTRAINT IF EXISTS api_keys_scheduling_mode_check;

ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_scheduling_mode_check
    CHECK (scheduling_mode IN ('cache_first', 'speed_first'));
