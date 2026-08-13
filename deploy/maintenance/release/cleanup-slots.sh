#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
ACTIVE_CLAIM=${ACTIVE_CLAIM:-$release_dir/.consumed}
source "$ACTIVE_CLAIM/assets/context.sh"
deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
managed_upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
[[ -f $active_slot_file && ! -L $active_slot_file ]]
[[ $(sed -n 's/^container=//p' "$active_slot_file") == sub2api ]]
active_port=$(sed -n 's/^port=//p' "$active_slot_file")
[[ $active_port == 18080 || $active_port == 18081 ]]
[[ $(sed -n 's/^release_id=//p' "$active_slot_file") == "$release_id" ]]
[[ $(sed -n 's/^instance_id=//p' "$active_slot_file") == "$final_instance_id" ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
health_headers=$(mktemp /tmp/sub2api-cleanup-health.XXXXXX)
trap 'rm -f "$health_headers"' EXIT
[[ $(curl -sS --max-time 15 -D "$health_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${active_port}/health") == 200 ]]
grep -Eiq "^x-sub2api-instance:[[:space:]]*$final_instance_id\r?$" "$health_headers"
grep -Eiq '^x-sub2api-background-ready:[[:space:]]*true\r?$' "$health_headers"
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
[[ $(systemctl is-active nginx) == active ]]
assert_final_compose_closure "$deploy_dir" "$active_port"
candidate_removed=false
if docker inspect "$candidate_container" >/dev/null 2>&1; then
  [[ $(application_connection_count "$candidate_container") == 0 ]]
  docker stop -t 30 "$candidate_container" >/dev/null
  docker rm "$candidate_container" >/dev/null
  candidate_removed=true
fi
previous_removed=false
printf 'candidate_removed=%s\nprevious_removed=%s\n' "$candidate_removed" "$previous_removed"
