#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
[[ $(id -u) == 0 ]] || exit 1

# One fresh VM-only database. Never invoke this fixture against an application DB.
root=${TEST_SOURCE_ROOT:-$(git rev-parse --show-toplevel)}
helper="$root/.agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-298-assert.sh"
migration="$root/backend/migrations/298_group_scheduling_protection_policies.sql"
[[ -f $helper && ! -L $helper && -f $migration && ! -L $migration ]] || exit 1
db="sub2api_v2_298_test_$$_${RANDOM}"
container=${TEST_DB_CONTAINER:-sub2api-postgres}
owner=${TEST_DB_USER:-sub2api}
work=$(mktemp -d)
exec 2>"$work/diagnostic.log"
trap 'printf "migration_298_postgres_integration=failed\n"' ERR
created=false
cleanup() {
  local result=$? cleanup_failed=false
  set +e
  if [[ $created == true && $db =~ ^sub2api_v2_298_test_[0-9]+_[0-9]+$ ]]; then
    docker exec "$container" dropdb -U "$owner" "$db" >/dev/null || cleanup_failed=true
  fi
  if [[ $result == 0 && $cleanup_failed == false ]]; then
    rm -rf -- "$work" || cleanup_failed=true
  else
    # Preserve failed diagnostics only on the VM, in the root-only owned directory.
    printf 'migration_298_postgres_evidence=%s\n' "$work"
  fi
  if [[ $cleanup_failed == true ]]; then
    printf 'migration_298_postgres_cleanup=failed\n'
    exit 1
  fi
  if [[ $result == 0 ]]; then
    printf 'migration_298_postgres_cleanup=pass\n'
    printf 'migration_298_postgres_integration=pass\n'
  fi
  exit "$result"
}
trap cleanup EXIT
docker exec "$container" createdb -U "$owner" "$db"
created=true
server_version=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c 'SHOW server_version_num')
[[ $server_version =~ ^[0-9]+$ && $server_version -ge 180000 && $server_version -lt 190000 ]]
printf 'migration_298_postgres_version=%s\n' "$server_version"
sql() {
  docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c "$1" >/dev/null
}
sql 'CREATE TABLE groups(id BIGINT PRIMARY KEY, alternate_id BIGINT UNIQUE, deleted_at TIMESTAMPTZ);
     CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);
     CREATE TABLE fork_group_ttft_guard_policies(group_id BIGINT PRIMARY KEY);
     CREATE TABLE schema_migrations(filename TEXT PRIMARY KEY, checksum TEXT NOT NULL);
     INSERT INTO groups(id,alternate_id) VALUES(1,1),(2,2),(3,3);
     INSERT INTO settings VALUES('"'"'upstream_cache_guard_settings'"'"','"'"'{"mode":"protect","group_ids":[1,3]}'"'"')'
printf 'profile=270\n' > "$work/context.sh"
cat > "$work/manifest.json" <<'JSON'
{"schema":2,"profile":"270","parent_profile":"269","new_migrations":["298_group_scheduling_protection_policies.sql"],"migration_catalog":[{"filename":"298_group_scheduling_protection_policies.sql","checksum":"ddbfe759a9fef717570ffe258edf13cd8409b576f427a91587a09eef4a4fe481"}]}
JSON
export ASSERT_CONTEXT_FILE="$work/context.sh" ASSERT_MANIFEST_FILE="$work/manifest.json"
export ASSERT_DB_CONTAINER="$container" ASSERT_DB_USER="$owner" ASSERT_DB_NAME="$db"
export ASSERT_VM_ISOLATED_DB="$db" ASSERT_MIGRATION_FILE="$migration"
expect_pass() {
  MIGRATION_STATUS="$2" bash "$helper" "$1" > "$work/result"
  [[ $(<"$work/result") == "migration_298_$1=pass" ]]
}
expect_failure() {
  if MIGRATION_STATUS=verified bash "$helper" verified_replay > "$work/result"; then exit 1; fi
  [[ $(<"$work/result") == "migration_298_failure_code=$1" ]]
}
expect_pass preflight absent
sql 'CREATE TABLE fork_group_scheduling_protection_policies(group_id BIGINT)'
if MIGRATION_STATUS=absent bash "$helper" preflight > "$work/result"; then exit 1; fi
[[ $(<"$work/result") == migration_298_failure_code=partial_schema ]]
sql 'DROP TABLE fork_group_scheduling_protection_policies'
docker exec -i "$container" psql -X -q -v ON_ERROR_STOP=1 -U "$owner" -d "$db" < "$migration" >/dev/null
sql "INSERT INTO schema_migrations VALUES ('298_group_scheduling_protection_policies.sql','ddbfe759a9fef717570ffe258edf13cd8409b576f427a91587a09eef4a4fe481')"
expect_pass preflight verified
expect_pass postflight verified
expect_pass verified_replay verified
public_before=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" \
  -c 'SELECT md5(jsonb_agg(to_jsonb(t) ORDER BY group_id)::text) FROM fork_group_scheduling_protection_policies t')
expect_pass vm_semantics verified
public_after=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" \
  -c 'SELECT md5(jsonb_agg(to_jsonb(t) ORDER BY group_id)::text) FROM fork_group_scheduling_protection_policies t')
[[ $public_before == "$public_after" ]]

