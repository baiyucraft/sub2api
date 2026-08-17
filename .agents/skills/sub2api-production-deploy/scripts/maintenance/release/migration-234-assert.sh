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
[[ $profile == 235 || $profile == 236 || $profile == 237 || $profile == 238 || $profile == 239 ]]
[[ $phase == preflight || $phase == postflight ]]
[[ $migration_status == absent || $migration_status == verified ]]

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

schema_state=$(query "SELECT
  COUNT(*) FILTER (WHERE column_name='long_context_pricing_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default='true'),
  COUNT(*) FILTER (WHERE column_name='model_pricing' AND data_type='jsonb' AND is_nullable='YES'),
  COUNT(*) FILTER (WHERE column_name='long_context_pricing_enabled' AND ordinal_position < (SELECT ordinal_position FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name='model_pricing'))
FROM information_schema.columns
WHERE table_schema='public' AND table_name='groups'"
)
printf '%s\n' "$migration_status" > "$state_dir/migration-234-status"
chmod 600 "$state_dir/migration-234-status"

if [[ $migration_status == absent && $phase == preflight ]]; then
  [[ $schema_state == '0|0|0' ]]
  printf 'migration_234_schema_state=absent\n'
  printf 'migration_234_preflight=pass\n'
  exit 0
fi

[[ $schema_state == '1|1|1' ]]
printf 'migration_234_schema_state=verified\n'
printf 'migration_234_schema_verified=true\n'
if [[ $phase == preflight ]]; then
  printf 'migration_234_preflight=pass\n'
else
  printf 'migration_234_postflight=pass\n'
fi
