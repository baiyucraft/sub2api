#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
drain_timeout=${DRAIN_TIMEOUT_SECONDS:-3600}
finalize_phase=preflight
failure_line=0
final_headers=
rollback_upstream=
public_headers=
finalize_cleanup() {
  local code=$?
  set +e
  rm -f ${final_headers:+"$final_headers"} ${rollback_upstream:+"$rollback_upstream"} ${public_headers:+"$public_headers"}
  if [[ $code -ne 0 ]]; then
    local failure_tmp="$state_dir/finalize-failure.tmp.$$"
    printf 'finalize_failure_phase=%s\nfinalize_failure_line=%s\n' "$finalize_phase" "$failure_line" > "$failure_tmp"
    chmod 600 "$failure_tmp"
    mv -T -- "$failure_tmp" "$state_dir/finalize-failure"
  fi
  exit "$code"
}
trap 'failure_line=$LINENO' ERR
trap finalize_cleanup EXIT
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
[[ $(docker inspect -f '{{.Image}}' "$candidate_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$candidate_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
grep -Fq "server 127.0.0.1:$candidate_port;" "$managed_upstream"
[[ -f $state_dir/pre-active-app && ! -L $state_dir/pre-active-app ]]
old_container=$(sed -n 's/^container=//p' "$state_dir/pre-active-app")
old_port=$(sed -n 's/^port=//p' "$state_dir/pre-active-app")
old_instance_id=$(sed -n 's/^instance_id=//p' "$state_dir/pre-active-app")
[[ $old_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $old_port == 18080 || $old_port == 18081 ]]

# Phase 1: traffic already targets the temporary candidate.  Wait for the old
# active slot to finish every established HTTP/SSE/WebSocket connection before
# replacing its Compose-managed slot.
finalize_phase=old_slot_drain
old_connections=$(wait_for_application_drain "$old_container" "$drain_timeout")
old_drain_status=drained
[[ $old_connections == 0 ]] || old_drain_status=$([[ $old_connections == unknown ]] && printf unknown || printf timeout)
if [[ $old_drain_status != drained ]]; then
  printf 'old_container=%s\nold_port=%s\ndrain_status=%s\ndrain_connections=%s\n' \
    "$old_container" "$old_port" "$([[ $old_connections == unknown ]] && printf unknown || printf timeout)" "$old_connections"
  exit 1
fi
finalize_phase=old_slot_remove
if [[ $old_container != "$candidate_container" ]]; then
  docker stop -t 30 "$old_container" >/dev/null
  docker rm "$old_container" >/dev/null
fi

# Persist the exact final Compose closure.  The standard service is rebuilt on
# the now-free old port and waits on the shared activation marker.
finalize_phase=compose_prepare
active_override_tmp="$deploy_dir/docker-compose.release-active.yml.tmp.$$"
cat > "$active_override_tmp" <<EOF
services:
  sub2api:
    image: $candidate_image_id
    container_name: sub2api
    environment:
      SUB2API_INSTANCE_ID: $final_instance_id
      SUB2API_BACKGROUND_ACTIVATION_FILE: /app/data/.sub2api-active-instance
EOF
chmod 600 "$active_override_tmp"
mv -T -- "$active_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
env_tmp="$deploy_dir/.env.active.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|BIND_HOST|SERVER_PORT)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\nBIND_HOST=127.0.0.1\nSERVER_PORT=%s\n' "$candidate_image_id" "$old_port" >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
load_release_compose_files "$deploy_dir"
compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
compose_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$candidate_image_id" ]]
jq -e --arg port "$old_port" --arg instance "$final_instance_id" '
  .services.sub2api.container_name == "sub2api" and
  .services.sub2api.environment.SUB2API_INSTANCE_ID == $instance and
  .services.sub2api.environment.SUB2API_BACKGROUND_ACTIVATION_FILE == "/app/data/.sub2api-active-instance" and
  ((.services.sub2api.ports // []) | any(.target == 8080 and (.published | tostring) == $port and .host_ip == "127.0.0.1"))
' <<<"$compose_json" >/dev/null
finalize_phase=final_container_start
docker compose "${release_compose_args[@]}" up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
final_headers=$(mktemp /tmp/sub2api-final-health.XXXXXX)
rollback_upstream=$(mktemp /tmp/sub2api-final-upstream.XXXXXX)
finalize_phase=final_instance_readiness
final_instance_ready=false
for _ in $(seq 1 30); do
  : > "$final_headers"
  if [[ $(curl -sS -D "$final_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${old_port}/health" 2>/dev/null || true) == 200 ]] &&
     assert_http_header_equals "$final_headers" X-Sub2API-Instance "$final_instance_id" &&
     (assert_http_header_equals "$final_headers" X-Sub2API-Background-Ready false ||
      assert_http_header_equals "$final_headers" X-Sub2API-Background-Ready true); then
    final_instance_ready=true
    break
  fi
  sleep 1
done
[[ $final_instance_ready == true ]]

# Activate and prove that all process-wide background services accepted the
# marker before routing traffic to the final Compose-managed instance.
finalize_phase=background_activation
activation_host_dir=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "$candidate_container")
[[ -n $activation_host_dir && -d $activation_host_dir && ! -L $activation_host_dir ]]
printf '%s\n' "$final_instance_id" > "$activation_host_dir/.sub2api-active-instance.tmp"
chmod 600 "$activation_host_dir/.sub2api-active-instance.tmp"
mv -T -- "$activation_host_dir/.sub2api-active-instance.tmp" "$activation_host_dir/.sub2api-active-instance"
background_ready=false
for _ in $(seq 1 120); do
  : > "$final_headers"
  if [[ $(curl -sS -D "$final_headers" -o /dev/null -w '%{http_code}' "http://127.0.0.1:${old_port}/health" 2>/dev/null || true) == 200 ]] &&
     assert_http_header_equals "$final_headers" X-Sub2API-Instance "$final_instance_id" &&
     assert_http_header_equals "$final_headers" X-Sub2API-Background-Ready true; then
    background_ready=true
    break
  fi
  sleep 1
done
[[ $background_ready == true ]]

# Phase 2: atomically route new requests to the final instance.  Keep the
# temporary candidate alive until its own accepted requests drain naturally.
finalize_phase=final_route
install -m 600 "$managed_upstream" "$rollback_upstream"
upstream_tmp="$managed_upstream.tmp.$$"
printf 'upstream sub2api_release_backend {\n    server 127.0.0.1:%s;\n    keepalive 128;\n}\n' "$old_port" > "$upstream_tmp"
chmod 600 "$upstream_tmp"
mv -T -- "$upstream_tmp" "$managed_upstream"
nginx -t >/dev/null 2>&1
systemctl reload nginx >/dev/null 2>&1
public_headers=$(mktemp /tmp/sub2api-final-public.XXXXXX)
public_verified=false
for _ in $(seq 1 30); do
  : > "$public_headers"
  if [[ $(curl -sS --resolve "$domain:443:$direct_ip" -D "$public_headers" -o /dev/null -w '%{http_code}' -H 'Connection: close' "https://$domain/health" 2>/dev/null || true) == 200 ]] && assert_http_header_equals "$public_headers" X-Sub2API-Instance "$final_instance_id"; then
    public_verified=true
    break
  fi
  sleep 1
done
if [[ $public_verified != true ]]; then
  upstream_restore_tmp="$managed_upstream.restore.$$"
  install -m 600 "$rollback_upstream" "$upstream_restore_tmp"
  mv -T -- "$upstream_restore_tmp" "$managed_upstream"
  nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1
  rm -f "$public_headers"
  exit 1
fi
rm -f "$public_headers"
public_headers=
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s\n' \
  "$old_port" "$candidate_image_id" "$release_id" "$final_instance_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
printf 'phase=final\nroute_port=%s\nprevious_port=%s\n' "$old_port" "$candidate_port" > "$state_dir/route-switched.tmp"
chmod 600 "$state_dir/route-switched.tmp"
mv -T -- "$state_dir/route-switched.tmp" "$state_dir/route-switched"

finalize_phase=candidate_drain
candidate_connections=$(wait_for_application_drain "$candidate_container" "$drain_timeout")
candidate_drain_status=drained
[[ $candidate_connections == 0 ]] || candidate_drain_status=$([[ $candidate_connections == unknown ]] && printf unknown || printf timeout)
if [[ $candidate_drain_status != drained ]]; then
  printf 'old_container=%s\nold_port=%s\ndrain_status=candidate_%s\ndrain_connections=%s\n' \
    "$old_container" "$old_port" "$([[ $candidate_connections == unknown ]] && printf unknown || printf timeout)" "$candidate_connections"
  exit 1
fi
finalize_phase=final_log_gate
critical=$(docker logs --since 5m sub2api 2>&1 | grep -Eic 'panic|fatal|migration.*(failed|error)|database.*(failed|error)|redis.*(failed|error)' || true)
[[ $critical == 0 ]]
assert_prompt_audit_disabled
finalize_phase=completed
rm -f "$state_dir/finalize-failure"
printf 'auto_sync_enabled=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'final_health=pass\nfinal_logs=pass\nbackground_activation=pass\ncompose_managed=pass\n'
printf 'old_container=%s\nold_port=%s\ndrain_status=drained\ndrain_connections=%s\n' "$old_container" "$old_port" "$old_connections"
printf 'candidate_drain_connections=%s\n' "$candidate_connections"
