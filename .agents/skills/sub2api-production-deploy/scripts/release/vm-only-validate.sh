#!/usr/bin/env bash
set -Eeuo pipefail

manifest=${1:?manifest path is required}
output_dir=${2:?output directory is required}
source_dir=/opt/sub2api-src
deploy_dir=/opt/sub2api-deploy
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock

[[ $(id -u) == 0 ]]
[[ -f $unit_lock && ! -L $unit_lock && $(stat -c '%U:%G:%a:%h' "$unit_lock") == root:root:600:1 ]]
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
[[ $scope == vm-only && $profile == 250 && $release_id == "250-${commit:0:12}-"* ]]
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ $(jq -er '.vm_identity' "$manifest") == sub2api-dev ]]
[[ $(jq -er '.vm_port' "$manifest") == 8211 ]]
[[ $(jq -er '.vm_data' "$manifest") == /opt/sub2api-deploy/data-dev ]]
[[ $(jq -er '.origin' "$manifest") == https://github.com/baiyucraft/sub2api.git ]]

[[ -d $source_dir && ! -L $source_dir && -d $deploy_dir && ! -L $deploy_dir ]]
cd "$source_dir"
[[ $(git remote get-url origin) == https://github.com/baiyucraft/sub2api.git ]]
git fetch origin +main:refs/remotes/origin/main >/dev/null 2>&1
[[ $(git rev-parse origin/main) == "$commit" ]]
git reset --hard "$commit" >/dev/null
[[ $(git rev-parse HEAD) == "$commit" ]]
source_tree_sha256=$(git rev-parse "$commit^{tree}")
[[ $source_tree_sha256 == "$(jq -er '.source_tree_sha256' "$manifest")" ]]

[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
[[ $(docker inspect -f '{{.HostConfig.NetworkMode}}' sub2api-dev) == host ]]
ss -H -ltn | awk '$4 ~ /:8211$/ {found=1} END {exit !found}'
[[ $(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' sub2api-dev) == /opt/sub2api-deploy/data-dev ]]

tag="sub2api:vm-only-$release_id"
build_log="${output_dir%/output}/build.log"
: >"$build_log"
chmod 600 "$build_log"
docker build --network=host --progress=plain \
  --build-arg COMMIT="$commit" --build-arg VERSION="$version" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t "$tag" . >"$build_log" 2>&1
candidate_image_id=$(docker image inspect -f '{{.Id}}' "$tag")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
candidate_version=$(docker run --rm --entrypoint /app/sub2api "$candidate_image_id" --version 2>&1)
[[ $candidate_version == *"commit: $commit"* && $candidate_version == *"Sub2API $version"* ]]

candidate_container="sub2api-vm-only-${release_id//[^a-zA-Z0-9]/-}"
candidate_port=38121
if ss -H -ltn | awk '{print $4}' | grep -Eq ":${candidate_port}$"; then
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
  if [[ $(curl -sS --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${candidate_port}/health" 2>/dev/null || true) == 200 ]]; then
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
  --arg candidate_image_id "$candidate_image_id" \
  --arg candidate_archive_sha256 "$candidate_archive_sha256" \
  --argjson candidate_size "$candidate_size" \
  '{gate_version:2,profile_id:250,manifest:$manifest,evidence:{candidate_image_id:$candidate_image_id,candidate_archive_sha256:$candidate_archive_sha256,candidate_size:$candidate_size,candidate_identity_verified:true,candidate_health:"pass",existing_app_health:"pass",vm_database_boundary:true,vm_redis_boundary:true,data_dev_boundary:true}}' >"$output_dir/gate.json"
chmod 400 "$output_dir/gate.json"
/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json" "$output_dir/gate.sig"
sha256sum "$output_dir/gate.json" "$output_dir/gate.sig" "$candidate_archive" >"$output_dir/SHA256SUMS"
chmod 400 "$output_dir/SHA256SUMS"
printf 'candidate_image_id=%s\nold_image_id=%s\nexisting_app_health=pass\ncandidate_health=%s\n' "$candidate_image_id" "$old_image_id" "$candidate_health"
