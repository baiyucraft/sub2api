#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 238 || $profile == 239 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1

query(){ docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }

schema_state=$(query "SELECT
  COUNT(*) FILTER (WHERE column_name='image_cost_routing_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default='false'),
  COUNT(*) FILTER (WHERE column_name='image_cost_routing_mode' AND data_type='character varying' AND is_nullable='NO' AND column_default LIKE '%prefer_lowest%'),
  COUNT(*) FILTER (WHERE column_name='image_cost_tolerance_percent' AND data_type='numeric' AND numeric_precision=5 AND numeric_scale=2 AND is_nullable='NO' AND column_default='5'),
  COUNT(*) FILTER (WHERE column_name='image_cost_stale_after_seconds' AND data_type='integer' AND is_nullable='NO' AND column_default='86400')
FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'") || exit 1

trigger_state=$(query "SELECT COUNT(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.proname='enqueue_group_auth_cache_invalidation' AND pg_get_functiondef(p.oid) LIKE '%image_cost_routing_enabled%'") || exit 1

# Persist the preflight/postflight status in the release recovery state, just
# like the other profile-specific migration assertions.  The downtime switch
# consumes this file before stopping the active application; omitting it makes
# a verified profile-238 preflight fail closed at the initialized stage.
printf '%s\n' "$migration_status" > "$state_dir/migration-237-status"
chmod 600 "$state_dir/migration-237-status"

if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $schema_state == '0|0|0|0' && $trigger_state == 0 ]] || exit 1
  printf 'migration_237_schema_state=absent\n'
else
  [[ $schema_state == '1|1|1|1' && $trigger_state == 1 ]] || exit 1
  printf 'migration_237_schema_state=verified\n'
fi
printf 'migration_237_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_237_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
