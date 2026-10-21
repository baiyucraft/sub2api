#!/usr/bin/env bash
# Explicit Linux-only regression. Owns one private container, no host DB access.
set -Eeuo pipefail
umask 077
root=${TEST_SOURCE_ROOT:-$(git rev-parse --show-toplevel)}
validator="$root/.agents/skills/sub2api-production-deploy/scripts/release/vm-validate.sh"
migration="$root/backend/migrations/285_upstream_null_rate_lifecycle.sql"
image=${TEST_POSTGRES_IMAGE:?set TEST_POSTGRES_IMAGE to an already installed PostgreSQL image ID}
expected_major=${TEST_POSTGRES_MAJOR:-18}
[[ $image =~ ^sha256:[0-9a-f]{64}$ && $expected_major =~ ^[1-9][0-9]*$ && -f $validator && -f $migration ]]
container="sub2api-probe-ownership-$$_${RANDOM}"
work=$(mktemp -d)
started=false
cleanup() {
  local result=$? cleanup_result=0
  trap - EXIT
  if [[ $started == true ]]; then
    command docker rm -f "$container" >"$work/cleanup.log" 2>&1 || cleanup_result=1
  fi
  if [[ $result == 0 && $cleanup_result == 0 ]]; then
    rm -rf -- "$work"
    printf 'vm_probe_ownership_cleanup=pass\nvm_probe_ownership_integration=pass\n'
  else
    printf 'vm_probe_ownership_evidence=%s\nvm_probe_ownership_cleanup=%s\n' "$work" "$cleanup_result"
  fi
  (( result != 0 )) || result=$cleanup_result
  exit "$result"
}
trap cleanup EXIT
trap 'printf "vm_probe_ownership_failure_line=%s\n" "$LINENO"' ERR
# Extract current production helpers, never an independently copied implementation.
awk '/^assert_probe_database_user\(\) \{/{copy=1} copy{print} copy && /^\}/{copy=0}' "$validator" > "$work/helpers.sh"
awk '/^restore_probe_database\(\) \{/{copy=1} copy{print} copy && /^\}/{copy=0}' "$validator" >> "$work/helpers.sh"
source "$work/helpers.sh"
declare -F assert_probe_database_user restore_probe_database >/dev/null
# The restore helper names the production service; route only that token into
# this script's private container. Every other Docker operation is explicit.
docker() {
  local arg
  local args=()
  for arg in "$@"; do
    if [[ $arg == sub2api-postgres ]]; then arg=$container; fi
    args+=("$arg")
  done
  command docker "${args[@]}"
}
command docker run -d --pull never --name "$container" --network none \
  --tmpfs /var/lib/postgresql:rw,nosuid -e POSTGRES_HOST_AUTH_METHOD=trust \
  "$image" > "$work/container.id"
started=true
ready=false
for _ in $(seq 1 60); do
  if command docker exec "$container" pg_isready -U postgres >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
[[ $ready == true ]]
sql() {
  local database=$1 role=$2
  command docker exec -i "$container" psql -X -q -A -t -v ON_ERROR_STOP=1 -U "$role" -d "$database"
}
actual_image=$(command docker inspect -f '{{.Image}}' "$container")
[[ $actual_image == "$image" ]]
server_version=$(sql postgres postgres <<<'SHOW server_version_num;')
[[ $server_version =~ ^[0-9]+$ && $((server_version / 10000)) == "$expected_major" ]]
printf 'vm_probe_ownership_image=%s\nvm_probe_ownership_postgres_version=%s\n' "$actual_image" "$server_version"
sql postgres postgres > "$work/setup.log" <<'SQL'
CREATE ROLE fixture_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
CREATE DATABASE sub2api_v2_ownership_source OWNER fixture_app;
CREATE DATABASE sub2api_v2_ownership_other OWNER fixture_app;
CREATE DATABASE sub2api_v2_ownership_bad OWNER fixture_app;
CREATE DATABASE sub2api_v2_ownership_fast OWNER fixture_app;
CREATE DATABASE sub2api_v2_ownership_restore OWNER fixture_app;
SQL
printf 'database:\n  user: "fixture_app"\n  password: "fixture-only"\n' > "$work/config.yaml"
assert_probe_database_user "$work/config.yaml" fixture_app
if assert_probe_database_user "$work/config.yaml" postgres; then exit 1; fi
[[ $(sql postgres postgres <<<'SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole FROM pg_roles WHERE rolname=current_user;') == f ]]
[[ $(sql sub2api_v2_ownership_source fixture_app <<<'SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole FROM pg_roles WHERE rolname=current_user;') == t ]]
sql sub2api_v2_ownership_source fixture_app > "$work/source.log" <<'SQL'
CREATE EXTENSION pg_trgm;
CREATE TYPE fixture_kind AS ENUM ('one','two');
CREATE DOMAIN fixture_positive AS INTEGER CHECK (VALUE>0);
CREATE SCHEMA fixture_owned;
CREATE SEQUENCE fixture_owned.fixture_sequence;
CREATE TABLE fixture_owned.fixture_table(id BIGINT PRIMARY KEY, kind fixture_kind, value fixture_positive);
CREATE INDEX fixture_index ON fixture_owned.fixture_table(kind);
CREATE VIEW fixture_owned.fixture_view AS SELECT id FROM fixture_owned.fixture_table;
INSERT INTO fixture_owned.fixture_table VALUES (1,'one',1);
CREATE TABLE upstream_keys(id BIGINT PRIMARY KEY, upstream_config_id BIGINT, status VARCHAR(20), platform VARCHAR(50), deleted_at TIMESTAMPTZ, rate_multiplier NUMERIC(20,10), source_rate_multiplier NUMERIC(20,10));
CREATE TABLE accounts(id BIGINT PRIMARY KEY, upstream_config_id BIGINT, upstream_key_id BIGINT, upstream_stale_pause_key_id BIGINT, upstream_stale_paused_at TIMESTAMPTZ, upstream_source_rate_multiplier NUMERIC(20,10), platform VARCHAR(50), rate_multiplier NUMERIC(20,10), priority INTEGER, schedulable BOOLEAN, deleted_at TIMESTAMPTZ);
CREATE FUNCTION validate_account_upstream_key_binding() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$;
CREATE TRIGGER trg_validate_account_upstream_key_binding BEFORE INSERT OR UPDATE ON accounts FOR EACH ROW EXECUTE FUNCTION validate_account_upstream_key_binding();
INSERT INTO upstream_keys VALUES (1,1,'active','openai',NULL,1,1);
INSERT INTO accounts(id,upstream_config_id,upstream_key_id,platform,rate_multiplier,priority,schedulable) VALUES(1,1,1,'openai',1,100,true);
SQL
fingerprint() {
  command docker exec "$container" pg_dump --no-owner --no-privileges -U fixture_app "$1" | \
    sed '/^--/d; /^\\restrict /d; /^\\unrestrict /d' | sha256sum | awk '{print $1}'
}
source_before=$(fingerprint sub2api_v2_ownership_source)
other_before=$(fingerprint sub2api_v2_ownership_other)
owners_before=$(sql postgres postgres <<<'SELECT md5(string_agg(datname||chr(58)||datdba::text,chr(44) ORDER BY datname)) FROM pg_database;')
command docker exec "$container" pg_dump -Fc -U fixture_app sub2api_v2_ownership_source > "$work/source.dump"
# The historical GRANT workaround must reproduce the exact ownership failure.
command docker exec -i "$container" pg_restore --exit-on-error --no-owner -U postgres -d sub2api_v2_ownership_bad < "$work/source.dump" > "$work/bad-restore.log" 2>&1
sql sub2api_v2_ownership_bad postgres > "$work/bad-grants.log" <<'SQL'
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO fixture_app;
GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO fixture_app;
SQL
if sql sub2api_v2_ownership_bad fixture_app < "$migration" > "$work/expected-failure.log" 2>&1; then exit 1; fi
grep -q 'must be owner of function validate_account_upstream_key_binding' "$work/expected-failure.log"
# Exercise both production sources: piped development clone and file restore.
command docker exec "$container" pg_dump -Fc -U fixture_app sub2api_v2_ownership_source | \
  restore_probe_database sub2api_v2_ownership_fast fixture_app > "$work/fast-restore.log" 2>&1
