#!/usr/bin/env bash

# Source-only. Preserve the exact supported Compose file closure and compare
# canonical rendered hashes without returning expanded configuration or secrets.
_recovery_compose_helper_dir=$(dirname -- "${BASH_SOURCE[0]}") || return 1
_recovery_compose_helper_dir=$(cd -- "$_recovery_compose_helper_dir" && pwd -P) || return 1
source "$_recovery_compose_helper_dir/compose-contract.sh" || return 1
unset _recovery_compose_helper_dir

_recovery_compose_directory() {
  local root=${1:?directory is required}
  _required_1=$(realpath -e -- "$root") || return 1
  [[ $root != / && -d $root && ! -L $root && ${_required_1} == "$root" ]] || return 1
}

_recovery_compose_private_directory() {
  _recovery_compose_directory "$1" || return 1
  _required_2=$(stat -c '%u:%g:%a' "$1") || return 1
  [[ ${_required_2} == 0:0:700 ]] || return 1
}

_recovery_compose_file() {
  local path=${1:?file is required}
  _required_3=$(stat -c '%u:%g:%h' "$path") || return 1
  [[ -f $path && ! -L $path && ${_required_3} == 0:0:1 ]] || return 1
  local observed_mode
  observed_mode=$(stat -c '%a' "$path") || return 1
  case "$observed_mode" in 400|600|640|644) ;; *) return 1 ;; esac
}

_recovery_compose_reference() {
  local value=${1:?reference is required}
  [[ $value =~ ^[A-Za-z0-9_.-]+$ && $value != . && $value != .. && $value != *..* ]] || return 1
}

_recovery_compose_config() (
  set -Eeuo pipefail
  local root=${1:?root is required}
  shift
  cd -- "$root" || return 1
  # .env is the only interpolation source. In particular, never inherit a
  # caller's COMPOSE_FILE, profiles, Docker endpoint, or application secrets.
  env -i PATH="$PATH" HOME=/root docker compose -p sub2api-recovery-closure \
    --env-file "$root/.env" "$@" config --format json 2>/dev/null
)

_recovery_compose_render_sha256() (
  set -Eeuo pipefail
  local root=$1
  shift
  _recovery_compose_config "$root" "$@" |
    jq -ecS --arg root "$root" '
      walk(if type == "string" and startswith($root + "/")
           then "<COMPOSE_ROOT>" + .[($root | length):] else . end)' |
    sha256sum | awk '{print $1}'
)

