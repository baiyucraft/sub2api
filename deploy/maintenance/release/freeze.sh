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
pre_image_id=$(docker inspect -f '{{.Image}}' sub2api)
pre_image_ref=$(docker inspect -f '{{.Config.Image}}' sub2api)
compose_sha=$(sha256sum docker-compose.yml | awk '{print $1}')
install -m 600 docker-compose.yml "$state_dir/docker-compose.yml"
install -m 600 .env "$state_dir/.env"
state_files=(docker-compose.yml .env)
if [[ -e docker-compose.release-active.yml || -L docker-compose.release-active.yml ]]; then
  [[ -f docker-compose.release-active.yml && ! -L docker-compose.release-active.yml ]]
  install -m 600 docker-compose.release-active.yml "$state_dir/docker-compose.release-active.yml"
  state_files+=(docker-compose.release-active.yml)
else
  : > "$state_dir/no-release-active-override"
  chmod 600 "$state_dir/no-release-active-override"
  state_files+=(no-release-active-override)
fi
printf '%s\n' "$pre_image_id" > "$state_dir/pre-image-id"
printf '%s\n' "$pre_image_ref" > "$state_dir/pre-image-ref"
state_files+=(pre-image-id pre-image-ref pre-migrations.tsv)
docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations ORDER BY filename" > "$state_dir/pre-migrations.tsv"
(cd "$state_dir" && sha256sum "${state_files[@]}" > SHA256SUMS)
systemctl stop nginx
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
docker compose stop -t 30 sub2api >/dev/null 2>&1
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
outbox_highwater=$(timeout 3s docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COALESCE(MAX(id),0) FROM scheduler_outbox" 2>/dev/null | tr -d '\r') || outbox_highwater=
outbox_watermark=$(printf '%s\n' "$redis_password" | timeout 3s docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning GET sched:v2:outbox:watermark' 2>/dev/null | tr -d '\r') || outbox_watermark=
[[ $outbox_highwater =~ ^[0-9]+$ && $outbox_watermark =~ ^[0-9]+$ && $outbox_watermark -ge $outbox_highwater ]]
write_tx=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state<>'idle' AND backend_xid IS NOT NULL")
[[ $write_tx == 0 ]]
printf 'writes_frozen=true\n'
printf 'outbox_drained=true\n'
printf 'state_dir=%s\n' "$state_dir"
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'compose_sha256=%s\n' "$compose_sha"
