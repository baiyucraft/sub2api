-- Cache guard facts contain raw upstream token metadata, never request content.
-- Runtime rows are versioned typed state shared by every application instance.
CREATE TABLE IF NOT EXISTS upstream_cache_guard_facts (
    group_id BIGINT NOT NULL,
    evidence_id VARCHAR(200) NOT NULL,
    account_id BIGINT NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    valid_sample BOOLEAN NOT NULL DEFAULT FALSE,
    processed BOOLEAN NOT NULL DEFAULT FALSE,
    claim_owner VARCHAR(64),
    claim_until TIMESTAMPTZ,
    evidence_json JSONB NOT NULL,
    PRIMARY KEY (group_id, evidence_id),
    CONSTRAINT upstream_cache_guard_fact_positive CHECK (group_id > 0 AND account_id > 0),
    CONSTRAINT upstream_cache_guard_fact_object CHECK (jsonb_typeof(evidence_json) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_upstream_cache_guard_valid_window
    ON upstream_cache_guard_facts (group_id, account_id, completed_at, evidence_id)
    WHERE valid_sample = TRUE AND processed = TRUE;
CREATE INDEX IF NOT EXISTS idx_upstream_cache_guard_fact_retention
    ON upstream_cache_guard_facts (completed_at) WHERE processed = TRUE;
CREATE INDEX IF NOT EXISTS idx_upstream_cache_guard_pending
    ON upstream_cache_guard_facts (completed_at, group_id, evidence_id)
    WHERE processed = FALSE;
CREATE INDEX IF NOT EXISTS idx_upstream_cache_guard_processed_history
    ON upstream_cache_guard_facts (group_id, account_id, completed_at, evidence_id)
    WHERE processed = TRUE;

CREATE TABLE IF NOT EXISTS upstream_cache_guard_runtime (
    group_id BIGINT NOT NULL,
    state_kind VARCHAR(16) NOT NULL,
    state_key VARCHAR(128) NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    state_json JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (group_id, state_kind, state_key),
    CONSTRAINT upstream_cache_guard_runtime_positive CHECK (group_id > 0 AND version > 0),
    CONSTRAINT upstream_cache_guard_runtime_kind CHECK (state_kind IN ('group', 'account', 'session')),
    CONSTRAINT upstream_cache_guard_runtime_object CHECK (jsonb_typeof(state_json) = 'object')
);
CREATE INDEX IF NOT EXISTS idx_upstream_cache_guard_runtime_updated
    ON upstream_cache_guard_runtime (updated_at);