restore_probe_database sub2api_v2_ownership_restore fixture_app < "$work/source.dump" > "$work/restore.log" 2>&1
for database in sub2api_v2_ownership_fast sub2api_v2_ownership_restore; do
  [[ $(sql "$database" fixture_app <<<'SELECT string_agg(extname,chr(44) ORDER BY extname) FROM pg_extension;') == pg_trgm,plpgsql ]]
  [[ $(sql "$database" fixture_app <<'SQL'
SELECT bool_and(owner_name='fixture_app') FROM (
  SELECT pg_get_userbyid(c.relowner) owner_name FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname IN ('public','fixture_owned') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')
  UNION ALL SELECT pg_get_userbyid(p.proowner) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname IN ('public','fixture_owned') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')
  UNION ALL SELECT pg_get_userbyid(t.typowner) FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
    WHERE n.nspname IN ('public','fixture_owned') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid IN (t.oid,t.typelem) AND d.deptype='e')
  UNION ALL SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='fixture_owned'
) owners;
SQL
  ) == t ]]
  sql "$database" fixture_app < "$migration" > "$work/$database-migration.log" 2>&1
  sql "$database" fixture_app > "$work/$database-mutations.log" <<'SQL'
ALTER TABLE fixture_owned.fixture_table ADD COLUMN checked BOOLEAN DEFAULT true;
ALTER INDEX fixture_owned.fixture_index RENAME TO fixture_renamed_index;
ALTER SEQUENCE fixture_owned.fixture_sequence RESTART WITH 10;
ALTER TYPE fixture_kind ADD VALUE 'three';
ALTER DOMAIN fixture_positive ADD CONSTRAINT fixture_under_limit CHECK(VALUE<100);
CREATE OR REPLACE VIEW fixture_owned.fixture_view AS SELECT id FROM fixture_owned.fixture_table WHERE checked;
ALTER SCHEMA fixture_owned RENAME TO fixture_renamed;
UPDATE accounts SET schedulable=false WHERE id=1;
DO $$ BEGIN
 IF (SELECT rate_multiplier FROM accounts WHERE id=1) <> 1 THEN RAISE EXCEPTION 'migration 285 semantic failure'; END IF;
 IF (SELECT COUNT(*) FROM fixture_renamed.fixture_table) <> 1 THEN RAISE EXCEPTION 'restore lost data'; END IF;
END; $$;
SQL
done
[[ $(fingerprint sub2api_v2_ownership_source) == "$source_before" ]]
[[ $(fingerprint sub2api_v2_ownership_other) == "$other_before" ]]
[[ $(sql sub2api_v2_ownership_source fixture_app <<<'SELECT count(*) FROM fixture_owned.fixture_table WHERE id=1 AND kind::text=chr(111)||chr(110)||chr(101);') == 1 ]]
owners_after=$(sql postgres postgres <<<'SELECT md5(string_agg(datname||chr(58)||datdba::text,chr(44) ORDER BY datname)) FROM pg_database;')
[[ $owners_after == "$owners_before" ]]
printf 'vm_probe_ownership_modes=2\nvm_probe_ownership_legacy_failure=verified\nvm_probe_ownership_source_unchanged=true\n'
