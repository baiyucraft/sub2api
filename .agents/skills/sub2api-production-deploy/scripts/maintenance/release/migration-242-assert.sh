#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 241 || $profile == 242 || $profile == 243 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
query() { docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }
constraint=$(query "SELECT COALESCE(pg_get_constraintdef(oid),'') FROM pg_constraint WHERE conrelid='user_platform_quotas'::regclass AND conname='user_platform_quotas_platform_check'")
printf '%s\n' "$migration_status" > "$state_dir/migration-242-status"
chmod 600 "$state_dir/migration-242-status"
if [[ $phase == preflight && $migration_status == absent ]]; then
  printf 'migration_242_schema_state=%s\n' "$([[ -z $constraint ]] && printf absent || printf present)"
else
  [[ $constraint == *kimi* && $constraint == *zhipu* && $constraint == *deepseek* ]] || exit 1
  printf 'migration_242_schema_state=verified\n'
fi
printf 'migration_242_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_242_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
