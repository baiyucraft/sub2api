#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
ACTIVE_CLAIM=${ACTIVE_CLAIM:-$release_dir/.consumed}
source "$ACTIVE_CLAIM/assets/context.sh"
deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
managed_upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
[[ -f $active_slot_file && ! -L $active_slot_file ]]
_required_1=$(sed -n 's/^container=//p' "$active_slot_file")
[[ ${_required_1} == sub2api ]]
active_port=$(sed -n 's/^port=//p' "$active_slot_file")
[[ $active_port == 18080 || $active_port == 18081 ]]
_required_2=$(sed -n 's/^release_id=//p' "$active_slot_file")
[[ ${_required_2} == "$release_id" ]]
_required_3=$(sed -n 's/^instance_id=//p' "$active_slot_file")
[[ ${_required_3} == "$final_instance_id" ]]
_required_4=$(docker inspect -f '{{.Image}}' sub2api)
[[ ${_required_4} == "$candidate_image_id" ]]
_required_5=$(docker inspect -f '{{.State.Health.Status}}' sub2api)
[[ ${_required_5} == healthy ]]
health_headers=$(mktemp /tmp/sub2api-cleanup-health.XXXXXX)
trap 'rm -f "$health_headers"' EXIT
_required_6=$(curl -sS --max-time 15 -D "$health_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${active_port}/health")
[[ ${_required_6} == 200 ]]
assert_http_header_equals "$health_headers" X-Sub2API-Instance "$final_instance_id"
assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready true
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
_required_7=$(systemctl is-active nginx)
[[ ${_required_7} == active ]]
assert_final_compose_closure "$deploy_dir" "$active_port"
candidate_removed=false
container_names=$(docker ps -a --format '{{.Names}}')
if [[ $candidate_container != sub2api && $'\n'"$container_names"$'\n' == *$'\n'"$candidate_container"$'\n'* ]]; then
  _required_8=$(application_connection_count "$candidate_container")
  [[ ${_required_8} == 0 ]]
  docker stop -t 30 "$candidate_container" >/dev/null
  docker rm "$candidate_container" >/dev/null
  candidate_removed=true
fi
previous_removed=false
printf 'candidate_removed=%s\nprevious_removed=%s\n' "$candidate_removed" "$previous_removed"
