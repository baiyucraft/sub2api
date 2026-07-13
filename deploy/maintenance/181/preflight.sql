\set ON_ERROR_STOP on

DO $$
DECLARE
    duplicate_dao_configs INTEGER;
    broken_bindings INTEGER;
    unsupported_platforms INTEGER;
BEGIN
    SELECT COUNT(*)
      INTO duplicate_dao_configs
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com';
    IF duplicate_dao_configs <> 1 THEN
        RAISE EXCEPTION 'expected exactly one active codexapis upstream config, found %', duplicate_dao_configs;
    END IF;

    SELECT COUNT(*)
      INTO broken_bindings
      FROM accounts a
      LEFT JOIN upstream_keys k ON k.id = a.upstream_key_id
     WHERE a.deleted_at IS NULL
       AND a.upstream_key_id IS NOT NULL
       AND (
            k.id IS NULL
         OR k.deleted_at IS NOT NULL
         OR k.upstream_config_id IS DISTINCT FROM a.upstream_config_id
       );
    IF broken_bindings <> 0 THEN
        RAISE EXCEPTION 'found % invalid upstream account bindings', broken_bindings;
    END IF;

	SELECT COUNT(*)
	  INTO unsupported_platforms
	  FROM upstream_keys
	 WHERE platform IS NOT NULL
	   AND platform NOT IN ('anthropic', 'openai', 'gemini', 'grok');
	IF unsupported_platforms <> 0 THEN
		RAISE EXCEPTION 'found % legacy upstream keys with unsupported platforms; calibrate them before migration 181', unsupported_platforms;
	END IF;
END
$$;

SELECT
    COUNT(*) FILTER (WHERE provider = 'newapi' AND status = 'active' AND deleted_at IS NULL) AS active_newapi_configs,
    COUNT(*) FILTER (WHERE lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com' AND deleted_at IS NULL) AS dao_configs
FROM upstream_configs;

SELECT
    COUNT(*) AS bound_accounts,
    COUNT(*) FILTER (WHERE a.schedulable) AS schedulable_accounts
FROM accounts a
WHERE a.deleted_at IS NULL
  AND a.upstream_key_id IS NOT NULL;
