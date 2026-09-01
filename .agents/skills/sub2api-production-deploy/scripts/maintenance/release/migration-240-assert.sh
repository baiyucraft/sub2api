#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 240 || $profile == 241 || $profile == 242 || $profile == 243 || $profile == 244 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1

query() {
  docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 \
    -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"
}

schema_state=$(query "SELECT COUNT(*) FILTER (WHERE column_name='observation_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default IN ('true','true'::boolean::text)), COUNT(*) FILTER (WHERE column_name='observation_enabled' AND data_type='boolean' AND is_nullable='NO') FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_keys'")
index_state=$(query "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='upstream_keys' AND indexname IN ('idx_upstream_keys_observation_enabled','upstream_keys_observation_enabled_idx')")

printf '%s\n' "$migration_status" > "$state_dir/migration-240-status"
chmod 600 "$state_dir/migration-240-status"

if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $schema_state == '0|0' || $schema_state == '0|1' ]] || exit 1
  printf 'migration_240_schema_state=absent\n'
else
  [[ $schema_state == '1|1' ]] || exit 1
  printf 'migration_240_schema_state=verified\n'
fi
printf 'migration_240_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_240_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