_recovery_compose_dependencies() (
  set -Eeuo pipefail
  local root=$1 file raw item normalized
  load_release_compose_files "$root" || return 1
  for file in "${release_compose_files[@]}"; do
    # include/extends can depend on files omitted by the rendered model. Until
    # their transitive closure is supported, refuse them before any mutation.
    python3 - "$root/$file" >/dev/null 2>&1 <<'PY' || return 1
import sys
import yaml

document = yaml.safe_load(open(sys.argv[1], encoding='utf-8'))
if not isinstance(document, dict) or 'include' in document:
    raise SystemExit(1)
services = document.get('services', {})
if not isinstance(services, dict):
    raise SystemExit(1)
for service in services.values():
    if not isinstance(service, dict) or 'extends' in service:
        raise SystemExit(1)
PY
  done
  raw=$(cd -- "$root" && env -i PATH="$PATH" HOME=/root \
    docker compose -p sub2api-recovery-closure --env-file "$root/.env" \
    "${release_compose_args[@]}" config --no-env-resolution --format json 2>/dev/null) || return 1
  jq -e 'type == "object" and (.services | type) == "object"' <<<"$raw" >/dev/null || return 1
  # Docker parses YAML, interpolation, anchors and env_file long syntax. Never
  # infer dependencies with a line-oriented approximation of the YAML grammar.
  normalized=$(jq -r '
    [.services[]?.env_file[]? | if type == "string" then . else .path end] +
    [.configs[]?.file, .secrets[]?.file | select(. != null)] |
    if all(.[]; type == "string" and length > 0) then .[] else error("dependency") end
  ' <<<"$raw") || return 1
  if [[ -n $normalized ]]; then
    while IFS= read -r item; do
      if [[ $item == "$root/"* ]]; then item=${item#"$root/"}; fi
      item=${item#./}
      _recovery_compose_reference "$item" || return 1
      _recovery_compose_file "$root/$item" || return 1
      printf '%s\n' "$item" || return 1
    done <<<"$normalized"
  fi
)

_recovery_compose_inventory() {
  local src=$1 line hash item seen=':' count=0
  _recovery_compose_private_directory "$src" || return 1
  _recovery_compose_file "$src/.recovery-compose-files.sha256" || return 1
  _recovery_compose_file "$src/.recovery-compose-render.sha256" || return 1
  _required_5=$(stat -c '%a' "$src/.recovery-compose-files.sha256") || return 1
  _required_4=$(stat -c '%a' "$src/.recovery-compose-render.sha256") || return 1
  [[ ${_required_5} == 400 &&
     ${_required_4} == 400 ]] || return 1
  RECOVERY_COMPOSE_FILES=()
  while IFS= read -r line; do
    [[ $line =~ ^([0-9a-f]{64})\ \ ([A-Za-z0-9_.-]+)$ ]] || return 1
    hash=${BASH_REMATCH[1]}; item=${BASH_REMATCH[2]}
    _recovery_compose_reference "$item" || return 1
    [[ $seen != *":$item:"* && $item != .recovery-compose-* ]] || return 1
    seen+="$item:"
    _recovery_compose_file "$src/$item" || return 1
    _required_6=$(sha256sum "$src/$item" | awk '{print $1}') || return 1
    [[ ${_required_6} == "$hash" ]] || return 1
    RECOVERY_COMPOSE_FILES+=("$item")
    ((count+=1))
    ((count <= 32)) || return 1
  done < "$src/.recovery-compose-files.sha256"
  ((count >= 2)) || return 1
  [[ $seen == *':.env:'* && $seen == *':docker-compose.yml:'* ]] || return 1
  [[ $(<"$src/.recovery-compose-render.sha256") =~ ^[0-9a-f]{64}$ ]] || return 1
  load_release_compose_files "$src" || return 1
  for item in "${release_compose_files[@]}"; do [[ $seen == *":$item:"* ]] || return 1; done
  if [[ " ${release_compose_files[*]} " == *' docker-compose.release-active.yml '* ]]; then
    [[ ! -e $src/no-release-active-override && ! -L $src/no-release-active-override ]] || return 1
  else
    [[ $seen == *':no-release-active-override:'* && ! -s $src/no-release-active-override &&
       ! -e $src/docker-compose.release-active.yml && ! -L $src/docker-compose.release-active.yml ]] || return 1
  fi
}

recovery_compose_snapshot() (
  set -Eeuo pipefail
  local root=${1:?deployment root is required} dest=${2:?snapshot directory is required}
  local raw item source_sha copied_sha
  local -a files=() dependencies=() sorted=()
  _recovery_compose_directory "$root" || return 1
  _recovery_compose_private_directory "$dest" || return 1
  _required_7=$(find "$dest" -mindepth 1 -maxdepth 1 -print -quit) || return 1
  [[ $root != "$dest" && -z ${_required_7} ]] || return 1
  load_release_compose_files "$root" || return 1
  files=(.env "${release_compose_files[@]}")
  raw=$(_recovery_compose_dependencies "$root") || return 1
  if [[ -n $raw ]]; then mapfile -t dependencies <<<"$raw" || return 1; files+=("${dependencies[@]}"); fi
  source_sha=$(_recovery_compose_render_sha256 "$root" "${release_compose_args[@]}") || return 1
  [[ $source_sha =~ ^[0-9a-f]{64}$ ]] || return 1
  raw=$(printf '%s\n' "${files[@]}" | LC_ALL=C sort -u) || return 1
  mapfile -t sorted <<<"$raw" || return 1
  ((${#sorted[@]} >= 2 && ${#sorted[@]} <= 31)) || return 1
  for item in "${sorted[@]}"; do
    _recovery_compose_reference "$item" || return 1
    _recovery_compose_file "$root/$item" || return 1
    install -o root -g root -m 600 -- "$root/$item" "$dest/$item" || return 1
  done
  if [[ " ${release_compose_files[*]} " != *' docker-compose.release-active.yml '* ]]; then
    install -o root -g root -m 600 /dev/null "$dest/no-release-active-override" || return 1
    sorted+=(no-release-active-override)
  fi
  (cd -- "$dest" && sha256sum -- "${sorted[@]}") > "$dest/.recovery-compose-files.sha256" || return 1
  printf '%s\n' "$source_sha" > "$dest/.recovery-compose-render.sha256" || return 1
  chmod 400 "$dest/.recovery-compose-files.sha256" "$dest/.recovery-compose-render.sha256" || return 1
  _recovery_compose_inventory "$dest" || return 1
  # Verify both the copied closure and the live source after copying. A .env
  # or dependency changed midway cannot become a silently inconsistent backup.
  copied_sha=$(_recovery_compose_render_sha256 "$dest" "${release_compose_args[@]}") || return 1
  [[ $copied_sha == "$source_sha" ]] || return 1
  load_release_compose_files "$root" || return 1
  _required_8=$(_recovery_compose_render_sha256 "$root" "${release_compose_args[@]}") || return 1
  [[ ${_required_8} == "$source_sha" ]] || return 1
  printf 'compose_snapshot_render=verified\n'
)

recovery_compose_verify_live() (
  set -Eeuo pipefail
  local src=${1:?snapshot directory is required} dest=${2:?live directory is required}
  local expected_image=${3:?expected image ID is required} item rendered actual_image
  [[ $expected_image =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  _recovery_compose_inventory "$src" || return 1
  _recovery_compose_directory "$dest" || return 1
  for item in "${RECOVERY_COMPOSE_FILES[@]}"; do
    _recovery_compose_file "$dest/$item" || return 1
    cmp -s -- "$src/$item" "$dest/$item" || return 1
  done
  if [[ -f $src/no-release-active-override ]]; then
    [[ ! -e $dest/docker-compose.release-active.yml && ! -L $dest/docker-compose.release-active.yml ]] || return 1
  fi
  load_release_compose_files "$dest" || return 1
  rendered=$(_recovery_compose_config "$dest" "${release_compose_args[@]}") || return 1
  actual_image=$(jq -er '.services.sub2api.image | select(type == "string" and length > 0)' <<<"$rendered") || return 1
  [[ $actual_image != *[[:space:]]* ]] || return 1
  _required_9=$(docker image inspect -f '{{.Id}}' "$actual_image" 2>/dev/null) || return 1
  [[ ${_required_9} == "$expected_image" ]] || return 1
  _required_10=$(_recovery_compose_render_sha256 "$dest" "${release_compose_args[@]}") || return 1
  [[ ${_required_10} == \
     "$(<"$src/.recovery-compose-render.sha256")" ]] || return 1
  printf 'compose_restore_render=verified\n'
)

recovery_compose_restore_verify() (
  set -Eeuo pipefail
  local src=${1:?snapshot directory is required} dest=${2:?private restore directory is required}
  local expected_image=${3:?expected image ID is required} item
  _recovery_compose_inventory "$src" || return 1
  _recovery_compose_private_directory "$dest" || return 1
  _required_11=$(find "$dest" -mindepth 1 -maxdepth 1 -print -quit) || return 1
  [[ $src != "$dest" && -z ${_required_11} ]] || return 1
  for item in "${RECOVERY_COMPOSE_FILES[@]}"; do install -o root -g root -m 600 -- "$src/$item" "$dest/$item" || return 1; done
  recovery_compose_verify_live "$src" "$dest" "$expected_image"
)
