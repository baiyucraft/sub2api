#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
domain=${PUBLIC_DOMAIN:?PUBLIC_DOMAIN is required}
direct_ip=${DIRECT_IP:?DIRECT_IP is required}
cd "$deploy_dir"
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
env_tmp="$deploy_dir/.env.release.$$"
awk '!/^UPSTREAM_SYNC_AUTO_ENABLED=/' "$deploy_dir/.env" > "$env_tmp"
printf 'UPSTREAM_SYNC_AUTO_ENABLED=true\n' >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
docker compose up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(docker compose config --format json | jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED') == true ]]
[[ $(curl -sS --resolve "$domain:443:$direct_ip" --max-time 15 -o /dev/null -w '%{http_code}' "https://$domain/health") == 200 ]]
critical=$(docker logs --since 5m sub2api 2>&1 | grep -Eic 'panic|fatal|migration.*(failed|error)|database.*(failed|error)|redis.*(failed|error)' || true)
[[ $critical == 0 ]]
printf 'auto_sync_enabled=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'final_health=pass\n'
printf 'final_logs=pass\n'
