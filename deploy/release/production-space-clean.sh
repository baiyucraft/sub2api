#!/usr/bin/env bash
set -Eeuo pipefail

mode=${1:?cleanup mode is required}
release_id=${2:?release ID is required}
expected_current_image=${3:?current image ID is required}
pre_switch_image=${4:?pre-switch image ID is required}
expected_plan_sha256=${5:--}
[[ $mode == dry-run || $mode == apply ]]
[[ $release_id =~ ^(182|187|191|192|194|195|197|198|199)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $expected_current_image =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $pre_switch_image =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $expected_current_image != "$pre_switch_image" ]]
if [[ $mode == apply ]]; then
  [[ $expected_plan_sha256 =~ ^[0-9a-f]{64}$ ]]
else
  [[ $expected_plan_sha256 == - ]]
fi

required_commands=(awk curl cut df docker flock grep mktemp ps rm sha256sum sort systemctl tr wc)
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || exit 127
done

exec 9>/run/lock/sub2api-production-release.lock
flock -n 9
exec 8>/run/lock/sub2api-backup-global.lock
flock -n 8
docker info >/dev/null 2>&1

work_dir=$(mktemp -d)
cleanup() {
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

active_claim=/opt/sub2api/releases/.active-release
release_root=/opt/sub2api/releases
state_root=/opt/sub2api/backups/release-state
release_dir="$release_root/$release_id"
consumed_dir="$release_dir/.consumed"
release_state_dir="$state_root/$release_id"
build_cache_max_used_space=2gb
build_cache_reserved_space=2gb

assert_release_idle() {
  [[ ! -e $active_claim && ! -L $active_claim ]]
  local release_dir
  for release_dir in "$release_root"/*; do
    [[ ! -L $release_dir ]]
    [[ -d $release_dir && ! -L $release_dir ]] || continue
    if [[ -e $release_dir/.prepared || -L $release_dir/.prepared ]]; then
      [[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
      if [[ -e $release_dir/.consumed || -L $release_dir/.consumed ]]; then
        [[ -d $release_dir/.consumed && ! -L $release_dir/.consumed ]]
        [[ ! -e $release_dir/.recovered && ! -L $release_dir/.recovered ]]
      else
        [[ -d $release_dir/.recovered && ! -L $release_dir/.recovered ]]
        [[ ! -e $release_dir/.consumed && ! -L $release_dir/.consumed ]]
      fi
    fi
  done
  [[ $(ps -eo args= | awk '/docker (build|buildx)|buildctl|release\.py (deploy|deploy-start)|release\.supervisor/ && ! /awk/ {count++} END {print count+0}') == 0 ]]
}

assert_release_marker() {
  [[ -d $release_dir && ! -L $release_dir ]]
  [[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
  [[ -d $consumed_dir && ! -L $consumed_dir ]]
  [[ ! -e $release_dir/.recovered && ! -L $release_dir/.recovered ]]
  [[ -f $consumed_dir/marker && ! -L $consumed_dir/marker ]]
  [[ -f $consumed_dir/plaintext-cleaned && ! -L $consumed_dir/plaintext-cleaned ]]
  [[ -f $consumed_dir/CLAIM_SHA256SUMS && ! -L $consumed_dir/CLAIM_SHA256SUMS ]]
  grep -Fxq "release_id=$release_id" "$release_dir/.prepared"
  grep -Fxq "candidate_image_id=$expected_current_image" "$release_dir/.prepared"
  grep -Fxq "release_id=$release_id" "$consumed_dir/marker"
  grep -Fxq "candidate_image_id=$expected_current_image" "$consumed_dir/marker"
  [[ -d $release_state_dir && ! -L $release_state_dir ]]
  [[ -f $release_state_dir/pre-image-id && ! -L $release_state_dir/pre-image-id ]]
  [[ $(tr -d '\r\n' < "$release_state_dir/pre-image-id") == "$pre_switch_image" ]]
}

assert_release_identity() {
  assert_release_marker
  (cd "$consumed_dir" && sha256sum -c CLAIM_SHA256SUMS >/dev/null)
}

assert_services() {
  [[ $(docker inspect -f '{{.Image}}' sub2api) == "$expected_current_image" ]]
  [[ $(docker inspect -f '{{.State.Status}}' sub2api) == running ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
  [[ $(docker inspect -f '{{.State.Status}}' sub2api-postgres) == running ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-postgres) == healthy ]]
  [[ $(docker inspect -f '{{.State.Status}}' sub2api-redis) == running ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
  [[ $(systemctl is-active nginx) == active ]]
  [[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) == inactive ]]
  [[ $(systemctl is-active sub2api-backup.timer) == active ]]
  [[ $(systemctl is-enabled sub2api-backup.timer) == enabled ]]
  [[ $(docker image inspect -f '{{.Id}}' "$pre_switch_image") == "$pre_switch_image" ]]
  [[ $(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/health) == 200 ]]
}

write_container_images() {
  local container_ids
  container_ids=$(docker ps -aq --no-trunc)
  if [[ -n $container_ids ]]; then
    docker inspect -f '{{.Image}}' $container_ids
  fi
}

write_protected_images() {
  printf '%s\n%s\n' "$expected_current_image" "$pre_switch_image"
  write_container_images
  local release_state path image_id
  [[ -d $state_root && ! -L $state_root ]]
  for release_state in "$state_root"/*; do
    [[ ! -L $release_state ]]
    [[ -d $release_state && ! -L $release_state ]] || continue
    path="$release_state/pre-image-id"
    if [[ ! -e $path && ! -L $path ]]; then
      continue
    fi
    [[ -f $path && ! -L $path ]]
    image_id=$(tr -d '\r\n' < "$path")
    [[ $image_id =~ ^sha256:[0-9a-f]{64}$ ]]
    printf '%s\n' "$image_id"
  done
}

list_image_candidates() {
  local image_id image_size valid has_tag tag
  write_protected_images | LC_ALL=C sort -u > "$work_dir/protected-images"
  docker image ls --no-trunc --filter 'reference=sub2api:*' --format '{{.ID}}' | LC_ALL=C sort -u > "$work_dir/sub2api-images"
  : > "$work_dir/image-candidates"
  while IFS= read -r image_id; do
    [[ $image_id =~ ^sha256:[0-9a-f]{64}$ ]] || continue
    grep -Fxq "$image_id" "$work_dir/protected-images" && continue
    mapfile -t tags < <(docker image inspect -f '{{range .RepoTags}}{{println .}}{{end}}' "$image_id")
    valid=true
    has_tag=false
    for tag in "${tags[@]}"; do
      [[ -n $tag ]] || continue
      has_tag=true
      [[ $tag =~ ^sub2api:.+-[0-9a-f]{40}$ ]] || valid=false
    done
    [[ $valid == true && $has_tag == true ]] || continue
    image_size=$(docker image inspect -f '{{.Size}}' "$image_id")
    [[ $image_size =~ ^[0-9]+$ ]]
    printf '%s\t%s\n' "$image_id" "$image_size" >> "$work_dir/image-candidates"
  done < "$work_dir/sub2api-images"
  cut -f1 "$work_dir/image-candidates"
}

remove_image_candidates() {
  local image_id removed=0 valid has_tag tag
  list_image_candidates > "$work_dir/apply-image-ids"
  while IFS= read -r image_id; do
    [[ -n $image_id ]] || continue
    assert_release_idle
    assert_release_marker
    assert_services
    write_protected_images | LC_ALL=C sort -u > "$work_dir/protected-images-now"
    grep -Fxq "$image_id" "$work_dir/protected-images-now" && continue
    mapfile -t tags < <(docker image inspect -f '{{range .RepoTags}}{{println .}}{{end}}' "$image_id")
    valid=true
    has_tag=false
    for tag in "${tags[@]}"; do
      [[ -n $tag ]] || continue
      has_tag=true
      [[ $tag =~ ^sub2api:.+-[0-9a-f]{40}$ ]] || valid=false
    done
    [[ $valid == true && $has_tag == true ]]
    docker image rm "${tags[@]}" >/dev/null 2>&1
    ! docker image inspect "$image_id" >/dev/null 2>&1
    removed=$((removed + 1))
  done < "$work_dir/apply-image-ids"
  printf '%s\n' "$removed"
}

assert_release_idle
assert_release_identity
assert_services
root_free_before_bytes=$(df -PB1 / | awk 'NR==2{print $4}')
containerd_free_before_bytes=$(df -PB1 /var/lib/containerd | awk 'NR==2{print $4}')
migration_evidence_containers=$(docker ps -a --format '{{.Names}}' | awk '$0 ~ /^sub2api-migrate-/ {count++} END {print count+0}')
list_image_candidates > "$work_dir/image-candidate-ids"
image_candidates=$(awk 'NF {count++} END {print count+0}' "$work_dir/image-candidate-ids")
image_candidate_logical_bytes=$(awk -F '\t' '{total += $2} END {printf "%.0f\n", total+0}' "$work_dir/image-candidates")
{
  printf 'release_id=%s\n' "$release_id"
  printf 'current_image_id=%s\n' "$expected_current_image"
  printf 'pre_switch_image_id=%s\n' "$pre_switch_image"
  printf '%s\n' image_candidates
  cat "$work_dir/image-candidate-ids"
} > "$work_dir/cleanup-plan"
plan_sha256=$(sha256sum "$work_dir/cleanup-plan" | awk '{print $1}')
[[ $plan_sha256 =~ ^[0-9a-f]{64}$ ]]
build_cache_records_before=$(docker buildx du --format '{{json .}}' 2>/dev/null | wc -l | tr -d ' ')
[[ $build_cache_records_before =~ ^[0-9]+$ ]]
removed_images=0
build_cache_gc_attempted=false

if [[ $mode == apply ]]; then
  [[ $plan_sha256 == "$expected_plan_sha256" ]]
  removed_images=$(remove_image_candidates)
  assert_release_idle
  assert_release_marker
  assert_services
  if (( build_cache_records_before > 0 )); then
    docker buildx prune --all --force \
      --max-used-space "$build_cache_max_used_space" \
      --reserved-space "$build_cache_reserved_space" >/dev/null 2>&1
    build_cache_gc_attempted=true
  fi
  assert_release_idle
  assert_release_identity
  assert_services
  cleanup_status=completed
else
  cleanup_status=ready
fi

list_image_candidates > "$work_dir/image-candidate-ids-after"
image_candidates_after=$(awk 'NF {count++} END {print count+0}' "$work_dir/image-candidate-ids-after")
build_cache_records_after=$(docker buildx du --format '{{json .}}' 2>/dev/null | wc -l | tr -d ' ')
[[ $build_cache_records_after =~ ^[0-9]+$ ]]
root_free_after_bytes=$(df -PB1 / | awk 'NR==2{print $4}')
containerd_free_after_bytes=$(df -PB1 /var/lib/containerd | awk 'NR==2{print $4}')
root_free_delta_bytes=$((root_free_after_bytes - root_free_before_bytes))
containerd_free_delta_bytes=$((containerd_free_after_bytes - containerd_free_before_bytes))
printf 'cleanup_mode=%s\n' "$mode"
printf 'cleanup_status=%s\n' "$cleanup_status"
printf 'plan_sha256=%s\n' "$plan_sha256"
printf 'release_id=%s\n' "$release_id"
printf 'current_image_id=%s\n' "$expected_current_image"
printf 'pre_switch_image_id=%s\n' "$pre_switch_image"
printf 'root_free_before_bytes=%s\n' "$root_free_before_bytes"
printf 'root_free_after_bytes=%s\n' "$root_free_after_bytes"
printf 'root_free_delta_bytes=%s\n' "$root_free_delta_bytes"
printf 'containerd_free_before_bytes=%s\n' "$containerd_free_before_bytes"
printf 'containerd_free_after_bytes=%s\n' "$containerd_free_after_bytes"
printf 'containerd_free_delta_bytes=%s\n' "$containerd_free_delta_bytes"
printf 'migration_evidence_containers=%s\n' "$migration_evidence_containers"
printf 'image_candidates=%s\n' "$image_candidates"
printf 'image_candidates_after=%s\n' "$image_candidates_after"
printf 'image_candidate_logical_bytes=%s\n' "$image_candidate_logical_bytes"
printf 'removed_images=%s\n' "$removed_images"
printf 'build_cache_records_before=%s\n' "$build_cache_records_before"
printf 'build_cache_records_after=%s\n' "$build_cache_records_after"
printf 'build_cache_policy=%s\n' "all_lru_maxused_${build_cache_max_used_space}_reserved_${build_cache_reserved_space}"
printf 'build_cache_gc_attempted=%s\n' "$build_cache_gc_attempted"
