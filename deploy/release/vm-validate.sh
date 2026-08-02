#!/usr/bin/env bash
set -Eeuo pipefail

required_commands=(awk chmod cp curl date df diff docker find flock git grep gzip head id install jq ln mkdir mv rm sed seq sha256sum sleep sort ss stat tr xargs)
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || exit 127
done
docker info >/dev/null 2>&1
git --version >/dev/null 2>&1

manifest=${1:?manifest path is required}
output_dir=${2:?output directory is required}
source_dir=/opt/sub2api-src
deploy_dir=/opt/sub2api-deploy
data_dir="$deploy_dir/data-dev"
state_root="$deploy_dir/release-gates"
[[ $(id -u) == 0 ]]
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
[[ -f $unit_lock && ! -L $unit_lock && $(stat -c '%U:%G:%a:%h' "$unit_lock") == root:root:600:1 ]]
exec 8<>"$unit_lock"
[[ $(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8) == root:root:600:1 ]]
flock -s 8
[[ -f $manifest && ! -L $manifest ]]
commit=$(jq -er '.commit_sha' "$manifest")
release_id=$(jq -er '.release_id' "$manifest")
version=$(jq -er '.version' "$manifest")
tag="sub2api:baiyu-$version-$commit"
test_tag="sub2api:vm-test-$commit"
[[ $commit =~ ^[0-9a-f]{40}$ ]]
profile=$(jq -er '.profile' "$manifest")
[[ $release_id =~ ^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $profile == 182 || $profile == 187 || $profile == 191 || $profile == 192 || $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]
[[ $(jq -er '.vm_identity' "$manifest") == sub2api-dev ]]
[[ $(jq -er '.origin' "$manifest") == https://github.com/baiyucraft/sub2api.git ]]
[[ $(jq -er '.vm_validator_sha256' "$manifest") == "$(sha256sum "$0" | awk '{print $1}')" ]]
[[ $(jq -er '.vm_gate_signer_sha256' "$manifest") == "$(sha256sum /usr/local/libexec/sub2api-sign-gate | awk '{print $1}')" ]]
[[ $(jq -er '.vm_dr_signer_sha256' "$manifest") == "$(sha256sum /usr/local/libexec/sub2api-sign-dr-evidence | awk '{print $1}')" ]]
for release_asset in "$0" /usr/local/libexec/sub2api-sign-gate /usr/local/libexec/sub2api-sign-dr-evidence; do
  [[ -f $release_asset && ! -L $release_asset && $(stat -c '%U:%G:%a' "$release_asset") == root:root:700 ]]
done
state_dir="$state_root/$release_id"
[[ $output_dir == "$state_dir/output" ]]
[[ ! -e $state_dir && ! -L $state_dir ]]
install -d -m 700 "$state_root"
exec 9>"$state_root/release.lock"
flock -n 9
install -d -m 700 "$state_dir" "$state_dir/backup" "$output_dir"
install -m 400 "$manifest" "$state_dir/manifest.json"
manifest="$state_dir/manifest.json"
mark_stage() {
  printf '%s\n' "$1" > "$state_dir/stage.tmp"
  mv -T -- "$state_dir/stage.tmp" "$state_dir/stage"
}
mark_stage preflight

cd "$source_dir"
[[ -f .sub2api-deploy-worktree ]]
[[ $(git remote get-url origin) == https://github.com/baiyucraft/sub2api.git ]]
[[ -z $(git status --porcelain --untracked-files=all | grep -v '^?? .sub2api-deploy-worktree$' || true) ]]
git fetch origin main >/dev/null 2>&1
[[ $(git rev-parse origin/main) == "$commit" ]]
git reset --hard "$commit" >/dev/null
while IFS=$'\t' read -r relative expected; do
  [[ $relative =~ ^deploy/(release\.py|release/([^/]+|trust/[^/]+|drverify/[^/]+)|maintenance/release/[^/]+|maintenance/181/(mask-backup-units|restore-backup-units)\.sh)$ ]]
  [[ -f $source_dir/$relative && ! -L $source_dir/$relative ]]
  [[ $(sha256sum "$source_dir/$relative" | awk '{print $1}') == "$expected" ]]
done < <(jq -r '.release_asset_sha256 | to_entries[] | [.key,.value] | @tsv' "$manifest")
[[ -d $data_dir && ! -L $data_dir ]]
old_image_id=$(docker inspect -f '{{.Image}}' sub2api-dev)
old_image_ref=$(docker inspect -f '{{.Config.Image}}' sub2api-dev)
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]]
extract_config() {
  local section=$1 field=$2
  sed -n "/^${section}:/,/^[^[:space:]]/p" "$data_dir/config.yaml" | sed -n "s/^[[:space:]]*${field}:[[:space:]]*//p" | head -n1 | tr -d '"'
}
db_host=$(extract_config database host)
redis_host=$(extract_config redis host)
server_port=$(extract_config server port)
[[ $db_host =~ ^(127\.0\.0\.1|localhost)$ ]]
[[ $redis_host =~ ^(127\.0\.0\.1|localhost)$ ]]
[[ $server_port =~ ^[0-9]+$ ]]
[[ $(ss -ltn | awk '$4 ~ /:5432$/ {n++} END{print n+0}') -ge 1 ]]
[[ $(ss -ltn | awk '$4 ~ /:6379$/ {n++} END{print n+0}') -ge 1 ]]

free_before=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
database_size=$(docker exec sub2api-postgres sh -lc \
  'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d postgres -c "SELECT pg_database_size('"'"'sub2api_dev'"'"')"' \
  | tr -d '\r')
current_image_size=$(docker image inspect -f '{{.Size}}' "$old_image_id")
required_before=$((database_size * 2 + current_image_size * 2 + 1073741824))
[[ $free_before -gt $required_before ]]
preexisting_tag_image=$(docker image inspect -f '{{.Id}}' "$tag" 2>/dev/null || true)
cleanup_candidate_tag() {
  local current_tag_image
  current_tag_image=$(docker image inspect -f '{{.Id}}' "$tag" 2>/dev/null || true)
  if [[ -n $current_tag_image && $current_tag_image != "$preexisting_tag_image" ]]; then
    docker image rm "$tag" >/dev/null 2>&1 || true
    docker image rm "$current_tag_image" >/dev/null 2>&1 || true
  fi
  if [[ -n $preexisting_tag_image ]] && docker image inspect "$preexisting_tag_image" >/dev/null 2>&1; then
    docker tag "$preexisting_tag_image" "$tag" >/dev/null 2>&1 || true
  fi
}
on_build_failure() {
  code=$?
  trap - ERR INT TERM
  printf '%s\n' candidate_build > "$state_dir/failure-category"
  rm -f "$state_dir/candidate.tar.gz"
  cleanup_candidate_tag
  exit "$code"
}
trap on_build_failure ERR INT TERM
export DOCKER_BUILDKIT=1
mark_stage candidate_build
docker build --network=host --progress=plain \
  --build-arg NODE_IMAGE=docker.m.daocloud.io/library/node:24-alpine \
  --build-arg GOLANG_IMAGE=docker.m.daocloud.io/library/golang:1.26.5-alpine \
  --build-arg ALPINE_IMAGE=docker.m.daocloud.io/library/alpine:3.21 \
  --build-arg POSTGRES_IMAGE=docker.m.daocloud.io/library/postgres:18-alpine \
  --build-arg COMMIT="$commit" --build-arg VERSION="$version" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" -t "$tag" . >/dev/null 2>&1
candidate_image_id=$(docker image inspect -f '{{.Id}}' "$tag")
candidate_size=$(docker image inspect -f '{{.Size}}' "$tag")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
free_after_build=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
required_free=$((database_size * 2 + candidate_size + 1073741824))
[[ $free_after_build -gt $required_free ]]
trap - ERR INT TERM
probe_suffix=${release_id//[^a-zA-Z0-9]/}
probe_db="sub2api_probe_${probe_suffix:0:24}"
probe_dir="$state_dir/probe-data"
probe_redis="sub2api-probe-redis-${probe_suffix:0:12}"
probe_app="sub2api-probe-app-${probe_suffix:0:12}"
old_probe_app="sub2api-probe-old-${probe_suffix:0:12}"
probe_network="sub2api-probe-net-${probe_suffix:0:12}"
redis_image=$(docker inspect -f '{{.Config.Image}}' sub2api-redis)
database_owner=$(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d postgres -c "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname='"'"'sub2api_dev'"'"'"' | tr -d '\r')
[[ $database_owner =~ ^[a-zA-Z_][a-zA-Z0-9_]*$ ]]
create_probe_database() {
  docker exec -i -e DB_OWNER="$database_owner" sub2api-postgres sh -lc 'psql -X -v ON_ERROR_STOP=1 -v db_owner="$DB_OWNER" -U "${POSTGRES_USER:-postgres}" -d postgres' >/dev/null <<SQL
SELECT format('CREATE DATABASE %I OWNER %I', '$probe_db', :'db_owner') \gexec
SQL
}
cleanup_probe() {
  docker rm -f "$probe_app" "$old_probe_app" "$probe_redis" >/dev/null 2>&1 || true
  docker network disconnect -f "$probe_network" sub2api-postgres >/dev/null 2>&1 || true
  docker network rm "$probe_network" >/dev/null 2>&1 || true
  docker exec sub2api-postgres sh -lc "dropdb --if-exists -U \"\${POSTGRES_USER:-postgres}\" $probe_db" >/dev/null 2>&1 || true
  rm -rf "$probe_dir" "$state_dir/probe.dump"
}
on_failure() {
  code=$?
  trap - ERR INT TERM
  category=unknown
  current_stage=$(<"$state_dir/stage")
  if [[ $current_stage == migration_assertion_* || $current_stage == runtime_assertion_* ]]; then
    category=$current_stage
  fi
  if [[ $current_stage == old_image_compatibility_* ]]; then
    category=$current_stage
  fi
  if [[ $current_stage == old_image_compatibility_auth && -f $state_dir/old-image-auth-status ]]; then
    old_image_auth_status=$(<"$state_dir/old-image-auth-status")
    [[ $old_image_auth_status =~ ^[0-9]{3}$ ]] && category="old_image_compatibility_auth_http_$old_image_auth_status"
  fi
  if [[ -f $state_dir/migrate-candidate.log ]]; then
    category=migration_other
    grep -qi 'migration 182:' "$state_dir/migrate-candidate.log" && category=migration_182_semantic
    grep -qi 'migration 195:' "$state_dir/migrate-candidate.log" && category=migration_195_semantic
    failed_migration=$(sed -n 's/.*apply migration \([0-9][0-9a-z]*\)_[^:]*:.*/\1/p' "$state_dir/migrate-candidate.log" | head -n1)
    [[ -z $failed_migration || $failed_migration =~ ^[0-9][0-9a-z]*$ ]] || failed_migration=
    [[ -n $failed_migration ]] && category="migration_file_$failed_migration"
    grep -qi 'relation .*group_rate_snapshots.* does not exist' "$state_dir/migrate-candidate.log" && category=migration_missing_group_rate_snapshots
    grep -qi 'relation .*groups.* does not exist' "$state_dir/migrate-candidate.log" && category=migration_missing_groups
    grep -qi 'relation .*timezone_lock.* does not exist' "$state_dir/migrate-candidate.log" && category=migration_missing_timezone_lock
    grep -qi 'function pg_advisory_xact_lock.* does not exist' "$state_dir/migrate-candidate.log" && category=migration_missing_advisory_function
    grep -qi 'column .*timezone.* does not exist' "$state_dir/migrate-candidate.log" && category=migration_missing_timezone_column
    timezone_length=$(sed -n 's/.*timezone_len=\([0-9][0-9]*\).*/\1/p' "$state_dir/migrate-candidate.log" | head -n1)
    timezone_sha=$(sed -n 's/.*timezone_sha=\([0-9a-f][0-9a-f]*\).*/\1/p' "$state_dir/migrate-candidate.log" | head -n1)
    [[ -z $timezone_length || $timezone_length =~ ^[0-9]+$ ]] || timezone_length=
    [[ -z $timezone_sha || $timezone_sha =~ ^[0-9a-f]{12}$ ]] || timezone_sha=
    grep -qi 'Failed to load migration config' "$state_dir/migrate-candidate.log" && category=migration_config
    grep -qi 'create schema_migrations\|check schema_migrations\|list migrations' "$state_dir/migrate-candidate.log" && category=migration_runner_init
    grep -qi 'acquire migrations lock\|release migrations lock' "$state_dir/migrate-candidate.log" && category=migration_advisory_lock
    grep -qi 'invalid timezone\|ensure group rate timezone snapshots' "$state_dir/migrate-candidate.log" && category=migration_timezone
    grep -qi 'invalid timezone .*unknown time zone' "$state_dir/migrate-candidate.log" && category=migration_go_timezone
    grep -qi 'invalid value for parameter .*TimeZone\|unrecognized configuration parameter .*TimeZone' "$state_dir/migrate-candidate.log" && category=migration_database_timezone
    grep -qi 'group rate snapshot' "$state_dir/migrate-candidate.log" && category=migration_group_rate_snapshot
    grep -qi 'checksum' "$state_dir/migrate-candidate.log" && category=migration_checksum
    grep -qi 'already exists\|duplicate' "$state_dir/migrate-candidate.log" && category=migration_duplicate
    grep -qi 'does not exist\|undefined' "$state_dir/migrate-candidate.log" && category=migration_missing_object
    grep -qi 'constraint\|violat' "$state_dir/migrate-candidate.log" && category=migration_constraint
    grep -qi 'syntax' "$state_dir/migrate-candidate.log" && category=migration_syntax
    grep -qi 'permission denied\|must be owner' "$state_dir/migrate-candidate.log" && category=migration_permission
    grep -qi 'connection refused\|no such host\|dial tcp' "$state_dir/migrate-candidate.log" && category=migration_connection
    grep -qi 'timeout\|deadline exceeded' "$state_dir/migrate-candidate.log" && category=migration_timeout
    migration_sqlstate=$(sed -n 's/.*sqlstate=\([0-9A-Z][0-9A-Z]*\).*/\1/p' "$state_dir/migrate-candidate.log" | head -n1)
    [[ -z $migration_sqlstate || $migration_sqlstate =~ ^[0-9A-Z]{5}$ ]] || migration_sqlstate=
    [[ -n $migration_sqlstate ]] && category="migration_sqlstate_$migration_sqlstate"
    if [[ $category == migration_timezone || $category == migration_group_rate_snapshot ]] && [[ -n $timezone_length && -n $timezone_sha ]]; then
      category="migration_timezone_${timezone_length}_$timezone_sha"
    fi
    rm -f "$state_dir/migrate-candidate.log"
  fi
  if [[ -f $state_dir/stage && $(<"$state_dir/stage") == candidate_health ]] && docker inspect "$probe_app" >/dev/null 2>&1; then
    probe_log="$state_dir/probe-app.log"
    docker logs --tail 300 "$probe_app" > "$probe_log" 2>&1 || true
    grep -qi 'permission denied' "$probe_log" && category=permission
    grep -qi 'connection refused\|no such host\|dial tcp' "$probe_log" && category=connection
    grep -qi 'redis' "$probe_log" && category=redis
    grep -qi 'database\|postgres' "$probe_log" && category=database
    grep -qi 'address already in use' "$probe_log" && category=port_conflict
    grep -qi 'panic\|fatal' "$probe_log" && category=fatal
    rm -f "$probe_log"
  fi
  printf '%s\n' "$category" > "$state_dir/failure-category"
  rm -f "$state_dir/validator.stderr"
  cleanup_probe
  rm -f "$state_dir/candidate.tar.gz"
  cleanup_candidate_tag
  exit "$code"
}
trap on_failure ERR INT TERM
exec 2>"$state_dir/validator.stderr"

mark_stage isolated_database
install -d -m 700 "$probe_dir"
docker network create "$probe_network" >/dev/null 2>&1
docker network connect --alias sub2api-postgres "$probe_network" sub2api-postgres >/dev/null 2>&1
docker exec sub2api-postgres sh -lc 'pg_dump -Fc -Z 1 -U "${POSTGRES_USER:-postgres}" -d sub2api_dev' > "$state_dir/probe.dump"
create_probe_database
docker exec -i sub2api-postgres sh -lc "pg_restore --exit-on-error -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" < "$state_dir/probe.dump" >/dev/null 2>&1
cp -a "$data_dir/." "$probe_dir/"
sed -i "/^database:/,/^[^[:space:]]/ s/^[[:space:]]*dbname:[[:space:]]*.*/  dbname: $probe_db/" "$probe_dir/config.yaml"
sed -i '/^database:/,/^[^[:space:]]/ s/^[[:space:]]*host:[[:space:]]*.*/  host: sub2api-postgres/' "$probe_dir/config.yaml"

mark_stage isolated_redis
docker run -d --name "$probe_redis" --network "$probe_network" --network-alias probe-redis "$redis_image" redis-server --save '' --appendonly no >/dev/null 2>&1
for _ in $(seq 1 30); do
  [[ $(docker exec "$probe_redis" redis-cli PING 2>/dev/null | tr -d '\r') == PONG ]] && break
  sleep 1
done
[[ $(docker exec "$probe_redis" redis-cli PING 2>/dev/null | tr -d '\r') == PONG ]]
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*host:[[:space:]]*.*/  host: probe-redis/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*port:[[:space:]]*.*/  port: 6379/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*password:[[:space:]]*.*/  password: ""/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*db:[[:space:]]*.*/  db: 0/' "$probe_dir/config.yaml"

mark_stage migrate_candidate
probe_timezone=$(sed -n 's/^timezone:[[:space:]]*//p' "$probe_dir/config.yaml" | head -n1 | tr -d '"\r')
[[ $probe_timezone =~ ^UTC$|^[a-zA-Z_+-]+(/[a-zA-Z0-9_+-]+)+$ ]]
docker run --rm --entrypoint sh -e PROBE_TIMEZONE="$probe_timezone" "$candidate_image_id" -lc 'test -f "/usr/share/zoneinfo/$PROBE_TIMEZONE"' >/dev/null 2>&1
fixture_rejected=false
restore_completed=false
clean_preflight=false
verified_replay=false
verified_low_watermark_rejected=false
managed_monitor_key_names_verified=false
reasoning_effort_policy_verified=false
alipay_mobile_precreate_migration_verified=false
group_auth_cache_image_generation_verified=false
composite_model_routes_verified=false
session_id_columns_verified=false
live_request_type_verified=false
group_allow_live_verified=false
email_alias_index_verified=false
live_runtime_capability_verified=false
passkey_schema_verified=false
user_usage_aggregation_schema_verified=false
group_profit_control_schema_verified=false
group_profit_auth_cache_trigger_verified=false
vm_old_image_compatibility_verified=false
migration_211_status=not_applicable
migration_212_status=not_applicable
if [[ $profile == 212 ]]; then
  mark_stage migration_assertion_profile_212_status
  profile_212_migration_status() {
    local filename=$1 expected row
    expected=$(jq -er --arg filename "$filename" '.migration_sha256[$filename]' "$manifest")
    row=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT filename,checksum FROM schema_migrations WHERE filename='$filename'\"")
    if [[ -z $row ]]; then
      printf 'absent\n'
    else
      [[ $row == "$filename|$expected" ]]
      printf 'verified\n'
    fi
  }
  migration_211_status=$(profile_212_migration_status 211_group_profit_control.sql)
  migration_212_status=$(profile_212_migration_status 212_group_profit_control_auth_cache_invalidation.sql)
fi
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  migration_195_context="$state_dir/migration-195-context.sh"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$state_dir" > "$migration_195_context"
  chmod 400 "$migration_195_context"
  fixture_key_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT k.id FROM upstream_keys k JOIN accounts a ON a.upstream_key_id=k.id WHERE k.rate_multiplier IS NOT NULL ORDER BY k.id LIMIT 1\"")
  [[ $fixture_key_id =~ ^[0-9]+$ ]]
  fixture_key_hash=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT md5(row_to_json(k)::text) FROM upstream_keys k WHERE id=$fixture_key_id\"")
  [[ $fixture_key_hash =~ ^[0-9a-f]{32}$ ]]
  docker exec sub2api-postgres sh -lc "psql -X -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"UPDATE upstream_keys SET rate_multiplier=NULL WHERE id=$fixture_key_id\"" >/dev/null
  if ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=absent RELEASE_DIR="$state_dir" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight >/dev/null 2>&1; then
    false
  fi
  fixture_rejected=true
  docker exec sub2api-postgres sh -lc "dropdb -U \"\${POSTGRES_USER:-postgres}\" $probe_db" >/dev/null 2>&1
  create_probe_database
  docker exec -i sub2api-postgres sh -lc "pg_restore --exit-on-error -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" < "$state_dir/probe.dump" >/dev/null 2>&1
  [[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.accounts') IS NOT NULL AND to_regclass('public.upstream_keys') IS NOT NULL\"") == t ]]
  [[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT md5(row_to_json(k)::text) FROM upstream_keys k WHERE id=$fixture_key_id\"") == "$fixture_key_hash" ]]
  restore_completed=true
  probe_migration_195_recorded=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM schema_migrations WHERE filename='195_upstream_scheduling_monitor_rates.sql'\"" | tr -d '\r')
  [[ $probe_migration_195_recorded =~ ^[01]$ ]]
  migration_195_status=absent
  if [[ $probe_migration_195_recorded == 1 ]]; then
    probe_outbox_highwater=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COALESCE(MAX(id),0) FROM scheduler_outbox\"" | tr -d '\r')
    [[ $probe_outbox_highwater =~ ^[0-9]+$ ]]
    docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$probe_outbox_highwater" >/dev/null
    migration_195_status=verified
  fi
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight >/dev/null
  clean_preflight=true
  cp "$state_dir/migration-195-data-plan.sha256" "$state_dir/fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$state_dir/fake-recovery.sha256")" > "$state_dir/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" bind >/dev/null
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  docker exec sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"INSERT INTO settings (key,value,updated_at) VALUES ('ALIPAY_MOBILE_PRECREATE_DEEP_LINK','true',NOW()) ON CONFLICT (key) DO UPDATE SET value='true',updated_at=NOW()\"" >/dev/null
fi
docker run --rm --network "$probe_network" -v "$probe_dir:/app/data" "$candidate_image_id" /app/sub2api --migrate-only >"$state_dir/migrate-candidate.log" 2>&1
rm -f "$state_dir/migrate-candidate.log"
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" postflight_db >/dev/null
  consumed_event_id=$(<"$state_dir/migration-195-outbox-event.id")
  if [[ $migration_195_status == absent ]]; then
    [[ $consumed_event_id =~ ^[1-9][0-9]*$ ]]
  else
    [[ $consumed_event_id =~ ^[0-9]+$ ]]
  fi
  sentinel_event_id=$(docker exec sub2api-postgres sh -lc "psql -X -q -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"INSERT INTO scheduler_outbox (event_type,payload) VALUES ('account_changed','{}'::jsonb) RETURNING id\"" | tr -d '\r')
  [[ $sentinel_event_id =~ ^[1-9][0-9]*$ ]]
  [[ $consumed_event_id == 0 || $sentinel_event_id -gt $consumed_event_id ]]
  docker exec sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"WITH expected_accounts AS (SELECT COALESCE(jsonb_agg(id ORDER BY id),'[]'::jsonb) AS ids FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL) DELETE FROM scheduler_outbox o USING expected_accounts e WHERE o.event_type='account_bulk_changed' AND o.payload->'account_ids'=e.ids\"" >/dev/null
  low_watermark=$((sentinel_event_id - 1))
  docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$low_watermark" >/dev/null
  migration_195_low_state="$state_dir/migration-195-verified-low"
  migration_195_low_context="$state_dir/migration-195-verified-low-context.sh"
  install -d -m 700 "$migration_195_low_state"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$migration_195_low_state" > "$migration_195_low_context"
  chmod 400 "$migration_195_low_context"
  if ASSERT_CONTEXT_FILE="$migration_195_low_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_low_state" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight >/dev/null 2>&1; then
    false
  fi
  verified_low_watermark_rejected=true
  docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$sentinel_event_id" >/dev/null
  migration_195_verified_state="$state_dir/migration-195-verified"
  migration_195_verified_context="$state_dir/migration-195-verified-context.sh"
  install -d -m 700 "$migration_195_verified_state"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$migration_195_verified_state" > "$migration_195_verified_context"
  chmod 400 "$migration_195_verified_context"
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight >/dev/null
  cp "$migration_195_verified_state/migration-195-data-plan.sha256" "$migration_195_verified_state/fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$migration_195_verified_state/fake-recovery.sha256")" > "$migration_195_verified_state/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" bind >/dev/null
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" postflight_db >/dev/null
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage old_image_compatibility_start
  docker run -d --name "$old_probe_app" --network "$probe_network" \
    -e SERVER_HOST=0.0.0.0 -e SERVER_PORT="$server_port" -e UPSTREAM_SYNC_AUTO_ENABLED=false \
    --health-cmd "wget -q -T 5 -O /dev/null http://127.0.0.1:$server_port/health || exit 1" \
    --health-interval 5s --health-timeout 5s --health-start-period 5s --health-retries 6 \
    -v "$probe_dir:/app/data" "$old_image_id" >/dev/null 2>&1
  mark_stage old_image_compatibility_health
  for _ in $(seq 1 90); do
    [[ $(docker inspect -f '{{.State.Health.Status}}' "$old_probe_app") == healthy ]] && break
    sleep 2
  done
  mark_stage old_image_compatibility_image
  [[ $(docker inspect -f '{{.Image}}' "$old_probe_app") == "$old_image_id" ]]
  mark_stage old_image_compatibility_health
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$old_probe_app") == healthy ]]
  mark_stage old_image_compatibility_network
  old_probe_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$old_probe_app")
  [[ $old_probe_ip =~ ^[0-9a-fA-F:.]+$ ]]
  mark_stage old_image_compatibility_auth
  old_image_auth_status=$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' "http://$old_probe_ip:$server_port/api/v1/auth/me")
  printf '%s\n' "$old_image_auth_status" > "$state_dir/old-image-auth-status"
  [[ $old_image_auth_status == 401 ]]
  rm -f "$state_dir/old-image-auth-status"
  docker rm -f "$old_probe_app" >/dev/null
  vm_old_image_compatibility_verified=true
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  admin_user_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL ORDER BY id LIMIT 1\"" | tr -d '\r')
  [[ $admin_user_id =~ ^[1-9][0-9]*$ ]]
  fixture_admin_key="admin-vm-gate-profile-206-${release_id}"
  fixture_live_key="sk-vm-gate-profile-206-${release_id}"
  fixture_live_group="vm-gate-live-${release_id}"
  docker exec -i \
    -e FIXTURE_ADMIN_KEY="$fixture_admin_key" \
    -e FIXTURE_ADMIN_USER_ID="$admin_user_id" \
    -e FIXTURE_LIVE_KEY="$fixture_live_key" \
    -e FIXTURE_LIVE_GROUP="$fixture_live_group" \
    sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -v fixture_admin_key=\"\$FIXTURE_ADMIN_KEY\" -v fixture_admin_user_id=\"\$FIXTURE_ADMIN_USER_ID\" -v fixture_live_key=\"\$FIXTURE_LIVE_KEY\" -v fixture_live_group=\"\$FIXTURE_LIVE_GROUP\" -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" >/dev/null <<'SQL'
