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
printf '%s\n' "$pre_image_id" > "$state_dir/pre-image-id"
printf '%s\n' "$pre_image_ref" > "$state_dir/pre-image-ref"
docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations ORDER BY filename" > "$state_dir/pre-migrations.tsv"
(cd "$state_dir" && sha256sum docker-compose.yml .env pre-image-id pre-image-ref pre-migrations.tsv > SHA256SUMS)
systemctl stop nginx
docker compose stop -t 30 sub2api >/dev/null 2>&1
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
write_tx=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND state<>'idle' AND backend_xid IS NOT NULL")
[[ $write_tx == 0 ]]
printf 'writes_frozen=true\n'
printf 'state_dir=%s\n' "$state_dir"
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'compose_sha256=%s\n' "$compose_sha"
