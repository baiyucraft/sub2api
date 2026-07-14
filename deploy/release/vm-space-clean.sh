#!/usr/bin/env bash
set -Eeuo pipefail

mode=${1:?cleanup mode is required}
target_commit=${2:?target commit is required}
[[ $mode == dry-run || $mode == apply ]]
[[ $target_commit =~ ^[0-9a-f]{40}$ ]]

required_commands=(awk cut df docker grep mktemp rm sort stat tr)
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || exit 127
done
docker info >/dev/null 2>&1

work_dir=$(mktemp -d)
cleanup() {
  rm -rf -- "$work_dir"
}
trap cleanup EXIT

docker_free_bytes() {
  df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}'
}

database_size=$(docker exec sub2api-postgres sh -lc \
  'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d postgres -c "SELECT pg_database_size('\''sub2api_dev'\'')"' \
  | tr -d '\r')
[[ $database_size =~ ^[0-9]+$ ]]
current_image_size=$(docker image inspect -f '{{.Size}}' "$(docker inspect -f '{{.Image}}' sub2api-dev)")
[[ $current_image_size =~ ^[0-9]+$ ]]
required_bytes=$((database_size * 2 + current_image_size * 2 + 1073741824))

list_container_candidates() {
  docker ps -a --filter status=exited --format '{{.ID}}\t{{.Names}}' \
    | awk -F '\t' '$2 ~ /^sub2api-dev-pre/ {print $1}' \
    | sort -u
}

container_reclaimable_bytes() {
  local container_id size total=0
  while IFS= read -r container_id; do
    [[ -n $container_id ]] || continue
    size=$(docker inspect --size -f '{{.SizeRw}}' "$container_id" 2>/dev/null || true)
    [[ $size =~ ^[0-9]+$ ]] || size=0
    total=$((total + size))
  done
  printf '%s\n' "$total"
}

write_referenced_images() {
  local container_ids
  container_ids=$(docker ps -aq --no-trunc)
  if [[ -n $container_ids ]]; then
    docker inspect -f '{{.Image}}' $container_ids | sort -u
  fi
}

list_image_candidates() {
  local image_id image_size tag valid has_tag
  write_referenced_images > "$work_dir/referenced-images"
  docker image ls --no-trunc --format '{{.ID}}' | sort -u > "$work_dir/image-ids"
  : > "$work_dir/image-candidates"
  while IFS= read -r image_id; do
    [[ $image_id =~ ^sha256:[0-9a-f]{64}$ ]] || continue
    grep -Fxq "$image_id" "$work_dir/referenced-images" && continue
    mapfile -t tags < <(docker image inspect -f '{{range .RepoTags}}{{println .}}{{end}}' "$image_id" 2>/dev/null)
    valid=true
    has_tag=false
    for tag in "${tags[@]}"; do
      [[ -n $tag ]] || continue
      has_tag=true
      if [[ ! $tag =~ ^sub2api:.+-([0-9a-f]{40})$ || ${BASH_REMATCH[1]} == "$target_commit" ]]; then
        valid=false
        break
      fi
    done
    [[ $valid == true && $has_tag == true ]] || continue
    image_size=$(docker image inspect -f '{{.Size}}' "$image_id" 2>/dev/null || true)
    [[ $image_size =~ ^[0-9]+$ ]] || continue
    printf '%s\t%s\n' "$image_id" "$image_size" >> "$work_dir/image-candidates"
  done < "$work_dir/image-ids"
  cut -f1 "$work_dir/image-candidates"
}

remove_container_candidates() {
  local container_id name status removed=0
  while IFS= read -r container_id; do
    [[ -n $container_id ]] || continue
    name=$(docker inspect -f '{{.Name}}' "$container_id" 2>/dev/null || true)
    status=$(docker inspect -f '{{.State.Status}}' "$container_id" 2>/dev/null || true)
    [[ $name =~ ^/sub2api-dev-pre && $status == exited ]] || continue
    docker rm "$container_id" >/dev/null 2>&1
    removed=$((removed + 1))
  done < "$work_dir/container-candidates"
  printf '%s\n' "$removed"
}

remove_image_candidates() {
  local image_id removed=0 tag valid has_tag
  list_image_candidates > "$work_dir/apply-image-ids"
  while IFS= read -r image_id; do
    [[ -n $image_id ]] || continue
    mapfile -t tags < <(docker image inspect -f '{{range .RepoTags}}{{println .}}{{end}}' "$image_id" 2>/dev/null)
    valid=true
    has_tag=false
    for tag in "${tags[@]}"; do
      [[ -n $tag ]] || continue
      has_tag=true
      if [[ ! $tag =~ ^sub2api:.+-([0-9a-f]{40})$ || ${BASH_REMATCH[1]} == "$target_commit" ]]; then
        valid=false
        break
      fi
    done
    [[ $valid == true && $has_tag == true ]] || continue
    write_referenced_images > "$work_dir/referenced-images-now"
    grep -Fxq "$image_id" "$work_dir/referenced-images-now" && continue
    docker image rm "${tags[@]}" >/dev/null 2>&1
    if ! docker image inspect "$image_id" >/dev/null 2>&1; then
      removed=$((removed + 1))
    fi
  done < "$work_dir/apply-image-ids"
  printf '%s\n' "$removed"
}

list_container_candidates > "$work_dir/container-candidates"
container_candidates=$(awk 'NF {count++} END {print count+0}' "$work_dir/container-candidates")
container_bytes=$(container_reclaimable_bytes < "$work_dir/container-candidates")
list_image_candidates > "$work_dir/image-candidate-ids"
image_candidates=$(awk 'NF {count++} END {print count+0}' "$work_dir/image-candidate-ids")
image_bytes=$(awk -F '\t' '{total += $2} END {printf "%.0f\n", total+0}' "$work_dir/image-candidates")
removed_containers=0
removed_images=0

if [[ $mode == apply ]]; then
  removed_containers=$(remove_container_candidates)
  removed_images=$(remove_image_candidates)
fi

free_bytes=$(docker_free_bytes)
[[ $free_bytes =~ ^[0-9]+$ ]]
if (( free_bytes > required_bytes )); then
  space_status=sufficient
else
  space_status=insufficient
fi

printf 'cleanup_mode=%s\n' "$mode"
printf 'space_status=%s\n' "$space_status"
printf 'free_bytes=%s\n' "$free_bytes"
printf 'required_bytes=%s\n' "$required_bytes"
printf 'container_candidates=%s\n' "$container_candidates"
printf 'container_reclaimable_bytes=%s\n' "$container_bytes"
printf 'image_candidates=%s\n' "$image_candidates"
printf 'image_reclaimable_bytes=%s\n' "$image_bytes"
printf 'removed_containers=%s\n' "$removed_containers"
printf 'removed_images=%s\n' "$removed_images"
