#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 260 || $profile == 261 || $profile == 262 ]] || exit 1
[[ $phase == preflight || $phase == postflight || $phase == verified_replay || $phase == vm_semantics ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ $phase == preflight || $migration_status == verified ]] || exit 1

db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}

fail() {
  printf 'migration_285_failure_code=%s\n' "$1"
  exit 1
}

# Suppress database diagnostics; only booleans and fixed failure codes leave this helper.
query() {
  docker exec "$db_container" psql -X -q -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "BEGIN READ ONLY; SET LOCAL search_path = public, pg_catalog; $1; COMMIT;" 2>/dev/null
}

verify_schema() {
  local result
  result=$(query "WITH expected(table_name,column_name,column_type) AS (VALUES
    ('accounts','id','bigint'),
    ('accounts','upstream_config_id','bigint'),
    ('accounts','upstream_key_id','bigint'),
    ('accounts','platform','character varying(50)'),
    ('accounts','rate_multiplier','numeric(20,10)'),
    ('accounts','upstream_source_rate_multiplier','numeric(20,10)'),
    ('accounts','priority','integer'),
    ('accounts','schedulable','boolean'),
    ('accounts','deleted_at','timestamp with time zone'),
    ('accounts','upstream_stale_pause_key_id','bigint'),
    ('accounts','upstream_stale_paused_at','timestamp with time zone'),
    ('upstream_keys','id','bigint'),
    ('upstream_keys','upstream_config_id','bigint'),
    ('upstream_keys','status','character varying(20)'),
    ('upstream_keys','platform','character varying(50)'),
    ('upstream_keys','deleted_at','timestamp with time zone'),
    ('upstream_keys','rate_multiplier','numeric(20,10)'),
    ('upstream_keys','source_rate_multiplier','numeric(20,10)'))
    SELECT COUNT(*)=18 AND bool_and(COALESCE((format_type(a.atttypid,a.atttypmod)=e.column_type
      OR ('$phase'='preflight' AND '$migration_status'='absent'
        AND e.column_name='rate_multiplier' AND format_type(a.atttypid,a.atttypmod)='numeric(10,4)'))
      AND (e.table_name <> 'upstream_keys' OR e.column_name <> 'rate_multiplier' OR NOT a.attnotnull),FALSE))
    FROM expected e LEFT JOIN pg_namespace n ON n.nspname='public'
    LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=e.table_name AND c.relkind='r'
    LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.column_name AND a.attnum>0 AND NOT a.attisdropped") || fail schema_query
  [[ $result == t ]] || fail schema_contract
}

verify_contract() {
  local result
  # Normalize CRLF only; whitespace inside SQL strings remains significant.
  result=$(query "SELECT COUNT(*)=1 AND bool_and(
      p.pronargs=0 AND p.prorettype='trigger'::regtype AND p.prokind='f'
      AND l.lanname='plpgsql' AND NOT p.prosecdef AND p.proconfig IS NULL
      AND encode(sha256(convert_to(replace(p.prosrc,E'\\r\\n',E'\\n'),'UTF8')),'hex')
        ='11365864edc956a17760dc87be4acdc3667b2862fdf5329f9c0a3d04fd080751')
    FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    JOIN pg_language l ON l.oid=p.prolang
    WHERE n.nspname='public' AND p.proname='validate_account_upstream_key_binding'") || fail function_query
  [[ $result == t ]] || fail function_contract
  result=$(query "SELECT COUNT(*)=1 AND bool_and(
      t.tgtype=23 AND t.tgenabled IN ('O','A') AND t.tgnargs=0 AND t.tgqual IS NULL
      AND t.tgfoid='public.validate_account_upstream_key_binding()'::regprocedure
      AND ARRAY(SELECT a.attname::text FROM unnest(t.tgattr::smallint[]) AS x(attnum)
        JOIN pg_attribute a ON a.attrelid=t.tgrelid AND a.attnum=x.attnum ORDER BY a.attname)
        = ARRAY['deleted_at','platform','priority','rate_multiplier','schedulable',
          'upstream_config_id','upstream_key_id','upstream_source_rate_multiplier']::text[])
    FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname='accounts'
      AND t.tgname='trg_validate_account_upstream_key_binding' AND NOT t.tgisinternal") || fail trigger_query
  [[ $result == t ]] || fail trigger_contract
}

