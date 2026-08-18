#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 237 || $profile == 238 || $profile == 239 || $profile == 240 || $profile == 241 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
query(){ docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }
state=$(query "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_group_rollup_state' AND column_name='timezone_name'") || exit 1
if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $state == 0 ]] || exit 1
  printf 'migration_236_schema_state=absent\n'
else
  [[ $state == 1 ]] || exit 1
  printf 'migration_236_schema_state=verified\n'
fi
printf 'migration_236_%s=pass\n' "$phase"
