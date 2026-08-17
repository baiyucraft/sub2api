#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 240 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1

query() {
  docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 \
    -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"
}

# The precision migration intentionally keeps source_rate_multiplier intact;
# this assertion only verifies the public/effective fields can represent ten
# decimal places and that no negative values were introduced.
schema_state=$(query "SELECT COUNT(*) FILTER (WHERE table_name='upstream_keys' AND column_name='rate_multiplier' AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10), COUNT(*) FILTER (WHERE table_name='accounts' AND column_name='rate_multiplier' AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10), COUNT(*) FILTER (WHERE table_name='usage_logs' AND column_name='account_rate_multiplier' AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10) FROM information_schema.columns WHERE table_schema='public' AND ((table_name='upstream_keys' AND column_name='rate_multiplier') OR (table_name='accounts' AND column_name='rate_multiplier') OR (table_name='usage_logs' AND column_name='account_rate_multiplier'))")
invalid_values=$(query "SELECT (SELECT COUNT(*) FROM upstream_keys WHERE rate_multiplier < 0), (SELECT COUNT(*) FROM accounts WHERE rate_multiplier < 0), (SELECT COUNT(*) FROM usage_logs WHERE account_rate_multiplier < 0)")

printf '%s\n' "$migration_status" > "$release_dir/migration-241-status"
chmod 600 "$release_dir/migration-241-status"

if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $schema_state == '0|0|0' || $schema_state == '1|1|1' ]] || exit 1
  printf 'migration_241_schema_state=%s\n' "$([[ $schema_state == '0|0|0' ]] && printf absent || printf verified)"
else
  [[ $schema_state == '1|1|1' && $invalid_values == '0|0|0' ]] || exit 1
  printf 'migration_241_schema_state=verified\n'
fi
printf 'migration_241_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_241_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
