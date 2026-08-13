#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
drain_timeout=${DRAIN_TIMEOUT_SECONDS:-3600}
[[ -f $state_dir/nginx-upstream.conf && ! -L $state_dir/nginx-upstream.conf ]]
old_state="$state_dir/pre-active-app"
[[ -f $old_state && ! -L $old_state ]]
old_container=$(sed -n 's/^container=//p' "$old_state")
old_port=$(sed -n 's/^port=//p' "$old_state")
old_instance_id=$(sed -n 's/^instance_id=//p' "$old_state")
[[ $old_container =~ ^[A-Za-z0-9_.-]{1,100}$ ]]
[[ $old_port =~ ^(18080|18081)$ ]]
route_to_port() {
  local port=${1:?port is required}
  local tmp="$managed_upstream.rollback.$$"
  local previous="$managed_upstream.previous.$$"
  local restore="$managed_upstream.restore.$$"
  install -m 600 "$managed_upstream" "$previous"
  printf 'upstream sub2api_release_backend {\n    server 127.0.0.1:%s;\n    keepalive 128;\n}\n' "$port" > "$tmp"
  chmod 600 "$tmp"
  mv -T -- "$tmp" "$managed_upstream"
  if ! nginx -t >/dev/null 2>&1 || ! systemctl reload nginx >/dev/null 2>&1; then
    install -m 600 "$previous" "$restore"
    mv -T -- "$restore" "$managed_upstream"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    rm -f "$previous" "$tmp" "$restore"
    return 1
  fi
  rm -f "$previous" "$tmp" "$restore"
}
wait_healthy() {
  local container=${1:?container is required}
  for _ in $(seq 1 90); do
    [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true) == healthy ]] && return 0
    sleep 2
  done
  return 1
}

# If the final Compose instance already occupies the old port, first send new
# traffic back to the still-running temporary candidate, then drain the final
# instance before freeing the port for the saved old Compose definition.  The
# container name alone is insufficient because both generations use sub2api.
pre_image_id=$(<"$state_dir/pre-image-id")
current_sub2api_image=$(docker inspect -f '{{.Image}}' sub2api 2>/dev/null || true)
if [[ -n $current_sub2api_image && $current_sub2api_image != "$pre_image_id" ]]; then
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$candidate_container" 2>/dev/null || true) == healthy ]]
  route_to_port "$candidate_port"
  final_connections=$(wait_for_application_drain sub2api "$drain_timeout")
  [[ $final_connections == 0 ]]
  docker stop -t 30 sub2api >/dev/null
  docker rm sub2api >/dev/null
fi
if [[ -n $old_instance_id ]]; then
  marker_source=$candidate_container
  docker inspect "$marker_source" >/dev/null 2>&1 || marker_source=$old_container
  activation_host_dir=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$marker_source")
  [[ -n $activation_host_dir && -d $activation_host_dir && ! -L $activation_host_dir ]]
  printf '%s\n' "$old_instance_id" > "$activation_host_dir/.sub2api-active-instance.tmp"
  chmod 600 "$activation_host_dir/.sub2api-active-instance.tmp"
  mv -T -- "$activation_host_dir/.sub2api-active-instance.tmp" "$activation_host_dir/.sub2api-active-instance"
fi
old_container_image=$(docker inspect -f '{{.Image}}' "$old_container" 2>/dev/null || true)
if [[ $old_container_image != "$pre_image_id" ]]; then
  [[ -z $old_container_image ]] || { docker stop -t 30 "$old_container" >/dev/null 2>&1 || true; docker rm "$old_container" >/dev/null; }
  deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
  load_release_compose_files "$state_dir"
  install -m 600 "$state_dir/.env" "$deploy_dir/.env"
  for compose_file in "${release_compose_files[@]}"; do
    install -m 600 "$state_dir/$compose_file" "$deploy_dir/$compose_file"
  done
  if [[ " ${release_compose_files[*]} " != *" docker-compose.release-active.yml "* ]]; then
    [[ -f $state_dir/no-release-active-override && ! -L $state_dir/no-release-active-override ]]
    rm -f "$deploy_dir/docker-compose.release-active.yml"
  fi
  cd "$deploy_dir"
  load_release_compose_files "$deploy_dir"
  docker compose "${release_compose_args[@]}" up -d --no-deps sub2api >/dev/null 2>&1
  old_container=sub2api
fi
if [[ $(docker inspect -f '{{.State.Status}}' "$old_container" 2>/dev/null || true) != running ]]; then
  docker start "$old_container" >/dev/null
fi
wait_healthy "$old_container"
[[ $(docker inspect -f '{{.Image}}' "$old_container") == "$pre_image_id" ]]
route_to_port "$old_port"
headers=$(mktemp /tmp/sub2api-route-rollback.XXXXXX)
trap 'rm -f "$headers"' EXIT
[[ $(curl -sS --resolve "$domain:443:$direct_ip" -D "$headers" -o /dev/null -w '%{http_code}' -H 'Connection: close' "https://$domain/health") == 200 ]]
if [[ -n $old_instance_id ]]; then
  assert_http_header_equals "$headers" X-Sub2API-Instance "$old_instance_id"
fi
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=%s\nport=%s\nimage_id=%s\ninstance_id=%s\n' "$old_container" "$old_port" "$pre_image_id" "$old_instance_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
rm -f "$state_dir/route-switch-intent" "$state_dir/route-switched"
candidate_preserved=false
if docker inspect "$candidate_container" >/dev/null 2>&1; then
  candidate_preserved=true
fi
printf 'route_rollback=true\n'
printf 'nginx_reload=pass\n'
printf 'active_container=%s\n' "$old_container"
printf 'active_port=%s\n' "$old_port"
printf 'candidate_preserved=%s\n' "$candidate_preserved"