INSERT INTO settings (key,value,updated_at)
VALUES ('admin_api_key', :'fixture_admin_key', NOW())
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at;
INSERT INTO settings (key,value,updated_at)
VALUES (
  'admin_compliance_acknowledgement:' || :'fixture_admin_user_id',
  json_build_object(
    'version','v2026.06.10',
    'document_zh','docs/legal/admin-compliance.zh.md',
    'document_en','docs/legal/admin-compliance.en.md',
    'admin_user_id',(:'fixture_admin_user_id')::bigint,
    'accepted_at',NOW()
  )::text,
  NOW()
)
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at;
UPDATE users SET balance=GREATEST(balance,100), concurrency=GREATEST(concurrency,1)
WHERE id=(:'fixture_admin_user_id')::bigint;
WITH fixture_group AS (
  INSERT INTO groups (name,status,platform,subscription_type,allow_live,created_at,updated_at)
  VALUES (:'fixture_live_group','active','openai','standard',true,NOW(),NOW())
  RETURNING id
)
INSERT INTO api_keys (key,name,status,quota,quota_used,user_id,group_id,created_at,updated_at)
SELECT :'fixture_live_key','vm-gate-live','active',0,0,(:'fixture_admin_user_id')::bigint,id,NOW(),NOW()
FROM fixture_group;
SQL
fi

