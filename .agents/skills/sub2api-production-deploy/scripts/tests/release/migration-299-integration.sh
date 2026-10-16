#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ $(id -u) == 0 ]] || exit 1

# One fresh VM-only database. Never invoke this fixture against an application DB.
root=${TEST_SOURCE_ROOT:-$(git rev-parse --show-toplevel)}
helper="$root/.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-299-assert.sh"
migration="$root/backend/migrations/299_upstream_cache_guard_revisions.sql"
[[ -f $helper && ! -L $helper && -f $migration && ! -L $migration ]] || exit 1
db="sub2api_v2_299_test_$$_${RANDOM}"
container=${TEST_DB_CONTAINER:-sub2api-postgres}
owner=${TEST_DB_USER:-sub2api}
work=$(mktemp -d)
exec 2>"$work/diagnostic.log"
trap 'printf "migration_299_postgres_integration=failed\n"' ERR
created=false
cleanup() {
  local result=$? cleanup_failed=false
  set +e
  if [[ $created == true && $db =~ ^sub2api_v2_299_test_[0-9]+_[0-9]+$ ]]; then
    docker exec "$container" dropdb -U "$owner" "$db" >/dev/null || cleanup_failed=true
  fi
  if [[ $result == 0 && $cleanup_failed == false ]]; then
    rm -rf -- "$work" || cleanup_failed=true
  else
    # Preserve failed diagnostics only on the VM, in the root-only owned directory.
    printf 'migration_299_postgres_evidence=%s\n' "$work"
  fi
  if [[ $cleanup_failed == true ]]; then
    printf 'migration_299_postgres_cleanup=failed\n'
    exit 1
  fi
  if [[ $result == 0 ]]; then
    printf 'migration_299_postgres_cleanup=pass\n'
    printf 'migration_299_postgres_integration=pass\n'
  fi
  exit "$result"
}
trap cleanup EXIT
docker exec "$container" createdb -U "$owner" "$db"
created=true
server_version=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c 'SHOW server_version_num')
[[ $server_version =~ ^[0-9]+$ && $server_version -ge 180000 && $server_version -lt 190000 ]]
printf 'migration_299_postgres_version=%s\n' "$server_version"
sql() {
  docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c "$1" >/dev/null
}
sql 'CREATE TABLE upstream_cache_guard_facts(id BIGINT PRIMARY KEY,account_id BIGINT,upstream_key_id BIGINT,processed BOOLEAN,completed_at TIMESTAMPTZ);
     CREATE TABLE upstream_cache_guard_runtime(group_id BIGINT,state_kind VARCHAR(32),state_key TEXT,state_json JSONB,updated_at TIMESTAMPTZ);
     CREATE TABLE schema_migrations(filename TEXT PRIMARY KEY, checksum TEXT NOT NULL);
     INSERT INTO upstream_cache_guard_facts VALUES(1,1,10,TRUE,now())'
printf 'profile=271\n' > "$work/context.sh"
cat > "$work/manifest.json" <<'JSON'
{"schema":2,"profile":"271","parent_profile":"270","new_migrations":["299_upstream_cache_guard_revisions.sql"],"migration_catalog":[{"filename":"299_upstream_cache_guard_revisions.sql","checksum":"e9935004d96bc29d8b6687c59fc44d0ff7283beb344f1698ce61cf20f7cc2799"}]}
JSON
export ASSERT_CONTEXT_FILE="$work/context.sh" ASSERT_MANIFEST_FILE="$work/manifest.json"
export ASSERT_DB_CONTAINER="$container" ASSERT_DB_USER="$owner" ASSERT_DB_NAME="$db"
export ASSERT_VM_ISOLATED_DB="$db" ASSERT_MIGRATION_FILE="$migration"
expect_pass() {
  MIGRATION_STATUS="$2" bash "$helper" "$1" > "$work/result"
  [[ $(<"$work/result") == "migration_299_$1=pass" ]]
}
expect_failure() {
  if MIGRATION_STATUS=verified bash "$helper" verified_replay > "$work/result"; then exit 1; fi
  [[ $(<"$work/result") == "migration_299_failure_code=$1" ]]
}
expect_pass preflight absent
docker exec -i "$container" psql -X -q -v ON_ERROR_STOP=1 -U "$owner" -d "$db" < "$migration" >/dev/null
sql "INSERT INTO schema_migrations VALUES ('299_upstream_cache_guard_revisions.sql','e9935004d96bc29d8b6687c59fc44d0ff7283beb344f1698ce61cf20f7cc2799')"
expect_pass preflight verified
expect_pass postflight verified
expect_pass verified_replay verified
expect_pass vm_semantics verified
sql 'ALTER TABLE upstream_cache_guard_fact_versions ALTER COLUMN version SET DEFAULT 2'
expect_failure column_contract
sql 'ALTER TABLE upstream_cache_guard_fact_versions ALTER COLUMN version SET DEFAULT 1'
expect_pass verified_replay verified
sql 'ALTER TABLE upstream_cache_guard_runtime ALTER COLUMN has_pending_admissions SET DEFAULT TRUE'
expect_failure pending_column_contract
sql 'ALTER TABLE upstream_cache_guard_runtime ALTER COLUMN has_pending_admissions SET DEFAULT FALSE'
expect_pass verified_replay verified
sql 'ALTER TABLE upstream_cache_guard_facts DISABLE TRIGGER upstream_cache_guard_fact_revision'
expect_failure trigger_contract
sql 'ALTER TABLE upstream_cache_guard_facts ENABLE TRIGGER upstream_cache_guard_fact_revision'
expect_pass verified_replay verified
sql 'DROP INDEX idx_cache_guard_processed_account_time'
expect_failure index_contract
sql 'CREATE INDEX idx_cache_guard_processed_account_time ON upstream_cache_guard_facts(account_id,upstream_key_id,completed_at) WHERE processed=TRUE'
expect_pass verified_replay verified
