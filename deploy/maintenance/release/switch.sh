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
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 ]]; then
  [[ -f $state_dir/migration-195-plan.sha256 && ! -L $state_dir/migration-195-plan.sha256 ]]
  migration_status=$(<"$state_dir/migration-195-status")
  export MIGRATION_STATUS="$migration_status"
fi
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
if [[ $profile == 198 || $profile == 199 ]]; then
  managed_monitor_key_name_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT character_maximum_length, (SELECT COUNT(*) FROM api_keys k JOIN channel_monitors m ON m.id=k.managed_monitor_id AND m.managed_api_key_id=k.id WHERE k.purpose='managed_monitor' AND k.deleted_at IS NULL AND k.name IS DISTINCT FROM '监控-' || BTRIM(m.name)) FROM information_schema.columns WHERE table_schema='public' AND table_name='api_keys' AND column_name='name'")
  [[ $managed_monitor_key_name_state == '103|0' ]]
fi
if [[ $profile == 199 ]]; then
  reasoning_effort_policy_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT COALESCE(MAX(CASE WHEN column_name='max_reasoning_effort' THEN data_type || ':' || is_nullable || ':' || column_default END),''), COALESCE(MAX(CASE WHEN column_name='reasoning_effort_mappings' THEN data_type || ':' || is_nullable || ':' || column_default END),'') FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name IN ('max_reasoning_effort','reasoning_effort_mappings')")
  [[ $reasoning_effort_policy_state == *'character varying:NO:'*"''::character varying"*'|'*'jsonb:NO:'*"'[]'::jsonb"* ]]
fi
[[ $(docker inspect -f '{{.Image}}' "$migration_container") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.ExitCode}}' "$migration_container") == 0 ]]
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 ]]; then
  migration_checksum=$(jq -er '.manifest.migration_sha256["195_upstream_scheduling_monitor_rates.sql"]' "$active_claim/gate.json")
  migration_manifest_sha256=$(jq -cS '.manifest.migration_sha256' "$active_claim/gate.json" | sha256sum | awk '{print $1}')
  marker_tmp="$state_dir/.migration-committed.tmp.$$"
  printf 'plan_sha256=%s\nmigration_manifest_sha256=%s\n' "$(<"$state_dir/migration-195-plan.sha256")" "$migration_manifest_sha256" > "$marker_tmp"
  while IFS=$'\t' read -r migration migration_checksum; do
    printf 'migration=%s checksum=%s\n' "$migration" "$migration_checksum" >> "$marker_tmp"
  done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
  chmod 600 "$marker_tmp"
  [[ ! -e $state_dir/migration-committed && ! -L $state_dir/migration-committed ]]
  mv -T -- "$marker_tmp" "$state_dir/migration-committed"
  "$assets_dir/migration-195-assert.sh" postflight_db
fi
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
assert_prompt_audit_disabled
if [[ $profile == 195 || $profile == 197 || $profile == 198 || $profile == 199 ]]; then
  "$assets_dir/migration-195-assert.sh" postflight_runtime
fi
printf 'migration_verified=true\n'
printf 'running_image_id=%s\n' "$candidate_image_id"
printf 'internal_health=pass\n'
printf 'public_traffic_enabled=false\n'
if [[ $profile == 198 || $profile == 199 ]]; then
  printf 'managed_monitor_key_names_verified=true\n'
fi
if [[ $profile == 199 ]]; then
  printf 'reasoning_effort_policy_verified=true\n'
fi
