#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
cd "$deploy_dir"
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
[[ -d $state_dir && ! -L $state_dir ]]
active_override="$deploy_dir/docker-compose.release-active.yml"
override_tmp="$active_override.tmp.$$"
cat > "$override_tmp" <<EOF
services:
  sub2api:
    image: $candidate_image_id
    environment:
      UPSTREAM_SYNC_AUTO_ENABLED: \${UPSTREAM_SYNC_AUTO_ENABLED:-false}
EOF
chmod 600 "$override_tmp"
mv -T -- "$override_tmp" "$active_override"
env_tmp="$deploy_dir/.env.release.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|UPSTREAM_SYNC_AUTO_ENABLED)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=docker-compose.yml:docker-compose.release-active.yml\n' >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\n' "$candidate_image_id" >> "$env_tmp"
printf 'UPSTREAM_SYNC_AUTO_ENABLED=false\n' >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
export BIND_HOST=127.0.0.1
compose_image=$(docker compose config --format json | jq -r '.services.sub2api.image // empty')
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$candidate_image_id" ]]
[[ $(docker compose config --format json | jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED') == false ]]
mapfile -t migrations < <(jq -er '.manifest.migrations[]' "$active_claim/gate.json")
migration_container="sub2api-migrate-$release_id"
[[ -z $(docker ps -aq -f "name=^${migration_container}$") ]]
docker compose run --name "$migration_container" --no-deps sub2api /app/sub2api --migrate-only >/dev/null 2>&1
while IFS=$'\t' read -r migration migration_checksum; do
  recorded=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT checksum FROM schema_migrations WHERE filename='$migration'")
  [[ $recorded == "$migration_checksum" ]]
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
[[ $(docker inspect -f '{{.Image}}' "$migration_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.ExitCode}}' "$migration_container") == 0 ]]
docker rm "$migration_container" >/dev/null
docker compose up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/health) == 200 ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(docker compose config --format json | jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED') == false ]]
printf 'migration_verified=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'internal_health=pass\n'
printf 'public_traffic_enabled=false\n'
