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
[[ $release_id =~ ^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $profile == 182 || $profile == 187 || $profile == 191 || $profile == 192 || $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]
[[ $release_id == "$profile-${commit:0:12}-"* ]]
[[ $(jq -er '.schema' "$manifest") == 1 ]]
[[ $(jq -er '.version' "$manifest") == "$version" ]]
layout_field=$(jq -er 'if has("release_asset_layout") then .release_asset_layout else "__missing__" end' "$manifest")
case "$layout_field" in
  __missing__)
    release_asset_layout=deploy-v1
    release_asset_pattern='^(deploy/(release\.py|release/([^/]+|trust/[^/]+|drverify/[^/]+)|maintenance/release/[^/]+|maintenance/181/(mask-backup-units|restore-backup-units)\.sh))$'
    migration_assertion_dir="$source_dir/deploy/maintenance/release"
    ;;
  skill-v1)
    release_asset_layout=skill-v1
    release_asset_pattern='^(\.agents/skills/sub2api-production-deploy/scripts/(release\.py|release/([^/]+|trust/[^/]+|drverify/[^/]+)|maintenance/release/[^/]+|maintenance/181/(mask-backup-units|restore-backup-units)\.sh|logging/release_logging/[^/]+|windows/[^/]+))$'
    migration_assertion_dir="$source_dir/.agents/skills/sub2api-production-deploy/scripts/maintenance/release"
    ;;
  *)
    exit 1
    ;;
esac
[[ $(jq -er '.migrations | length' "$manifest") == $(jq -er '.migration_sha256 | length' "$manifest") ]]
jq -e '.migrations as $m | (.migration_sha256 | keys) == ($m | sort)' "$manifest" >/dev/null
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  if [[ $profile == 232 ]]; then
    [[ $version == 0.1.173-baiyu ]]
    [[ $(jq -er '.migrations | length' "$manifest") == 49 ]]
    expected_tail='216_channel_monitor_v2.sql|217_channel_monitor_mode.sql|218_channel_monitor_v2_ignored_error_categories.sql|219_channel_monitor_v2_seed_popular_models.sql|220_channel_monitor_v2_health_thresholds.sql|221_channel_monitor_v2_fixed_rollups.sql|222_channel_monitor_v2_rollup_permissions.sql|223_channel_monitor_v2_refresh_5m.sql|224_channel_monitor_v2_full_table_permissions.sql|225_channel_monitor_v2_default_ignore_and_cache.sql|226_channel_monitor_hide_throughput.sql|227_channel_monitor_v2_reset_factory_cache_thresholds.sql|228_channel_monitor_v2_privacy_defaults.sql|229_group_video_model_prices.sql|230_group_audio_voice_pricing.sql|231_group_search_price_per_1k.sql|232_clear_non_grok_video_generation_config.sql'
    [[ $(jq -er '.migrations[-17:] | join("|")' "$manifest") == "$expected_tail" ]]
  elif [[ $profile == 233 || $profile == 234 ]]; then
    if [[ $profile == 233 ]]; then
      [[ $version == 0.1.173-baiyu ]]
    else
      [[ $version == 0.1.175-baiyu ]]
    fi
    [[ $(jq -er '.migrations | length' "$manifest") == 50 ]]
    expected_tail='216_channel_monitor_v2.sql|217_channel_monitor_mode.sql|218_channel_monitor_v2_ignored_error_categories.sql|219_channel_monitor_v2_seed_popular_models.sql|220_channel_monitor_v2_health_thresholds.sql|221_channel_monitor_v2_fixed_rollups.sql|222_channel_monitor_v2_rollup_permissions.sql|223_channel_monitor_v2_refresh_5m.sql|224_channel_monitor_v2_full_table_permissions.sql|225_channel_monitor_v2_default_ignore_and_cache.sql|226_channel_monitor_hide_throughput.sql|227_channel_monitor_v2_reset_factory_cache_thresholds.sql|228_channel_monitor_v2_privacy_defaults.sql|229_group_video_model_prices.sql|230_group_audio_voice_pricing.sql|231_group_search_price_per_1k.sql|232_clear_non_grok_video_generation_config.sql|233_upstream_management.sql'
    [[ $(jq -er '.migrations[-18:] | join("|")' "$manifest") == "$expected_tail" ]]
  elif [[ $profile == 235 || $profile == 236 ]]; then
    if [[ $profile == 235 ]]; then
      [[ $version == 0.1.176-baiyu ]]
    else
      [[ $version == 0.176-baiyu ]]
    fi
    [[ $(jq -er '.migrations | length' "$manifest") == 51 ]]
    expected_tail='216_channel_monitor_v2.sql|217_channel_monitor_mode.sql|218_channel_monitor_v2_ignored_error_categories.sql|219_channel_monitor_v2_seed_popular_models.sql|220_channel_monitor_v2_health_thresholds.sql|221_channel_monitor_v2_fixed_rollups.sql|222_channel_monitor_v2_rollup_permissions.sql|223_channel_monitor_v2_refresh_5m.sql|224_channel_monitor_v2_full_table_permissions.sql|225_channel_monitor_v2_default_ignore_and_cache.sql|226_channel_monitor_hide_throughput.sql|227_channel_monitor_v2_reset_factory_cache_thresholds.sql|228_channel_monitor_v2_privacy_defaults.sql|229_group_video_model_prices.sql|230_group_audio_voice_pricing.sql|231_group_search_price_per_1k.sql|232_clear_non_grok_video_generation_config.sql|233_upstream_management.sql|234_group_model_pricing.sql'
    [[ $(jq -er '.migrations[-19:] | join("|")' "$manifest") == "$expected_tail" ]]
  elif [[ $profile == 237 ]]; then
    [[ $version == 0.1.177-baiyu ]]
    [[ $(jq -er '.migrations | length' "$manifest") == 53 ]]
    expected_tail='233_upstream_management.sql|234_group_model_pricing.sql|235_group_usage_daily_rollups.sql|236_group_usage_rollup_timezone.sql'
    [[ $(jq -er '.migrations[-4:] | join("|")' "$manifest") == "$expected_tail" ]]
  fi
fi
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
git fetch origin +main:refs/remotes/origin/main >/dev/null 2>&1
[[ $(git rev-parse origin/main) == "$commit" ]]
git reset --hard "$commit" >/dev/null
while IFS=$'\t' read -r relative expected; do
  [[ $relative =~ $release_asset_pattern ]]
  [[ -f $source_dir/$relative && ! -L $source_dir/$relative ]]
  [[ $(sha256sum "$source_dir/$relative" | awk '{print $1}') == "$expected" ]]
