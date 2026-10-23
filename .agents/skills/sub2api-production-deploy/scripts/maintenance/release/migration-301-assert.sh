#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 274 || $profile == 275 ]] || exit 1
[[ $phase == preflight || $phase == postflight || $phase == verified_replay || $phase == vm_semantics ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ $phase == preflight || $migration_status == verified ]] || exit 1
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
filename=301_upstream_cache_guard_admissions.sql
catalog_checksum=b8922fdf7d91505e8341d90cfe592f177ca493083f674c53efce37f870584f2e
fail() { printf 'migration_301_failure_code=%s\n' "$1"; exit 1; }
query() {
  docker exec "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" \
    -c "BEGIN READ ONLY; SET LOCAL search_path = public, pg_catalog; $1; COMMIT;" 2>/dev/null
}
document=${ASSERT_MANIFEST_FILE:-${RELEASE_DIR:-${active_claim:-/opt/sub2api/releases/.active-release}}/gate.json}
[[ -f $document && ! -L $document ]] || fail manifest_missing
jq -e --arg filename "$filename" --arg checksum "$catalog_checksum" '
  (if has("manifest") then .manifest else . end) as $m |
  $m.schema == 2 and
  (($m.profile == "274" and $m.parent_profile == "273" and $m.new_migrations == [$filename]) or
   ($m.profile == "275" and $m.parent_profile == "274" and $m.new_migrations == ["302_enable_gemini_image_generation_groups.sql"])) and
  $m.deployment_mode == "downtime" and
  ([$m.migration_catalog[] | select(.filename == $filename)] | length) == 1 and
  ([$m.migration_catalog[] | select(.filename == $filename)][0].checksum == $checksum)
' "$document" >/dev/null 2>&1 || fail manifest_contract
result=$(query "SELECT to_regclass('public.upstream_cache_guard_runtime') IS NOT NULL") || fail base_query
[[ $result == t ]] || fail base_contract
if [[ $phase == preflight && $migration_status == absent ]]; then
  result=$(query "SELECT to_regclass('public.upstream_cache_guard_admissions') IS NULL") || fail schema_query
  [[ $result == t ]] || fail partial_schema
