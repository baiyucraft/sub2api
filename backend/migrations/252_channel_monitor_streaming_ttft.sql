-- Channel monitor streaming probes: time to first non-empty text delta.
-- Nullable for compatibility with historical non-streaming observations.
ALTER TABLE channel_monitor_histories
    ADD COLUMN IF NOT EXISTS ttft_ms BIGINT;
