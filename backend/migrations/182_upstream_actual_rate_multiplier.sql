-- Make the persisted upstream key rate the only public/business cost multiplier.
-- The provider rate remains internal and is used only to recompute the actual rate.

ALTER TABLE upstream_keys
    ADD COLUMN source_rate_multiplier DECIMAL(20,10);

DO $$
DECLARE
    invalid_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO invalid_count
      FROM upstream_configs
     WHERE recharge_rate <= 0 OR recharge_rate > 100;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 182: invalid upstream recharge rates: %', invalid_count;
    END IF;

    SELECT COUNT(*) INTO invalid_count
      FROM upstream_keys k
      JOIN upstream_configs c ON c.id = k.upstream_config_id
     WHERE k.rate_multiplier IS NOT NULL
       AND (
           k.rate_multiplier < 0
           OR ROUND(k.rate_multiplier * c.recharge_rate, 4) < 0
           OR ROUND(k.rate_multiplier * c.recharge_rate, 4) > 999999.9999
           OR ROUND(k.rate_multiplier * c.recharge_rate, 4) * 100 > 2147483647
       );
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 182: upstream rates cannot be represented safely: %', invalid_count;
    END IF;

    SELECT COUNT(*) INTO invalid_count
      FROM accounts a
      LEFT JOIN upstream_keys k ON k.id = a.upstream_key_id AND k.deleted_at IS NULL
     WHERE (
           (a.upstream_config_id IS NULL) <> (a.upstream_key_id IS NULL)
           OR (a.upstream_key_id IS NOT NULL AND (
               k.id IS NULL
               OR k.upstream_config_id IS DISTINCT FROM a.upstream_config_id
               OR k.rate_multiplier IS NULL
           ))
       );
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 182: invalid upstream account bindings: %', invalid_count;
    END IF;

    SELECT COUNT(*) INTO invalid_count
      FROM accounts
     WHERE upstream_key_id IS NOT NULL
       AND concurrency > 1073741823;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 182: upstream account concurrency cannot derive a safe load factor: %', invalid_count;
    END IF;
END
$$;

UPDATE upstream_keys
   SET source_rate_multiplier = rate_multiplier,
       rate_multiplier = ROUND(rate_multiplier * c.recharge_rate, 4),
       extra = COALESCE(upstream_keys.extra, '{}'::jsonb)
           - ARRAY['default_rate_multiplier', 'dedicated_rate_multiplier', 'has_dedicated_rate_multiplier'],
       updated_at = NOW()
  FROM upstream_configs c
 WHERE c.id = upstream_keys.upstream_config_id
   AND upstream_keys.rate_multiplier IS NOT NULL;

UPDATE upstream_keys
   SET extra = COALESCE(extra, '{}'::jsonb)
       - ARRAY['default_rate_multiplier', 'dedicated_rate_multiplier', 'has_dedicated_rate_multiplier'],
       updated_at = NOW()
 WHERE rate_multiplier IS NULL
   AND COALESCE(extra, '{}'::jsonb) ?| ARRAY['default_rate_multiplier', 'dedicated_rate_multiplier', 'has_dedicated_rate_multiplier'];

CREATE TEMP TABLE migration_182_changed_account_ids (
    id BIGINT PRIMARY KEY
) ON COMMIT DROP;

INSERT INTO migration_182_changed_account_ids (id)
SELECT id
  FROM accounts
 WHERE deleted_at IS NULL
   AND upstream_key_id IS NOT NULL;

INSERT INTO migration_182_changed_account_ids (id)
SELECT id
  FROM accounts
 WHERE deleted_at IS NULL
   AND COALESCE(extra, '{}'::jsonb) ?| ARRAY[
       'upstream_rate_multiplier',
       'upstream_source_rate_multiplier',
       'upstream_recharge_rate',
       'upstream_effective_cost_multiplier',
       'sub2api_upstream_rate_multiplier'
   ]
ON CONFLICT (id) DO NOTHING;

WITH derived AS (
    SELECT a.id,
           k.rate_multiplier,
           ROUND(k.rate_multiplier * 100)::INTEGER AS priority,
           GREATEST(a.concurrency::BIGINT, 1::BIGINT) AS base_concurrency
      FROM accounts a
      JOIN upstream_keys k ON k.id = a.upstream_key_id
     WHERE a.upstream_key_id IS NOT NULL
), values_to_apply AS (
    SELECT id,
           rate_multiplier,
           priority,
           LEAST(
               base_concurrency * 2,
               GREATEST(1, ROUND(base_concurrency * CASE
                   WHEN priority <= 5 THEN 2.0
                   WHEN priority <= 10 THEN 1.5
                   WHEN priority <= 20 THEN 1.0
                   WHEN priority <= 50 THEN 0.75
                   ELSE 0.5
               END)::INTEGER)
           ) AS load_factor
      FROM derived
)
UPDATE accounts a
   SET rate_multiplier = v.rate_multiplier,
       priority = v.priority,
       load_factor = v.load_factor,
       updated_at = NOW()
  FROM values_to_apply v
 WHERE v.id = a.id;

