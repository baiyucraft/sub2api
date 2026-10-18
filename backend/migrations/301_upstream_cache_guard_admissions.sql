-- Requires quiesced application writers. Session JSON no longer carries the
-- per-request admission ledger; rollback requires a coordinated database restore.
CREATE TABLE upstream_cache_guard_admissions (
    group_id BIGINT NOT NULL,
    session_key VARCHAR(128) NOT NULL,
    admission_key VARCHAR(128) NOT NULL,
    account_id BIGINT NOT NULL,
    upstream_key_id BIGINT NOT NULL DEFAULT 0,
    route_key VARCHAR(128) NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    completed BOOLEAN NOT NULL DEFAULT FALSE,
    admission_json JSONB NOT NULL,
    PRIMARY KEY (group_id, session_key, admission_key),
    CONSTRAINT upstream_cache_guard_admission_identity CHECK (group_id > 0 AND account_id > 0 AND upstream_key_id >= 0),
    CONSTRAINT upstream_cache_guard_admission_object CHECK (jsonb_typeof(admission_json) = 'object')
);

-- Legacy admissions may omit the originating Key. Only a frozen route with
-- the same route digest can supply it; never attribute them to today's Key.
INSERT INTO upstream_cache_guard_admissions
    (group_id, session_key, admission_key, account_id, upstream_key_id, route_key, started_at, completed, admission_json)
SELECT runtime.group_id, runtime.state_key, admission.key,
    (admission.value->>'account_id')::BIGINT,
    provenance.key_id,
    COALESCE(admission.value->>'route_key', ''),
    (admission.value->>'started_at')::TIMESTAMPTZ,
    COALESCE((admission.value->>'completed')::BOOLEAN, FALSE),
    admission.value || jsonb_build_object('upstream_key_id', provenance.key_id)
FROM upstream_cache_guard_runtime runtime
CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(runtime.state_json->'admissions')='object'
    THEN runtime.state_json->'admissions' ELSE '{}'::JSONB END) admission
CROSS JOIN LATERAL (SELECT COALESCE(
    NULLIF((admission.value->>'upstream_key_id')::BIGINT, 0),
    NULLIF((runtime.state_json->'routes'->(admission.value->>'route_key')->>'upstream_key_id')::BIGINT, 0),
    0::BIGINT) AS key_id) provenance
WHERE runtime.state_kind='session';

-- Retain the session identity, natural ordinal, routes and affinity exactly.
-- The existing revision/pending trigger remains compatible with the small body.
UPDATE upstream_cache_guard_runtime
SET state_json=state_json-'admissions', version=version+1
WHERE state_kind='session' AND state_json ? 'admissions';

CREATE INDEX idx_cache_guard_admission_session_pending
ON upstream_cache_guard_admissions(group_id,session_key,account_id,route_key,started_at)
WHERE completed=FALSE;
CREATE INDEX idx_cache_guard_admission_session_retained
ON upstream_cache_guard_admissions(group_id,session_key,started_at)
WHERE completed=TRUE;
CREATE INDEX idx_cache_guard_admission_pending_age
ON upstream_cache_guard_admissions(started_at,account_id,upstream_key_id)
WHERE completed=FALSE;
CREATE INDEX idx_cache_guard_admission_retention
ON upstream_cache_guard_admissions(started_at,group_id,session_key,admission_key)
WHERE completed=TRUE;
CREATE INDEX idx_cache_guard_admission_acknowledgement
ON upstream_cache_guard_admissions(account_id,upstream_key_id,admission_key)
WHERE completed=TRUE;
