-- Non-transactional indexes for large existing tables and partial uniqueness.

WITH account_refs AS (
    SELECT upstream_key_id, COUNT(*) AS ref_count
    FROM accounts
    WHERE upstream_key_id IS NOT NULL
    GROUP BY upstream_key_id
), ranked AS (
    SELECT uk.id,
           FIRST_VALUE(uk.id) OVER (
               PARTITION BY uk.upstream_config_id, uk.remote_key_id
               ORDER BY COALESCE(ar.ref_count, 0) DESC, uk.last_seen_at DESC NULLS LAST, uk.id ASC
           ) AS keeper_key_id,
           ROW_NUMBER() OVER (
               PARTITION BY uk.upstream_config_id, uk.remote_key_id
               ORDER BY COALESCE(ar.ref_count, 0) DESC, uk.last_seen_at DESC NULLS LAST, uk.id ASC
           ) AS duplicate_rank
    FROM upstream_keys uk
    LEFT JOIN account_refs ar ON ar.upstream_key_id = uk.id
    WHERE uk.remote_key_id IS NOT NULL AND uk.deleted_at IS NULL
)
UPDATE accounts a
SET upstream_key_id = ranked.keeper_key_id, updated_at = NOW()
FROM ranked
WHERE a.upstream_key_id = ranked.id AND ranked.duplicate_rank > 1;

WITH account_refs AS (
    SELECT upstream_key_id, COUNT(*) AS ref_count
    FROM accounts
    WHERE upstream_key_id IS NOT NULL
    GROUP BY upstream_key_id
), ranked AS (
    SELECT uk.id, ROW_NUMBER() OVER (
        PARTITION BY uk.upstream_config_id, uk.remote_key_id
        ORDER BY COALESCE(ar.ref_count, 0) DESC, uk.last_seen_at DESC NULLS LAST, uk.id ASC
    ) AS duplicate_rank
    FROM upstream_keys uk
    LEFT JOIN account_refs ar ON ar.upstream_key_id = uk.id
    WHERE uk.remote_key_id IS NOT NULL AND uk.deleted_at IS NULL
)
UPDATE upstream_keys uk
SET deleted_at = NOW(), updated_at = NOW()
FROM ranked
WHERE ranked.id = uk.id AND ranked.duplicate_rank > 1;

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_upstream_keys_config_remote_key_id_active
    ON upstream_keys(upstream_config_id, remote_key_id)
    WHERE remote_key_id IS NOT NULL AND deleted_at IS NULL;

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_upstream_config_created_at;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_upstream_config_created_at
    ON usage_logs(upstream_config_id, created_at);

DROP INDEX CONCURRENTLY IF EXISTS idx_usage_logs_upstream_key_created_at;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_upstream_key_created_at
    ON usage_logs(upstream_key_id, created_at);