done < <(jq -r '.release_asset_sha256 | to_entries[] | [.key,.value] | @tsv' "$manifest")
bash "$source_dir/.agents/skills/sub2api-production-deploy/scripts/tests/release/compose-contract-integration.sh" >/dev/null
if [[ $release_asset_layout == skill-v1 ]]; then
  source "$migration_assertion_dir/compose-contract.sh"
fi
[[ -d $data_dir && ! -L $data_dir ]]
old_image_id=$(docker inspect -f '{{.Image}}' sub2api-dev)
old_image_ref=$(docker inspect -f '{{.Config.Image}}' sub2api-dev)
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]]
compat_image_id=$old_image_id
if [[ $profile == 215 ]]; then
  compat_merge_commit=$(git rev-list --first-parent --merges -n 1 "$commit")
  [[ $compat_merge_commit =~ ^[0-9a-f]{40}$ ]]
  compat_commit=$(git rev-parse "$compat_merge_commit^1")
  [[ $compat_commit =~ ^[0-9a-f]{40}$ ]]
  compat_tag="sub2api:baiyu-0.1.171-baiyu-$compat_commit"
  compat_image_id=$(docker image inspect -f '{{.Id}}' "$compat_tag")
  [[ $compat_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  compat_version=$(jq -er '.compatibility_version' "$manifest")
  compat_commit=$(jq -er '.compatibility_commit' "$manifest")
  expected_compat_image_id=$(jq -er '.compatibility_image_id' "$manifest")
  [[ $compat_version == 0.1.172-baiyu ]]
  [[ $compat_commit == 74e47e67205084750ccd994c331ead328e4ce35b ]]
  [[ $expected_compat_image_id == sha256:cd3dff0ce18762d7faa9d4a4492eb770b616f9b01b66256ce6280c2f4855abd6 ]]
  compat_tag="sub2api:baiyu-$compat_version-$compat_commit"
  compat_image_id=$(docker image inspect -f '{{.Id}}' "$compat_tag")
  [[ $compat_image_id == "$expected_compat_image_id" ]]
fi
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
current_image_size=$(docker image inspect -f '{{.Size}}' "$old_image_id" 2>/dev/null || true)
if [[ ! $current_image_size =~ ^[0-9]+$ ]]; then
  current_image_size=$(docker inspect --size -f '{{.SizeRootFs}}' sub2api-dev 2>/dev/null || true)
fi
[[ $current_image_size =~ ^[0-9]+$ && $current_image_size -gt 0 ]]
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
  --build-arg GOLANG_IMAGE=docker.m.daocloud.io/library/golang:1.26.6-alpine \
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
  failed_line=${1:-0}
  trap - ERR INT TERM
  [[ $failed_line =~ ^[0-9]+$ ]] || failed_line=0
  category=unknown
  current_stage=$(<"$state_dir/stage")
  if [[ $current_stage == migration_assertion_* || $current_stage == runtime_assertion_* ]]; then
    category=$current_stage
  fi
  if [[ $current_stage == old_image_compatibility_* ]]; then
    category=$current_stage
  fi
  if [[ $current_stage == candidate_health || $current_stage == candidate_background_activation ]]; then
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
    chmod 400 "$state_dir/migrate-candidate.log" || true
  fi
  if [[ -f $state_dir/stage && ( $(<"$state_dir/stage") == candidate_health || $(<"$state_dir/stage") == candidate_background_activation ) ]] && docker inspect "$probe_app" >/dev/null 2>&1; then
    probe_log="$state_dir/probe-app.log"
    docker logs --tail 300 "$probe_app" > "$probe_log" 2>&1 || true
    grep -qi 'permission denied' "$probe_log" && category=permission
    grep -qi 'connection refused\|no such host\|dial tcp' "$probe_log" && category=connection
    grep -qi 'redis' "$probe_log" && category=redis
    # Only classify database failures when an error-like token is present on
    # the same log line. Ordinary startup messages mentioning the database
    # must not mask the real candidate stage (for example activation timeout).
    grep -Eiq '(error|failed|fatal|panic).*(database|postgres)|(database|postgres).*(error|failed|fatal|panic)' "$probe_log" && category=database
    grep -qi 'address already in use' "$probe_log" && category=port_conflict
    grep -qi 'panic\|fatal' "$probe_log" && category=fatal
    chmod 400 "$probe_log" || true
  fi
  if [[ -f $state_dir/stage && $(<"$state_dir/stage") == candidate_background_activation ]]; then
    if [[ -f ${activation_headers:-} && ! -L ${activation_headers:-} ]]; then
      cp -- "$activation_headers" "$state_dir/candidate-background-headers"
      chmod 400 "$state_dir/candidate-background-headers" || true
      if grep -Eiq '^x-sub2api-background-failure:[[:space:]]*[a-z0-9_]+[[:space:]]*$' "$activation_headers"; then
        sed -nE 's/^x-sub2api-background-failure:[[:space:]]*([a-z0-9_]+)[[:space:]]*$/\1/ip' "$activation_headers" | head -n1 > "$state_dir/candidate-background-header-failure"
        chmod 400 "$state_dir/candidate-background-header-failure" || true
      fi
    fi
    marker_source=
    if docker inspect "$probe_app" >/dev/null 2>&1; then
      marker_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$probe_app" 2>/dev/null || true)
    fi
    if [[ -n $marker_source && -f $marker_source/.sub2api-active-instance && ! -L $marker_source/.sub2api-active-instance ]]; then
      cp -- "$marker_source/.sub2api-active-instance" "$state_dir/candidate-activation-marker"
      chmod 400 "$state_dir/candidate-activation-marker" || true
    fi
    if [[ -n $marker_source ]]; then
      marker_exists=false
      [[ -f $marker_source/.sub2api-active-instance && ! -L $marker_source/.sub2api-active-instance ]] && marker_exists=true
      printf 'source_kind=%s marker_exists=%s\n' \
        "$(case "$marker_source" in "$probe_dir") printf expected;; /opt/sub2api-deploy/release-gates/*) printf release_gate;; *) printf other;; esac)" \
        "$marker_exists" > "$state_dir/candidate-marker-location"
      chmod 400 "$state_dir/candidate-marker-location" || true
    fi
  fi
  printf '%s\n' "$category" > "$state_dir/failure-category"
  printf '%s\n' "$failed_line" > "$state_dir/failure-line"
  chmod 400 "$state_dir/failure-category" "$state_dir/failure-line" || true
  if [[ -f $state_dir/validator.stderr && ! -L $state_dir/validator.stderr ]]; then
    chmod 400 "$state_dir/validator.stderr" || true
  fi
  cleanup_probe
  rm -f "$state_dir/candidate.tar.gz"
  cleanup_candidate_tag
  exit "$code"
}
trap 'on_failure $LINENO' ERR INT TERM
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
usage_log_upstream_model_columns_verified=false
usage_log_upstream_model_mismatch_index_verified=false
channel_monitor_v2_schema_verified=false
channel_monitor_v2_defaults_verified=false
group_media_pricing_schema_verified=false
group_media_auth_cache_trigger_verified=false
migration_232_data_plan_verified=false
migration_232_postflight_verified=false
migration_233_preflight_verified=false
migration_235_schema_verified=false
migration_236_schema_verified=false
migration_233_postflight_verified=false
migration_234_preflight_verified=false
migration_235_preflight_verified=false
migration_236_preflight_verified=false
vm_old_image_compatibility_verified=false
migration_211_status=not_applicable
migration_212_status=not_applicable
migration_214_status=not_applicable
migration_215_status=not_applicable
migration_216_status=not_applicable
migration_217_status=not_applicable
migration_218_status=not_applicable
migration_219_status=not_applicable
migration_220_status=not_applicable
migration_221_status=not_applicable
migration_222_status=not_applicable
migration_223_status=not_applicable
migration_224_status=not_applicable
migration_225_status=not_applicable
migration_226_status=not_applicable
migration_227_status=not_applicable
migration_228_status=not_applicable
migration_229_status=not_applicable
migration_230_status=not_applicable
migration_231_status=not_applicable
migration_232_status=not_applicable
migration_233_status=not_applicable
migration_234_status=not_applicable
if [[ $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_212_status
  profile_212_migration_status() {
    local filename=$1 migration_number expected actual command_status
    migration_number=${filename%%_*}
    mark_stage "migration_assertion_status_${migration_number}"
    set +e
    expected=$(jq -er --arg filename "$filename" '.migration_sha256[$filename]' "$manifest")
    command_status=$?
    set -e
    if [[ $command_status != 0 || ! $expected =~ ^[0-9a-f]{64}$ ]]; then
      mark_stage "migration_assertion_status_manifest_${migration_number}"
      return 1
    fi
    set +e
    actual=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT checksum FROM schema_migrations WHERE filename='$filename'\"" 2>/dev/null | tr -d '\r\n')
    command_status=$?
    set -e
    if [[ $command_status != 0 ]]; then
      mark_stage "migration_assertion_status_query_${migration_number}"
      return 1
    fi
    if [[ -z $actual ]]; then
      printf 'absent\n'
    else
      if [[ ! $actual =~ ^[0-9a-f]{64}$ || $actual != "$expected" ]]; then
        mark_stage "migration_assertion_status_checksum_${migration_number}"
        return 1
      fi
      printf 'verified\n'
    fi
  }
  migration_211_status=$(profile_212_migration_status 211_group_profit_control.sql)
  migration_212_status=$(profile_212_migration_status 212_group_profit_control_auth_cache_invalidation.sql)
fi
if [[ $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_214_status=$(profile_212_migration_status 214_add_usage_log_upstream_response_model.sql)
  migration_215_status=$(profile_212_migration_status 215_add_usage_log_upstream_model_mismatch_index_notx.sql)
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_216_status=$(profile_212_migration_status 216_channel_monitor_v2.sql)
  migration_217_status=$(profile_212_migration_status 217_channel_monitor_mode.sql)
  migration_218_status=$(profile_212_migration_status 218_channel_monitor_v2_ignored_error_categories.sql)
  migration_219_status=$(profile_212_migration_status 219_channel_monitor_v2_seed_popular_models.sql)
  migration_220_status=$(profile_212_migration_status 220_channel_monitor_v2_health_thresholds.sql)
  migration_221_status=$(profile_212_migration_status 221_channel_monitor_v2_fixed_rollups.sql)
  migration_222_status=$(profile_212_migration_status 222_channel_monitor_v2_rollup_permissions.sql)
  migration_223_status=$(profile_212_migration_status 223_channel_monitor_v2_refresh_5m.sql)
  migration_224_status=$(profile_212_migration_status 224_channel_monitor_v2_full_table_permissions.sql)
  migration_225_status=$(profile_212_migration_status 225_channel_monitor_v2_default_ignore_and_cache.sql)
  migration_226_status=$(profile_212_migration_status 226_channel_monitor_hide_throughput.sql)
  migration_227_status=$(profile_212_migration_status 227_channel_monitor_v2_reset_factory_cache_thresholds.sql)
  migration_228_status=$(profile_212_migration_status 228_channel_monitor_v2_privacy_defaults.sql)
  migration_229_status=$(profile_212_migration_status 229_group_video_model_prices.sql)
  migration_230_status=$(profile_212_migration_status 230_group_audio_voice_pricing.sql)
  migration_231_status=$(profile_212_migration_status 231_group_search_price_per_1k.sql)
  migration_232_status=$(profile_212_migration_status 232_clear_non_grok_video_generation_config.sql)
fi
if [[ $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_233_status=$(profile_212_migration_status 233_upstream_management.sql)
fi
if [[ $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_234_status=$(profile_212_migration_status 234_group_model_pricing.sql)
fi
if [[ $profile == 237 ]]; then
  migration_235_status=$(profile_212_migration_status 235_group_usage_daily_rollups.sql)
  migration_236_status=$(profile_212_migration_status 236_group_usage_rollup_timezone.sql)
fi
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_195_fixture
  migration_195_context="$state_dir/migration-195-context.sh"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$state_dir" > "$migration_195_context"
  chmod 400 "$migration_195_context"
  fixture_key_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT k.id FROM upstream_keys k JOIN accounts a ON a.upstream_key_id=k.id WHERE k.rate_multiplier IS NOT NULL ORDER BY k.id LIMIT 1\"")
  [[ $fixture_key_id =~ ^[0-9]+$ ]]
  fixture_key_hash=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT md5(row_to_json(k)::text) FROM upstream_keys k WHERE id=$fixture_key_id\"")
  [[ $fixture_key_hash =~ ^[0-9a-f]{32}$ ]]
  docker exec sub2api-postgres sh -lc "psql -X -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"UPDATE upstream_keys SET rate_multiplier=NULL WHERE id=$fixture_key_id\"" >/dev/null
  if ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=absent RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-195-assert.sh" preflight >/dev/null 2>&1; then
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
    docker exec -i sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" >/dev/null <<'SQL'
WITH expected_accounts AS (
  SELECT COALESCE(jsonb_agg(id ORDER BY id), '[]'::jsonb) AS ids,
         COUNT(*) AS affected
    FROM accounts
   WHERE deleted_at IS NULL
     AND upstream_key_id IS NOT NULL
)
INSERT INTO scheduler_outbox (event_type, payload)
SELECT 'account_bulk_changed', jsonb_build_object('account_ids', expected_accounts.ids)
  FROM expected_accounts
 WHERE expected_accounts.affected > 0
   AND NOT EXISTS (
     SELECT 1
       FROM scheduler_outbox
      WHERE event_type = 'account_bulk_changed'
        AND payload->'account_ids' = expected_accounts.ids
   );
SQL
    probe_outbox_highwater=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COALESCE(MAX(id),0) FROM scheduler_outbox\"" | tr -d '\r')
    [[ $probe_outbox_highwater =~ ^[0-9]+$ ]]
    docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$probe_outbox_highwater" >/dev/null
    migration_195_status=verified
  fi
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-195-assert.sh" preflight >/dev/null
  clean_preflight=true
  cp "$state_dir/migration-195-data-plan.sha256" "$state_dir/fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$state_dir/fake-recovery.sha256")" > "$state_dir/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-195-assert.sh" bind >/dev/null
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  docker exec sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"INSERT INTO settings (key,value,updated_at) VALUES ('ALIPAY_MOBILE_PRECREATE_DEEP_LINK','true',NOW()) ON CONFLICT (key) DO UPDATE SET value='true',updated_at=NOW()\"" >/dev/null
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_232_context="$state_dir/migration-232-context.sh"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$state_dir" > "$migration_232_context"
  chmod 400 "$migration_232_context"
  if [[ $migration_232_status == absent ]]; then
    fixture_group_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT id FROM groups WHERE platform IS DISTINCT FROM 'grok' AND platform IS DISTINCT FROM 'composite' ORDER BY id LIMIT 1\"")
    [[ $fixture_group_id =~ ^[1-9][0-9]*$ ]]
    docker exec sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"UPDATE groups SET video_price_480p=0.1234 WHERE id=$fixture_group_id\"" >/dev/null
  fi
  ASSERT_CONTEXT_FILE="$migration_232_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_232_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-232-assert.sh" preflight >/dev/null
  cp "$state_dir/migration-232-data-plan.sha256" "$state_dir/migration-232-fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$state_dir/migration-232-fake-recovery.sha256")" > "$state_dir/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_232_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_232_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-232-assert.sh" bind >/dev/null
  migration_232_data_plan_verified=true
fi
if [[ $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  migration_233_context="$state_dir/migration-233-context.sh"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$state_dir" > "$migration_233_context"
  chmod 400 "$migration_233_context"
  ASSERT_CONTEXT_FILE="$migration_233_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_233_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-233-assert.sh" preflight >/dev/null
  migration_233_preflight_verified=true
fi
if [[ $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  group_model_pricing_context="$state_dir/migration-234-context.sh"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$state_dir" > "$group_model_pricing_context"
  chmod 400 "$group_model_pricing_context"
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_234_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-234-assert.sh" preflight >/dev/null
  migration_234_preflight_verified=true
fi
if [[ $profile == 237 ]]; then
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_235_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-235-assert.sh" preflight >/dev/null
  migration_235_preflight_verified=true
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_236_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-236-assert.sh" preflight >/dev/null
  migration_236_preflight_verified=true
fi
docker run --rm --network "$probe_network" -v "$probe_dir:/app/data" "$candidate_image_id" /app/sub2api --migrate-only >"$state_dir/migrate-candidate.log" 2>&1
rm -f "$state_dir/migrate-candidate.log"
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_195_postflight_db
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-195-assert.sh" postflight_db >/dev/null
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
  mark_stage migration_assertion_profile_195_low_watermark
  low_watermark=$((sentinel_event_id - 1))
  docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$low_watermark" >/dev/null
  migration_195_low_state="$state_dir/migration-195-verified-low"
  migration_195_low_context="$state_dir/migration-195-verified-low-context.sh"
  install -d -m 700 "$migration_195_low_state"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$migration_195_low_state" > "$migration_195_low_context"
  chmod 400 "$migration_195_low_context"
  if ASSERT_CONTEXT_FILE="$migration_195_low_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_low_state" bash "$migration_assertion_dir/migration-195-assert.sh" preflight >/dev/null 2>&1; then
    false
  fi
  verified_low_watermark_rejected=true
  docker exec "$probe_redis" redis-cli SET sched:v2:outbox:watermark "$sentinel_event_id" >/dev/null
  mark_stage migration_assertion_profile_195_verified_replay
  migration_195_verified_state="$state_dir/migration-195-verified"
  migration_195_verified_context="$state_dir/migration-195-verified-context.sh"
  install -d -m 700 "$migration_195_verified_state"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$migration_195_verified_state" > "$migration_195_verified_context"
  chmod 400 "$migration_195_verified_context"
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_CONFIG_FILE="$probe_dir/config.yaml" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$migration_assertion_dir/migration-195-assert.sh" preflight >/dev/null
  cp "$migration_195_verified_state/migration-195-data-plan.sha256" "$migration_195_verified_state/fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$migration_195_verified_state/fake-recovery.sha256")" > "$migration_195_verified_state/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$migration_assertion_dir/migration-195-assert.sh" bind >/dev/null
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$migration_assertion_dir/migration-195-assert.sh" postflight_db >/dev/null
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage old_image_compatibility_start
  if [[ $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
    mark_stage old_image_compatibility_version
    old_image_version_output=$(docker run --rm --entrypoint /app/sub2api "$compat_image_id" --version 2>&1)
    if [[ $profile == 215 ]]; then
      grep -Fq 'Sub2API 0.1.171-baiyu ' <<<"$old_image_version_output"
    else
      grep -Fq 'Sub2API 0.1.172-baiyu ' <<<"$old_image_version_output"
    fi
    unset old_image_version_output
  fi
  docker run -d --name "$old_probe_app" --network "$probe_network" \
    -e SERVER_HOST=0.0.0.0 -e SERVER_PORT="$server_port" -e UPSTREAM_SYNC_AUTO_ENABLED=false \
    -p "127.0.0.1::$server_port" \
    --health-cmd "wget -q -T 5 -O /dev/null http://127.0.0.1:$server_port/health || exit 1" \
    --health-interval 5s --health-timeout 5s --health-start-period 5s --health-retries 6 \
    -v "$probe_dir:/app/data" "$compat_image_id" >/dev/null 2>&1
  mark_stage old_image_compatibility_health
  for _ in $(seq 1 90); do
    [[ $(docker inspect -f '{{.State.Health.Status}}' "$old_probe_app") == healthy ]] && break
    sleep 2
  done
  mark_stage old_image_compatibility_image
  [[ $(docker inspect -f '{{.Image}}' "$old_probe_app") == "$compat_image_id" ]]
  mark_stage old_image_compatibility_health
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$old_probe_app") == healthy ]]
  mark_stage old_image_compatibility_network
  old_probe_port=$(docker port "$old_probe_app" "$server_port/tcp" | sed -n 's/^127\.0\.0\.1://p')
  [[ $old_probe_port =~ ^[1-9][0-9]{0,4}$ && $old_probe_port -le 65535 ]]
  mark_stage old_image_compatibility_auth
  set +e
  old_image_auth_status=$(curl -q --noproxy '*' --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 10 "http://127.0.0.1:$old_probe_port/api/v1/auth/me")
  old_image_auth_command_status=$?
  set -e
  if [[ $old_image_auth_command_status == 0 && $old_image_auth_status =~ ^[0-9]{3}$ ]]; then
    printf '%s\n' "$old_image_auth_status" > "$state_dir/old-image-auth-status"
  fi
  [[ $old_image_auth_command_status == 0 ]]
  [[ $old_image_auth_status == 401 ]]
  rm -f "$state_dir/old-image-auth-status"
  docker rm -f "$old_probe_app" >/dev/null
  vm_old_image_compatibility_verified=true
fi
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage runtime_fixture_profile_206_sequences
  docker exec -i sub2api-postgres sh -lc "psql -X -q -v ON_ERROR_STOP=1 -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" >/dev/null <<'SQL'
SELECT setval(pg_get_serial_sequence('groups','id'), COALESCE(MAX(id),0)+1, false) FROM groups;
SELECT setval(pg_get_serial_sequence('api_keys','id'), COALESCE(MAX(id),0)+1, false) FROM api_keys;
SELECT setval(pg_get_serial_sequence('group_rate_snapshots','id'), COALESCE(MAX(id),0)+1, false) FROM group_rate_snapshots;
SELECT setval(pg_get_serial_sequence('upstream_events','id'), COALESCE(MAX(id),0)+1, false) FROM upstream_events;
SQL
  mark_stage runtime_fixture_profile_206_admin
  admin_user_id=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT id FROM users WHERE role='admin' AND status='active' AND deleted_at IS NULL ORDER BY id LIMIT 1\"" | tr -d '\r')
  [[ $admin_user_id =~ ^[1-9][0-9]*$ ]]
  fixture_admin_key="admin-vm-gate-profile-206-${release_id}"
  fixture_live_key="sk-vm-gate-profile-206-${release_id}"
  fixture_live_group="vm-gate-live-${release_id}"
  mark_stage runtime_fixture_profile_206_insert
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
activation_instance="$release_id-vm-gate"
rm -f "$probe_dir/.sub2api-active-instance"
docker run -d --name "$probe_app" --network "$probe_network" \
  -e SERVER_HOST=0.0.0.0 -e SERVER_PORT="$server_port" -e UPSTREAM_SYNC_AUTO_ENABLED=false \
  -e "SUB2API_INSTANCE_ID=$activation_instance" \
  -e SUB2API_BACKGROUND_ACTIVATION_FILE=/app/data/.sub2api-active-instance \
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
if [[ $release_asset_layout == skill-v1 ]]; then
  mark_stage candidate_background_activation
  probe_app_port=$(docker port "$probe_app" "$server_port/tcp" | sed -n 's/^127\.0\.0\.1://p')
  [[ $probe_app_port =~ ^[1-9][0-9]{0,4}$ && $probe_app_port -le 65535 ]]
  activation_headers="$state_dir/activation-health.headers"
  [[ $(curl -sS -D "$activation_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$probe_app_port/health") == 200 ]]
  assert_http_header_equals "$activation_headers" X-Sub2API-Instance "$activation_instance"
  assert_http_header_equals "$activation_headers" X-Sub2API-Background-Ready false
  if ! write_release_activation_marker "$probe_app" "$activation_instance"; then
    printf '%s\n' "${RELEASE_ACTIVATION_MARKER_FAILURE_REASON:-unknown}" > "$state_dir/candidate-background-failure"
    chmod 400 "$state_dir/candidate-background-failure" || true
    exit 1
  fi
  marker_before_loop=false
  [[ -f "$probe_dir/.sub2api-active-instance" && ! -L "$probe_dir/.sub2api-active-instance" ]] && marker_before_loop=true
  printf 'before_loop=%s\n' "$marker_before_loop" > "$state_dir/candidate-marker-presence"
  chmod 400 "$state_dir/candidate-marker-presence" || true
  activation_ready=false
  for _ in $(seq 1 120); do
    : > "$activation_headers"
    if [[ $(curl -sS -D "$activation_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:$probe_app_port/health" 2>/dev/null || true) == 200 ]] &&
       assert_http_header_equals "$activation_headers" X-Sub2API-Instance "$activation_instance" &&
       assert_http_header_equals "$activation_headers" X-Sub2API-Background-Ready true; then
      activation_ready=true
      break
    fi
    sleep 1
  done
  marker_after_loop=false
  [[ -f "$probe_dir/.sub2api-active-instance" && ! -L "$probe_dir/.sub2api-active-instance" ]] && marker_after_loop=true
  printf 'after_loop=%s\n' "$marker_after_loop" >> "$state_dir/candidate-marker-presence"
  rm -f "$activation_headers"
  [[ $activation_ready == true ]]
fi

if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
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
if [[ $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_prompt_audit
  prompt_audit_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"WITH config AS (SELECT COALESCE(NULLIF((SELECT value FROM settings WHERE key='prompt_audit_config'), ''), '{}')::jsonb AS value) SELECT NOT COALESCE((value->>'enabled')::boolean, false) AND NOT COALESCE((value->>'blocking_enabled')::boolean, false) AND NOT COALESCE((value->>'store_pass_events')::boolean, false) AND jsonb_typeof(COALESCE(value->'endpoints', '[]'::jsonb)) = 'array' AND jsonb_array_length(COALESCE(value->'endpoints', '[]'::jsonb)) = 0, (SELECT COUNT(*) FROM prompt_audit_jobs), (SELECT COUNT(*) FROM prompt_audit_events) FROM config\"")
  [[ $prompt_audit_state == 't|0|0' ]]
fi
if [[ $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_managed_monitor
  managed_monitor_key_name_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT character_maximum_length, (SELECT COUNT(*) FROM api_keys k JOIN channel_monitors m ON m.id=k.managed_monitor_id AND m.managed_api_key_id=k.id WHERE k.purpose='managed_monitor' AND k.deleted_at IS NULL AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name)) FROM information_schema.columns WHERE table_schema='public' AND table_name='api_keys' AND column_name='name'\"")
  [[ $managed_monitor_key_name_state == '103|0' ]]
  managed_monitor_key_names_verified=true
fi
if [[ $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_reasoning_effort
  reasoning_effort_policy_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COALESCE(MAX(CASE WHEN column_name='max_reasoning_effort' THEN data_type || ':' || is_nullable || ':' || column_default END),''), COALESCE(MAX(CASE WHEN column_name='reasoning_effort_mappings' THEN data_type || ':' || is_nullable || ':' || column_default END),'') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('max_reasoning_effort','reasoning_effort_mappings')\"")
  [[ $reasoning_effort_policy_state == *'character varying:NO:'*"''::character varying"*'|'*'jsonb:NO:'*"'[]'::jsonb"* ]]
  reasoning_effort_policy_verified=true
fi
if [[ $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
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
if [[ $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
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
if [[ $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_208_passkey_schema
  passkey_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.passkey_user_handles') IS NOT NULL, to_regclass('public.passkey_credentials') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_user_handles' AND ((column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='user_handle' AND data_type='bytea' AND is_nullable='NO') OR (column_name='created_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='passkey_credentials' AND ((column_name='id' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE 'nextval(%') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name='credential_id' AND data_type='bytea' AND is_nullable='NO') OR (column_name='name' AND data_type='character varying' AND character_maximum_length=100 AND is_nullable='NO' AND column_default LIKE '''Passkey''%') OR (column_name='credential_data' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='last_used_at' AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name IN ('created_at','updated_at') AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='u'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('passkey_user_handles'::regclass,'passkey_credentials'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND tablename='passkey_credentials' AND indexname IN ('passkey_credentials_user_id_idx','passkey_credentials_last_used_at_idx'))\"")
  [[ $passkey_schema_state == 't|t|3|8|2|2|2|2' ]]
  passkey_schema_verified=true
fi
if [[ $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_209_user_usage_aggregation_schema
  user_usage_aggregation_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT to_regclass('public.usage_dashboard_user_hourly') IS NOT NULL, to_regclass('public.usage_dashboard_user_daily') IS NOT NULL, to_regclass('public.usage_dashboard_user_backfill_state') IS NOT NULL, (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_hourly' AND ((column_name='bucket_start' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_daily' AND ((column_name='bucket_date' AND data_type='date' AND is_nullable='NO') OR (column_name='user_id' AND data_type='bigint' AND is_nullable='NO') OR (column_name IN ('input_tokens','output_tokens','cache_creation_tokens','cache_read_tokens') AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name IN ('user_spend','account_cost') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=10 AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='computed_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_dashboard_user_backfill_state' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name IN ('earliest_covered_date','last_completed_date') AND data_type='date' AND is_nullable='YES') OR (column_name='status' AND data_type='character varying' AND character_maximum_length=20 AND is_nullable='NO' AND column_default LIKE '''unavailable''%') OR (column_name IN ('coverage_start','coverage_end','target_end','completed_at') AND data_type='timestamp with time zone' AND is_nullable='YES') OR (column_name='attempt_count' AND data_type='bigint' AND is_nullable='NO' AND column_default LIKE '0%') OR (column_name='last_error' AND data_type='text' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO' AND column_default LIKE 'now()%'))), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass,'usage_dashboard_user_backfill_state'::regclass) AND contype='p'), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid IN ('usage_dashboard_user_hourly'::regclass,'usage_dashboard_user_daily'::regclass) AND contype='f' AND confrelid='users'::regclass AND confdeltype='c'), (SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname IN ('idx_usage_dashboard_user_hourly_user_bucket','idx_usage_dashboard_user_daily_user_bucket')), (SELECT COUNT(*) FROM pg_constraint WHERE conrelid='usage_dashboard_user_backfill_state'::regclass AND contype='c'), (SELECT COUNT(*)=1 AND BOOL_AND(id=1 AND status IN ('available','building','partial','unavailable')) FROM usage_dashboard_user_backfill_state)\"")
  [[ $user_usage_aggregation_schema_state == 't|t|t|9|9|11|3|2|2|3|t' ]]
  user_usage_aggregation_schema_verified=true
fi
if [[ $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
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
if [[ $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_215_usage_log_model
  usage_log_upstream_model_columns_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FILTER (WHERE column_name='upstream_response_model' AND data_type='character varying' AND character_maximum_length=200 AND is_nullable='YES'), COUNT(*) FILTER (WHERE column_name='upstream_model_mismatch' AND data_type='boolean' AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND table_name='usage_logs'\"")
  [[ $usage_log_upstream_model_columns_state == '1|1' ]]
  usage_log_upstream_model_columns_verified=true
  usage_log_upstream_model_mismatch_index_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT i.indisvalid,i.indisready,pg_get_expr(i.indpred,i.indrelid)='(upstream_model_mismatch IS TRUE)',pg_get_indexdef(i.indexrelid) LIKE '%(created_at DESC, id DESC)%' FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_class t ON t.oid=i.indrelid JOIN pg_namespace n ON n.oid=t.relnamespace WHERE n.nspname='public' AND t.relname='usage_logs' AND c.relname='idx_usage_logs_upstream_model_mismatch_created_at'\"")
  [[ $usage_log_upstream_model_mismatch_index_state == 't|t|t|t' ]]
  usage_log_upstream_model_mismatch_index_verified=true
fi
if [[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_232_channel_monitor_media
  ASSERT_CONTEXT_FILE="$migration_232_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_232_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-232-assert.sh" postflight >/dev/null
  channel_monitor_v2_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"WITH expected_tables(name) AS (VALUES ('channel_monitor_v2_config'),('channel_monitor_v2_metrics_1m'),('channel_monitor_v2_user_metrics_1m'),('channel_monitor_v2_error_metrics_1m'),('channel_monitor_v2_latency_histograms_1m'),('channel_monitor_v2_watermarks'),('channel_monitor_v2_metrics_rollup'),('channel_monitor_v2_user_metrics_rollup'),('channel_monitor_v2_error_metrics_rollup'),('channel_monitor_v2_latency_histograms_rollup')), expected_indexes(name) AS (VALUES ('idx_channel_monitor_v2_metrics_platform_time'),('idx_channel_monitor_v2_metrics_group_time'),('idx_channel_monitor_v2_metrics_model_time'),('idx_channel_monitor_v2_user_metrics_user_time'),('idx_channel_monitor_v2_user_metrics_time'),('idx_channel_monitor_v2_errors_time'),('idx_channel_monitor_v2_errors_category_time'),('idx_channel_monitor_v2_histograms_time'),('idx_channel_monitor_v2_metrics_rollup_platform_time'),('idx_channel_monitor_v2_metrics_rollup_group_time'),('idx_channel_monitor_v2_metrics_rollup_model_time'),('idx_channel_monitor_v2_user_rollup_user_time'),('idx_channel_monitor_v2_user_rollup_time'),('idx_channel_monitor_v2_errors_rollup_time'),('idx_channel_monitor_v2_errors_rollup_category_time'),('idx_channel_monitor_v2_histograms_rollup_time')) SELECT (SELECT COUNT(*) FROM expected_tables WHERE to_regclass('public.' || name) IS NOT NULL), (SELECT COUNT(*) FROM pg_constraint WHERE contype='p' AND conrelid IN (SELECT to_regclass('public.' || name) FROM expected_tables)), (SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_monitor_v2_config' AND ((column_name='id' AND data_type='smallint' AND is_nullable='NO') OR (column_name='version' AND data_type='integer' AND is_nullable='NO') OR (column_name='enabled' AND data_type='boolean' AND is_nullable='NO') OR (column_name='refresh_interval_seconds' AND data_type='integer' AND is_nullable='NO') OR (column_name='platforms' AND data_type='jsonb' AND is_nullable='NO') OR (column_name='group_ids' AND data_type='ARRAY' AND is_nullable='NO') OR (column_name='updated_by' AND data_type='bigint' AND is_nullable='YES') OR (column_name='updated_at' AND data_type='timestamp with time zone' AND is_nullable='NO') OR (column_name='ignored_error_categories' AND data_type='ARRAY' AND is_nullable='NO') OR (column_name='health_thresholds' AND data_type='jsonb' AND is_nullable='NO'))), (SELECT COUNT(*) FROM pg_constraint WHERE contype='c' AND conrelid IN (SELECT to_regclass('public.' || name) FROM expected_tables)), (SELECT COUNT(*) FROM expected_indexes e JOIN pg_class c ON c.relname=e.name JOIN pg_namespace n ON n.oid=c.relnamespace AND n.nspname='public' JOIN pg_index i ON i.indexrelid=c.oid WHERE i.indisvalid AND i.indisready), (SELECT COUNT(*) FROM expected_tables WHERE has_table_privilege(current_user, 'public.' || name, 'SELECT,INSERT,UPDATE,DELETE'))\"")
  [[ $channel_monitor_v2_schema_state == '10|10|10|9|16|10' ]]
  channel_monitor_v2_defaults_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT (SELECT value FROM settings WHERE key='channel_monitor_mode'), (SELECT value FROM settings WHERE key='channel_monitor_hide_throughput'), (SELECT refresh_interval_seconds FROM channel_monitor_v2_config WHERE id=1), (SELECT enabled FROM channel_monitor_v2_config WHERE id=1), (SELECT cardinality(ignored_error_categories) FROM channel_monitor_v2_config WHERE id=1), (SELECT (health_thresholds->>'minimum_sample')::integer FROM channel_monitor_v2_config WHERE id=1), (SELECT (health_thresholds->>'warning_cache_rate')::numeric FROM channel_monitor_v2_config WHERE id=1), (SELECT (health_thresholds->>'critical_cache_rate')::numeric FROM channel_monitor_v2_config WHERE id=1), (SELECT platforms::text LIKE '%gpt-5.6-luna%' FROM channel_monitor_v2_config WHERE id=1), (SELECT platforms::text LIKE '%lcodex%' FROM channel_monitor_v2_config WHERE id=1)\"")
  [[ $channel_monitor_v2_defaults_state == 'v1|true|300|t|8|50|0|0|t|f' ]]
  group_media_pricing_schema_state=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -F '|' -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FILTER (WHERE column_name='video_model_prices' AND data_type='jsonb' AND is_nullable='YES'), COUNT(*) FILTER (WHERE column_name IN ('audio_realtime_price_per_min','audio_tts_price_per_million_chars','audio_stt_price_per_hour','search_price_per_1k') AND data_type='numeric' AND numeric_precision=20 AND numeric_scale=8 AND is_nullable='YES') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups'\"")
  [[ $group_media_pricing_schema_state == '1|4' ]]
  migration_232_verified_state="$state_dir/migration-232-verified"
  migration_232_verified_context="$state_dir/migration-232-verified-context.sh"
  install -d -m 700 "$migration_232_verified_state"
  printf 'profile=%q\nstate_dir=%q\n' "$profile" "$migration_232_verified_state" > "$migration_232_verified_context"
  chmod 400 "$migration_232_verified_context"
  ASSERT_CONTEXT_FILE="$migration_232_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS=verified RELEASE_DIR="$migration_232_verified_state" bash "$migration_assertion_dir/migration-232-assert.sh" preflight >/dev/null
  cp "$migration_232_verified_state/migration-232-data-plan.sha256" "$migration_232_verified_state/fake-recovery.sha256"
  printf '%s  recovery-point.age\n' "$(<"$migration_232_verified_state/fake-recovery.sha256")" > "$migration_232_verified_state/recovery-point.age.sha256"
  ASSERT_CONTEXT_FILE="$migration_232_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS=verified RELEASE_DIR="$migration_232_verified_state" bash "$migration_assertion_dir/migration-232-assert.sh" bind >/dev/null
  ASSERT_CONTEXT_FILE="$migration_232_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS=verified RELEASE_DIR="$migration_232_verified_state" bash "$migration_assertion_dir/migration-232-assert.sh" postflight >/dev/null
  channel_monitor_v2_schema_verified=true
  channel_monitor_v2_defaults_verified=true
  group_media_pricing_schema_verified=true
  group_media_auth_cache_trigger_verified=true
  migration_232_postflight_verified=true
fi
if [[ $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_profile_233_upstream_management
  ASSERT_CONTEXT_FILE="$migration_233_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_233_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-233-assert.sh" postflight >/dev/null
  migration_233_postflight_verified=true
fi
if [[ $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_234_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-234-assert.sh" postflight >/dev/null
  migration_234_schema_verified=true
fi
if [[ $profile == 237 ]]; then
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_235_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-235-assert.sh" postflight >/dev/null
  ASSERT_CONTEXT_FILE="$group_model_pricing_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" MIGRATION_STATUS="$migration_236_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-236-assert.sh" postflight >/dev/null
  migration_235_schema_verified=true
  migration_236_schema_verified=true
fi
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]]; then
  mark_stage migration_assertion_195_runtime_current
  ASSERT_CONTEXT_FILE="$migration_195_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS="$migration_195_status" RELEASE_DIR="$state_dir" bash "$migration_assertion_dir/migration-195-assert.sh" postflight_runtime >/dev/null
  mark_stage migration_assertion_195_runtime_replay
  ASSERT_CONTEXT_FILE="$migration_195_verified_context" ASSERT_DB_CONTAINER=sub2api-postgres ASSERT_DB_USER="$database_owner" ASSERT_DB_NAME="$probe_db" ASSERT_REDIS_CONTAINER="$probe_redis" MIGRATION_STATUS=verified RELEASE_DIR="$migration_195_verified_state" bash "$migration_assertion_dir/migration-195-assert.sh" postflight_runtime >/dev/null
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
  --argjson prompt_audit_disabled "$([[ $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]] && printf true || printf false)" \
  --argjson migration_195_verified "$([[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 || $profile == 202 || $profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 ]] && printf true || printf false)" \
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
  --arg migration_214_status "$migration_214_status" \
  --arg migration_215_status "$migration_215_status" \
  --arg migration_216_status "$migration_216_status" \
  --arg migration_217_status "$migration_217_status" \
  --arg migration_218_status "$migration_218_status" \
  --arg migration_219_status "$migration_219_status" \
  --arg migration_220_status "$migration_220_status" \
  --arg migration_221_status "$migration_221_status" \
  --arg migration_222_status "$migration_222_status" \
  --arg migration_223_status "$migration_223_status" \
  --arg migration_224_status "$migration_224_status" \
  --arg migration_225_status "$migration_225_status" \
  --arg migration_226_status "$migration_226_status" \
  --arg migration_227_status "$migration_227_status" \
  --arg migration_228_status "$migration_228_status" \
  --arg migration_229_status "$migration_229_status" \
  --arg migration_230_status "$migration_230_status" \
  --arg migration_231_status "$migration_231_status" \
  --arg migration_232_status "$migration_232_status" \
  --arg migration_233_status "$migration_233_status" \
  --arg migration_234_status "$migration_234_status" \
  --arg migration_235_status "$migration_235_status" \
  --arg migration_236_status "$migration_236_status" \
  --argjson usage_log_upstream_model_columns_verified "$usage_log_upstream_model_columns_verified" \
  --argjson usage_log_upstream_model_mismatch_index_verified "$usage_log_upstream_model_mismatch_index_verified" \
  --argjson channel_monitor_v2_schema_verified "$channel_monitor_v2_schema_verified" \
  --argjson channel_monitor_v2_defaults_verified "$channel_monitor_v2_defaults_verified" \
  --argjson group_media_pricing_schema_verified "$group_media_pricing_schema_verified" \
  --argjson group_media_auth_cache_trigger_verified "$group_media_auth_cache_trigger_verified" \
  --argjson migration_232_data_plan_verified "$migration_232_data_plan_verified" \
  --argjson migration_232_postflight_verified "$migration_232_postflight_verified" \
  --argjson migration_233_preflight_verified "$migration_233_preflight_verified" \
  --argjson migration_233_postflight_verified "$migration_233_postflight_verified" \
  --argjson migration_234_preflight_verified "$migration_234_preflight_verified" \
  --argjson migration_234_schema_verified "${migration_234_schema_verified:-false}" \
  --argjson migration_235_preflight_verified "$migration_235_preflight_verified" \
  --argjson migration_236_preflight_verified "$migration_236_preflight_verified" \
  --argjson migration_235_schema_verified "$migration_235_schema_verified" \
  --argjson migration_236_schema_verified "$migration_236_schema_verified" \
  --arg vm_old_image_id "$compat_image_id" \
  --argjson vm_old_image_compatibility_verified "$vm_old_image_compatibility_verified" \
  --argjson fixture_rejected "$fixture_rejected" \
  --argjson restore_completed "$restore_completed" \
  --argjson clean_preflight "$clean_preflight" \
  --argjson verified_replay "$verified_replay" \
  --argjson verified_low_watermark_rejected "$verified_low_watermark_rejected" \
  '{manifest:$manifest[0],evidence:{candidate_image_id:$candidate_image_id,candidate_archive_sha256:$candidate_archive_sha256,candidate_size:$candidate_size,integration_verified:true,vm_restore_verified:true,vm_database_boundary:true,vm_redis_boundary:true,data_dev_boundary:true,prompt_audit_disabled:$prompt_audit_disabled,migration_195_verified:$migration_195_verified,managed_monitor_key_names_verified:$managed_monitor_key_names_verified,reasoning_effort_policy_verified:$reasoning_effort_policy_verified,alipay_mobile_precreate_migration_verified:$alipay_mobile_precreate_migration_verified,group_auth_cache_image_generation_verified:$group_auth_cache_image_generation_verified,composite_model_routes_verified:$composite_model_routes_verified,session_id_columns_verified:$session_id_columns_verified,live_request_type_verified:$live_request_type_verified,group_allow_live_verified:$group_allow_live_verified,email_alias_index_verified:$email_alias_index_verified,live_runtime_capability_verified:$live_runtime_capability_verified,passkey_schema_verified:$passkey_schema_verified,user_usage_aggregation_schema_verified:$user_usage_aggregation_schema_verified,migration_211_status:$migration_211_status,migration_212_status:$migration_212_status,migration_214_status:$migration_214_status,migration_215_status:$migration_215_status,migration_216_status:$migration_216_status,migration_217_status:$migration_217_status,migration_218_status:$migration_218_status,migration_219_status:$migration_219_status,migration_220_status:$migration_220_status,migration_221_status:$migration_221_status,migration_222_status:$migration_222_status,migration_223_status:$migration_223_status,migration_224_status:$migration_224_status,migration_225_status:$migration_225_status,migration_226_status:$migration_226_status,migration_227_status:$migration_227_status,migration_228_status:$migration_228_status,migration_229_status:$migration_229_status,migration_230_status:$migration_230_status,migration_231_status:$migration_231_status,migration_232_status:$migration_232_status,migration_233_status:$migration_233_status,migration_234_status:$migration_234_status,migration_235_status:$migration_235_status,migration_236_status:$migration_236_status,migration_235_preflight_verified:$migration_235_preflight_verified,migration_236_preflight_verified:$migration_236_preflight_verified,migration_235_schema_verified:$migration_235_schema_verified,migration_236_schema_verified:$migration_236_schema_verified,usage_log_upstream_model_columns_verified:$usage_log_upstream_model_columns_verified,usage_log_upstream_model_mismatch_index_verified:$usage_log_upstream_model_mismatch_index_verified,channel_monitor_v2_schema_verified:$channel_monitor_v2_schema_verified,channel_monitor_v2_defaults_verified:$channel_monitor_v2_defaults_verified,group_media_pricing_schema_verified:$group_media_pricing_schema_verified,group_media_auth_cache_trigger_verified:$group_media_auth_cache_trigger_verified,migration_232_data_plan_verified:$migration_232_data_plan_verified,migration_232_postflight_verified:$migration_232_postflight_verified,migration_233_preflight_verified:$migration_233_preflight_verified,migration_233_postflight_verified:$migration_233_postflight_verified,migration_234_preflight_verified:$migration_234_preflight_verified,migration_234_schema_verified:$migration_234_schema_verified,group_profit_control_schema_verified:$group_profit_control_schema_verified,group_profit_auth_cache_trigger_verified:$group_profit_auth_cache_trigger_verified,vm_old_image_id:$vm_old_image_id,vm_old_image_compatibility_verified:$vm_old_image_compatibility_verified,fixture_rejected:$fixture_rejected,restore_completed:$restore_completed,clean_preflight:$clean_preflight,verified_replay:$verified_replay,verified_low_watermark_rejected:$verified_low_watermark_rejected}}' \
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
