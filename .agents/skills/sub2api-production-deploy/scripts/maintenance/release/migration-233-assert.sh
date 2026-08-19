#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
if [[ -n ${ASSERT_CONTEXT_FILE:-} ]]; then
  [[ -f $ASSERT_CONTEXT_FILE && ! -L $ASSERT_CONTEXT_FILE ]] || { printf 'migration_233_preflight=fail\nmigration_233_failure_code=context_or_state\n' >&2; exit 1; }
  source "$ASSERT_CONTEXT_FILE"
else
  source /opt/sub2api/releases/.active-release/assets/context.sh
fi
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
[[ $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 || $profile == 238 || $profile == 239 || $profile == 240 || $profile == 241 || $profile == 242 ]] || { printf 'migration_233_preflight=fail\nmigration_233_failure_code=context_or_state\n' >&2; exit 1; }
[[ $phase == preflight || $phase == postflight ]] || { printf 'migration_233_preflight=fail\nmigration_233_failure_code=context_or_state\n' >&2; exit 1; }
[[ $migration_status == absent || $migration_status == verified ]] || { printf 'migration_233_preflight=fail\nmigration_233_failure_code=context_or_state\n' >&2; exit 1; }

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}
fail() {
  local code=$1
  local failure_file="$release_dir/migration-233-failure"
  printf 'migration_233_failure_code=%s\n' "$code" > "$failure_file.tmp.$$"
  chmod 600 "$failure_file.tmp.$$"
  mv -T -- "$failure_file.tmp.$$" "$failure_file"
  printf 'migration_233_preflight=fail\n'
  printf 'migration_233_failure_code=%s\n' "$code"
  exit 1
}

duplicate_count=$(query "SELECT COUNT(*) FROM (SELECT upstream_key_id FROM accounts WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL GROUP BY upstream_key_id HAVING COUNT(*) > 1) duplicates") || fail permission
[[ $duplicate_count =~ ^[0-9]+$ ]] || fail duplicate_keys
[[ $duplicate_count == 0 ]] || fail duplicate_keys

table_state=$(query "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='upstream_health_observations'") || fail permission
[[ $table_state =~ ^[01]$ ]] || fail table_or_columns
printf '%s\n' "$migration_status" > "$state_dir/migration-233-status"
chmod 600 "$state_dir/migration-233-status" || fail permission

if [[ $migration_status == absent && $phase == preflight ]]; then
  [[ $table_state == 0 ]] || fail table_or_columns
  printf 'migration_233_duplicate_keys=%s\n' "$duplicate_count"
  printf 'migration_233_table_state=absent\n'
  printf 'migration_233_preflight=pass\n'
  exit 0
fi

[[ $table_state == 1 ]] || fail table_or_columns
index_state=$(query "SELECT i.indisvalid,i.indisready,regexp_replace(pg_get_expr(i.indpred,i.indrelid),'[()[:space:]]','','g')='upstream_key_idISNOTNULLANDdeleted_atISNULL',pg_get_indexdef(i.indexrelid) LIKE '%(upstream_key_id)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='idx_accounts_upstream_key_id_active'") || fail permission
[[ $index_state == 't|t|t|t' ]] || fail index
column_state=$(query "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_health_observations' AND column_name IN ('id','upstream_config_id','upstream_key_id','account_id','platform','model','protocol','source','state','result','reason','http_status','ttft_ms','duration_ms','input_tokens','output_tokens','output_tps','observed_at','created_at')") || fail permission
[[ $column_state == 19 ]] || fail table_or_columns
health_index_state=$(query "SELECT i.indisvalid,i.indisready,pg_get_indexdef(i.indexrelid) LIKE '%(upstream_key_id, observed_at)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='idx_upstream_health_observations_key_observed'") || fail permission
[[ $health_index_state == 't|t|t' ]] || fail index
privilege_state=$(query "SELECT has_table_privilege(current_user,'public.upstream_health_observations','SELECT,INSERT,UPDATE,DELETE'),has_sequence_privilege(current_user,'public.upstream_health_observations_id_seq','USAGE,SELECT')") || fail permission
[[ $privilege_state == 't|t' ]] || fail privilege
trigger_state=$(query "SELECT COUNT(*)=1,position('NEW.load_factor :=' IN pg_get_functiondef(p.oid))=0,position('upstream account concurrency cannot derive a safe load factor' IN pg_get_functiondef(p.oid))=0,position('NEW.priority := CEIL(key_actual_rate * 100)::INTEGER' IN pg_get_functiondef(p.oid))>0 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='validate_account_upstream_key_binding' GROUP BY p.oid") || fail permission
[[ $trigger_state == 't|t|t|t' ]] || fail trigger

printf 'migration_233_duplicate_keys=%s\n' "$duplicate_count"
printf 'migration_233_index_verified=true\n'
printf 'migration_233_table_state=verified\n'
printf 'migration_233_columns_verified=true\n'
printf 'migration_233_health_index_verified=true\n'
printf 'migration_233_privileges_verified=true\n'
printf 'migration_233_trigger_verified=true\n'
if [[ $phase == preflight ]]; then
  printf 'migration_233_preflight=pass\n'
else
  printf 'migration_233_postflight=pass\n'
fi
