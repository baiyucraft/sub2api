-- Per-user usage aggregates for the admin user statistics matrix.
-- Daily rows are retained permanently so lifetime totals do not depend on the
-- raw usage_logs retention window. Hourly rows follow the existing dashboard
-- hourly retention policy.

CREATE TABLE IF NOT EXISTS usage_dashboard_user_hourly (
    bucket_start TIMESTAMPTZ NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    user_spend NUMERIC(20, 10) NOT NULL DEFAULT 0,
    account_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_start, user_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_user_hourly_user_bucket
    ON usage_dashboard_user_hourly (user_id, bucket_start DESC);

CREATE TABLE IF NOT EXISTS usage_dashboard_user_daily (
    bucket_date DATE NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    user_spend NUMERIC(20, 10) NOT NULL DEFAULT 0,
    account_cost NUMERIC(20, 10) NOT NULL DEFAULT 0,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (bucket_date, user_id)
);

CREATE INDEX IF NOT EXISTS idx_usage_dashboard_user_daily_user_bucket
    ON usage_dashboard_user_daily (user_id, bucket_date DESC);

CREATE TABLE IF NOT EXISTS usage_dashboard_user_backfill_state (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    earliest_covered_date DATE NULL,
    last_completed_date DATE NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'unavailable'
        CHECK (status IN ('available', 'building', 'partial', 'unavailable')),
    coverage_start TIMESTAMPTZ NULL,
    coverage_end TIMESTAMPTZ NULL,
    target_end TIMESTAMPTZ NULL,
    attempt_count BIGINT NOT NULL DEFAULT 0,
    last_error TEXT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ NULL,
    CHECK (
        coverage_start IS NULL
        OR coverage_end IS NULL
        OR coverage_end >= coverage_start
    )
);

INSERT INTO usage_dashboard_user_backfill_state (id)
VALUES (1)
ON CONFLICT (id) DO NOTHING;

COMMENT ON TABLE usage_dashboard_user_hourly IS
    'Per-user hourly usage aggregates in the configured application timezone.';
COMMENT ON TABLE usage_dashboard_user_daily IS
    'Permanent per-user daily usage aggregates in the configured application timezone.';
COMMENT ON TABLE usage_dashboard_user_backfill_state IS
    'Singleton state for resumable user usage backfill and raw-log cleanup coverage.';
COMMENT ON COLUMN usage_dashboard_user_backfill_state.coverage_start IS
    'Inclusive start of the contiguous raw usage interval represented by user aggregates.';
COMMENT ON COLUMN usage_dashboard_user_backfill_state.coverage_end IS
    'Exclusive end of the contiguous raw usage interval represented by user aggregates.';