mark_stage candidate_health
docker run -d --name "$probe_app" --network "$probe_network" \
  -e SERVER_HOST=0.0.0.0 -e SERVER_PORT="$server_port" -e UPSTREAM_SYNC_AUTO_ENABLED=false \
  -p "127.0.0.1::$server_port" \
  --health-cmd "wget -q -T 5 -O /dev/null http://127.0.0.1:$server_port/health || exit 1" \
  --health-interval 5s --health-timeout 5s --health-start-period 5s --health-retries 6 \
  -v "$probe_dir:/app/data" "$candidate_image_id" >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$probe_app") == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' "$probe_app") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$probe_app") == healthy ]]

if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage runtime_assertion_profile_206_live_capability
  probe_app_port=$(docker port "$probe_app" "$server_port/tcp" | sed -n 's/^127\.0\.0\.1://p')
  [[ $probe_app_port =~ ^[1-9][0-9]{0,4}$ && $probe_app_port -le 65535 ]]
  live_capability_status=$(curl --silent --show-error --max-time 10 \
    --output "$state_dir/live-capability-response.json" --write-out '%{http_code}' \
    -H "x-api-key: $fixture_admin_key" \
    "http://127.0.0.1:$probe_app_port/api/v1/admin/groups/live-capability")
  printf '%s\n' "$live_capability_status" > "$state_dir/live-capability-status"
  [[ $live_capability_status == 200 ]]
  live_capability_response=$(<"$state_dir/live-capability-response.json")
  rm -f "$state_dir/live-capability-response.json"
  jq -e '.code == 0 and .data.supported == false and (.data.reason | type == "string") and (.data.reason | length > 0)' >/dev/null <<<"$live_capability_response"
  fixture_live_key_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT id FROM api_keys WHERE key='$fixture_live_key'\"" | tr -d '\r')
  [[ $fixture_live_key_id =~ ^[1-9][0-9]*$ ]]
  live_usage_before=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$fixture_live_key_id AND request_type=5\"" | tr -d '\r')
  live_redis_before=$(docker exec "$probe_redis" sh -lc "redis-cli --scan --pattern 'live:*'; redis-cli --scan --pattern 'concurrency:live:*'" | awk 'NF{n++} END{print n+0}')
  [[ $live_usage_before == 0 && $live_redis_before == 0 ]]
  live_http_status=$(curl --silent --show-error --max-time 10 \
    --output "$state_dir/live-runtime-response.json" --write-out '%{http_code}' \
    -H "Authorization: Bearer $fixture_live_key" -H "Content-Type: application/json" \
    --data-binary '{"sdp":"v=0\r\n","session":{"model":"gpt-live-vm-gate"}}' \
    "http://127.0.0.1:$probe_app_port/v1/live")
  [[ $live_http_status == 503 ]]
  jq -e '.error.type == "api_error" and (.error.message | type == "string") and (.error.message | length > 0)' >/dev/null "$state_dir/live-runtime-response.json"
  rm -f "$state_dir/live-runtime-response.json"
  live_usage_after=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM usage_logs WHERE api_key_id=$fixture_live_key_id AND request_type=5\"" | tr -d '\r')
  live_redis_after=$(docker exec "$probe_redis" sh -lc "redis-cli --scan --pattern 'live:*'; redis-cli --scan --pattern 'concurrency:live:*'" | awk 'NF{n++} END{print n+0}')
  [[ $live_usage_after == 0 && $live_redis_after == 0 ]]
  unset fixture_admin_key fixture_live_key fixture_live_group live_capability_response
  live_runtime_capability_verified=true