verify_vm_semantics() {
  local result
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  result=$(query "SELECT current_database() = '$db_name'") || fail isolation_query
  [[ $result == t ]] || fail isolation_boundary
  # Temporary tables shadow the restored production tables only in this transaction.
  # Execute the installed function, rather than a copied fixture implementation.
  result=$(docker exec -i "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" 2>/dev/null <<'SQL'
BEGIN;
SET LOCAL search_path = pg_temp, public, pg_catalog;
CREATE TEMP TABLE upstream_keys (
    id BIGINT PRIMARY KEY, upstream_config_id BIGINT, status VARCHAR(20),
    platform VARCHAR(50), deleted_at TIMESTAMPTZ,
    rate_multiplier NUMERIC(20,10), source_rate_multiplier NUMERIC(20,10)
) ON COMMIT DROP;
CREATE TEMP TABLE accounts (
    id BIGINT PRIMARY KEY, upstream_config_id BIGINT, upstream_key_id BIGINT,
    platform VARCHAR(50), rate_multiplier NUMERIC(20,10),
    upstream_source_rate_multiplier NUMERIC(20,10), priority INTEGER,
    schedulable BOOLEAN, deleted_at TIMESTAMPTZ,
    upstream_stale_pause_key_id BIGINT, upstream_stale_paused_at TIMESTAMPTZ
) ON COMMIT DROP;
CREATE TRIGGER trg_validate_account_upstream_key_binding
BEFORE INSERT OR UPDATE OF upstream_config_id, upstream_key_id, platform,
    rate_multiplier, upstream_source_rate_multiplier, priority, schedulable, deleted_at
ON pg_temp.accounts FOR EACH ROW EXECUTE FUNCTION public.validate_account_upstream_key_binding();
INSERT INTO pg_temp.upstream_keys VALUES
    (1,1,'active','openai',NULL,0.42,0.4201234567),
    (2,1,'active','openai',NULL,0.42,0.4201234567),
    (3,2,'active','openai',NULL,0.42,0.4201234567),
    (4,1,'active','openai',NULL,0,0),
    (5,1,'active','openai',NULL,0.42,NULL);
INSERT INTO pg_temp.accounts(id,upstream_config_id,upstream_key_id,platform,
    rate_multiplier,upstream_source_rate_multiplier,priority,schedulable,deleted_at) VALUES
    (1,1,1,'openai',9,9,900,TRUE,NULL),
    (2,1,1,'openai',9,9,900,TRUE,NULL),
    (3,1,1,'openai',9,9,900,TRUE,NOW()),
    (4,1,1,'openai',9,9,900,FALSE,NULL),
    (7,1,5,'openai',9,9,900,TRUE,NULL),
    (8,NULL,NULL,'openai',9,9,900,FALSE,NULL);
UPDATE pg_temp.upstream_keys SET rate_multiplier=NULL, source_rate_multiplier=0.88 WHERE id IN (1,2,3);
UPDATE pg_temp.upstream_keys SET rate_multiplier=NULL WHERE id=5;
DO $$
DECLARE
    statement TEXT;
    rejected BOOLEAN;
BEGIN
    UPDATE pg_temp.accounts SET schedulable=FALSE WHERE id=1;
    UPDATE pg_temp.accounts SET deleted_at=NOW() WHERE id=2;
    UPDATE pg_temp.accounts SET schedulable=FALSE,deleted_at=NOW() WHERE id=7;
    IF (SELECT rate_multiplier=0.42 AND upstream_source_rate_multiplier IS NULL AND priority=42
        FROM pg_temp.accounts WHERE id=7) IS DISTINCT FROM TRUE THEN
        RAISE EXCEPTION 'null source preservation failed';
    END IF;
    IF (SELECT COUNT(*) FROM pg_temp.accounts WHERE id IN (1,2,3,4)
        AND upstream_config_id=1 AND upstream_key_id=1 AND platform='openai'
        AND rate_multiplier=0.42 AND upstream_source_rate_multiplier=0.4201234567 AND priority=42) <> 4
       OR (SELECT schedulable FROM pg_temp.accounts WHERE id=1) IS DISTINCT FROM FALSE
       OR (SELECT deleted_at IS NOT NULL AND schedulable FROM pg_temp.accounts WHERE id=2) IS DISTINCT FROM TRUE THEN
        RAISE EXCEPTION 'lifecycle preservation failed';
    END IF;
    FOREACH statement IN ARRAY ARRAY[
        'UPDATE pg_temp.accounts SET deleted_at=NULL, schedulable=FALSE WHERE id=3',
        'UPDATE pg_temp.accounts SET schedulable=TRUE WHERE id=1',
        'UPDATE pg_temp.accounts SET schedulable=TRUE WHERE id=4',
        'INSERT INTO pg_temp.accounts VALUES (5,1,1,''openai'',0.42,0.4201234567,42,FALSE,NULL,NULL,NULL)',
        'INSERT INTO pg_temp.accounts VALUES (5,1,1,''openai'',0.42,0.4201234567,42,FALSE,NOW(),NULL,NULL)',
        'UPDATE pg_temp.accounts SET upstream_key_id=2 WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_key_id=3,upstream_config_id=2 WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_config_id=2 WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_config_id=1,upstream_key_id=1 WHERE id=8',
        'UPDATE pg_temp.accounts SET platform=''claude'' WHERE id=1',
        'UPDATE pg_temp.accounts SET rate_multiplier=0.88 WHERE id=1',
        'UPDATE pg_temp.accounts SET priority=88 WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_source_rate_multiplier=0.88 WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_source_rate_multiplier=NULL WHERE id=1',
        'UPDATE pg_temp.accounts SET upstream_source_rate_multiplier=0 WHERE id=7',
        'UPDATE pg_temp.accounts SET schedulable=FALSE,rate_multiplier=0.88 WHERE id=1',
        'UPDATE pg_temp.accounts SET deleted_at=NOW(),priority=88 WHERE id=1'
    ] LOOP
        rejected := FALSE;
        BEGIN
            EXECUTE statement;
        EXCEPTION WHEN check_violation THEN
            rejected := TRUE;
        END;
        IF NOT rejected THEN
            RAISE EXCEPTION 'unsafe lifecycle accepted';
        END IF;
    END LOOP;
    UPDATE pg_temp.upstream_keys SET deleted_at=NOW() WHERE id=1;
    UPDATE pg_temp.accounts SET deleted_at=NOW() WHERE id=1;
    UPDATE pg_temp.accounts SET schedulable=FALSE WHERE id=2;
    IF (SELECT COUNT(*) FROM pg_temp.accounts WHERE id IN (1,2)
        AND deleted_at IS NOT NULL AND rate_multiplier=0.42
        AND upstream_source_rate_multiplier=0.4201234567 AND priority=42) <> 2 THEN
        RAISE EXCEPTION 'deleted key archive preservation failed';
    END IF;
    INSERT INTO pg_temp.accounts VALUES (6,1,4,'openai',9,9,900,TRUE,NULL,NULL,NULL);
    UPDATE pg_temp.accounts SET schedulable=FALSE WHERE id=6;
    UPDATE pg_temp.accounts SET schedulable=TRUE WHERE id=6;
    UPDATE pg_temp.accounts SET deleted_at=NOW() WHERE id=6;
    UPDATE pg_temp.accounts SET deleted_at=NULL WHERE id=6;
    UPDATE pg_temp.accounts SET upstream_key_id=4 WHERE id=4;
    IF (SELECT COUNT(*) FROM pg_temp.accounts WHERE id IN (4,6) AND rate_multiplier=0
        AND upstream_source_rate_multiplier=0 AND priority=0 AND deleted_at IS NULL) <> 2
       OR (SELECT schedulable FROM pg_temp.accounts WHERE id=6) IS DISTINCT FROM TRUE THEN
        RAISE EXCEPTION 'zero actual rate failed';
    END IF;
END $$;
SELECT 'verified';
ROLLBACK;
SQL
  ) || fail vm_semantics
  [[ $result == verified ]] || fail vm_semantics
}

verify_schema
if [[ $phase != preflight || $migration_status == verified ]]; then
  verify_contract
fi
if [[ $phase == vm_semantics ]]; then
  verify_vm_semantics
fi
printf 'migration_285_%s=pass\n' "$phase"
