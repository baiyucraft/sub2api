#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
state_root="$deploy_dir/backups/release-state"
install -d -m 700 "$state_root"
[[ -d $state_dir && ! -L $state_dir ]]
[[ -f $state_dir/masked.committed && ! -L $state_dir/masked.committed ]]
if [[ ${RELEASE_LOCK_HELD:-false} != true ]]; then
  exec 9>/run/lock/sub2api-backup-global.lock
  flock -n 9
fi
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
pre_image_id=$(docker inspect -f '{{.Image}}' "$active_container")
pre_image_ref=$(docker inspect -f '{{.Config.Image}}' "$active_container")
compose_sha=$(sha256sum docker-compose.yml | awk '{print $1}')
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
install -m 600 .env "$state_dir/.env"
state_files=(.env)
for compose_file in "${release_compose_files[@]}"; do
  install -m 600 "$deploy_dir/$compose_file" "$state_dir/$compose_file"
  state_files+=("$compose_file")
done
if [[ " ${release_compose_files[*]} " != *" docker-compose.release-active.yml "* ]]; then
  : > "$state_dir/no-release-active-override"
  chmod 600 "$state_dir/no-release-active-override"
  state_files+=(no-release-active-override)
fi
printf '%s\n' "$pre_image_id" > "$state_dir/pre-image-id"
printf '%s\n' "$pre_image_ref" > "$state_dir/pre-image-ref"
printf 'container=%s\nport=%s\n' "$active_container" "$active_port" > "$state_dir/pre-active-app"
state_files+=(pre-image-id pre-image-ref pre-active-app pre-migrations.tsv)
docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations ORDER BY filename" > "$state_dir/pre-migrations.tsv"
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
[[ -f $managed_upstream && ! -L $managed_upstream ]]
install -m 600 "$managed_upstream" "$state_dir/nginx-upstream.conf"
state_files+=(nginx-upstream.conf)
(cd "$state_dir" && sha256sum "${state_files[@]}" > SHA256SUMS)
redis_password=$(docker inspect sub2api-redis | jq -er '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--requirepass")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first) end')
outbox_highwater=
outbox_watermark=
drain_deadline=$((SECONDS + 30))
while (( SECONDS < drain_deadline )); do
  outbox_highwater=$(timeout 3s docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COALESCE(MAX(id),0) FROM scheduler_outbox" 2>/dev/null | tr -d '\r') || outbox_highwater=
  outbox_watermark=$(printf '%s\n' "$redis_password" | timeout 3s docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning GET sched:v2:outbox:watermark' 2>/dev/null | tr -d '\r') || outbox_watermark=
  [[ $outbox_highwater =~ ^[0-9]+$ && $outbox_watermark =~ ^[0-9]+$ && $outbox_watermark -ge $outbox_highwater ]] && break
  sleep 1
done
[[ $outbox_highwater =~ ^[0-9]+$ && $outbox_watermark =~ ^[0-9]+$ && $outbox_watermark -ge $outbox_highwater ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
printf 'traffic_preserved=true\n'
printf 'release_state_captured=true\n'
printf 'outbox_checkpoint=true\n'
printf 'state_dir=%s\n' "$state_dir"
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'compose_sha256=%s\n' "$compose_sha"