fi

mark_stage migration_assertions
mark_stage migration_assertion_checksums
while IFS=$'\t' read -r migration migration_checksum; do
  recorded_checksum=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT checksum FROM schema_migrations WHERE filename='$migration'\"")
  [[ $recorded_checksum == "$migration_checksum" ]]
done < <(jq -r '.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$manifest")
mark_stage migration_assertion_account_rates
[[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.upstream_key_id IS NOT NULL AND (a.rate_multiplier IS DISTINCT FROM k.rate_multiplier OR a.priority IS DISTINCT FROM ROUND(k.rate_multiplier*100)::int)\"") == 0 ]]
[[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM accounts WHERE extra ?| ARRAY['upstream_rate_multiplier','upstream_source_rate_multiplier','upstream_recharge_rate','upstream_effective_cost_multiplier','sub2api_upstream_rate_multiplier']\"") == 0 ]]
if [[ $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_prompt_audit
  prompt_audit_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"WITH config AS (SELECT COALESCE(NULLIF((SELECT value FROM settings WHERE key='prompt_audit_config'), ''), '{}')::jsonb AS value) SELECT NOT COALESCE((value->>'enabled')::boolean, false) AND NOT COALESCE((value->>'blocking_enabled')::boolean, false) AND NOT COALESCE((value->>'store_pass_events')::boolean, false) AND jsonb_typeof(COALESCE(value->'endpoints', '[]'::jsonb)) = 'array' AND jsonb_array_length(COALESCE(value->'endpoints', '[]'::jsonb)) = 0, (SELECT COUNT(*) FROM prompt_audit_jobs), (SELECT COUNT(*) FROM prompt_audit_events) FROM config\"")
  [[ $prompt_audit_state == 't|0|0' ]]
fi
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_managed_monitor
  managed_monitor_key_name_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT character_maximum_length, (SELECT COUNT(*) FROM api_keys k JOIN channel_monitors m ON m.id=k.managed_monitor_id AND m.managed_api_key_id=k.id WHERE k.purpose='managed_monitor' AND k.deleted_at IS NULL AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name)) FROM information_schema.columns WHERE table_schema='public' AND table_name='api_keys' AND column_name='name'\"")
  [[ $managed_monitor_key_name_state == '103|0' ]]
  managed_monitor_key_names_verified=true
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_reasoning_effort
  reasoning_effort_policy_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COALESCE(MAX(CASE WHEN column_name='max_reasoning_effort' THEN data_type || ':' || is_nullable || ':' || column_default END),''), COALESCE(MAX(CASE WHEN column_name='reasoning_effort_mappings' THEN data_type || ':' || is_nullable || ':' || column_default END),'') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('max_reasoning_effort','reasoning_effort_mappings')\"")
  [[ $reasoning_effort_policy_state == *'character varying:NO:'*"''::character varying"*'|'*'jsonb:NO:'*"'[]'::jsonb"* ]]
  reasoning_effort_policy_verified=true
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_profile_202_alipay
  alipay_mobile_precreate_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*)=1 AND BOOL_AND(value='true') FROM settings WHERE key='ALIPAY_MOBILE_PRECREATE_DEEP_LINK'\"")
  [[ $alipay_mobile_precreate_state == t ]]
  alipay_mobile_precreate_migration_verified=true
  mark_stage migration_assertion_profile_202_group_auth
  group_auth_cache_image_state=$(docker exec -i sub2api-postgres sh -lc "psql -X -q -A -t -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" <<'SQL'
BEGIN;
DO $$
DECLARE
  target_group_id BIGINT;
  target_user_id BIGINT;
  fixture_key TEXT := 'sk-vm-gate-profile-202-auth-cache';
  before_count BIGINT;
  after_image_change BIGINT;
  after_unrelated_change BIGINT;
BEGIN
  SELECT id INTO STRICT target_group_id FROM groups WHERE deleted_at IS NULL ORDER BY id LIMIT 1;
  SELECT id INTO STRICT target_user_id FROM users WHERE deleted_at IS NULL ORDER BY id LIMIT 1;
  INSERT INTO api_keys (user_id,key,name,group_id,status)
  VALUES (target_user_id,fixture_key,'vm-gate-profile-202',target_group_id,'active');
  SELECT COUNT(*) INTO before_count FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  UPDATE groups SET allow_image_generation=NOT allow_image_generation WHERE id=target_group_id;
  SELECT COUNT(*) INTO after_image_change FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  UPDATE groups SET name=name WHERE id=target_group_id;
  SELECT COUNT(*) INTO after_unrelated_change FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  IF after_image_change <> before_count + 1 OR after_unrelated_change <> after_image_change THEN
    RAISE EXCEPTION 'profile 202 group auth-cache invalidation contract failed';
  END IF;
END;
$$;
ROLLBACK;
SELECT true;
SQL
)
  [[ $group_auth_cache_image_state == t ]]
  group_auth_cache_image_generation_verified=true
  mark_stage migration_assertion_profile_202_composite
  composite_model_routes_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.composite_model_routes') IS NOT NULL, COALESCE((SELECT confdeltype='c' FROM pg_constraint WHERE conrelid='public.composite_model_routes'::regclass AND contype='f' LIMIT 1),false), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='public.composite_model_routes'::regclass AND contype='c')=3, (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='composite_model_routes' AND indexname IN ('idx_composite_model_routes_unique_active','idx_composite_model_routes_group_enabled','idx_composite_model_routes_group_priority'))=3\"")
  [[ $composite_model_routes_state == 't|t|t|t' ]]
  composite_model_routes_verified=true
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_profile_206_session_id
  session_id_columns_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FILTER (WHERE table_name='usage_logs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES'), COUNT(*) FILTER (WHERE table_name='batch_image_jobs' AND data_type='character varying' AND character_maximum_length=255 AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND column_name='session_id'\"")
  [[ $session_id_columns_state == '1|1' ]]
  session_id_columns_verified=true
  mark_stage migration_assertion_profile_206_live_request_type
  live_request_type_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*)=1 AND BOOL_AND(pg_get_constraintdef(oid) LIKE '%request_type <= 5%') FROM pg_constraint WHERE conrelid='usage_logs'::regclass AND conname='usage_logs_request_type_check'\"")
  [[ $live_request_type_state == t ]]
  live_request_type_verified=true
  mark_stage migration_assertion_profile_206_group_allow_live
  group_allow_live_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name='allow_live'\"")
  [[ $group_allow_live_state == 'boolean|NO|false' ]]
  group_allow_live_verified=true
  mark_stage migration_assertion_profile_206_email_alias_index
  email_alias_index_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT i.indisvalid,i.indisready,pg_get_expr(i.indexprs,i.indrelid)='replace(lower(TRIM(BOTH FROM email)), ''.''::text, ''''::text)',pg_get_expr(i.indpred,i.indrelid)='(deleted_at IS NULL)',o.opcname='text_pattern_ops' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_opclass o ON o.oid=i.indclass[0] WHERE c.relname='idx_users_email_dot_stripped'\"")
  [[ $email_alias_index_state == 't|t|t|t|t' ]]
  email_alias_index_verified=true
fi
if [[ $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_profile_208_passkey_schema
  passkey_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.passkey_user_handles') IS NOT NULL, to_regclass('public.passkey_credentials') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_user_handles' AND ((column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='user_handle' AND data_type='bytea' AND is_nullable='NO') OR (column_name='created_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_credentials' AND ((column_name='id' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE 'nextval(%') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='credential_id' AND data_type='bytea' AND is_nullable='NO') OR (column_name='name' AND data_type='character varying' AND character_maximum_length=100 AND is_nullable='NO' AND column_default LIKE '''Passkey''%') OR (column_name='credential_data' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='last_used_at' AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name IN ('created_at','updated_at') AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='u'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='passkey_credentials' AND indexname IN ('passkey_credentials_user_id_idx','passkey_credentials_last_used_at_idx'))\"")
  [[ $passkey_schema_state == 't|t|3|8|2|2|2|2' ]]
  passkey_schema_verified=true
