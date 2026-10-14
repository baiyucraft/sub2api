#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ $(id -u) == 0 ]] || exit 1

# Run only against the VM PostgreSQL container. Own and remove one fresh database.
root=${TEST_SOURCE_ROOT:-$(git rev-parse --show-toplevel)}
helper="$root/.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-297-assert.sh"
db="sub2api_v2_297_test_$$_${RANDOM}"
container=${TEST_DB_CONTAINER:-sub2api-postgres}
owner=${TEST_DB_USER:-sub2api}
work=$(mktemp -d)
exec 2>"$work/diagnostic.log"
trap 'printf "migration_297_postgres_integration=failed\n"' ERR
created=false
cleanup() {
  local result=$? cleanup_failed=false
  set +e
  if [[ $created == true && $db =~ ^sub2api_v2_297_test_[0-9]+_[0-9]+$ ]]; then
    docker exec "$container" dropdb -U "$owner" "$db" >/dev/null || cleanup_failed=true
  fi
  if [[ $result == 0 && $cleanup_failed == false ]]; then
    rm -rf -- "$work" || cleanup_failed=true
  else
    # Keep diagnostics on the VM only. This directory and its files are root-only.
    printf 'migration_297_postgres_evidence=%s\n' "$work"
  fi
  if [[ $cleanup_failed == true ]]; then
    printf 'migration_297_postgres_cleanup=failed\n'
    exit 1
  fi
  if [[ $result == 0 ]]; then
    printf 'migration_297_postgres_integration=pass\n'
  fi
  exit "$result"
}
trap cleanup EXIT
docker exec "$container" createdb -U "$owner" "$db"
created=true
server_version=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c 'SHOW server_version_num')
[[ $server_version =~ ^[0-9]+$ ]]
printf 'migration_297_postgres_version=%s\n' "$server_version"
sql() {
  docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c "$1" >/dev/null
}
sql 'CREATE TABLE accounts(id BIGINT, upstream_key_id BIGINT);
     CREATE TABLE schema_migrations(filename TEXT PRIMARY KEY, checksum TEXT NOT NULL)'
docker exec -i "$container" psql -X -q -v ON_ERROR_STOP=1 -U "$owner" -d "$db" \
  < "$root/backend/migrations/296_upstream_cache_guard.sql" >/dev/null
printf 'profile=269\n' > "$work/context.sh"
cat > "$work/manifest.json" <<'JSON'
{"schema":2,"profile":"269","parent_profile":"268","new_migrations":["297_upstream_cache_guard_account_scope.sql"],"migration_catalog":[{"filename":"297_upstream_cache_guard_account_scope.sql","checksum":"fee4af54727867a75c9984d81033db6ca8a2bc57145d7107b0a013f20e354b54"}]}
JSON
export ASSERT_CONTEXT_FILE="$work/context.sh" ASSERT_MANIFEST_FILE="$work/manifest.json"
export ASSERT_DB_CONTAINER="$container" ASSERT_DB_USER="$owner" ASSERT_DB_NAME="$db"
export ASSERT_VM_ISOLATED_DB="$db" ASSERT_MIGRATION_FILE="$root/backend/migrations/297_upstream_cache_guard_account_scope.sql"
expect_pass() {
  MIGRATION_STATUS="$2" bash "$helper" "$1" > "$work/result"
  [[ $(<"$work/result") == "migration_297_$1=pass" ]]
}
expect_failure() {
  if MIGRATION_STATUS=verified bash "$helper" verified_replay > "$work/result"; then
    exit 1
  fi
  [[ $(<"$work/result") == "migration_297_failure_code=$1" ]]
}
expect_pass preflight absent
sql 'ALTER TABLE upstream_cache_guard_facts ADD COLUMN upstream_key_id BIGINT'
if MIGRATION_STATUS=absent bash "$helper" preflight > "$work/result"; then exit 1; fi
[[ $(<"$work/result") == migration_297_failure_code=preflight_contract ]]
sql 'ALTER TABLE upstream_cache_guard_facts DROP COLUMN upstream_key_id'
docker exec -i "$container" psql -X -q -v ON_ERROR_STOP=1 -U "$owner" -d "$db" \
  < "$ASSERT_MIGRATION_FILE" >/dev/null
