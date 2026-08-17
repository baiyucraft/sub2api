#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
[[ $release_dir =~ ^/opt/sub2api/releases/((182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8})$ ]]
release_id=${BASH_REMATCH[1]}
[[ -d $release_dir && ! -L $release_dir ]]
[[ -f $release_dir/.prepared && ! -L $release_dir/.prepared ]]
active_claim=${ACTIVE_CLAIM:-/opt/sub2api/releases/.active-release}
[[ $active_claim == /opt/sub2api/releases/.active-release || $active_claim == "$release_dir/.consumed" ]]
[[ -d $active_claim && ! -L $active_claim ]]
grep -Fxq "release_id=$release_id" "$active_claim/release_id"
[[ -f $active_claim/gate.json && ! -L $active_claim/gate.json ]]
(cd "$active_claim" && sha256sum -c CLAIM_SHA256SUMS >/dev/null)
assets_dir="$active_claim/assets"
source "$assets_dir/compose-contract.sh"
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$active_claim/gate.json")
candidate_archive_sha=$(jq -er '.evidence.candidate_archive_sha256' "$active_claim/gate.json")
commit=$(jq -er '.manifest.commit_sha' "$active_claim/gate.json")
profile=$(jq -er '.manifest.profile' "$active_claim/gate.json")
version=$(jq -er '.manifest.version' "$active_claim/gate.json")
deployment_mode=${DEPLOYMENT_MODE:-$(jq -er '.manifest.deployment_mode' "$active_claim/gate.json")}
[[ $deployment_mode == blue-green || $deployment_mode == downtime ]]
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
active_instance_id=
if [[ -f $active_slot_file && ! -L $active_slot_file ]]; then
  [[ $(grep -c '^container=' "$active_slot_file") == 1 ]]
  [[ $(grep -c '^port=' "$active_slot_file") == 1 ]]
  parsed_container=
  parsed_port=
  while IFS='=' read -r key value; do
    case "$key" in
      container) [[ $value =~ ^[a-zA-Z0-9_.-]{1,80}$ ]]; parsed_container=$value ;;
      port) [[ $value == 18080 || $value == 18081 ]]; parsed_port=$value ;;
      instance_id) [[ -z $value || $value =~ ^[a-zA-Z0-9_.-]{1,128}$ ]]; active_instance_id=$value ;;
    esac
  done < "$active_slot_file"
  [[ -n $parsed_container && -n $parsed_port ]]
  active_container=$parsed_container
  active_port=$parsed_port
elif [[ -e $active_slot_file || -L $active_slot_file ]]; then
  exit 1
fi
final_instance_id="$release_id-active"
if [[ $deployment_mode == downtime ]]; then
  candidate_port=$active_port
  candidate_container=sub2api
  candidate_instance_id=$final_instance_id
else
  candidate_port=18081
  [[ $active_port == 18081 ]] && candidate_port=18080
  candidate_container="sub2api-candidate-$release_id"
  candidate_instance_id="$release_id-candidate"
  if [[ -f $state_dir/candidate-app && ! -L $state_dir/candidate-app ]]; then
    while IFS='=' read -r key value; do
      case "$key" in
        container) [[ $value =~ ^sub2api-candidate-[a-zA-Z0-9_.-]+$ ]] && candidate_container=$value ;;
        port) [[ $value == 18080 || $value == 18081 ]] && candidate_port=$value ;;
        instance_id) [[ $value =~ ^[a-zA-Z0-9_.-]{1,128}$ ]] && candidate_instance_id=$value ;;
      esac
    done < "$state_dir/candidate-app"
  fi
fi
[[ $candidate_container =~ ^[a-zA-Z0-9_.-]{1,100}$ ]]
[[ $candidate_instance_id =~ ^[a-zA-Z0-9_.-]{1,128}$ ]]
[[ $final_instance_id =~ ^[a-zA-Z0-9_.-]{1,128}$ ]]

application_connection_count() {
  local container=${1:?container is required}
  docker exec "$container" sh -lc 'awk "NR>1 && \$2 ~ /:1F90\$/ && \$4 == \"01\" {count++} END {print count+0}" /proc/net/tcp /proc/net/tcp6' 2>/dev/null || printf 'unknown'
}