fi
if [[ $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_profile_209_user_usage_aggregation_schema
  user_usage_aggregation_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.usage_dashboard_user_hourly') IS NOT NULL, to_regclass('public.usage_dashboard_user_daily') IS NOT NULL, to_regclass('public.usage_dashboard_user_backfill_state') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_hourly' AND ((column_name='bucket_start' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_daily' AND ((column_name='bucket_date' AND data_type='date' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_backfill_state' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name IN ('earliest_covered_date','last_completed_date') AND data_type='date' AND is_nullable='YES') OR (column_name='status' AND data_type='character varying' AND character_maximum_length=20 AND is_nullable='NO' AND column_default LIKE '''unavailable''%') OR (column_name IN ('coverage_start','coverage_end','target_end','completed_at') AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name='attempt_count' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='last_error' AND data_type='text' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass,'usage_dashboard_user_backfill_state'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('idx_usage_dashboard_user_hourly_user_bucket','idx_usage_dashboard_user_daily_user_bucket')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='usage_dashboard_user_backfill_state'::regclass AND contype='c'), (SELECT COUNT(*)=1 AND BOOL_AND(id=1 AND status IN ('available','building','partial','unavailable')) FROM usage_dashboard_user_backfill_state)\"")
  [[ $user_usage_aggregation_schema_state == 't|t|t|9|9|11|3|2|2|3|t' ]]
  user_usage_aggregation_schema_verified=true
fi
if [[ $profile == 212 ]]; then
  mark_stage migration_assertion_profile_212_profit_schema
  group_profit_control_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FILTER (WHERE column_name='profit_control_enabled' AND data_type='boolean' AND is_nullable='NO' AND column_default='false'), COUNT(*) FILTER (WHERE column_name IN ('profit_min_margin','profit_safety_buffer') AND data_type='numeric' AND numeric_precision=10 AND numeric_scale=4 AND is_nullable='NO' AND column_default='0') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'\"")
  [[ $group_profit_control_schema_state == '1|2' ]]
  group_profit_control_schema_verified=true
  mark_stage migration_assertion_profile_212_auth_cache_trigger
  group_profit_trigger_definition=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT pg_get_functiondef('enqueue_group_auth_cache_invalidation()'::regprocedure)\"")
  for trigger_field in status is_exclusive allow_image_generation platform subscription_type rate_multiplier peak_rate_enabled peak_start peak_end peak_rate_multiplier profit_control_enabled profit_min_margin profit_safety_buffer deleted_at; do
    grep -Fq "OLD.$trigger_field IS NOT DISTINCT FROM NEW.$trigger_field" <<<"$group_profit_trigger_definition"
  done
  group_profit_trigger_state=$(docker exec -i sub2api-postgres sh -lc "psql -X -q -A -t -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" <<'SQL'
BEGIN;
DO $$
DECLARE
  target_group_id BIGINT;
  target_user_id BIGINT;
  fixture_key TEXT := 'sk-vm-gate-profile-212-auth-cache';
  before_count BIGINT;
  after_profit_change BIGINT;
  after_image_change BIGINT;
  after_unrelated_change BIGINT;
BEGIN
  SELECT id INTO STRICT target_group_id FROM groups WHERE deleted_at IS NULL ORDER BY id LIMIT 1;
  SELECT id INTO STRICT target_user_id FROM users WHERE deleted_at IS NULL ORDER BY id LIMIT 1;
  INSERT INTO api_keys (user_id,key,name,group_id,status)
  VALUES (target_user_id,fixture_key,'vm-gate-profile-212',target_group_id,'active');
  SELECT COUNT(*) INTO before_count FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  UPDATE groups SET profit_control_enabled=NOT profit_control_enabled WHERE id=target_group_id;
  SELECT COUNT(*) INTO after_profit_change FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  UPDATE groups SET allow_image_generation=NOT allow_image_generation WHERE id=target_group_id;
  SELECT COUNT(*) INTO after_image_change FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  UPDATE groups SET name=name WHERE id=target_group_id;
  SELECT COUNT(*) INTO after_unrelated_change FROM auth_cache_invalidation_outbox WHERE cache_key=encode(sha256(convert_to(fixture_key,'UTF8')),'hex');
  IF after_profit_change <> before_count + 1 OR after_image_change <> after_profit_change + 1 OR after_unrelated_change <> after_image_change THEN
    RAISE EXCEPTION 'profile 212 group auth-cache invalidation contract failed';
  END IF;
END;
$$;
ROLLBACK;
SELECT true;
SQL
)
  [[ $group_profit_trigger_state == t ]]
  group_profit_auth_cache_trigger_verified=true
fi
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]]; then
  mark_stage migration_assertion_195_runtime_current
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" postflight_runtime >/dev/null
  mark_stage migration_assertion_195_runtime_replay
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" postflight_runtime >/dev/null
  verified_replay=true
fi

mark_stage isolated_cleanup
cleanup_probe
[[ $(docker inspect -f '{{.Image}}' sub2api-dev) == "$old_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
integration_verified=true
vm_restore_verified=true

docker save "$candidate_image_id" | gzip -1 > "$state_dir/candidate.tar.gz"
candidate_archive_sha=$(sha256sum "$state_dir/candidate.tar.gz" | awk '{print $1}')
trap - ERR INT TERM
rm -f "$state_dir/validator.stderr"

mark_stage gate_signing
jq -n --slurpfile manifest "$manifest" \
  --arg candidate_image_id "$candidate_image_id" \
  --arg candidate_archive_sha256 "$candidate_archive_sha" \
  --argjson candidate_size "$candidate_size" \
  --argjson prompt_audit_disabled "$([[ $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]] && printf true || printf false)" \
  --argjson migration_195_verified "$([[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 ]] && printf true || printf false)" \
  --argjson managed_monitor_key_names_verified "$managed_monitor_key_names_verified" \
  --argjson reasoning_effort_policy_verified "$reasoning_effort_policy_verified" \
  --argjson alipay_mobile_precreate_migration_verified "$alipay_mobile_precreate_migration_verified" \
  --argjson group_auth_cache_image_generation_verified "$group_auth_cache_image_generation_verified" \
  --argjson composite_model_routes_verified "$composite_model_routes_verified" \
  --argjson session_id_columns_verified "$session_id_columns_verified" \
  --argjson live_request_type_verified "$live_request_type_verified" \
  --argjson group_allow_live_verified "$group_allow_live_verified" \
  --argjson email_alias_index_verified "$email_alias_index_verified" \
  --argjson live_runtime_capability_verified "$live_runtime_capability_verified" \
  --argjson passkey_schema_verified "$passkey_schema_verified" \
  --argjson user_usage_aggregation_schema_verified "$user_usage_aggregation_schema_verified" \
  --argjson group_profit_control_schema_verified "$group_profit_control_schema_verified" \
  --argjson group_profit_auth_cache_trigger_verified "$group_profit_auth_cache_trigger_verified" \
  --arg migration_211_status "$migration_211_status" \
  --arg migration_212_status "$migration_212_status" \
  --arg vm_old_image_id "$old_image_id" \
  --argjson vm_old_image_compatibility_verified "$vm_old_image_compatibility_verified" \
  --argjson fixture_rejected "$fixture_rejected" \
  --argjson restore_completed "$restore_completed" \
  --argjson clean_preflight "$clean_preflight" \
  --argjson verified_replay "$verified_replay" \
  --argjson verified_low_watermark_rejected "$verified_low_watermark_rejected" \
  '{manifest:$manifest[0],evidence:{candidate_image_id:$candidate_image_id,candidate_archive_sha256:$candidate_archive_sha256,candidate_size:$candidate_size,integration_verified:true,vm_restore_verified:true,vm_database_boundary:true,vm_redis_boundary:true,data_dev_boundary:true,prompt_audit_disabled:$prompt_audit_disabled,migration_195_verified:$migration_195_verified,managed_monitor_key_names_verified:$managed_monitor_key_names_verified,reasoning_effort_policy_verified:$reasoning_effort_policy_verified,alipay_mobile_precreate_migration_verified:$alipay_mobile_precreate_migration_verified,group_auth_cache_image_generation_verified:$group_auth_cache_image_generation_verified,composite_model_routes_verified:$composite_model_routes_verified,session_id_columns_verified:$session_id_columns_verified,live_request_type_verified:$live_request_type_verified,group_allow_live_verified:$group_allow_live_verified,email_alias_index_verified:$email_alias_index_verified,live_runtime_capability_verified:$live_runtime_capability_verified,passkey_schema_verified:$passkey_schema_verified,user_usage_aggregation_schema_verified:$user_usage_aggregation_schema_verified,migration_211_status:$migration_211_status,migration_212_status:$migration_212_status,group_profit_control_schema_verified:$group_profit_control_schema_verified,group_profit_auth_cache_trigger_verified:$group_profit_auth_cache_trigger_verified,vm_old_image_id:$vm_old_image_id,vm_old_image_compatibility_verified:$vm_old_image_compatibility_verified,fixture_rejected:$fixture_rejected,restore_completed:$restore_completed,clean_preflight:$clean_preflight,verified_replay:$verified_replay,verified_low_watermark_rejected:$verified_low_watermark_rejected}}' \
  | jq -cS . > "$output_dir/gate.json.tmp"
chmod 400 "$output_dir/gate.json.tmp"
mv -T -- "$output_dir/gate.json.tmp" "$output_dir/gate.json"
/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json" "$output_dir/gate.sig"
ln "$state_dir/candidate.tar.gz" "$output_dir/candidate.tar.gz"
sha256sum "$output_dir/gate.json" "$output_dir/gate.sig" "$output_dir/candidate.tar.gz" > "$output_dir/SHA256SUMS"
chmod 400 "$output_dir/gate.json" "$output_dir/gate.sig" "$output_dir/candidate.tar.gz" "$output_dir/SHA256SUMS"
rm -rf "$state_dir/backup"
rm -f "$state_dir/candidate.tar.gz"
mark_stage verified
printf 'gate_status=verified\n'
printf 'candidate_image_id=%s\n' "$candidate_image_id"
printf 'candidate_archive_sha256=%s\n' "$candidate_archive_sha"