sql "INSERT INTO schema_migrations VALUES ('297_upstream_cache_guard_account_scope.sql','fee4af54727867a75c9984d81033db6ca8a2bc57145d7107b0a013f20e354b54')"
expect_pass preflight verified
expect_pass postflight verified
expect_pass verified_replay verified
expect_pass vm_semantics verified
# PostgreSQL 18 NOT NULL catalog rows must not admit extra business constraints.
sql 'ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT test_extra_unique UNIQUE(version)'
expect_failure constraint_contract
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT test_extra_unique'
sql 'CREATE UNIQUE INDEX test_standalone_unique ON upstream_cache_guard_accounts(version)'
expect_failure index_contract
sql 'DROP INDEX test_standalone_unique'
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_accounts_pkey;
     ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_accounts_pkey
       PRIMARY KEY(account_id,upstream_key_id) DEFERRABLE INITIALLY IMMEDIATE'
expect_failure constraint_contract
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_accounts_pkey;
     ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_accounts_pkey
       PRIMARY KEY(account_id,upstream_key_id)'
sql 'CREATE INDEX test_ordinary_index ON upstream_cache_guard_accounts(version)'
expect_pass verified_replay verified
sql 'DROP INDEX test_ordinary_index'
sql 'ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT test_extra_check CHECK(version < 100)'
expect_failure constraint_contract
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT test_extra_check'
# Exercise the real server syntax without inventing support on older PostgreSQL.
# A non-enforced CHECK may also be unvalidated; this is a rejection contract,
# not proof that conenforced was an independently exploitable omission.
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_account_positive'
if sql 'ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_account_positive
        CHECK(account_id > 0 AND upstream_key_id > 0 AND version > 0) NOT ENFORCED'; then
  expect_failure constraint_contract
  printf 'migration_297_not_enforced_check=rejected\n'
  sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_account_positive'
else
  printf 'migration_297_not_enforced_check=syntax_not_supported\n'
fi
sql 'ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_account_positive
     CHECK(account_id > 0 AND upstream_key_id > 0 AND version > 0)'
sql 'ALTER TABLE upstream_cache_guard_accounts ALTER COLUMN version SET DEFAULT 2'
expect_failure column_contract
sql 'ALTER TABLE upstream_cache_guard_accounts ALTER COLUMN version SET DEFAULT 1'
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_account_positive;
     ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_account_positive
       CHECK (account_id > 0 OR upstream_key_id > 0 OR version > 0)'
expect_failure constraint_contract
sql 'ALTER TABLE upstream_cache_guard_accounts DROP CONSTRAINT upstream_cache_guard_account_positive;
     ALTER TABLE upstream_cache_guard_accounts ADD CONSTRAINT upstream_cache_guard_account_positive
       CHECK (account_id > 0 AND upstream_key_id > 0 AND version > 0)'
sql 'DROP INDEX idx_upstream_cache_guard_account_history;
     CREATE INDEX idx_upstream_cache_guard_account_history ON upstream_cache_guard_facts
       (account_id,upstream_key_id,completed_at,group_id,evidence_id) WHERE processed=FALSE'
expect_failure index_contract
sql 'DROP INDEX idx_upstream_cache_guard_account_history;
     CREATE INDEX idx_upstream_cache_guard_account_history ON upstream_cache_guard_facts
       (account_id,upstream_key_id,completed_at,group_id,evidence_id) WHERE processed=TRUE'
sql "DROP INDEX idx_upstream_cache_guard_legacy_identity;
     CREATE INDEX idx_upstream_cache_guard_legacy_identity ON upstream_cache_guard_runtime
       ((state_json->>'account_id'),(state_json->>'upstream_key_id')) WHERE state_kind='group'"
expect_failure index_contract
sql "DROP INDEX idx_upstream_cache_guard_legacy_identity;
     CREATE INDEX idx_upstream_cache_guard_legacy_identity ON upstream_cache_guard_runtime
       ((state_json->>'account_id'),(state_json->>'upstream_key_id')) WHERE state_kind='account'"
expect_pass verified_replay verified