nginx_draining_worker_count() {
  ps -eo args= | grep -Ec '^nginx: worker process is shutting down$' || true
}

wait_for_application_drain() {
  local container=${1:?container is required}
  local timeout=${2:?timeout is required}
  local deadline=$((SECONDS + timeout))
  local connections=unknown
  local draining_workers=unknown
  while docker inspect "$container" >/dev/null 2>&1; do
    connections=$(application_connection_count "$container")
    draining_workers=$(nginx_draining_worker_count)
    if [[ $connections == 0 && $draining_workers == 0 ]]; then
      printf '0\n'
      return 0
    fi
    if [[ ! $connections =~ ^[0-9]+$ || ! $draining_workers =~ ^[0-9]+$ ]]; then
      printf 'unknown\n'
      return 0
    fi
    if (( SECONDS >= deadline )); then
      # A zero application connection count is not sufficient while an old
      # Nginx worker is still shutting down.  Returning a distinct timeout
      # keeps callers fail-closed instead of reporting the slot as drained.
      printf 'timeout\n'
      return 0
    fi
    sleep 2
  done
  # A container disappearing before both counters reached zero is an
  # unproven drain, not a successful one.
  printf 'unknown\n'
}

# curl writes response headers with CRLF line endings.  GNU grep -E does not
# interpret \r as a carriage return, so patterns ending in \r? silently reject
# valid headers.  Normalize the file first, then compare the complete value.
assert_http_header_equals() {
  local headers=${1:?headers file is required}
  local name=${2:?header name is required}
  local expected=${3:?expected header value is required}
  [[ -f $headers && ! -L $headers ]]
  [[ $name =~ ^[A-Za-z0-9-]+$ ]]
  [[ $expected =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
  local actual
  actual=$(tr -d '\r' < "$headers" | awk -F: -v name="$name" '
    tolower($1) == tolower(name) {
      # Header names are case-insensitive and proxies may leave optional
      # whitespace around the field value.  Normalize both sides before the
      # single-value assertion so curl/HTTP/1.1 formatting cannot make a
      # valid health response fail closed.
      sub(/^[^:]*:[[:space:]]*/, "")
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      print
    }
  ')
  [[ $(printf '%s\n' "$actual" | grep -c .) == 1 ]]
  [[ $actual == "$expected" ]]
}

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

assert_sub2api_compose_closure() {
  local root=${1:?compose root is required}
  local expected_port=${2:?published port is required}
  local expected_image=${3:?expected image is required}
  local expected_instance=${4-}
  [[ $expected_port == 18080 || $expected_port == 18081 ]]
  load_release_compose_files "$root"
  local compose_json compose_image network_mode
  compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
  compose_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
  [[ -n $compose_image ]]
  [[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$expected_image" ]]
  network_mode=$(sub2api_compose_network_mode "$compose_json" "$expected_port")
  assert_sub2api_healthcheck_contract "$compose_json" "$network_mode" "$expected_port"
  jq -e --arg instance "$expected_instance" '
    .services.sub2api.container_name == "sub2api" and
    (
      $instance == "" or
      (
        .services.sub2api.environment.SUB2API_INSTANCE_ID == $instance and
        .services.sub2api.environment.SUB2API_BACKGROUND_ACTIVATION_FILE == "/app/data/.sub2api-active-instance"
      )
    )
  ' <<<"$compose_json" >/dev/null
  printf '%s\n' "$network_mode"
}

assert_final_compose_closure() {
  local root=${1:?compose root is required}
  local expected_port=${2:?published port is required}
  assert_sub2api_compose_closure "$root" "$expected_port" "$candidate_image_id" "$final_instance_id" >/dev/null
}

assert_prompt_audit_disabled() {
  if [[ $profile != 194 && $profile != 195 && $profile != 197 && $profile != 198 && $profile != 199 && $profile != 202 && $profile != 206 && $profile != 207 && $profile != 208 && $profile != 209 && $profile != 210 && $profile != 212 && $profile != 213 && $profile != 215 && $profile != 232 && $profile != 233 && $profile != 234 && $profile != 235 && $profile != 236 ]]; then
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
