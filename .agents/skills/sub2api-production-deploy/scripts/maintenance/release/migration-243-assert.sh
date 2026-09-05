#!/usr/bin/env bash
set -Eeuo pipefail
phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 241 || $profile == 242 || $profile == 243 || $profile == 244 || $profile == 245 || $profile == 246 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1
query() { docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }
invalid=$(query "SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL AND platform='openai' AND type='oauth' AND COALESCE(extra->>'codex_fingerprint_mode','') IN ('device','session','full') AND (extra->>'codex_fingerprint_seed' IS NULL OR btrim(extra->>'codex_fingerprint_seed')='' OR NOT (extra->>'codex_fingerprint_seed' ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' AND extra->>'codex_fingerprint_seed' <> '00000000-0000-0000-0000-000000000000'))")
printf '%s\n' "$migration_status" > "$state_dir/migration-243-status"
chmod 600 "$state_dir/migration-243-status"
if [[ $phase == preflight && $migration_status == absent ]]; then
  printf 'migration_243_schema_state=absent\n'
else
  [[ $invalid == 0 ]] || exit 1
  printf 'migration_243_schema_state=verified\n'
fi
printf 'migration_243_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_243_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
