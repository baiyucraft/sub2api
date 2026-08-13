#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
[[ $(docker inspect -f '{{.Image}}' "$candidate_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$candidate_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
health_headers=$(mktemp /tmp/sub2api-candidate-expose.XXXXXX)
previous_upstream=$(mktemp /tmp/sub2api-expose-previous.XXXXXX)
public_headers=
cp -p "$managed_upstream" "$previous_upstream"
switched=false
cleanup() {
  code=$?
  if [[ $code -ne 0 && $switched == true ]]; then
    restore_tmp="$managed_upstream.restore.$$"
    if install -m 600 "$previous_upstream" "$restore_tmp"; then
      mv -T -- "$restore_tmp" "$managed_upstream" || true
    fi
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
  fi
  rm -f "$health_headers" ${public_headers:+"$public_headers"} "$previous_upstream"
  exit "$code"
}
trap cleanup EXIT
[[ $(curl -sS -D "$health_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${candidate_port}/health") == 200 ]]
assert_http_header_equals "$health_headers" X-Sub2API-Instance "$candidate_instance_id"
assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready false
[[ -f $managed_upstream && ! -L $managed_upstream ]]
printf 'phase=candidate\nroute_port=%s\nprevious_port=%s\n' "$candidate_port" "$active_port" > "$state_dir/route-switch-intent.tmp"
chmod 600 "$state_dir/route-switch-intent.tmp"
mv -T -- "$state_dir/route-switch-intent.tmp" "$state_dir/route-switch-intent"
upstream_tmp="$managed_upstream.tmp.$$"
printf 'upstream sub2api_release_backend {\n    server 127.0.0.1:%s;\n    keepalive 128;\n}\n' "$candidate_port" > "$upstream_tmp"
chmod 600 "$upstream_tmp"
mv -T -- "$upstream_tmp" "$managed_upstream"
switched=true
nginx -t >/dev/null 2>&1
systemctl reload nginx
[[ $(systemctl is-active nginx) == active ]]
grep -Fq "server 127.0.0.1:$candidate_port;" "$managed_upstream"
public_headers=$(mktemp /tmp/sub2api-public-expose.XXXXXX)
[[ $(curl -sS --resolve "$domain:443:$direct_ip" -D "$public_headers" -o /dev/null -w '%{http_code}' -H 'Connection: close' "https://$domain/health") == 200 ]]
assert_http_header_equals "$public_headers" X-Sub2API-Instance "$candidate_instance_id"
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=%s\nport=%s\nimage_id=%s\nrelease_id=%s\n' "$candidate_container" "$candidate_port" "$candidate_image_id" "$release_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
printf 'phase=candidate\nroute_port=%s\nprevious_port=%s\n' "$candidate_port" "$active_port" > "$state_dir/route-switched.tmp"
chmod 600 "$state_dir/route-switched.tmp"
mv -T -- "$state_dir/route-switched.tmp" "$state_dir/route-switched"
printf 'public_traffic_enabled=true\n'
printf 'nginx_reload=pass\n'
printf 'new_active_container=%s\n' "$candidate_container"
printf 'new_active_port=%s\n' "$candidate_port"
printf 'previous_container=%s\n' "$active_container"
printf 'previous_port=%s\n' "$active_port"
