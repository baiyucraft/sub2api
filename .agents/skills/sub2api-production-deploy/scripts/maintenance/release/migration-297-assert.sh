#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 269 || $profile == 270 || $profile == 271 || $profile == 272 || $profile == 273 || $profile == 274 || $profile == 275 || $profile == 276 || $profile == 277 || $profile == 278 ]] || exit 1
[[ $phase == preflight || $phase == postflight || $phase == verified_replay || $phase == vm_semantics ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ $phase == preflight || $migration_status == verified ]] || exit 1

db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
filename=297_upstream_cache_guard_account_scope.sql
# The migration runner hashes trimmed SQL; the profile records the raw SHA.
catalog_checksum=fee4af54727867a75c9984d81033db6ca8a2bc57145d7107b0a013f20e354b54

fail() {
  printf 'migration_297_failure_code=%s\n' "$1"
  exit 1
}

query() {
  docker exec "$db_container" psql -X -q -A -t -F '|' -v ON_ERROR_STOP=1 \
    -U "$db_user" -d "$db_name" \
    -c "BEGIN READ ONLY; SET LOCAL search_path = public, pg_catalog; $1; COMMIT;" 2>/dev/null
}

verify_manifest() {
  local document=${ASSERT_MANIFEST_FILE:-${active_claim:-/opt/sub2api/releases/.active-release}/gate.json}
  [[ -f $document && ! -L $document ]] || fail manifest_missing
  jq -e --arg filename "$filename" --arg checksum "$catalog_checksum" '
    (if has("manifest") then .manifest else . end) as $m |
    $m.schema == 2 and (($m.profile == "269" and $m.parent_profile == "268" and $m.new_migrations == [$filename]) or
      ($m.profile == "270" and $m.parent_profile == "269" and $m.new_migrations == ["298_group_scheduling_protection_policies.sql"]) or
      ($m.profile == "271" and $m.parent_profile == "270" and $m.new_migrations == ["299_upstream_cache_guard_revisions.sql"]) or
      ($m.profile == "272" and $m.parent_profile == "271" and $m.new_migrations == []) or
   ($m.profile == "273" and $m.parent_profile == "272" and $m.new_migrations == ["300_upstream_health_history_index.sql"]) or
   ($m.profile == "274" and $m.parent_profile == "273" and $m.new_migrations == ["301_upstream_cache_guard_admissions.sql"]) or
   ($m.profile == "275" and $m.parent_profile == "274" and $m.new_migrations == ["302_enable_gemini_image_generation_groups.sql"]) or
   ($m.profile == "276" and $m.parent_profile == "275" and $m.new_migrations == ["303_group_strip_optional_image_tools.sql"]) or
   ($m.profile == "277" and $m.parent_profile == "276" and $m.new_migrations == ["304_upstream_confidence_fingerprint.sql"]) or
   ($m.profile == "278" and $m.parent_profile == "277" and $m.new_migrations == [])) and
    ([$m.migration_catalog[] | select(.filename == $filename)] | length) == 1 and
    ([$m.migration_catalog[] | select(.filename == $filename)][0].checksum == $checksum)
  ' "$document" >/dev/null 2>&1 || fail manifest_contract
}

verify_base() {
  local result
  result=$(query "WITH expected(rel, col, typ) AS (VALUES
    ('upstream_cache_guard_facts','group_id','bigint'),
    ('upstream_cache_guard_facts','evidence_id','character varying(200)'),
    ('upstream_cache_guard_facts','account_id','bigint'),
    ('upstream_cache_guard_facts','evidence_json','jsonb'),
    ('upstream_cache_guard_facts','processed','boolean'),
    ('upstream_cache_guard_runtime','group_id','bigint'),
    ('upstream_cache_guard_runtime','state_kind','character varying(16)'),
    ('upstream_cache_guard_runtime','state_json','jsonb'),
    ('accounts','id','bigint'),
    ('accounts','upstream_key_id','bigint'))
    SELECT COUNT(*)=10 AND bool_and(COALESCE(format_type(a.atttypid,a.atttypmod)=e.typ, FALSE))
    FROM expected e LEFT JOIN pg_namespace n ON n.nspname='public'
    LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=e.rel AND c.relkind='r'
    LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped") || fail base_query
  [[ $result == t ]] || fail base_contract
}

