\set ON_ERROR_STOP on

DO $$
DECLARE
    dao_config_count INTEGER;
    sunai_config_count INTEGER;
    mismatch_count INTEGER;
    actual_mapping_count INTEGER;
    unique_mapping_count INTEGER;
    unexpected_sunai_accounts INTEGER;
    dao_account_mismatches INTEGER;
BEGIN
    SELECT COUNT(*) INTO dao_config_count
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND name = '刀哥'
       AND lower(trim(trailing '/' FROM site_url)) = 'https://www.codexapis.com';
    IF dao_config_count <> 1 THEN
        RAISE EXCEPTION 'calibration verification failed: expected exactly one Dao config';
    END IF;

    SELECT COUNT(*) INTO sunai_config_count
      FROM upstream_configs
     WHERE deleted_at IS NULL
       AND lower(name) = 'sunai'
       AND lower(trim(trailing '/' FROM site_url)) = 'https://api.sunai.lol';
    IF sunai_config_count <> 1 THEN
        RAISE EXCEPTION 'calibration verification failed: expected exactly one SunAI config';
    END IF;

    WITH actual AS (
        SELECT lower(trim(trailing '/' FROM c.site_url)) AS site_url,
               k.name AS key_name
          FROM upstream_configs c
          JOIN upstream_keys k ON k.upstream_config_id = c.id
         WHERE c.deleted_at IS NULL
           AND k.deleted_at IS NULL
           AND lower(trim(trailing '/' FROM c.site_url)) IN ('https://www.codexapis.com', 'https://api.sunai.lol')
    )
    SELECT COUNT(*), COUNT(DISTINCT (site_url, key_name))
      INTO actual_mapping_count, unique_mapping_count
      FROM actual;
    IF actual_mapping_count <> 10 OR unique_mapping_count <> 10 THEN
        RAISE EXCEPTION 'calibration verification failed: expected exactly 10 unique key mappings, found % rows and % unique names', actual_mapping_count, unique_mapping_count;
    END IF;

    WITH expected(site_url, key_name, group_name, platform) AS (
        VALUES
            ('https://www.codexapis.com', 'pro', 'gptproo', 'openai'),
            ('https://www.codexapis.com', 'plus', 'gptplus', 'openai'),
            ('https://www.codexapis.com', 'kiro', 'kiro-pro', 'anthropic'),
            ('https://www.codexapis.com', 'ccmax', 'cc-max', 'anthropic'),
            ('https://api.sunai.lol', 'ccmax', 'Claude-MAX', 'anthropic'),
            ('https://api.sunai.lol', 'kiro', 'Claude-awsq', 'anthropic'),
            ('https://api.sunai.lol', 'plus', 'Codex福利分组', 'openai'),
            ('https://api.sunai.lol', 'pro', 'codex-Pro', 'openai'),
            ('https://api.sunai.lol', 'gemini', 'Gemini', 'gemini'),
            ('https://api.sunai.lol', 'grok', 'Grok-Xai', 'grok')
    ), actual AS (
        SELECT lower(trim(trailing '/' FROM c.site_url)) AS site_url,
               k.name AS key_name,
               k.upstream_group_name AS group_name,
               k.platform,
               k.platform_source,
               k.platform_detection_status,
               k.status
          FROM upstream_configs c
          JOIN upstream_keys k ON k.upstream_config_id = c.id
         WHERE c.deleted_at IS NULL
           AND k.deleted_at IS NULL
           AND lower(trim(trailing '/' FROM c.site_url)) IN ('https://www.codexapis.com', 'https://api.sunai.lol')
    )
    SELECT COUNT(*) INTO mismatch_count
      FROM expected e
      LEFT JOIN actual a
        ON a.site_url = e.site_url
       AND a.key_name = e.key_name
     WHERE a.key_name IS NULL
        OR a.group_name IS DISTINCT FROM e.group_name
        OR a.platform IS DISTINCT FROM e.platform
        OR NOT (
            (a.platform_source = 'auto' AND a.platform_detection_status = 'detected')
         OR (a.platform_source = 'manual' AND a.platform_detection_status IN ('detected', 'conflict'))
        )
        OR a.status IS DISTINCT FROM 'active';
    IF mismatch_count <> 0 THEN
        RAISE EXCEPTION 'calibration verification failed: % expected key mappings are missing or incorrect', mismatch_count;
    END IF;

    SELECT COUNT(*) INTO unexpected_sunai_accounts
      FROM accounts a
      JOIN upstream_configs c ON c.id = a.upstream_config_id
     WHERE a.deleted_at IS NULL
       AND lower(trim(trailing '/' FROM c.site_url)) = 'https://api.sunai.lol';
    IF unexpected_sunai_accounts <> 0 THEN
        RAISE EXCEPTION 'calibration verification failed: SunAI must not gain derived accounts automatically';
    END IF;

    SELECT COUNT(*) INTO dao_account_mismatches
      FROM accounts a
      JOIN upstream_configs c ON c.id = a.upstream_config_id
      JOIN upstream_keys k ON k.id = a.upstream_key_id
     WHERE a.deleted_at IS NULL
       AND lower(trim(trailing '/' FROM c.site_url)) = 'https://www.codexapis.com'
       AND (
            k.name NOT IN ('pro', 'plus')
         OR a.name IS DISTINCT FROM ('刀哥-' || k.name)
         OR a.platform IS DISTINCT FROM 'openai'
         OR a.status IS DISTINCT FROM 'active'
         OR NOT a.schedulable
       );
    IF dao_account_mismatches <> 0 THEN
        RAISE EXCEPTION 'calibration verification failed: % Dao derived accounts are invalid', dao_account_mismatches;
    END IF;

    SELECT COUNT(*) INTO dao_account_mismatches
      FROM accounts a
      JOIN upstream_configs c ON c.id = a.upstream_config_id
     WHERE a.deleted_at IS NULL
       AND lower(trim(trailing '/' FROM c.site_url)) = 'https://www.codexapis.com';
    IF dao_account_mismatches <> 2 THEN
        RAISE EXCEPTION 'calibration verification failed: expected exactly two existing Dao derived accounts, found %', dao_account_mismatches;
    END IF;
END
$$;

SELECT 'calibration_verified' AS status;
