-- A single account/Key state controls cache protection across all groups.
-- Zero denotes legacy evidence without a trustworthy originating Key; such
-- facts remain available for rollback but cannot enter account statistics.
ALTER TABLE upstream_cache_guard_facts
    ADD COLUMN upstream_key_id BIGINT NOT NULL DEFAULT 0;

UPDATE upstream_cache_guard_facts
SET upstream_key_id = (evidence_json->'request'->>'upstream_key_id')::BIGINT
WHERE evidence_json->'request'->>'upstream_key_id' ~ '^[1-9][0-9]{0,18}$'
  AND (length(evidence_json->'request'->>'upstream_key_id') < 19
       OR evidence_json->'request'->>'upstream_key_id' <= '9223372036854775807');

CREATE INDEX idx_upstream_cache_guard_account_history
    ON upstream_cache_guard_facts (account_id, upstream_key_id, completed_at, group_id, evidence_id)
    WHERE processed = TRUE;

CREATE INDEX idx_upstream_cache_guard_legacy_identity
    ON upstream_cache_guard_runtime ((state_json->>'account_id'), (state_json->>'upstream_key_id'))
    WHERE state_kind = 'account';

CREATE TABLE upstream_cache_guard_accounts (
    account_id BIGINT NOT NULL,
    upstream_key_id BIGINT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1,
    state_json JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (account_id, upstream_key_id),
    CONSTRAINT upstream_cache_guard_account_positive CHECK (account_id > 0 AND upstream_key_id > 0 AND version > 0),
    CONSTRAINT upstream_cache_guard_account_object CHECK (jsonb_typeof(state_json) = 'object')
);
