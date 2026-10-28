#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 271 || $profile == 272 || $profile == 273 || $profile == 274 || $profile == 275 || $profile == 276 || $profile == 277 || $profile == 278 ]] || exit 1
[[ $phase == preflight || $phase == postflight || $phase == verified_replay || $phase == vm_semantics ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ $phase == preflight || $migration_status == verified ]] || exit 1
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
filename=299_upstream_cache_guard_revisions.sql
catalog_checksum=e9935004d96bc29d8b6687c59fc44d0ff7283beb344f1698ce61cf20f7cc2799
fail() { printf 'migration_299_failure_code=%s\n' "$1"; exit 1; }
query() {
  docker exec "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" \
    -c "BEGIN READ ONLY; SET LOCAL search_path = public, pg_catalog; $1; COMMIT;" 2>/dev/null
}
document=${ASSERT_MANIFEST_FILE:-${RELEASE_DIR:-${active_claim:-/opt/sub2api/releases/.active-release}}/gate.json}
[[ -f $document && ! -L $document ]] || fail manifest_missing
jq -e --arg filename "$filename" --arg checksum "$catalog_checksum" '
  (if has("manifest") then .manifest else . end) as $m |
  $m.schema == 2 and
  (($m.profile == "271" and $m.parent_profile == "270" and $m.new_migrations == [$filename]) or
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
result=$(query "SELECT to_regclass('public.upstream_cache_guard_runtime') IS NOT NULL AND
  to_regclass('public.upstream_cache_guard_facts') IS NOT NULL") || fail base_query
[[ $result == t ]] || fail base_contract
if [[ $phase == preflight && $migration_status == absent ]]; then
  result=$(query "SELECT to_regclass('public.upstream_cache_guard_fact_versions') IS NULL
    AND NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='public.upstream_cache_guard_runtime'::regclass
      AND attname='has_pending_admissions' AND NOT attisdropped)
    AND to_regprocedure('public.upstream_cache_guard_advance_fact_version()') IS NULL
    AND to_regprocedure('public.upstream_cache_guard_pending_admissions()') IS NULL") || fail schema_query
  [[ $result == t ]] || fail partial_schema
else
  result=$(query "SELECT COUNT(*)=1 AND bool_and(checksum='$catalog_checksum') FROM public.schema_migrations WHERE filename='$filename'") || fail record_query
  [[ $result == t ]] || fail migration_record
  result=$(query "WITH expected(col,default_sql) AS (VALUES ('account_id',NULL),('upstream_key_id',NULL),('version','1'))
    SELECT COUNT(*)=3 AND bool_and(COALESCE(format_type(a.atttypid,a.atttypmod)='bigint' AND a.attnotnull
      AND a.attidentity='' AND a.attgenerated='' AND pg_get_expr(d.adbin,d.adrelid) IS NOT DISTINCT FROM e.default_sql,FALSE))
      AND (SELECT COUNT(*)=3 FROM pg_attribute WHERE attrelid=to_regclass('public.upstream_cache_guard_fact_versions') AND attnum>0 AND NOT attisdropped)
      AND (SELECT relkind='r' AND relpersistence='p' AND NOT relispartition FROM pg_class WHERE oid=to_regclass('public.upstream_cache_guard_fact_versions'))
    FROM expected e LEFT JOIN pg_attribute a ON a.attrelid=to_regclass('public.upstream_cache_guard_fact_versions') AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped
    LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum") || fail column_query
  [[ $result == t ]] || fail column_contract
  result=$(query "SELECT COUNT(*)=1 AND bool_and(a.atttypid='boolean'::regtype AND a.attnotnull AND pg_get_expr(d.adbin,d.adrelid)='false')
    FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
    WHERE a.attrelid='public.upstream_cache_guard_runtime'::regclass AND a.attname='has_pending_admissions' AND NOT a.attisdropped") || fail pending_column_query
  [[ $result == t ]] || fail pending_column_contract
  result=$(query "SELECT COUNT(*)=1 AND bool_and(c.contype='p' AND c.convalidated AND NOT c.condeferrable AND NOT c.condeferred
      AND pg_get_constraintdef(c.oid)='PRIMARY KEY (account_id, upstream_key_id)')
    FROM pg_constraint c WHERE c.conrelid='public.upstream_cache_guard_fact_versions'::regclass AND c.contype<>'n'") || fail key_query
  [[ $result == t ]] || fail key_contract
  result=$(query "WITH expected(name,body) AS (VALUES
    ('upstream_cache_guard_advance_fact_version','868570e268bf52742277627ac8399fc6'),
    ('upstream_cache_guard_pending_admissions','99c6cfb81e5ce1ff33b6b89ce0bb5560'))
    SELECT COUNT(*)=2 AND bool_and(COALESCE(md5(p.prosrc)=e.body AND p.prorettype='trigger'::regtype
      AND p.pronargs=0 AND l.lanname='plpgsql' AND NOT p.prosecdef AND p.proconfig IS NULL,FALSE))
    FROM expected e LEFT JOIN pg_proc p ON p.oid=to_regprocedure('public.'||e.name||'()') LEFT JOIN pg_language l ON l.oid=p.prolang") || fail function_query
  [[ $result == t ]] || fail function_contract
  result=$(query "WITH expected(name,table_name,function_name,kind,column_name) AS (VALUES
    ('upstream_cache_guard_fact_revision','upstream_cache_guard_facts','upstream_cache_guard_advance_fact_version',29,'processed'),
    ('upstream_cache_guard_pending_sessions','upstream_cache_guard_runtime','upstream_cache_guard_pending_admissions',23,'state_json'))
    SELECT COUNT(*)=2 AND bool_and(COALESCE(t.tgfoid=to_regprocedure('public.'||e.function_name||'()') AND t.tgtype=e.kind
      AND t.tgenabled='O' AND NOT t.tgisinternal AND t.tgqual IS NULL AND t.tgnargs=0
      AND t.tgattr::text=a.attnum::text,FALSE))
    FROM expected e LEFT JOIN pg_trigger t ON t.tgrelid=to_regclass('public.'||e.table_name) AND t.tgname=e.name
    LEFT JOIN pg_attribute a ON a.attrelid=t.tgrelid AND a.attname=e.column_name AND NOT a.attisdropped") || fail trigger_query
  [[ $result == t ]] || fail trigger_contract
  result=$(query "WITH expected(name,table_name,keys,predicate) AS (VALUES
    ('idx_cache_guard_runtime_active_sessions','upstream_cache_guard_runtime','updated_at, group_id, state_key','((state_kind)::text = ''session''::text)'),
    ('idx_cache_guard_processed_account_time','upstream_cache_guard_facts','account_id, upstream_key_id, completed_at','(processed = true)'),
    ('idx_cache_guard_runtime_pending_sessions','upstream_cache_guard_runtime','group_id, state_key','(((state_kind)::text = ''session''::text) AND (has_pending_admissions = true))'))
    SELECT COUNT(*)=3 AND bool_and(COALESCE(x.indrelid=to_regclass('public.'||e.table_name) AND x.indisvalid AND x.indisready
      AND NOT x.indisunique AND am.amname='btree' AND regexp_replace(pg_get_expr(x.indpred,x.indrelid),'[()[:space:]]','','g')=regexp_replace(e.predicate,'[()[:space:]]','','g')
      AND (SELECT string_agg(pg_get_indexdef(x.indexrelid,k,TRUE),', ' ORDER BY k) FROM generate_series(1,x.indnkeyatts) k)=e.keys,FALSE))
    FROM expected e LEFT JOIN pg_index x ON x.indexrelid=to_regclass('public.'||e.name)
    LEFT JOIN pg_class i ON i.oid=x.indexrelid LEFT JOIN pg_am am ON am.oid=i.relam") || fail index_query
  [[ $result == t ]] || fail index_contract
fi
if [[ $phase == vm_semantics ]]; then
  migration_file=${ASSERT_MIGRATION_FILE:-}
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  [[ -f $migration_file && ! -L $migration_file ]] || fail migration_source
  _required_1=$(sha256sum "$migration_file" | awk '{print $1}') || fail migration_source
  [[ ${_required_1} == 203ff042c1129f24549c696d6fdcb0dc27b0fedcff8f7e61413520b4bf9cc856 ]] || fail migration_source
  result=$(query "SELECT current_database() = '$db_name'") || fail isolation_query
  [[ $result == t ]] || fail isolation_boundary
  result=$({
    cat <<'SQL'
BEGIN;
SET LOCAL search_path = pg_temp, public, pg_catalog;
CREATE TEMP TABLE upstream_cache_guard_facts(id BIGINT PRIMARY KEY,account_id BIGINT,upstream_key_id BIGINT,processed BOOLEAN,completed_at TIMESTAMPTZ) ON COMMIT DROP;
CREATE TEMP TABLE upstream_cache_guard_runtime(group_id BIGINT,state_kind TEXT,state_key TEXT,state_json JSONB,updated_at TIMESTAMPTZ) ON COMMIT DROP;
INSERT INTO upstream_cache_guard_facts VALUES(1,1,10,TRUE,now());
INSERT INTO upstream_cache_guard_runtime VALUES(1,'session','a','{"admissions":{"first":{"completed":false}}}',now());
SQL
    cat "$migration_file"
    cat <<'SQL'
DO $$ BEGIN
  IF (SELECT version FROM pg_temp.upstream_cache_guard_fact_versions WHERE account_id=1 AND upstream_key_id=10) <> 1 THEN RAISE EXCEPTION 'backfill'; END IF;
  IF NOT (SELECT has_pending_admissions FROM pg_temp.upstream_cache_guard_runtime WHERE state_key='a') THEN RAISE EXCEPTION 'pending backfill'; END IF;
END $$;
INSERT INTO upstream_cache_guard_facts VALUES(2,1,10,FALSE,now());
UPDATE upstream_cache_guard_facts SET processed=TRUE WHERE id=2;
DELETE FROM upstream_cache_guard_facts WHERE id=2;
UPDATE upstream_cache_guard_runtime SET state_json='{"admissions":{"first":{"completed":true}}}' WHERE state_key='a';
INSERT INTO upstream_cache_guard_runtime VALUES(1,'session','null','{"admissions":null}',now(),FALSE);
DO $$ BEGIN
  IF (SELECT version FROM pg_temp.upstream_cache_guard_fact_versions WHERE account_id=1 AND upstream_key_id=10) <> 4 THEN RAISE EXCEPTION 'insert/completion/delete revision'; END IF;
  IF EXISTS(SELECT 1 FROM pg_temp.upstream_cache_guard_runtime WHERE has_pending_admissions) THEN RAISE EXCEPTION 'pending completion'; END IF;
END $$;
SQL
    cat "$migration_file"
    cat <<'SQL'
DO $$ BEGIN
  IF (SELECT version FROM pg_temp.upstream_cache_guard_fact_versions WHERE account_id=1 AND upstream_key_id=10) <> 4 THEN RAISE EXCEPTION 'replay reset'; END IF;
END $$;
SELECT 'verified';
ROLLBACK;
SQL
  } | docker exec -i "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" 2>/dev/null) || fail vm_semantics
  [[ $result == verified ]] || fail vm_semantics
fi
printf 'migration_299_%s=pass\n' "$phase"
