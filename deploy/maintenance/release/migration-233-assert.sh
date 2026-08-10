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
[[ $profile == 233 ]]
[[ $phase == preflight || $phase == postflight ]]
[[ $migration_status == absent || $migration_status == verified ]]

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

duplicate_count=$(query "SELECT COUNT(*) FROM (SELECT upstream_key_id FROM accounts WHERE upstream_key_id IS NOT NULL AND deleted_at IS NULL GROUP BY upstream_key_id HAVING COUNT(*) > 1) duplicates")
[[ $duplicate_count =~ ^[0-9]+$ ]]
[[ $duplicate_count == 0 ]]

if [[ $phase == preflight ]]; then
  printf '%s\n' "$migration_status" > "$state_dir/migration-233-status"
  chmod 600 "$state_dir/migration-233-status"
  printf 'migration_233_duplicate_keys=%s\n' "$duplicate_count"
  printf 'migration_233_preflight=pass\n'
  exit 0
fi

index_state=$(query "SELECT i.indisvalid,i.indisready,pg_get_expr(i.indpred,i.indrelid)='(upstream_key_id IS NOT NULL) AND (deleted_at IS NULL)',pg_get_indexdef(i.indexrelid) LIKE '%(upstream_key_id)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='idx_accounts_upstream_key_id_active'")
[[ $index_state == 't|t|t|t' ]]
printf 'migration_233_duplicate_keys=%s\n' "$duplicate_count"
printf 'migration_233_index_verified=true\n'
printf 'migration_233_postflight=pass\n'