verify_migration_record() {
  local result
  result=$(query "SELECT COUNT(*)=1 AND bool_and(checksum='$catalog_checksum')
    FROM public.schema_migrations WHERE filename='$filename'") || fail record_query
  [[ $result == t ]] || fail record_contract
}

verify_columns() {
  local result
  result=$(query "WITH expected(rel,col,typ,required,default_sql) AS (VALUES
    ('upstream_cache_guard_facts','upstream_key_id','bigint',TRUE,'0'),
    ('upstream_cache_guard_accounts','account_id','bigint',TRUE,NULL),
    ('upstream_cache_guard_accounts','upstream_key_id','bigint',TRUE,NULL),
    ('upstream_cache_guard_accounts','version','bigint',TRUE,'1'),
    ('upstream_cache_guard_accounts','state_json','jsonb',TRUE,NULL),
    ('upstream_cache_guard_accounts','updated_at','timestamp with time zone',TRUE,'CURRENT_TIMESTAMP'))
    SELECT COUNT(*)=6 AND bool_and(COALESCE(
      format_type(a.atttypid,a.atttypmod)=e.typ AND a.attnotnull=e.required AND
      pg_get_expr(d.adbin,d.adrelid) IS NOT DISTINCT FROM e.default_sql,FALSE))
    FROM expected e LEFT JOIN pg_namespace n ON n.nspname='public'
    LEFT JOIN pg_class c ON c.relnamespace=n.oid AND c.relname=e.rel AND c.relkind='r'
    LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped
    LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum") || fail column_query
  [[ $result == t ]] || fail column_contract
  result=$(query "SELECT COUNT(*)=5 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relname='upstream_cache_guard_accounts'
      AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped") || fail column_query
  [[ $result == t ]] || fail column_contract
}

verify_constraints() {
  local result
  # PostgreSQL 18 exposes column NOT NULL constraints as contype=n rows.
  # Keep exactly the three PK/CHECK contracts and reject every other kind;
  # column-level attnotnull is verified independently in verify_columns.
  result=$(query "SELECT COUNT(*) FILTER (WHERE contype IN ('p','c'))=3 AND
    COUNT(*) FILTER (WHERE contype NOT IN ('p','c','n') OR NOT convalidated
      OR NOT COALESCE((to_jsonb(pg_constraint)->>'conenforced')::boolean,TRUE))=0 AND
    COUNT(*) FILTER (WHERE conname='upstream_cache_guard_accounts_pkey' AND contype='p' AND convalidated
      AND NOT condeferrable AND NOT condeferred
      AND (SELECT array_agg(a.attname::text ORDER BY x.ordinality)
           FROM unnest(conkey) WITH ORDINALITY x(attnum,ordinality)
           JOIN pg_attribute a ON a.attrelid=conrelid AND a.attnum=x.attnum)
        = ARRAY['account_id','upstream_key_id']::text[]) = 1 AND
    COUNT(*) FILTER (WHERE conname='upstream_cache_guard_account_positive' AND contype='c' AND convalidated
      AND pg_get_expr(conbin,conrelid) = '((account_id > 0) AND (upstream_key_id > 0) AND (version > 0))') = 1 AND
    COUNT(*) FILTER (WHERE conname='upstream_cache_guard_account_object' AND contype='c' AND convalidated
      AND pg_get_expr(conbin,conrelid) = '(jsonb_typeof(state_json) = ''object''::text)') = 1
    FROM pg_constraint WHERE conrelid='public.upstream_cache_guard_accounts'::regclass") || fail constraint_query
  [[ $result == t ]] || fail constraint_contract
}

verify_indexes() {
  local result
  result=$(query "WITH expected(name,table_name,keys,predicate) AS (VALUES
    ('idx_upstream_cache_guard_account_history','upstream_cache_guard_facts',
      ARRAY['account_id','upstream_key_id','completed_at','group_id','evidence_id']::text[],'(processed = true)'),
    ('idx_upstream_cache_guard_legacy_identity','upstream_cache_guard_runtime',
      ARRAY['(state_json ->> ''account_id''::text)','(state_json ->> ''upstream_key_id''::text)']::text[],'((state_kind)::text = ''account''::text)')),
    actual AS (SELECT i.relname name,t.relname table_name,x.indisvalid,x.indisready,x.indisunique,
      ARRAY(SELECT pg_get_indexdef(i.oid,k,TRUE) FROM generate_series(1,x.indnkeyatts) k ORDER BY k) keys,
      pg_get_expr(x.indpred,x.indrelid) predicate,
      x.indnatts=x.indnkeyatts no_included_columns, x.indoption::text options, am.amname method
      FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid
      JOIN pg_am am ON am.oid=i.relam
      JOIN pg_class t ON t.oid=x.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace
      WHERE n.nspname='public' AND i.relname IN
        ('idx_upstream_cache_guard_account_history','idx_upstream_cache_guard_legacy_identity'))
    SELECT COUNT(*)=2 AND bool_and(COALESCE(a.table_name=e.table_name AND a.indisvalid AND a.indisready
      AND NOT a.indisunique AND a.no_included_columns AND a.method='btree'
      AND a.options=(repeat('0 ',cardinality(e.keys)-1)||'0')
      AND a.keys=e.keys AND a.predicate=e.predicate,FALSE))
    FROM expected e LEFT JOIN actual a ON a.name=e.name") || fail index_query
  [[ $result == t ]] || fail index_contract
  # A standalone UNIQUE index is absent from pg_constraint but still restricts
  # inserts. Only the immutable, immediate account/Key primary key may be unique.
  # Additional non-unique performance indexes do not alter this row contract.
  result=$(query "SELECT COUNT(*) FILTER (WHERE x.indisunique)=1 AND
    COUNT(*) FILTER (WHERE x.indisprimary AND x.indisunique AND x.indisvalid AND x.indisready
      AND NOT x.indisexclusion AND x.indnatts=2 AND x.indnkeyatts=2
      AND x.indexprs IS NULL AND x.indpred IS NULL AND x.indoption::text='0 0'
      AND am.amname='btree' AND i.relname='upstream_cache_guard_accounts_pkey'
      AND (SELECT array_agg(a.attname::text ORDER BY k.ordinality)
           FROM unnest(x.indkey::smallint[]) WITH ORDINALITY k(attnum,ordinality)
           JOIN pg_attribute a ON a.attrelid=x.indrelid AND a.attnum=k.attnum)
        = ARRAY['account_id','upstream_key_id']::text[])=1
    FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid
    JOIN pg_am am ON am.oid=i.relam
    WHERE x.indrelid='public.upstream_cache_guard_accounts'::regclass") || fail index_query
  [[ $result == t ]] || fail index_contract
}

verify_backfill() {
  local result
  # CASE prevents a malformed or overflowing JSON string from ever being cast.
  result=$(query "WITH facts AS (
    SELECT upstream_key_id,
      CASE WHEN evidence_json->'request'->>'upstream_key_id' ~ '^[1-9][0-9]{0,18}$'
        AND (length(evidence_json->'request'->>'upstream_key_id') < 19
          OR evidence_json->'request'->>'upstream_key_id' <= '9223372036854775807')
      THEN (evidence_json->'request'->>'upstream_key_id')::bigint ELSE 0 END expected_key
    FROM public.upstream_cache_guard_facts)
    SELECT COUNT(*) FILTER (WHERE upstream_key_id IS DISTINCT FROM expected_key)=0 FROM facts") || fail backfill_query
  [[ $result == t ]] || fail backfill_contract
}

verify_vm_semantics() {
  local result migration_file=${ASSERT_MIGRATION_FILE:-}
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  [[ -f $migration_file && ! -L $migration_file ]] || fail migration_source
  _required_1=$(sha256sum "$migration_file" | awk '{print $1}') || fail migration_source
  [[ ${_required_1} == a17cdd895a2fd8306eebc4d21c4c93ae22defaa93f9e54e2e7b8208ed2725244 ]] || fail migration_source
  result=$(query "SELECT current_database() = '$db_name'") || fail isolation_query
  [[ $result == t ]] || fail isolation_boundary
  # Re-execute the immutable migration only on transaction-local temporary tables.
  # The restored public tables and their rows are never written by this fixture.
  result=$({
    cat <<'SQL' || exit 1
BEGIN;
SET LOCAL search_path = pg_temp, public, pg_catalog;
CREATE TEMP TABLE upstream_cache_guard_facts
    (LIKE public.upstream_cache_guard_facts INCLUDING DEFAULTS INCLUDING CONSTRAINTS) ON COMMIT DROP;
ALTER TABLE pg_temp.upstream_cache_guard_facts DROP COLUMN upstream_key_id;
CREATE TEMP TABLE upstream_cache_guard_runtime
    (LIKE public.upstream_cache_guard_runtime INCLUDING DEFAULTS INCLUDING CONSTRAINTS) ON COMMIT DROP;
INSERT INTO pg_temp.upstream_cache_guard_facts
    (group_id,evidence_id,account_id,completed_at,processed,evidence_json)
SELECT 1, ordinal::text, 1, CURRENT_TIMESTAMP, TRUE, evidence
FROM (VALUES
    (1,'{"request":{"upstream_key_id":"1"}}'::jsonb),
    (2,'{"request":{"upstream_key_id":"9223372036854775807"}}'::jsonb),
    (3,'{"request":{"upstream_key_id":"9223372036854775808"}}'::jsonb),
    (4,'{"request":{"upstream_key_id":"99999999999999999999"}}'::jsonb),
    (5,'{"request":{"upstream_key_id":"0"}}'::jsonb),
    (6,'{"request":{"upstream_key_id":"-1"}}'::jsonb),
    (7,'{"request":{"upstream_key_id":"01"}}'::jsonb),
    (8,'{"request":{"upstream_key_id":"bad"}}'::jsonb),
    (9,'{}'::jsonb), (10,'{"request":{"upstream_key_id":42}}'::jsonb)
) AS fixture(ordinal,evidence);
SQL
    cat "$migration_file" || exit 1
    cat <<'SQL'
DO $$
DECLARE statement TEXT; rejected BOOLEAN;
BEGIN
    IF to_regclass('pg_temp.upstream_cache_guard_accounts') IS NULL
       OR (SELECT array_agg(upstream_key_id ORDER BY evidence_id::integer)
           FROM pg_temp.upstream_cache_guard_facts)
          IS DISTINCT FROM ARRAY[1,9223372036854775807,0,0,0,0,0,0,0,42]::bigint[] THEN
        RAISE EXCEPTION 'account identity backfill failed';
    END IF;
    INSERT INTO pg_temp.upstream_cache_guard_accounts(account_id,upstream_key_id,state_json)
        VALUES (1,1,'{}'),(1,2,'{}'),(2,1,'{}');
    IF (SELECT COUNT(*) FROM pg_temp.upstream_cache_guard_accounts
        WHERE version=1 AND updated_at=CURRENT_TIMESTAMP) <> 3 THEN
        RAISE EXCEPTION 'account state defaults failed';
    END IF;
    FOREACH statement IN ARRAY ARRAY[
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (0,1,1,''{}'',CURRENT_TIMESTAMP)',
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (3,0,1,''{}'',CURRENT_TIMESTAMP)',
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (3,1,0,''{}'',CURRENT_TIMESTAMP)',
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (3,1,1,''[]'',CURRENT_TIMESTAMP)',
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (3,1,1,NULL,CURRENT_TIMESTAMP)',
        'INSERT INTO pg_temp.upstream_cache_guard_accounts VALUES (1,1,1,''{}'',CURRENT_TIMESTAMP)'
    ] LOOP
        rejected := FALSE;
        BEGIN EXECUTE statement;
        EXCEPTION WHEN check_violation OR not_null_violation OR unique_violation THEN rejected := TRUE;
        END;
        IF NOT rejected THEN RAISE EXCEPTION 'invalid account state accepted'; END IF;
    END LOOP;
END $$;
SELECT 'verified';
ROLLBACK;
SQL
  } | docker exec -i "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" 2>/dev/null) || fail vm_semantics
  [[ $result == verified ]] || fail vm_semantics
}

verify_manifest
verify_base
if [[ $phase != preflight || $migration_status == verified ]]; then
  verify_migration_record
  verify_columns
  verify_constraints
  verify_indexes
  verify_backfill
else
  # A pending migration must not inherit a partial account-scope schema.
  result=$(query "SELECT to_regclass('public.upstream_cache_guard_accounts') IS NULL AND
    NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='public.upstream_cache_guard_facts'::regclass
      AND attname='upstream_key_id' AND attnum>0 AND NOT attisdropped)") || fail preflight_query
  [[ $result == t ]] || fail preflight_contract
fi
if [[ $phase == vm_semantics ]]; then
  verify_vm_semantics
fi
printf 'migration_297_%s=pass\n' "$phase"