else
  result=$(query "SELECT COUNT(*)=1 AND bool_and(checksum='$catalog_checksum') FROM public.schema_migrations WHERE filename='$filename'") || fail record_query
  [[ $result == t ]] || fail migration_record
  result=$(query "WITH expected(col,type_name,default_sql) AS (VALUES
    ('group_id','bigint',NULL),('session_key','character varying(128)',NULL),('admission_key','character varying(128)',NULL),
    ('account_id','bigint',NULL),('upstream_key_id','bigint','0'),('route_key','character varying(128)','''''::character varying'),
    ('started_at','timestamp with time zone',NULL),('completed','boolean','false'),('admission_json','jsonb',NULL))
    SELECT COUNT(*)=9 AND bool_and(COALESCE(format_type(a.atttypid,a.atttypmod)=e.type_name AND a.attnotnull
      AND a.attidentity='' AND a.attgenerated='' AND pg_get_expr(d.adbin,d.adrelid) IS NOT DISTINCT FROM e.default_sql,FALSE))
      AND (SELECT COUNT(*)=9 FROM pg_attribute WHERE attrelid=to_regclass('public.upstream_cache_guard_admissions') AND attnum>0 AND NOT attisdropped)
      AND (SELECT relkind='r' AND relpersistence='p' AND NOT relispartition FROM pg_class WHERE oid=to_regclass('public.upstream_cache_guard_admissions'))
    FROM expected e LEFT JOIN pg_attribute a ON a.attrelid=to_regclass('public.upstream_cache_guard_admissions') AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped
    LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum") || fail column_query
  [[ $result == t ]] || fail column_contract
  result=$(query "WITH expected(name,kind,definition) AS (VALUES
    ('upstream_cache_guard_admissions_pkey','p','PRIMARY KEY (group_id, session_key, admission_key)'),
    ('upstream_cache_guard_admission_identity','c','CHECK (((group_id > 0) AND (account_id > 0) AND (upstream_key_id >= 0)))'),
    ('upstream_cache_guard_admission_object','c','CHECK ((jsonb_typeof(admission_json) = ''object''::text))'))
    SELECT COUNT(*)=3 AND bool_and(COALESCE(c.contype::text=e.kind AND c.convalidated AND COALESCE((to_jsonb(c)->>'conenforced')::BOOLEAN,TRUE) AND NOT c.condeferrable AND NOT c.condeferred
      AND pg_get_constraintdef(c.oid)=e.definition,FALSE))
      AND (SELECT COUNT(*)=3 FROM pg_constraint WHERE conrelid='public.upstream_cache_guard_admissions'::regclass AND contype<>'n')
      AND NOT EXISTS(SELECT 1 FROM pg_index WHERE indrelid='public.upstream_cache_guard_admissions'::regclass AND indisunique AND NOT indisprimary)
    FROM expected e LEFT JOIN pg_constraint c ON c.conrelid='public.upstream_cache_guard_admissions'::regclass AND c.conname=e.name") || fail constraint_query
  [[ $result == t ]] || fail constraint_contract
  result=$(query "WITH expected(name,keys,predicate) AS (VALUES
    ('idx_cache_guard_admission_session_pending','group_id, session_key, account_id, route_key, started_at','(completed = false)'),
    ('idx_cache_guard_admission_session_retained','group_id, session_key, started_at','(completed = true)'),
    ('idx_cache_guard_admission_pending_age','started_at, account_id, upstream_key_id','(completed = false)'),
    ('idx_cache_guard_admission_retention','started_at, group_id, session_key, admission_key','(completed = true)'),
    ('idx_cache_guard_admission_acknowledgement','account_id, upstream_key_id, admission_key','(completed = true)'))
    SELECT COUNT(*)=5 AND bool_and(COALESCE(x.indrelid='public.upstream_cache_guard_admissions'::regclass AND x.indisvalid AND x.indisready
      AND NOT x.indisunique AND am.amname='btree' AND pg_get_expr(x.indpred,x.indrelid)=e.predicate
      AND (SELECT string_agg(pg_get_indexdef(x.indexrelid,k,TRUE),', ' ORDER BY k) FROM generate_series(1,x.indnkeyatts) k)=e.keys,FALSE))
    FROM expected e LEFT JOIN pg_index x ON x.indexrelid=to_regclass('public.'||e.name)
    LEFT JOIN pg_class i ON i.oid=x.indexrelid LEFT JOIN pg_am am ON am.oid=i.relam") || fail index_query
  [[ $result == t ]] || fail index_contract
  result=$(query "SELECT NOT EXISTS(SELECT 1 FROM public.upstream_cache_guard_runtime WHERE state_kind='session' AND state_json ? 'admissions')") || fail ledger_query
  [[ $result == t ]] || fail unsplit_ledger
fi
if [[ $phase == vm_semantics ]]; then
  migration_file=${ASSERT_MIGRATION_FILE:-}
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  [[ -f $migration_file && ! -L $migration_file ]] || fail migration_source
  source_checksum=$(sha256sum "$migration_file" | awk '{print $1}') || fail migration_source
  [[ $source_checksum == ec83ff7020753185f382202e5b6926f1917a63462b0428b3f05cd37e095879ee ]] || fail migration_source
  result=$(query "SELECT current_database() = '$db_name'") || fail isolation_query
  [[ $result == t ]] || fail isolation_boundary
  result=$({
    cat <<'SQL'
BEGIN;
SET LOCAL search_path = pg_temp, public, pg_catalog;
CREATE TEMP TABLE upstream_cache_guard_runtime(group_id BIGINT,state_kind TEXT,state_key TEXT,state_json JSONB,version BIGINT,updated_at TIMESTAMPTZ) ON COMMIT DROP;
INSERT INTO upstream_cache_guard_runtime VALUES
 (22,'session','long',jsonb_build_object('first_seen_at','2026-10-16T00:00:00Z','natural_trial_ordinal',17,'routes',jsonb_build_object('route',jsonb_build_object('upstream_key_id',296)),
 'admissions',(SELECT jsonb_object_agg('r'||n,jsonb_build_object('account_id',13328,'started_at','2026-10-18T00:00:00Z','route_key','route','completed',n%2=0,'warm_at_start',n>1,'sample',true)) FROM generate_series(1,1650) n)),9,'2026-10-18T00:00:00Z'),
 (22,'session','unknown','{"admissions":{"r":{"account_id":1,"started_at":"2026-10-18T00:00:00Z","upstream_key_id":0}}}',3,'2026-10-17T00:00:00Z'),
 (22,'quota','quota','{"sequence":17}',4,'2026-10-17T00:00:00Z');
CREATE TEMP TABLE fixture_original ON COMMIT DROP AS SELECT * FROM upstream_cache_guard_runtime;
SQL
    cat "$migration_file"
    cat <<'SQL'
DO $$ BEGIN
  IF (SELECT count(*) FROM pg_temp.upstream_cache_guard_admissions) <> 1651 THEN RAISE EXCEPTION 'lost admissions'; END IF;
  IF EXISTS(SELECT 1 FROM pg_temp.upstream_cache_guard_runtime r JOIN fixture_original o USING(group_id,state_kind,state_key)
    WHERE r.state_json IS DISTINCT FROM CASE WHEN r.state_kind='session' THEN o.state_json-'admissions' ELSE o.state_json END
      OR r.updated_at IS DISTINCT FROM o.updated_at OR r.version <> o.version + CASE WHEN r.state_kind='session' THEN 1 ELSE 0 END) THEN RAISE EXCEPTION 'session identity changed'; END IF;
  IF (SELECT count(*) FROM pg_temp.upstream_cache_guard_admissions WHERE session_key='long' AND upstream_key_id=296) <> 1650 THEN RAISE EXCEPTION 'route provenance'; END IF;
  IF (SELECT upstream_key_id FROM pg_temp.upstream_cache_guard_admissions WHERE session_key='unknown') <> 0 THEN RAISE EXCEPTION 'invented key'; END IF;
  IF (SELECT count(*) FROM pg_temp.upstream_cache_guard_admissions WHERE completed) <> 825 THEN RAISE EXCEPTION 'pending lost'; END IF;
  BEGIN
    INSERT INTO pg_temp.upstream_cache_guard_admissions SELECT * FROM pg_temp.upstream_cache_guard_admissions LIMIT 1;
    RAISE EXCEPTION 'duplicate accepted';
  EXCEPTION WHEN unique_violation THEN NULL; END;
  BEGIN
    UPDATE pg_temp.upstream_cache_guard_admissions SET upstream_key_id=-1;
    RAISE EXCEPTION 'invalid identity accepted';
  EXCEPTION WHEN check_violation THEN NULL; END;
END $$;
SELECT 'verified';
ROLLBACK;
SQL
  } | docker exec -i "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" 2>/dev/null) || fail vm_semantics
  [[ $result == verified ]] || fail vm_semantics
fi
printf 'migration_301_%s=pass\n' "$phase"