# Each mutation is reverted and a clean verified replay follows every negative.
table=fork_group_scheduling_protection_policies
default_drift() {
  sql "ALTER TABLE $table ALTER COLUMN $1 SET DEFAULT $2"
  expect_failure column_contract
  sql "ALTER TABLE $table ALTER COLUMN $1 $3"
  expect_pass verified_replay verified
}
default_drift group_id 1 'DROP DEFAULT'
default_drift cache_mode "'enabled'" "SET DEFAULT 'inherit'"
default_drift threshold_percent 74 'SET DEFAULT 75'
default_drift recovery_threshold_percent 81 'SET DEFAULT 80'
default_drift min_valid_requests 21 'SET DEFAULT 20'
default_drift priority "'cache_first'" "SET DEFAULT 'inherit'"
default_drift created_at 'clock_timestamp()' 'SET DEFAULT now()'
default_drift updated_at 'clock_timestamp()' 'SET DEFAULT now()'
sql "ALTER TABLE $table ADD COLUMN test_extra BIGINT"
expect_failure column_contract
sql "ALTER TABLE $table DROP COLUMN test_extra"
expect_pass verified_replay verified
sql "ALTER TABLE $table ALTER COLUMN created_at DROP NOT NULL"
expect_failure column_contract
sql "ALTER TABLE $table ALTER COLUMN created_at SET NOT NULL"
expect_pass verified_replay verified

constraint_drift() {
  sql "ALTER TABLE $table DROP CONSTRAINT $1; ALTER TABLE $table ADD CONSTRAINT $1 $2"
  expect_failure constraint_contract
  sql "ALTER TABLE $table DROP CONSTRAINT $1; ALTER TABLE $table ADD CONSTRAINT $1 $3"
  expect_pass verified_replay verified
}
constraint_drift fork_group_scheduling_protection_policies_cache_mode_check \
  "CHECK(cache_mode IN ('inherit','enabled','disabled','other'))" "CHECK(cache_mode IN ('inherit','enabled','disabled'))"
constraint_drift fork_group_scheduling_protection_polici_threshold_percent_check \
  'CHECK(threshold_percent >= 0 OR threshold_percent <= 100)' 'CHECK(threshold_percent >= 0 AND threshold_percent <= 100)'
constraint_drift fork_group_scheduling_protection_policies_check \
  'CHECK(recovery_threshold_percent > threshold_percent OR recovery_threshold_percent <= 100)' \
  'CHECK(recovery_threshold_percent > threshold_percent AND recovery_threshold_percent <= 100)'
constraint_drift fork_group_scheduling_protection_polic_min_valid_requests_check \
  'CHECK(min_valid_requests BETWEEN 0 AND 100000)' 'CHECK(min_valid_requests BETWEEN 1 AND 100000)'
constraint_drift fork_group_scheduling_protection_policies_priority_check \
  "CHECK(priority IN ('inherit','cache_first','ttft_first','other'))" "CHECK(priority IN ('inherit','cache_first','ttft_first'))"
# Preserve business constraint count while changing local/reference mappings.
sql "UPDATE $table SET min_valid_requests=group_id"
constraint_drift fork_group_scheduling_protection_policies_pkey 'PRIMARY KEY(min_valid_requests)' 'PRIMARY KEY(group_id)'
constraint_drift fork_group_scheduling_protection_policies_pkey 'PRIMARY KEY(group_id) DEFERRABLE' 'PRIMARY KEY(group_id)'
constraint_drift fork_group_scheduling_protection_policies_group_id_fkey \
  'FOREIGN KEY(min_valid_requests) REFERENCES groups(id) ON DELETE CASCADE' \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE'
constraint_drift fork_group_scheduling_protection_policies_group_id_fkey \
  'FOREIGN KEY(group_id) REFERENCES groups(alternate_id) ON DELETE CASCADE' \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE'
constraint_drift fork_group_scheduling_protection_policies_group_id_fkey \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE RESTRICT' \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE'
constraint_drift fork_group_scheduling_protection_policies_group_id_fkey \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE ON UPDATE CASCADE' \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE'
constraint_drift fork_group_scheduling_protection_policies_group_id_fkey \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE DEFERRABLE' \
  'FOREIGN KEY(group_id) REFERENCES groups(id) ON DELETE CASCADE'
constraint_drift fork_group_scheduling_protection_policies_priority_check \
  "CHECK(priority IN ('inherit','cache_first','ttft_first')) NOT VALID" \
  "CHECK(priority IN ('inherit','cache_first','ttft_first'))"
sql "ALTER TABLE $table ADD CONSTRAINT test_extra_check CHECK(min_valid_requests < 100000)"
expect_failure constraint_contract
sql "ALTER TABLE $table DROP CONSTRAINT test_extra_check"
expect_pass verified_replay verified
sql "ALTER TABLE $table ADD CONSTRAINT test_extra_unique UNIQUE(min_valid_requests)"
expect_failure constraint_contract
sql "ALTER TABLE $table DROP CONSTRAINT test_extra_unique"
expect_pass verified_replay verified
sql "CREATE UNIQUE INDEX test_extra_unique_index ON $table(cache_mode) WHERE group_id=2"
expect_failure index_contract
sql 'DROP INDEX test_extra_unique_index'
expect_pass verified_replay verified
# An ordinary non-unique performance index does not constrain valid policy rows.
sql "CREATE INDEX test_extra_nonunique_index ON $table(cache_mode)"
expect_pass verified_replay verified
sql 'DROP INDEX test_extra_nonunique_index'
expect_pass verified_replay verified
