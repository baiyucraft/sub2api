#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 239 || $profile == 240 || $profile == 241 ]] || exit 1
[[ $phase == preflight || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1

query(){ docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"; }

schema_state=$(query "SELECT
  COUNT(*) FILTER (WHERE column_name='upstream_lifecycle_owner' AND data_type='character varying' AND is_nullable='NO' AND column_default LIKE '%manual%'),
  COUNT(*) FILTER (WHERE column_name='upstream_archive_reason' AND data_type='character varying' AND is_nullable='YES')
FROM information_schema.columns WHERE table_schema='public' AND table_name='accounts'") || exit 1

constraint_state=$(query "SELECT
  COUNT(*) FILTER (WHERE conname='accounts_upstream_lifecycle_owner_check' AND pg_get_constraintdef(oid) LIKE '%sync_managed%'),
  COUNT(*) FILTER (WHERE conname='accounts_upstream_archive_reason_check' AND pg_get_constraintdef(oid) LIKE '%key_missing%')
FROM pg_constraint WHERE conrelid='public.accounts'::regclass") || exit 1

index_state=$(query "SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='accounts' AND indexname='idx_accounts_upstream_key_lifecycle_archive' AND indexdef LIKE '%upstream_lifecycle_owner%' AND indexdef LIKE '%upstream_archive_reason%'") || exit 1

printf '%s\n' "$migration_status" > "$state_dir/migration-238-status"
chmod 600 "$state_dir/migration-238-status"

if [[ $phase == preflight && $migration_status == absent ]]; then
  [[ $schema_state == '0|0' && $constraint_state == '0|0' && $index_state == 0 ]] || exit 1
  printf 'migration_238_schema_state=absent\n'
else
  invalid_rows=$(query "SELECT COUNT(*) FROM accounts WHERE upstream_lifecycle_owner NOT IN ('manual','sync_managed') OR (upstream_archive_reason IS NOT NULL AND upstream_archive_reason <> 'key_missing')") || exit 1
  [[ $schema_state == '1|1' && $constraint_state == '1|1' && $index_state == 1 && $invalid_rows == 0 ]] || exit 1
  printf 'migration_238_schema_state=verified\n'
fi
printf 'migration_238_preflight=%s\n' "$([[ $phase == preflight ]] && printf pass || printf not_applicable)"
printf 'migration_238_postflight=%s\n' "$([[ $phase == postflight ]] && printf pass || printf not_applicable)"
