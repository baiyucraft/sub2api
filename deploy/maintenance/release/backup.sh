#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
backup_root=${BACKUP_ROOT:-$deploy_dir/backups/automated}
recipient_file=${AGE_RECIPIENT_FILE:-/root/.config/sub2api-backup/age-recipient.txt}
upload_key=${BACKUP_UPLOAD_KEY:-/root/.ssh/sub2api_backup_upload}
upload_target=${BACKUP_UPLOAD_TARGET:-sub2api-backup@47.85.205.94}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
work="$backup_root/.release-$release_id-$timestamp"
plain="$backup_root/sub2api-$release_id-$timestamp.tar"
encrypted="$plain.age"
transport="sub2api-$timestamp.tar.age"
redis_stopped=false

cleanup() {
  code=$?
  if [[ $redis_stopped == true ]]; then (cd "$deploy_dir" && docker compose start redis >/dev/null) || true; fi
  rm -rf "$work" "$plain"
  exit "$code"
}
trap cleanup EXIT
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
cd "$deploy_dir"
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-active sub2api-backup.timer 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.service 2>/dev/null || true) == masked ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == masked ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$(<"$state_dir/pre-image-id")" ]]
compose_image=$(docker compose config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$(<"$state_dir/pre-image-id")" ]]
if [[ ${RELEASE_LOCK_HELD:-false} != true ]]; then
  exec 9>/run/lock/sub2api-backup-global.lock
  flock -n 9
fi
install -d -m 700 "$work/database" "$work/redis" "$work/config/app" "$work/config/nginx" "$work/config/certbot" "$work/metadata"
docker exec sub2api-postgres pg_dump -U sub2api -d sub2api -Fc -Z 6 > "$work/database/sub2api.dump"
docker exec sub2api-postgres pg_dumpall -U sub2api --globals-only > "$work/database/globals.sql"
[[ -s $work/database/sub2api.dump ]]
redis_command() {
  docker exec sub2api-redis sh -lc 'export REDISCLI_AUTH="${REDIS_PASSWORD:-}"; exec redis-cli --no-auth-warning "$@"' sh "$@"
}
redis_command BGSAVE >/dev/null || true
for _ in $(seq 1 120); do
  persistence=$(redis_command INFO persistence | tr -d '\r')
  in_progress=$(awk -F: '$1=="rdb_bgsave_in_progress"{print $2}' <<<"$persistence")
  last_status=$(awk -F: '$1=="rdb_last_bgsave_status"{print $2}' <<<"$persistence")
  [[ $in_progress == 0 ]] && break
  sleep 1
done
[[ $in_progress == 0 && $last_status == ok ]]
redis_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis)
[[ -n $redis_source && -d $redis_source ]]
docker compose stop -t 30 redis >/dev/null
redis_stopped=true
cp -a "$redis_source/." "$work/redis/"
docker compose start redis >/dev/null
redis_stopped=false
for _ in $(seq 1 60); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]] && break
  sleep 1
done
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
install -m 600 "$deploy_dir/.env" "$work/config/app/.env"
install -m 600 "$deploy_dir/docker-compose.yml" "$work/config/app/docker-compose.yml"
cp -a "$deploy_dir/data" "$work/config/app/data"
(cd "$work/config/app/data" && find . -type f -print0 | sort -z | xargs -0 sha256sum > "$work/metadata/data.sha256")
nginx -T > "$work/config/nginx/nginx-T.txt" 2>&1
cp -a /etc/nginx/nginx.conf /etc/nginx/sites-enabled "$work/config/nginx/"
cp -aL /etc/letsencrypt/live "$work/config/certbot/"
cp -a /etc/letsencrypt/archive /etc/letsencrypt/renewal "$work/config/certbot/"
docker inspect sub2api sub2api-postgres sub2api-redis --format '{{.Name}} {{.Config.Image}} {{.Image}}' > "$work/metadata/images.txt"
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$work/metadata/postgres.txt"
redis_command INFO server persistence keyspace > "$work/metadata/redis.txt"
redis_command DBSIZE | tr -d '\r' > "$work/metadata/redis-dbsize.txt"
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||count(*) FROM accounts UNION ALL SELECT 'users='||count(*) FROM users UNION ALL SELECT 'api_keys='||count(*) FROM api_keys UNION ALL SELECT 'upstream_configs='||count(*) FROM upstream_configs UNION ALL SELECT 'upstream_keys='||count(*) FROM upstream_keys" > "$work/metadata/core-counts.txt"
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM accounts t UNION ALL SELECT 'users='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM users t UNION ALL SELECT 'api_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM api_keys t UNION ALL SELECT 'upstream_configs='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_configs t UNION ALL SELECT 'upstream_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_keys t" > "$work/metadata/core-content-digests.txt"
(cd "$work/redis" && find . -type f -print0 | sort -z | xargs -0 sha256sum > "$work/metadata/redis-files.sha256")
printf 'release_id=%s\ncandidate_image_id=%s\npre_switch_image_id=%s\nwrites_frozen=true\n' "$release_id" "$candidate_image_id" "$(<"$state_dir/pre-image-id")" > "$work/metadata/manifest.txt"
(cd "$work" && find . -type f -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)
tar -C "$work" -cf "$plain" .
age -R "$recipient_file" -o "$encrypted" "$plain"
artifact_sha=$(sha256sum "$encrypted" | awk '{print $1}')
remote_result=$(ssh -i "$upload_key" -o BatchMode=yes -o StrictHostKeyChecking=yes "$upload_target" "upload daily $transport $artifact_sha" < "$encrypted")
grep -Fq "OK $transport $artifact_sha" <<<"$remote_result"
[[ $(docker inspect -f '{{.State.Status}}' sub2api) != running ]]
cp -a "$encrypted" "$state_dir/recovery-point.age"
printf '%s  recovery-point.age\n' "$artifact_sha" > "$state_dir/recovery-point.age.sha256"
printf 'artifact=%s\n' "$(basename "$encrypted")"
printf 'transport_artifact=%s\n' "$transport"
printf 'artifact_size=%s\n' "$(stat -c %s "$encrypted")"
printf 'artifact_sha256=%s\n' "$artifact_sha"
printf 'writes_frozen=true\n'
printf 'no_restart_path_proven=true\n'
trap - EXIT
rm -rf "$work" "$plain"
