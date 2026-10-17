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
trap 'printf "migration_298_postgres_failure_line=%s\nmigration_298_postgres_integration=failed\n" "$LINENO"' ERR
owned_dbs=()
cleanup() {
  local result=$? cleanup_failed=false
  set +e
  for owned_db in "${owned_dbs[@]}"; do
    if [[ $owned_db =~ ^sub2api_v2_298_test_[0-9]+_[0-9]+(_r[12])?$ ]]; then
      docker exec "$container" dropdb -U "$owner" "$owned_db" >/dev/null || cleanup_failed=true
    else
      cleanup_failed=true
    fi
  done
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
  printf 'migration_298_postgres_cleanup=pass\n'
  if [[ $result == 0 ]]; then
    printf 'migration_298_postgres_integration=pass\n'
  fi
  exit "$result"
}
trap cleanup EXIT
docker exec -e DB_OWNER="$owner" -e DB_NAME="$db" "$container" sh -lc \
  'createdb -U "${POSTGRES_USER:-postgres}" -O "$DB_OWNER" "$DB_NAME"'
owned_dbs+=("$db")
server_version=$(docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c 'SHOW server_version_num')
[[ $server_version =~ ^[0-9]+$ && $server_version -ge 180000 && $server_version -lt 190000 ]]
printf 'migration_298_postgres_version=%s\n' "$server_version"
sql() {
  if [[ ${restored_mutations:-false} == true ]]; then
    # --no-owner makes the restoring role own restored objects. Mutate only
    # this owned synthetic DB with that role; helper reads still use $owner.
    printf '%s\n' "$1" | docker exec -i "$container" sh -lc \
      'psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d "$1"' sh "$db" >/dev/null
  else
    docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" -c "$1" >/dev/null
  fi
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
printf 'migration_298_fresh=pass\n'

# pg_dump/pg_restore reparses varchar IN arrays into individual text casts.
# Exercise both real catalog shapes, including a second restore of the first
# restored catalog. Every database contains only this synthetic fixture.
fresh_db=$db
enum_fingerprint() {
  docker exec "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$owner" -d "$db" \
    -c "BEGIN READ ONLY; SET LOCAL search_path=public,pg_catalog;
      SELECT md5(string_agg(pg_get_constraintdef(oid), E'\\n' ORDER BY conname))
      FROM pg_constraint WHERE conrelid='public.$table'::regclass
        AND conname IN ('fork_group_scheduling_protection_policies_cache_mode_check',
          'fork_group_scheduling_protection_policies_priority_check'); COMMIT;"
}
fresh_fingerprint=$(enum_fingerprint)
restored_fingerprint=
for round in 1 2; do
  source_db=$db
  target_db="${fresh_db}_r${round}"
  docker exec -e DB_OWNER="$owner" -e DB_NAME="$target_db" "$container" sh -lc \
    'createdb -U "${POSTGRES_USER:-postgres}" -O "$DB_OWNER" "$DB_NAME"'
  owned_dbs+=("$target_db")
  docker exec "$container" pg_dump -Fc -U "$owner" "$source_db" > "$work/roundtrip.dump"
  docker exec -i "$container" sh -lc 'pg_restore --exit-on-error --no-owner -U "${POSTGRES_USER:-postgres}" -d "$1"' \
    sh "$target_db" < "$work/roundtrip.dump" >/dev/null
  printf 'GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO "%s"; GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO "%s";\n' \
    "$owner" "$owner" | docker exec -i "$container" sh -lc \
    'psql -X -q -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d "$1"' sh "$target_db" >/dev/null
  db=$target_db
  restored_mutations=true
  export ASSERT_DB_NAME="$db" ASSERT_VM_ISOLATED_DB="$db"
  expect_pass preflight verified
  expect_pass postflight verified
  expect_pass verified_replay verified
  expect_pass vm_semantics verified
  current_fingerprint=$(enum_fingerprint)
  [[ $current_fingerprint != "$fresh_fingerprint" ]]
  if [[ $round == 1 ]]; then
    restored_fingerprint=$current_fingerprint
  else
    [[ $current_fingerprint == "$restored_fingerprint" ]]
  fi
  # Revert each mutation to the exact restored form, so the next dump also
  # proves that the accepted restored expression remains stable on reparse.
  cache_restored="CHECK (((cache_mode)::text = ANY (ARRAY[('inherit'::character varying)::text, ('enabled'::character varying)::text, ('disabled'::character varying)::text])))"
  priority_restored="CHECK (((priority)::text = ANY (ARRAY[('inherit'::character varying)::text, ('cache_first'::character varying)::text, ('ttft_first'::character varying)::text])))"
  cache_constraint=fork_group_scheduling_protection_policies_cache_mode_check
  priority_constraint=fork_group_scheduling_protection_policies_priority_check
  for drift in \
    "CHECK(cache_mode IN ('inherit','enabled','disabled','other'))" \
    "CHECK(cache_mode IN ('inherit','disabled'))" \
    "CHECK(priority IN ('inherit','enabled','disabled'))" \
    "CHECK (((cache_mode)::text <> ANY (ARRAY[('inherit'::character varying)::text, ('enabled'::character varying)::text, ('disabled'::character varying)::text])))"; do
    constraint_drift "$cache_constraint" "$drift" "$cache_restored"
  done
  for drift in \
    "CHECK(priority IN ('inherit','cache_first','ttft_first','other'))" \
    "CHECK(priority IN ('inherit','cache_first'))" \
    "CHECK(cache_mode IN ('inherit','enabled','disabled'))" \
    "CHECK (((priority)::text <> ANY (ARRAY[('inherit'::character varying)::text, ('cache_first'::character varying)::text, ('ttft_first'::character varying)::text])))"; do
    constraint_drift "$priority_constraint" "$drift" "$priority_restored"
  done
  [[ $(enum_fingerprint) == "$restored_fingerprint" ]]
  printf 'migration_298_restore_round_%s=pass\n' "$round"
done
expect_pass verified_replay verified
