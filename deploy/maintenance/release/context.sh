#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
[[ $release_dir =~ ^/opt/sub2api/releases/((182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8})$ ]]
release_id=${BASH_REMATCH[1]}
[[ -d $release_dir && ! -L $release_dir ]]
[[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
active_claim=/opt/sub2api/releases/.active-release
[[ -d $active_claim && ! -L $active_claim ]]
grep -Fxq "release_id=$release_id" "$active_claim/release_id"
[[ -f $active_claim/gate.json && ! -L $active_claim/gate.json ]]
(cd "$active_claim" && sha256sum -c CLAIM_SHA256SUMS >/dev/null)
assets_dir="$active_claim/assets"
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$active_claim/gate.json")
candidate_archive_sha=$(jq -er '.evidence.candidate_archive_sha256' "$active_claim/gate.json")
commit=$(jq -er '.manifest.commit_sha' "$active_claim/gate.json")
profile=$(jq -er '.manifest.profile' "$active_claim/gate.json")
version=$(jq -er '.manifest.version' "$active_claim/gate.json")
candidate_tag="sub2api:baiyu-$version-$commit"
mapfile -t migrations < <(jq -er '.manifest.migrations[]' "$active_claim/gate.json")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $candidate_archive_sha =~ ^[0-9a-f]{64}$ ]]
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ ${#migrations[@]} -gt 0 ]]
grep -Fxq "release_id=$release_id" "$release_dir/.prepared"
grep -Fxq "candidate_image_id=$candidate_image_id" "$release_dir/.prepared"
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
state_dir="/opt/sub2api/backups/release-state/$release_id"

# Production is allowed to have two application slots during a graceful
# release.  The active slot is recorded outside the release claim so a
# subsequent release can select the opposite loopback port without relying on
# a floating Docker tag or a hard-coded container name.
active_slot_file=${ACTIVE_SLOT_FILE:-/opt/sub2api/active-app}
active_container=sub2api
active_port=18080
if [[ -f $active_slot_file && ! -L $active_slot_file ]]; then
  [[ $(grep -c '^container=' "$active_slot_file") == 1 ]]
  [[ $(grep -c '^port=' "$active_slot_file") == 1 ]]
  parsed_container=
  parsed_port=
  while IFS='=' read -r key value; do
    case "$key" in
      container) [[ $value =~ ^[a-zA-Z0-9_.-]{1,80}$ ]]; parsed_container=$value ;;
      port) [[ $value == 18080 || $value == 18081 ]]; parsed_port=$value ;;
    esac
  done < "$active_slot_file"
  [[ -n $parsed_container && -n $parsed_port ]]
  active_container=$parsed_container
  active_port=$parsed_port
elif [[ -e $active_slot_file || -L $active_slot_file ]]; then
  exit 1
fi
candidate_port=18081
[[ $active_port == 18081 ]] && candidate_port=18080
candidate_container="sub2api-candidate-$release_id"
if [[ -f $state_dir/candidate-app && ! -L $state_dir/candidate-app ]]; then
  while IFS='=' read -r key value; do
    case "$key" in
      container) [[ $value =~ ^sub2api-candidate-[a-zA-Z0-9_.-]+$ ]] && candidate_container=$value ;;
      port) [[ $value == 18080 || $value == 18081 ]] && candidate_port=$value ;;
    esac
  done < "$state_dir/candidate-app"
fi
[[ $candidate_container =~ ^[a-zA-Z0-9_.-]{1,100}$ ]]

# Resolve the exact Compose closure declared by the deployment .env.  Release
# scripts must not silently fall back to docker-compose.yml because production
# commonly adds docker-compose.local.yml for the real /app/data bind mount.
load_release_compose_files() {
  local root=${1:?compose root is required}
  local env_file="$root/.env"
  [[ -f $env_file && ! -L $env_file ]]
  local count raw item
  count=$(grep -c '^COMPOSE_FILE=' "$env_file" || true)
  [[ $count == 0 || $count == 1 ]]
  if [[ $count == 1 ]]; then
    raw=$(sed -n 's/^COMPOSE_FILE=//p' "$env_file")
    [[ -n $raw && $raw != *[[:space:]]* ]]
  else
    raw=docker-compose.yml
  fi
  IFS=':' read -r -a release_compose_files <<<"$raw"
  [[ ${#release_compose_files[@]} -gt 0 ]]
  release_compose_args=()
  local seen=':'
  for item in "${release_compose_files[@]}"; do
    [[ $item =~ ^[A-Za-z0-9_.-]+\.ya?ml$ ]]
    [[ $seen != *":$item:"* ]]
    [[ -f $root/$item && ! -L $root/$item ]]
    seen+="$item:"
    release_compose_args+=(-f "$root/$item")
  done
  [[ " $raw " == *docker-compose.yml* ]]
}

release_compose_value_with_active_override() {
  local result= item
  for item in "${release_compose_files[@]}"; do
    [[ $item == docker-compose.release-active.yml ]] && continue
    [[ -z $result ]] && result=$item || result+=":$item"
  done
  [[ -n $result ]]
  printf '%s:docker-compose.release-active.yml\n' "$result"
}

assert_prompt_audit_disabled() {
  if [[ $profile != 194 && $profile != 195 && $profile != 197 && $profile != 198 && $profile != 199 && $profile != 202 && $profile != 206 && $profile != 207 && $profile != 208 && $profile != 209 && $profile != 210 && $profile != 212 && $profile != 213 && $profile != 215 && $profile != 232 && $profile != 233 && $profile != 234 ]]; then
    printf 'prompt_audit_disabled=not_applicable\n'
    printf 'prompt_audit_jobs=not_applicable\n'
    printf 'prompt_audit_events=not_applicable\n'
    return 0
  fi
  local row
  row=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "
WITH config AS (
  SELECT COALESCE(NULLIF((SELECT value FROM settings WHERE key='prompt_audit_config'), ''), '{}')::jsonb AS value
)
SELECT
  NOT COALESCE((value->>'enabled')::boolean, false)
    AND NOT COALESCE((value->>'blocking_enabled')::boolean, false)
    AND NOT COALESCE((value->>'store_pass_events')::boolean, false)
    AND jsonb_typeof(COALESCE(value->'endpoints', '[]'::jsonb)) = 'array'
    AND jsonb_array_length(COALESCE(value->'endpoints', '[]'::jsonb)) = 0,
  (SELECT COUNT(*) FROM prompt_audit_jobs),
  (SELECT COUNT(*) FROM prompt_audit_events)
FROM config")
  [[ $row == 't|0|0' ]]
  printf 'prompt_audit_disabled=true\n'
  printf 'prompt_audit_jobs=0\n'
  printf 'prompt_audit_events=0\n'
}
