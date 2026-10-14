#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 270 ]] || exit 1
[[ $phase == preflight || $phase == postflight || $phase == verified_replay || $phase == vm_semantics ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
[[ $phase == preflight || $migration_status == verified ]] || exit 1
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
filename=298_group_scheduling_protection_policies.sql
catalog_checksum=ddbfe759a9fef717570ffe258edf13cd8409b576f427a91587a09eef4a4fe481
fail() { printf 'migration_298_failure_code=%s\n' "$1"; exit 1; }
query() {
  docker exec "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" \
    -c "BEGIN READ ONLY; SET LOCAL search_path = public, pg_catalog; $1; COMMIT;" 2>/dev/null
}
document=${ASSERT_MANIFEST_FILE:-${RELEASE_DIR:-${active_claim:-/opt/sub2api/releases/.active-release}}/gate.json}
[[ -f $document && ! -L $document ]] || fail manifest_missing
jq -e --arg filename "$filename" --arg checksum "$catalog_checksum" '
  (if has("manifest") then .manifest else . end) as $m |
  $m.schema == 2 and $m.profile == "270" and $m.parent_profile == "269" and
  $m.new_migrations == [$filename] and
  ([$m.migration_catalog[] | select(.filename == $filename)] | length) == 1 and
  ([$m.migration_catalog[] | select(.filename == $filename)][0].checksum == $checksum)
' "$document" >/dev/null 2>&1 || fail manifest_contract
result=$(query "SELECT to_regclass('public.groups') IS NOT NULL AND
  to_regclass('public.settings') IS NOT NULL AND to_regclass('public.fork_group_ttft_guard_policies') IS NOT NULL") || fail base_query
[[ $result == t ]] || fail base_contract
if [[ $phase == preflight && $migration_status == absent ]]; then
  result=$(query "SELECT to_regclass('public.fork_group_scheduling_protection_policies') IS NULL") || fail schema_query
  [[ $result == t ]] || fail partial_schema
else
  result=$(query "SELECT COUNT(*)=1 AND bool_and(checksum='$catalog_checksum') FROM public.schema_migrations WHERE filename='$filename'") || fail record_query
  [[ $result == t ]] || fail migration_record
  result=$(query "WITH expected(col,typ) AS (VALUES ('group_id','bigint'),('cache_mode','character varying(16)'),
    ('threshold_percent','double precision'),('recovery_threshold_percent','double precision'),
    ('min_valid_requests','integer'),('priority','character varying(16)'),
    ('created_at','timestamp with time zone'),('updated_at','timestamp with time zone'))
    SELECT COUNT(*)=8 AND bool_and(COALESCE(format_type(a.atttypid,a.atttypmod)=e.typ AND a.attnotnull,FALSE))
    FROM expected e LEFT JOIN pg_attribute a ON a.attrelid=to_regclass('public.fork_group_scheduling_protection_policies')
      AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped") || fail column_query
  [[ $result == t ]] || fail column_contract
  result=$(query "SELECT COUNT(*) FILTER(WHERE contype='c')=5 AND COUNT(*) FILTER(WHERE contype='p')=1 AND
    COUNT(*) FILTER(WHERE contype='f' AND confrelid='public.groups'::regclass AND confdeltype='c')=1 AND bool_and(convalidated)
    FROM pg_constraint WHERE conrelid='public.fork_group_scheduling_protection_policies'::regclass") || fail constraint_query
  [[ $result == t ]] || fail constraint_contract
fi
if [[ $phase == vm_semantics ]]; then
  migration_file=${ASSERT_MIGRATION_FILE:-}
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  [[ -f $migration_file && ! -L $migration_file ]] || fail migration_source
  [[ $(sha256sum "$migration_file" | awk '{print $1}') == 97ccd6842138d835393d61e719e84b4b17bd169e95d92053416e62411989b568 ]] || fail migration_source
  result=$({
    cat <<'SQL'
BEGIN;
SET LOCAL search_path = pg_temp, public, pg_catalog;
CREATE TEMP TABLE groups(id BIGINT PRIMARY KEY,deleted_at TIMESTAMPTZ) ON COMMIT DROP;
CREATE TEMP TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL) ON COMMIT DROP;
INSERT INTO groups(id) VALUES(1),(2),(3);
INSERT INTO settings VALUES('upstream_cache_guard_settings','{"mode":"protect","group_ids":[1,3]}');
SQL
    cat "$migration_file"
    cat <<'SQL'
DO $$ BEGIN
  IF (SELECT array_agg(cache_mode ORDER BY group_id) FROM pg_temp.fork_group_scheduling_protection_policies)
      IS DISTINCT FROM ARRAY['inherit','disabled','inherit']::varchar[] THEN
    RAISE EXCEPTION 'legacy execution scope migration failed';
  END IF;
  IF (SELECT value::jsonb->'group_ids' FROM pg_temp.settings) <> '[1,3]'::jsonb THEN
    RAISE EXCEPTION 'legacy rollback settings changed';
  END IF;
  UPDATE pg_temp.fork_group_scheduling_protection_policies SET cache_mode='enabled',priority='ttft_first' WHERE group_id=1;
END $$;
SQL
    cat "$migration_file"
    cat <<'SQL'
DO $$ DECLARE rejected BOOLEAN; BEGIN
  IF (SELECT cache_mode <> 'enabled' OR priority <> 'ttft_first' FROM pg_temp.fork_group_scheduling_protection_policies WHERE group_id=1) THEN
    RAISE EXCEPTION 'migration replay changed custom policy';
  END IF;
  rejected := FALSE;
  BEGIN UPDATE pg_temp.fork_group_scheduling_protection_policies SET threshold_percent=90,recovery_threshold_percent=80 WHERE group_id=1;
  EXCEPTION WHEN check_violation THEN rejected := TRUE; END;
  IF NOT rejected THEN RAISE EXCEPTION 'invalid cache thresholds accepted'; END IF;
END $$;
SELECT 'verified';
ROLLBACK;
SQL
  } | docker exec -i "$db_container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" 2>/dev/null) || fail vm_semantics
  [[ $result == verified ]] || fail vm_semantics
fi
printf 'migration_298_%s=pass\n' "$phase"