UPDATE accounts
   SET extra = COALESCE(extra, '{}'::jsonb) - ARRAY[
	   'upstream_rate_multiplier',
	   'upstream_source_rate_multiplier',
	   'upstream_recharge_rate',
	   'upstream_effective_cost_multiplier',
	   'sub2api_upstream_rate_multiplier'
	],
	updated_at = NOW()
 WHERE COALESCE(extra, '{}'::jsonb) ?| ARRAY[
	   'upstream_rate_multiplier',
	   'upstream_source_rate_multiplier',
	   'upstream_recharge_rate',
	   'upstream_effective_cost_multiplier',
	   'sub2api_upstream_rate_multiplier'
	];

INSERT INTO scheduler_outbox (event_type, payload)
SELECT 'account_bulk_changed', jsonb_build_object('account_ids', jsonb_agg(id ORDER BY id))
  FROM migration_182_changed_account_ids
HAVING COUNT(*) > 0;

ALTER TABLE upstream_keys
    DROP CONSTRAINT IF EXISTS upstream_keys_source_rate_valid,
    DROP CONSTRAINT IF EXISTS upstream_keys_actual_rate_valid,
    ADD CONSTRAINT upstream_keys_source_rate_valid
        CHECK (source_rate_multiplier IS NULL OR source_rate_multiplier >= 0),
    ADD CONSTRAINT upstream_keys_actual_rate_valid
        CHECK (rate_multiplier IS NULL OR (rate_multiplier >= 0 AND rate_multiplier <= 999999.9999));

COMMENT ON COLUMN upstream_keys.source_rate_multiplier IS 'Internal provider multiplier; never expose through product APIs';
COMMENT ON COLUMN upstream_keys.rate_multiplier IS 'Actual cost multiplier: round(source_rate_multiplier * recharge_rate, 4)';

CREATE OR REPLACE FUNCTION validate_account_upstream_key_binding()
RETURNS TRIGGER AS $$
DECLARE
    key_config_id BIGINT;
    key_status VARCHAR(20);
    key_platform VARCHAR(50);
    key_deleted_at TIMESTAMPTZ;
    key_actual_rate NUMERIC(10,4);
    derived_priority INTEGER;
    base_concurrency BIGINT;
BEGIN
    IF NEW.upstream_key_id IS NULL THEN
        NEW.upstream_stale_pause_key_id := NULL;
        NEW.upstream_stale_paused_at := NULL;
        RETURN NEW;
    END IF;

    SELECT upstream_config_id, status, platform, deleted_at, rate_multiplier
      INTO key_config_id, key_status, key_platform, key_deleted_at, key_actual_rate
      FROM upstream_keys
     WHERE id = NEW.upstream_key_id;

    IF NOT FOUND OR key_deleted_at IS NOT NULL OR key_config_id IS DISTINCT FROM NEW.upstream_config_id THEN
        RAISE EXCEPTION 'invalid upstream key binding' USING ERRCODE = '23514';
    END IF;
    IF key_actual_rate IS NULL THEN
        RAISE EXCEPTION 'cannot bind an upstream key without an actual rate' USING ERRCODE = '23514';
    END IF;
    IF NEW.concurrency > 1073741823 THEN
        RAISE EXCEPTION 'upstream account concurrency cannot derive a safe load factor' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id)
       AND key_status <> 'active' THEN
        RAISE EXCEPTION 'cannot bind an inactive upstream key' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' OR NEW.upstream_key_id IS DISTINCT FROM OLD.upstream_key_id)
       AND (key_platform IS NULL OR key_platform IS DISTINCT FROM NEW.platform) THEN
        RAISE EXCEPTION 'cannot bind an unassigned or mismatched upstream key platform' USING ERRCODE = '23514';
    END IF;
    IF NEW.schedulable AND key_status = 'stale' THEN
        RAISE EXCEPTION 'cannot schedule an account bound to a stale upstream key' USING ERRCODE = '23514';
    END IF;
    IF NEW.schedulable AND (key_platform IS NULL OR key_platform IS DISTINCT FROM NEW.platform) THEN
        RAISE EXCEPTION 'cannot schedule an account with a mismatched upstream key platform' USING ERRCODE = '23514';
    END IF;

    NEW.rate_multiplier := key_actual_rate;
    derived_priority := ROUND(key_actual_rate * 100)::INTEGER;
    NEW.priority := derived_priority;
    base_concurrency := GREATEST(NEW.concurrency::BIGINT, 1::BIGINT);
    NEW.load_factor := LEAST(
        base_concurrency * 2,
        GREATEST(1, ROUND(base_concurrency * CASE
            WHEN derived_priority <= 5 THEN 2.0
            WHEN derived_priority <= 10 THEN 1.5
            WHEN derived_priority <= 20 THEN 1.0
            WHEN derived_priority <= 50 THEN 0.75
            ELSE 0.5
        END)::INTEGER)
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_validate_account_upstream_key_binding ON accounts;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, platform,
    rate_multiplier, priority, load_factor, concurrency, deleted_at
ON accounts
FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
