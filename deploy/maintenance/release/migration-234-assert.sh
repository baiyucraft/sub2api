#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
if [[ -n ${ASSERT_CONTEXT_FILE:-} ]]; then
  [[ -f $ASSERT_CONTEXT_FILE && ! -L $ASSERT_CONTEXT_FILE ]]
  source "$ASSERT_CONTEXT_FILE"
else
  source /opt/sub2api/releases/.active-release/assets/context.sh
fi
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
[[ $profile == 234 ]]
[[ $phase == preflight || $phase == postflight ]]
[[ $migration_status == absent || $migration_status == verified ]]

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

table_state=$(query "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='upstream_health_observations'")
[[ $table_state =~ ^[01]$ ]]
if [[ $migration_status == absent && $phase == preflight ]]; then
  [[ $table_state == 0 ]]
  printf 'migration_234_table_state=absent\n'
  printf 'migration_234_preflight=pass\n'
  exit 0
fi
[[ $table_state == 1 ]]

column_state=$(query "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_health_observations' AND column_name IN ('id','upstream_config_id','upstream_key_id','account_id','platform','model','protocol','source','state','result','reason','http_status','ttft_ms','duration_ms','input_tokens','output_tokens','output_tps','observed_at','created_at')")
[[ $column_state == 19 ]]
index_state=$(query "SELECT i.indisvalid,i.indisready,pg_get_indexdef(i.indexrelid) LIKE '%(upstream_key_id, observed_at)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='idx_upstream_health_observations_key_observed'")
[[ $index_state == 't|t|t' ]]
printf 'migration_234_table_state=verified\n'
printf 'migration_234_columns_verified=true\n'
printf 'migration_234_index_verified=true\n'
if [[ $phase == preflight ]]; then
  printf 'migration_234_preflight=pass\n'
else
  printf 'migration_234_postflight=pass\n'
fi
