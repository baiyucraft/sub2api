#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
managed_upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
[[ ! -e $release_dir/.consumed ]]
[[ -f /opt/sub2api/active-app && ! -L /opt/sub2api/active-app ]]
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
active_port=$(sed -n 's/^port=//p' /opt/sub2api/active-app)
active_instance_id=$(sed -n 's/^instance_id=//p' /opt/sub2api/active-app)
active_release_id=$(sed -n 's/^release_id=//p' /opt/sub2api/active-app)
[[ $active_container == sub2api ]]
[[ $active_port == 18080 || $active_port == 18081 ]]
[[ $active_instance_id == "$final_instance_id" ]]
[[ $active_release_id == "$release_id" ]]
[[ $(docker inspect -f '{{.Image}}' "$active_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
health_headers=$(mktemp /tmp/sub2api-consume-health.XXXXXX)
trap 'rm -f "$health_headers"' EXIT
[[ $(curl -sS --max-time 15 -D "$health_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${active_port}/health") == 200 ]]
assert_http_header_equals "$health_headers" X-Sub2API-Instance "$final_instance_id"
assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready true
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
[[ $(systemctl is-active nginx) == active ]]
assert_final_compose_closure "$deploy_dir" "$active_port"
printf 'release_id=%s\ncandidate_image_id=%s\nconsumed_at=%s\n' "$release_id" "$candidate_image_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/marker"
chmod 400 "$active_claim/marker"
mv -T -- "$active_claim" "$release_dir/.consumed"
[[ -d $release_dir/.consumed && ! -L $release_dir/.consumed && ! -e $active_claim ]]
printf 'gate_consumed=true\n'
