-- Managed monitor key names mirror their channel monitor names. The API key
-- row, plaintext key, quota and usage history remain unchanged.

ALTER TABLE api_keys
    ALTER COLUMN name TYPE VARCHAR(103);

DO $$
DECLARE
    invalid_live_binding_count BIGINT;
BEGIN
    SELECT COUNT(*) INTO invalid_live_binding_count
      FROM api_keys AS k
      LEFT JOIN channel_monitors AS m
        ON m.id = k.managed_monitor_id
       AND m.managed_api_key_id = k.id
     WHERE k.purpose = 'managed_monitor'
       AND k.deleted_at IS NULL
       AND k.group_id IS NOT NULL
       AND m.id IS NULL;

    IF invalid_live_binding_count <> 0 THEN
        RAISE EXCEPTION 'migration 198: invalid live managed monitor key bindings: %', invalid_live_binding_count;
    END IF;
END $$;

UPDATE api_keys AS k
	SET name = '监控-' || BTRIM(m.name),
	   updated_at = NOW()
  FROM channel_monitors AS m
 WHERE k.managed_monitor_id = m.id
   AND m.managed_api_key_id = k.id
   AND k.purpose = 'managed_monitor'
	   AND k.deleted_at IS NULL
	   AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name);
