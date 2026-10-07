#!/usr/bin/env bash
# Sourced only by the signed downtime switch context. No standalone write path.
sub2api_activity_reward_cost_postflight() {
  [[ $release_profile == 264 && $deployment_mode == downtime ]]
  [[ $(docker inspect -f '{{.State.Running}}' "$active_container") == false ]]
  local container containers inspection is_application
  containers=$(docker ps -q)
  while IFS= read -r container; do
    [[ -n $container ]] || continue
    # Detect both Compose and raw-run applications; never return inspect data.
    inspection=$(docker inspect "$container")
    is_application=$(jq -er 'if type!="array" or length!=1 or (.[0].Config|type)!="object" then error("invalid container state") else any(.[]; (.Config.Labels["com.docker.compose.service"] == "sub2api") or any([.Path, .Config.Entrypoint[]?, .Config.Cmd[]?][]; type=="string" and (.=="sub2api" or test("(^|[^A-Za-z0-9_./-])/app/sub2api([^A-Za-z0-9_./-]|$)")))) | tostring end' <<<"$inspection")
    [[ $is_application == false ]]
  done <<<"$containers"
  local audit_before="$state_dir/activity-reward-cost-before.json"
  local audit_apply="$state_dir/activity-reward-cost-apply.json"
  local audit_after="$state_dir/activity-reward-cost-after.json"
  local cache_result="$state_dir/dashboard-cache-refresh.json"
  local audit_file
  for audit_file in "$audit_before" "$audit_apply" "$audit_after" "$cache_result"; do
    [[ ! -e $audit_file && ! -L $audit_file ]]
  done
  [[ ${SUB2API_RELEASE_RAW_LOG:-} == /opt/sub2api/releases/*/logs/production.raw.log ]]
  [[ -f $SUB2API_RELEASE_RAW_LOG && ! -L $SUB2API_RELEASE_RAW_LOG ]]
  [[ $(stat -c '%U:%G:%a:%h' "$SUB2API_RELEASE_RAW_LOG") == root:root:600:1 ]]
  local output status
  # Connection stays inside the PostgreSQL container; no DSN or password argv.
  psql() { docker exec -i sub2api-postgres psql -U sub2api -d sub2api "$@"; }
  status=0
  output=$(source "$assets_dir/backfill-activity-reward-costs.sh" --mode check) || status=$?
  unset -f psql
  [[ $status == 0 || $status == 1 ]]
  # A missing entry may be repaired; database failure, orphan or snapshot drift
  # cannot be interpreted as permission to mutate the ledger.
  jq -e 'type == "object" and (.verified|type)=="boolean" and (.missing_count|type)=="number" and .orphan_count==0 and .mismatch_count==0' <<<"$output" >/dev/null
  (umask 077; printf '%s\n' "$output" > "$audit_before")
  psql() { docker exec -i sub2api-postgres psql -U sub2api -d sub2api "$@"; }
  output=$(source "$assets_dir/backfill-activity-reward-costs.sh" --mode apply --old-instances-drained)
  unset -f psql
  jq -e '.verified==true' <<<"$output" >/dev/null
  (umask 077; printf '%s\n' "$output" > "$audit_apply")
  psql() { docker exec -i sub2api-postgres psql -U sub2api -d sub2api "$@"; }
  output=$(source "$assets_dir/backfill-activity-reward-costs.sh" --mode check)
  unset -f psql
  jq -e '.verified==true and .missing_count==0 and .orphan_count==0 and .mismatch_count==0' <<<"$output" >/dev/null
  (umask 077; printf '%s\n' "$output" > "$audit_after")
  output=$(docker compose "${candidate_compose_args[@]}" run --rm --no-deps sub2api /app/sub2api --clear-dashboard-cache 2>> "$SUB2API_RELEASE_RAW_LOG")
  jq -e 'type=="object" and keys==["dashboard_cache_cleared"] and .dashboard_cache_cleared==true' <<<"$output" >/dev/null
  (umask 077; printf '%s\n' "$output" > "$cache_result")
}
