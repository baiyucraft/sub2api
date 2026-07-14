#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-10737418240}
canary_key_file=${CANARY_KEY_FILE:-/root/.config/sub2api-release/canary-api-key}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ ! -e $release_dir/.consumed ]]
[[ -f $canary_key_file && ! -L $canary_key_file && $(stat -c '%a' "$canary_key_file") == 600 ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
cd "$deploy_dir"
[[ -f docker-compose.yml && -f .env ]]
[[ $(docker inspect -f '{{.State.Status}}' sub2api) == running ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-postgres) == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
backup_exec=$(systemctl show sub2api-backup.service -p ExecStart --value)
backup_path=$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$backup_exec" | head -n1)
[[ -f $backup_path && ! -L $backup_path ]]
grep -Fq '/run/lock/sub2api-backup-global.lock' "$backup_path"
migration_status=verified
while IFS=$'\t' read -r migration migration_checksum; do
  migration_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='$migration'")
  if [[ -z $migration_state ]]; then
    migration_status=absent
  else
    [[ $migration_state == "$migration|$migration_checksum" ]]
  fi
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
compose_json=$(docker compose config --format json)
rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ -n $rendered_image ]]
pre_image_id=$(docker inspect -f '{{.Image}}' sub2api)
[[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$pre_image_id" ]]
jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
jq -e '(.services.sub2api.network_mode == "host" and .services.sub2api.environment.SERVER_HOST == "127.0.0.1" and (.services.sub2api.environment.SERVER_PORT | tostring) == "18080") or ((.services.sub2api.ports // []) | any(.target == 8080 and (.published | tostring) == "18080" and .host_ip == "127.0.0.1"))' <<<"$compose_json" >/dev/null
printf 'preflight=pass\n'
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'free_bytes=%s\n' "$free_bytes"
printf 'migration_status=%s\n' "$migration_status"
