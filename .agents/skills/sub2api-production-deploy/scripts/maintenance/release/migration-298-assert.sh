#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
context_file=${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}
[[ -f $context_file && ! -L $context_file ]] || exit 1
source "$context_file"
[[ $profile == 270 || $profile == 271 || $profile == 272 || $profile == 273 || $profile == 274 ]] || exit 1
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
  $m.schema == 2 and
  (($m.profile == "270" and $m.parent_profile == "269" and $m.new_migrations == [$filename]) or
   ($m.profile == "271" and $m.parent_profile == "270" and $m.new_migrations == ["299_upstream_cache_guard_revisions.sql"]) or
   ($m.profile == "272" and $m.parent_profile == "271" and $m.new_migrations == []) or
   ($m.profile == "273" and $m.parent_profile == "272" and $m.new_migrations == ["300_upstream_health_history_index.sql"]) or
   ($m.profile == "274" and $m.parent_profile == "273" and $m.new_migrations == ["301_upstream_cache_guard_admissions.sql"])) and
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
  result=$(query "WITH expected(col,typ,default_sql) AS (VALUES
    ('group_id','bigint',NULL),('cache_mode','character varying(16)','''inherit''::character varying'),
    ('threshold_percent','double precision','75'),('recovery_threshold_percent','double precision','80'),
    ('min_valid_requests','integer','20'),('priority','character varying(16)','''inherit''::character varying'),
    ('created_at','timestamp with time zone','now()'),('updated_at','timestamp with time zone','now()'))
    SELECT COUNT(*)=8 AND bool_and(COALESCE(format_type(a.atttypid,a.atttypmod)=e.typ AND a.attnotnull
      AND a.attidentity='' AND a.attgenerated=''
      AND pg_get_expr(d.adbin,d.adrelid) IS NOT DISTINCT FROM e.default_sql,FALSE))
      AND (SELECT COUNT(*)=8 FROM pg_attribute WHERE attrelid=to_regclass('public.fork_group_scheduling_protection_policies')
        AND attnum>0 AND NOT attisdropped)
      AND (SELECT relkind='r' AND relpersistence='p' AND NOT relispartition FROM pg_class
        WHERE oid=to_regclass('public.fork_group_scheduling_protection_policies'))
    FROM expected e LEFT JOIN pg_attribute a ON a.attrelid=to_regclass('public.fork_group_scheduling_protection_policies')
      AND a.attname=e.col AND a.attnum>0 AND NOT a.attisdropped
    LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum") || fail column_query
  [[ $result == t ]] || fail column_contract
  # Match installed public-schema semantics, not only constraint counts. PG18
  # adds contype=n rows; attnotnull above verifies those column requirements.
  # pg_dump/pg_restore reparses the two enum arrays into per-element text casts.
  # Accept only those exact definitions for those names; never normalize SQL.
  result=$(query "WITH expected(name,kind,keys,definition) AS (VALUES
    ('fork_group_scheduling_protection_policies_pkey','p',ARRAY['group_id']::text[],'PRIMARY KEY (group_id)'),
    ('fork_group_scheduling_protection_policies_group_id_fkey','f',ARRAY['group_id']::text[],
      'FOREIGN KEY (group_id) REFERENCES groups(id) ON DELETE CASCADE'),
    ('fork_group_scheduling_protection_policies_cache_mode_check','c',ARRAY['cache_mode']::text[],
      'CHECK (((cache_mode)::text = ANY ((ARRAY[''inherit''::character varying, ''enabled''::character varying, ''disabled''::character varying])::text[])))'),
    ('fork_group_scheduling_protection_polici_threshold_percent_check','c',ARRAY['threshold_percent']::text[],
      'CHECK (((threshold_percent >= (0)::double precision) AND (threshold_percent <= (100)::double precision)))'),
    ('fork_group_scheduling_protection_policies_check','c',ARRAY['recovery_threshold_percent','threshold_percent']::text[],
      'CHECK (((recovery_threshold_percent > threshold_percent) AND (recovery_threshold_percent <= (100)::double precision)))'),
    ('fork_group_scheduling_protection_polic_min_valid_requests_check','c',ARRAY['min_valid_requests']::text[],
      'CHECK (((min_valid_requests >= 1) AND (min_valid_requests <= 100000)))'),
    ('fork_group_scheduling_protection_policies_priority_check','c',ARRAY['priority']::text[],
      'CHECK (((priority)::text = ANY ((ARRAY[''inherit''::character varying, ''cache_first''::character varying, ''ttft_first''::character varying])::text[])))')),
    restored_expected(name,definition) AS (VALUES
    ('fork_group_scheduling_protection_policies_cache_mode_check',
      'CHECK (((cache_mode)::text = ANY (ARRAY[(''inherit''::character varying)::text, (''enabled''::character varying)::text, (''disabled''::character varying)::text])))'),
    ('fork_group_scheduling_protection_policies_priority_check',
      'CHECK (((priority)::text = ANY (ARRAY[(''inherit''::character varying)::text, (''cache_first''::character varying)::text, (''ttft_first''::character varying)::text])))')),
    actual AS (SELECT c.*, pg_get_constraintdef(c.oid) definition,
      (SELECT array_agg(a.attname::text ORDER BY x.ordinality)
        FROM unnest(c.conkey) WITH ORDINALITY x(attnum,ordinality)
        JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=x.attnum) keys,
      (SELECT array_agg(a.attname::text ORDER BY x.ordinality)
        FROM unnest(c.confkey) WITH ORDINALITY x(attnum,ordinality)
        JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=x.attnum) foreign_keys
      FROM pg_constraint c WHERE c.conrelid='public.fork_group_scheduling_protection_policies'::regclass)
    SELECT COUNT(*)=7 AND bool_and(COALESCE(a.contype::text=e.kind AND a.keys=e.keys
      AND (a.definition=e.definition OR a.definition=r.definition)
      AND a.convalidated AND COALESCE(to_jsonb(a)->>'conenforced','true')='true'
      AND NOT a.condeferrable AND NOT a.condeferred
      AND a.conislocal AND a.coninhcount=0
      AND (e.kind <> 'f' OR (a.confrelid='public.groups'::regclass AND a.foreign_keys=ARRAY['id']::text[]
        AND a.confdeltype='c' AND a.confupdtype='a' AND a.confmatchtype='s')),FALSE))
      AND (SELECT COUNT(*)=7 FROM actual WHERE contype<>'n')
      AND (SELECT COUNT(*)=0 FROM actual WHERE contype NOT IN ('p','f','c','n') OR NOT convalidated)
    FROM expected e LEFT JOIN actual a ON a.conname=e.name
    LEFT JOIN restored_expected r ON r.name=e.name") || fail constraint_query
  [[ $result == t ]] || fail constraint_contract
  # Standalone unique indexes do not appear in pg_constraint, but can reject
  # valid default policy rows. The original PK is the sole unique index.
  result=$(query "SELECT COUNT(*)=1 AND bool_and(x.indisprimary AND x.indisvalid AND x.indisready
      AND x.indnatts=1 AND x.indnkeyatts=1 AND x.indpred IS NULL AND x.indexprs IS NULL
      AND x.indoption::text='0' AND am.amname='btree'
      AND pg_get_indexdef(x.indexrelid,1,TRUE)='group_id'
      AND x.indexrelid=(SELECT conindid FROM pg_constraint
        WHERE conrelid=x.indrelid AND contype='p'))
    FROM pg_index x JOIN pg_class i ON i.oid=x.indexrelid JOIN pg_am am ON am.oid=i.relam
    WHERE x.indrelid='public.fork_group_scheduling_protection_policies'::regclass AND x.indisunique") || fail index_query
  [[ $result == t ]] || fail index_contract
fi
if [[ $phase == vm_semantics ]]; then
  migration_file=${ASSERT_MIGRATION_FILE:-}
  [[ $db_name =~ ^sub2api_v2_[A-Za-z0-9_]+$ && ${ASSERT_VM_ISOLATED_DB:-} == "$db_name" ]] || fail isolation_boundary
  [[ -f $migration_file && ! -L $migration_file ]] || fail migration_source
  _required_1=$(sha256sum "$migration_file" | awk '{print $1}') || fail migration_source
  [[ ${_required_1} == 97ccd6842138d835393d61e719e84b4b17bd169e95d92053416e62411989b568 ]] || fail migration_source
  result=$(query "SELECT current_database() = '$db_name'") || fail isolation_query
  [[ $result == t ]] || fail isolation_boundary
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
