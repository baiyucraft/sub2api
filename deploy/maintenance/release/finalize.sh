#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
[[ $(docker inspect -f '{{.Image}}' "$candidate_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$candidate_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(grep -F "server 127.0.0.1:$candidate_port;" /etc/nginx/conf.d/sub2api-release-upstream.conf) ]]
# The candidate remains a separate slot.  Never recreate the active container
# after traffic has switched; that would break existing SSE/WebSocket streams.
docker exec "$candidate_container" sh -lc 'true' >/dev/null
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$candidate_container") == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' "$candidate_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$candidate_container") == healthy ]]
[[ $(curl -sS --resolve "$domain:443:$direct_ip" --max-time 15 -o /dev/null -w '%{http_code}' "https://$domain/health") == 200 ]]
critical=$(docker logs --since 5m "$candidate_container" 2>&1 | grep -Eic 'panic|fatal|migration.*(failed|error)|database.*(failed|error)|redis.*(failed|error)' || true)
[[ $critical == 0 ]]
assert_prompt_audit_disabled
drain_timeout=${DRAIN_TIMEOUT_SECONDS:-3600}
drain_deadline=$((SECONDS + drain_timeout))
[[ -f $state_dir/pre-active-app && ! -L $state_dir/pre-active-app ]]
old_container=$(sed -n 's/^container=//p' "$state_dir/pre-active-app")
old_port=$(sed -n 's/^port=//p' "$state_dir/pre-active-app")
[[ $old_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $old_port == 18080 || $old_port == 18081 ]]
drain_observed=unknown
while [[ $old_container != "$candidate_container" && $old_container != "" ]]; do
  connections=$(docker exec "$old_container" sh -lc 'awk "NR>1 && \$2 ~ /:1F90\$/ && \$4 == \"01\" {count++} END {print count+0}" /proc/net/tcp /proc/net/tcp6' 2>/dev/null || printf 'unknown')
  [[ $connections =~ ^[0-9]+$ ]] && drain_observed=$connections
  [[ $connections == 0 ]] && break
  (( SECONDS >= drain_deadline )) && break
  sleep 2
done
if [[ $old_container != "$candidate_container" && $old_container != "" ]]; then
  if [[ $drain_observed == 0 ]]; then
    docker stop -t 30 "$old_container" >/dev/null
    docker rm "$old_container" >/dev/null
    drain_status=drained
  elif [[ $drain_observed == unknown ]]; then
    drain_status=unknown
  else
    drain_status=timeout
  fi
else
  drain_status=not_applicable
fi
if [[ $drain_status == timeout || $drain_status == unknown ]]; then
  RELEASE_DIR="$release_dir" PUBLIC_DOMAIN="$domain" DIRECT_IP="$direct_ip" \
    "$assets_dir/rollback-route.sh" >/dev/null 2>&1 || true
  exit 1
fi
# Persist the active image only after the old slot has drained.  The running
# candidate is not recreated: this state is for the next release, recovery and
# operator-driven compose actions.
active_override_tmp="$deploy_dir/docker-compose.release-active.yml.tmp.$$"
cat > "$active_override_tmp" <<EOF
services:
  sub2api:
    image: $candidate_image_id
EOF
chmod 600 "$active_override_tmp"
mv -T -- "$active_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
env_tmp="$deploy_dir/.env.active.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\n' "$candidate_image_id" >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
load_release_compose_files "$deploy_dir"
compose_image=$(docker compose "${release_compose_args[@]}" config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$candidate_image_id" ]]
printf '%s\n' "$release_id" > "$deploy_dir/data/.sub2api-active-instance.tmp"
chmod 600 "$deploy_dir/data/.sub2api-active-instance.tmp"
mv -T -- "$deploy_dir/data/.sub2api-active-instance.tmp" "$deploy_dir/data/.sub2api-active-instance"
for _ in $(seq 1 10); do
  [[ $(docker exec "$candidate_container" sh -lc 'cat /app/data/.sub2api-active-instance') == "$release_id" ]] && break
  sleep 1
done
[[ $(docker exec "$candidate_container" sh -lc 'cat /app/data/.sub2api-active-instance') == "$release_id" ]]
printf 'auto_sync_enabled=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'final_health=pass\n'
printf 'final_logs=pass\n'
printf 'old_container=%s\n' "$old_container"
printf 'old_port=%s\n' "$old_port"
printf 'drain_status=%s\n' "$drain_status"
printf 'drain_connections=%s\n' "$drain_observed"
