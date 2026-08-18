#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 241 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
query() { docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }
state=$(query "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_model_pricing' AND column_name='time_pricing' AND data_type='jsonb'")
printf '%s\n' "$migration_status" > "$state_dir/migration-244-status"
chmod 600 "$state_dir/migration-244-status"
if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $state == 0 || $state == 1 ]] || exit 1
  printf 'migration_244_schema_state=%s\n' "$([[ $state == 0 ]] && printf absent || printf present)"
else
  [[ $state == 1 ]] || exit 1
  printf 'migration_244_schema_state=verified\n'
fi
printf 'migration_244_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_244_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
