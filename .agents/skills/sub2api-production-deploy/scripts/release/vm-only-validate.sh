#!/usr/bin/env bash
set -Eeuo pipefail

manifest=${1:?manifest path is required}
output_dir=${2:?output directory is required}
source_dir=/opt/sub2api-src
deploy_dir=/opt/sub2api-deploy
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock

{
  required_query_1=$(id -u)
  [[ ${required_query_1} == 0 ]]
}
{
  required_query_2=$(stat -c '%U:%G:%a:%h' "$unit_lock")
  [[ -f $unit_lock && ! -L $unit_lock && ${required_query_2} == root:root:600:1 ]]
}
exec 8<>"$unit_lock"
flock -s 8
[[ -f $manifest && ! -L $manifest ]]
[[ $output_dir =~ ^/opt/sub2api-deploy/release-gates/[0-9]+-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}/output$ ]]
install -d -m 700 "${output_dir%/output}" "$output_dir"

scope=$(jq -er '.scope' "$manifest")
profile=$(jq -er '.profile' "$manifest")
commit=$(jq -er '.commit_sha' "$manifest")
version=$(jq -er '.version' "$manifest")
release_id=$(jq -er '.release_id' "$manifest")
[[ $scope == vm-only && ( $profile == 255 || $profile == 256 || $profile == 257 || $profile == 258 || $profile == 259 || $profile == 260 || $profile == 261 || $profile == 262 || $profile == 263 || $profile == 264 || $profile == 265 || $profile == 266 || $profile == 267 || $profile == 268 || $profile == 269 || $profile == 270 || $profile == 271 || $profile == 272 || $profile == 273 || $profile == 274 ) && $release_id == "${profile}-${commit:0:12}-"* ]]
[[ $commit =~ ^[0-9a-f]{40}$ ]]
{
  required_query_3=$(jq -er '.vm_identity' "$manifest")
  [[ ${required_query_3} == sub2api-dev ]]
}
{
  required_query_4=$(jq -er '.vm_port' "$manifest")
  [[ ${required_query_4} == 8211 ]]
}
{
  required_query_5=$(jq -er '.vm_data' "$manifest")
  [[ ${required_query_5} == /opt/sub2api-deploy/data-dev ]]
}
{
  required_query_6=$(jq -er '.origin' "$manifest")
  [[ ${required_query_6} == https://github.com/baiyucraft/sub2api.git ]]
}

[[ -d $source_dir && ! -L $source_dir && -d $deploy_dir && ! -L $deploy_dir ]]
cd "$source_dir"
{
  required_query_7=$(git remote get-url origin)
  [[ ${required_query_7} == https://github.com/baiyucraft/sub2api.git ]]
}
git fetch origin +main:refs/remotes/origin/main >/dev/null 2>&1
{
  required_query_8=$(git rev-parse origin/main)
  [[ ${required_query_8} == "$commit" ]]
}
git reset --hard "$commit" >/dev/null
{
  required_query_9=$(git rev-parse HEAD)
  [[ ${required_query_9} == "$commit" ]]
}
source_tree_sha256=$(git rev-parse "$commit^{tree}")
{
  required_query_10=$(jq -er '.source_tree_sha256' "$manifest")
  [[ $source_tree_sha256 == "${required_query_10}" ]]
}

{
  required_query_11=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev)
  [[ ${required_query_11} == healthy ]]
}
{
  required_query_12=$(docker inspect -f '{{.HostConfig.NetworkMode}}' sub2api-dev)
  [[ ${required_query_12} == host ]]
}
ss -H -ltn | awk '$4 ~ /:8211$/ {found=1} END {exit !found}'
{
  required_query_13=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' sub2api-dev)
  [[ ${required_query_13} == /opt/sub2api-deploy/data-dev ]]
}

tag="sub2api:vm-only-$release_id"
build_state_dir="${output_dir%/output}"
on_vm_only_build_failure() {
  local exit_code=$?
  trap - ERR INT TERM
  printf '%s\n' vm_only_candidate_build > "$build_state_dir/failure-category"
  local failure_code=${VM_BUILD_FAILURE_CODE:-build_unknown}
  [[ $failure_code != none ]] || failure_code=build_unknown
  printf '%s\n' "$failure_code" > "$build_state_dir/build-failure-code"
  chmod 400 "$build_state_dir/failure-category" "$build_state_dir/build-failure-code"
  exit "$exit_code"
}
trap on_vm_only_build_failure ERR INT TERM
printf '%s\n' candidate_build > "$build_state_dir/stage"
build_helper_relative=.agents/skills/sub2api-production-deploy/scripts/release/vm-build-common.sh
build_helper="$source_dir/$build_helper_relative"
build_helper_expected=$(git show "$commit:$build_helper_relative" | sha256sum | awk '{print $1}')
[[ $build_helper_expected =~ ^[0-9a-f]{64}$ && -f $build_helper && ! -L $build_helper ]]
build_helper_actual=$(sha256sum "$build_helper" | awk '{print $1}')
[[ $build_helper_actual == "$build_helper_expected" ]]
source "$build_helper"
install -d -m 700 "$build_state_dir/build-dependencies"
vm_build_preflight "$build_state_dir/build-dependencies"
build_log="$build_state_dir/build.log"
required_query_14=$(date -u +%Y-%m-%dT%H:%M:%SZ)
vm_build_candidate "$build_log" \
  --build-arg COMMIT="$commit" --build-arg VERSION="$version" \
  --build-arg DATE="${required_query_14}" \
  -t "$tag" .
candidate_image_id=$(docker image inspect -f '{{.Id}}' "$tag")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
candidate_version=$(docker run --rm --entrypoint /app/sub2api "$candidate_image_id" --version 2>&1)
[[ $candidate_version == *"commit: $commit"* && $candidate_version == *"Sub2API $version"* ]]
trap - ERR INT TERM

candidate_container="sub2api-vm-only-${release_id//[^a-zA-Z0-9]/-}"
candidate_port=38121
listener_inventory=$(ss -H -ltn)
listener_matches=$(printf '%s\n' "$listener_inventory" | awk -v port="$candidate_port" '$4 ~ (":" port "$") {count++} END {print count+0}')
[[ $listener_matches =~ ^[0-9]+$ ]]
if (( listener_matches > 0 )); then
  exit 1
fi
cleanup_candidate() {
  docker rm -f "$candidate_container" >/dev/null 2>&1 || true
}
trap cleanup_candidate EXIT
docker run -d --name "$candidate_container" --network host \
  -e DATA_DIR=/app/data \
  -e SERVER_HOST=127.0.0.1 -e SERVER_PORT="$candidate_port" \
  -e AUTO_SETUP=false -e UPSTREAM_SYNC_AUTO_ENABLED=false \
  -e DASHBOARD_AGGREGATION_ENABLED=false \
  --read-only --tmpfs /tmp --tmpfs /run \
  --tmpfs /app/.tmp:rw,nosuid,nodev,noexec,size=64m \
  -v "$deploy_dir/data-dev:/app/data:ro" "$candidate_image_id" >/dev/null
candidate_health=fail
for _ in $(seq 1 60); do
  if probe_http_status=$(curl -sS --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${candidate_port}/health" 2>/dev/null) && [[ $probe_http_status == 200 ]]; then
    candidate_health=pass
    break
  fi
  sleep 2
done
[[ $candidate_health == pass ]]
cleanup_candidate
trap - EXIT

candidate_archive="$output_dir/candidate.tar.gz"
docker save "$candidate_image_id" | gzip -1 >"$candidate_archive"
candidate_archive_sha256=$(sha256sum "$candidate_archive" | awk '{print $1}')
candidate_size=$(stat -c '%s' "$candidate_archive")
[[ $candidate_size =~ ^[1-9][0-9]*$ ]]

old_image_id=$(docker inspect -f '{{.Image}}' sub2api-dev)
manifest_json=$(cat "$manifest")
jq -cnS \
  --argjson manifest "$manifest_json" \
  --arg profile "$profile" \
  --arg candidate_image_id "$candidate_image_id" \
  --arg candidate_archive_sha256 "$candidate_archive_sha256" \
  --argjson candidate_size "$candidate_size" \
  '{gate_version:2,profile_id:($profile|tonumber),manifest:$manifest,evidence:{candidate_image_id:$candidate_image_id,candidate_archive_sha256:$candidate_archive_sha256,candidate_size:$candidate_size,candidate_identity_verified:true,candidate_health:"pass",existing_app_health:"pass",vm_database_boundary:true,vm_redis_boundary:true,data_dev_boundary:true}}' >"$output_dir/gate.json"
chmod 400 "$output_dir/gate.json"
/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json" "$output_dir/gate.sig"
sha256sum "$output_dir/gate.json" "$output_dir/gate.sig" "$candidate_archive" >"$output_dir/SHA256SUMS"
chmod 400 "$output_dir/SHA256SUMS"
printf 'candidate_image_id=%s\nold_image_id=%s\nexisting_app_health=pass\ncandidate_health=%s\n' "$candidate_image_id" "$old_image_id" "$candidate_health"
