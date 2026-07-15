-- Persist the recharge rate used by each balance observation and initialize the
-- global cost-group selection as an explicit snapshot of current groups.

ALTER TABLE upstream_balance_snapshots
    ADD COLUMN IF NOT EXISTS recharge_rate DECIMAL(20,10),
    ADD COLUMN IF NOT EXISTS balance_formula_version INTEGER NOT NULL DEFAULT 1;

DO $$
DECLARE
    invalid_count BIGINT;
    event_row RECORD;
    event_old_rate NUMERIC;
    event_new_rate NUMERIC;
    previous_config_id BIGINT;
    previous_new_rate NUMERIC;
    current_config_rate NUMERIC;
BEGIN
    SELECT COUNT(*) INTO invalid_count
      FROM upstream_configs
     WHERE recharge_rate <= 0 OR recharge_rate > 100;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 187: invalid recharge rates: %', invalid_count;
    END IF;

    -- Historical balances can only be converted safely when the immutable
    -- recharge-rate event chain is complete and agrees with the current row.
    FOR event_row IN
        SELECT id, upstream_config_id, payload
          FROM upstream_events
         WHERE event_type = 'recharge_rate_changed'
         ORDER BY upstream_config_id, occurred_at, id
    LOOP
        BEGIN
            event_old_rate := NULLIF(event_row.payload->>'old_rate', '')::numeric;
            event_new_rate := NULLIF(event_row.payload->>'new_rate', '')::numeric;
        EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
            RAISE EXCEPTION 'migration 187: invalid recharge-rate event payload: event_id=%', event_row.id;
        END;

        IF event_old_rate IS NULL OR event_new_rate IS NULL
           OR event_old_rate <= 0 OR event_old_rate > 100
           OR event_new_rate <= 0 OR event_new_rate > 100 THEN
            RAISE EXCEPTION 'migration 187: invalid recharge-rate event values: event_id=%', event_row.id;
        END IF;

        IF previous_config_id IS DISTINCT FROM event_row.upstream_config_id THEN
            IF previous_config_id IS NOT NULL THEN
                SELECT recharge_rate INTO current_config_rate
                  FROM upstream_configs
                 WHERE id = previous_config_id;
                IF current_config_rate IS DISTINCT FROM previous_new_rate THEN
                    RAISE EXCEPTION 'migration 187: recharge-rate event chain does not match current config: config_id=%', previous_config_id;
                END IF;
            END IF;
            previous_config_id := event_row.upstream_config_id;
            previous_new_rate := NULL;
        ELSIF event_old_rate IS DISTINCT FROM previous_new_rate THEN
            RAISE EXCEPTION 'migration 187: broken recharge-rate event chain: event_id=%', event_row.id;
        END IF;

        previous_new_rate := event_new_rate;
    END LOOP;

    IF previous_config_id IS NOT NULL THEN
        SELECT recharge_rate INTO current_config_rate
          FROM upstream_configs
         WHERE id = previous_config_id;
        IF current_config_rate IS DISTINCT FROM previous_new_rate THEN
            RAISE EXCEPTION 'migration 187: recharge-rate event chain does not match current config: config_id=%', previous_config_id;
        END IF;
    END IF;

    SELECT COUNT(*) INTO invalid_count
      FROM (
          SELECT upstream_config_id, sync_run_id
            FROM upstream_key_rate_snapshots
           WHERE sync_run_id IS NOT NULL
           GROUP BY upstream_config_id, sync_run_id
          HAVING COUNT(DISTINCT recharge_rate) <> 1
      ) inconsistent_runs;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 187: inconsistent recharge rates within sync runs: %', invalid_count;
    END IF;

    UPDATE upstream_balance_snapshots s
       SET recharge_rate = COALESCE(
               (
                   SELECT NULLIF(e.payload->>'new_rate', '')::numeric
                     FROM upstream_events e
                    WHERE e.upstream_config_id = s.upstream_config_id
                      AND e.event_type = 'recharge_rate_changed'
                      AND e.occurred_at <= s.observed_at
                    ORDER BY e.occurred_at DESC, e.id DESC
                    LIMIT 1
               ),
               (
                   SELECT NULLIF(e.payload->>'old_rate', '')::numeric
                     FROM upstream_events e
                    WHERE e.upstream_config_id = s.upstream_config_id
                      AND e.event_type = 'recharge_rate_changed'
                      AND e.occurred_at > s.observed_at
                    ORDER BY e.occurred_at ASC, e.id ASC
                    LIMIT 1
               ),
               (
                   SELECT k.recharge_rate
                     FROM upstream_key_rate_snapshots k
                    WHERE k.upstream_config_id = s.upstream_config_id
                      AND k.sync_run_id = s.sync_run_id
                      AND s.sync_run_id IS NOT NULL
                    ORDER BY k.id
                    LIMIT 1
               ),
               (
                   SELECT CASE WHEN c.recharge_rate = 1 THEN 1 ELSE NULL END
                     FROM upstream_configs c
                    WHERE c.id = s.upstream_config_id
               )
           )
     WHERE recharge_rate IS NULL;

    SELECT COUNT(*) INTO invalid_count
      FROM upstream_balance_snapshots
     WHERE recharge_rate IS NULL;
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 187: historical recharge rate cannot be proven: snapshots=%', invalid_count;
    END IF;

    UPDATE upstream_balance_snapshots
       SET balance_cny = CASE WHEN balance_cny IS NULL THEN NULL ELSE ROUND(balance_cny * recharge_rate, 10) END,
           used_cny = CASE WHEN used_cny IS NULL THEN NULL ELSE ROUND(used_cny * recharge_rate, 10) END,
           total_recharged_cny = CASE WHEN total_recharged_cny IS NULL THEN NULL ELSE ROUND(total_recharged_cny * recharge_rate, 10) END,
           balance_formula_version = 2
     WHERE balance_formula_version = 1;

    SELECT COUNT(*) INTO invalid_count
      FROM upstream_configs c
     WHERE EXISTS (
           SELECT 1
             FROM unnest(ARRAY['balance_cny', 'used_cny', 'total_recharged_cny']) AS amount_key
            WHERE c.extra ? amount_key
              AND c.extra->amount_key <> 'null'::jsonb
              AND jsonb_typeof(c.extra->amount_key) NOT IN ('number', 'string')
     );
    IF invalid_count <> 0 THEN
        RAISE EXCEPTION 'migration 187: invalid current balance JSON values: %', invalid_count;
    END IF;

    BEGIN
        PERFORM (c.extra->>amount_key)::numeric
          FROM upstream_configs c
          CROSS JOIN unnest(ARRAY['balance_cny', 'used_cny', 'total_recharged_cny']) AS amount_key
         WHERE c.extra ? amount_key
           AND c.extra->amount_key <> 'null'::jsonb;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'migration 187: non-numeric current balance value';
    END;

    UPDATE upstream_configs c
       SET extra = COALESCE(c.extra, '{}'::jsonb)
                   || jsonb_strip_nulls(jsonb_build_object(
                       'balance_cny', CASE
                           WHEN c.extra ? 'balance_cny' AND c.extra->'balance_cny' <> 'null'::jsonb
                           THEN to_jsonb(ROUND((c.extra->>'balance_cny')::numeric * c.recharge_rate, 10))
                           ELSE NULL
                       END,
                       'used_cny', CASE
                           WHEN c.extra ? 'used_cny' AND c.extra->'used_cny' <> 'null'::jsonb
                           THEN to_jsonb(ROUND((c.extra->>'used_cny')::numeric * c.recharge_rate, 10))
                           ELSE NULL
                       END,
                       'total_recharged_cny', CASE
                           WHEN c.extra ? 'total_recharged_cny' AND c.extra->'total_recharged_cny' <> 'null'::jsonb
                           THEN to_jsonb(ROUND((c.extra->>'total_recharged_cny')::numeric * c.recharge_rate, 10))
                           ELSE NULL
                       END
                   ))
                   || jsonb_build_object('recharge_rate', c.recharge_rate, 'balance_formula_version', 2),
           updated_at = NOW()
     WHERE (c.extra ? 'balance_cny' OR c.extra ? 'used_cny' OR c.extra ? 'total_recharged_cny')
       AND COALESCE(c.extra->>'balance_formula_version', '') <> '2';
END
$$;

ALTER TABLE upstream_balance_snapshots
    ALTER COLUMN recharge_rate SET NOT NULL,
    ADD CONSTRAINT upstream_balance_snapshots_recharge_rate_valid
        CHECK (recharge_rate > 0 AND recharge_rate <= 100),
    ADD CONSTRAINT upstream_balance_snapshots_formula_version_valid
        CHECK (balance_formula_version IN (1, 2));

INSERT INTO settings (key, value, updated_at)
SELECT 'upstream_cost_included_group_ids',
       COALESCE((SELECT jsonb_agg(g.id ORDER BY g.id)::text
                   FROM groups g
                  WHERE g.deleted_at IS NULL AND g.status = 'active'), '[]'),
       NOW()
 WHERE NOT EXISTS (
       SELECT 1 FROM settings WHERE key = 'upstream_cost_included_group_ids'
 );

COMMENT ON COLUMN upstream_balance_snapshots.recharge_rate IS 'Recharge conversion rate used for this immutable balance observation';
COMMENT ON COLUMN upstream_balance_snapshots.balance_formula_version IS '1=legacy currency conversion, 2=currency conversion multiplied by recharge_rate';
