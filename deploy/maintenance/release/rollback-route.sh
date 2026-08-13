#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
[[ -f $state_dir/nginx-upstream.conf && ! -L $state_dir/nginx-upstream.conf ]]
[[ -f $state_dir/pre-active-app && ! -L $state_dir/pre-active-app ]]
old_container=$(sed -n 's/^container=//p' "$state_dir/pre-active-app")
old_port=$(sed -n 's/^port=//p' "$state_dir/pre-active-app")
[[ $old_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $old_port =~ ^(18080|18081)$ ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$old_container" 2>/dev/null || true) == healthy ]]
tmp="$managed_upstream.rollback.$$"
install -m 600 "$state_dir/nginx-upstream.conf" "$tmp"
mv -T -- "$tmp" "$managed_upstream"
if ! nginx -t >/dev/null 2>&1 || ! systemctl reload nginx >/dev/null 2>&1; then
  install -m 600 "$state_dir/nginx-upstream.conf" "$tmp"
  mv -T -- "$tmp" "$managed_upstream"
  nginx -t >/dev/null 2>&1
  systemctl reload nginx >/dev/null 2>&1
  exit 1
fi
headers=$(mktemp /tmp/sub2api-route-rollback.XXXXXX)
trap 'rm -f "$headers"' EXIT
[[ $(curl -sS --resolve "$domain:443:$direct_ip" -D "$headers" -o /dev/null -w '%{http_code}' -H 'Connection: close' "https://$domain/health") == 200 ]]
slot_tmp="$active_slot_file.tmp.$$"
pre_image_id=$(docker inspect -f '{{.Image}}' "$old_container")
printf 'container=%s\nport=%s\nimage_id=%s\n' "$old_container" "$old_port" "$pre_image_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
rm -f "$state_dir/route-switched"
candidate_removed=not_applicable
if [[ $candidate_container != "$old_container" ]] && docker inspect "$candidate_container" >/dev/null 2>&1; then
  docker stop -t 30 "$candidate_container" >/dev/null
  docker rm "$candidate_container" >/dev/null
  candidate_removed=true
fi
printf 'route_rollback=true\n'
printf 'nginx_reload=pass\n'
printf 'active_container=%s\n' "$old_container"
printf 'active_port=%s\n' "$old_port"
printf 'candidate_removed=%s\n' "$candidate_removed"
