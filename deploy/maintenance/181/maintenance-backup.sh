#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
backup_root=${BACKUP_ROOT:-$deploy_dir/backups/automated}
recipient_file=${AGE_RECIPIENT_FILE:-/root/.config/sub2api-backup/age-recipient.txt}
upload_key=${BACKUP_UPLOAD_KEY:-/root/.ssh/sub2api_backup_upload}
upload_target=${BACKUP_UPLOAD_TARGET:-sub2api-backup@47.85.205.94}
release_commit=${RELEASE_COMMIT:?RELEASE_COMMIT is required}
release_version=${RELEASE_VERSION:?RELEASE_VERSION is required}
candidate_image_id=${CANDIDATE_IMAGE_ID:?CANDIDATE_IMAGE_ID is required}
pre_switch_image_id=${PRE_SWITCH_IMAGE_ID:?PRE_SWITCH_IMAGE_ID is required}
compose_sha256=${COMPOSE_SHA256:?COMPOSE_SHA256 is required}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
work_dir="$backup_root/.release181-$timestamp"
plain_archive="$backup_root/sub2api-release181-$timestamp.tar"
encrypted_archive="$plain_archive.age"
transport_name="sub2api-$timestamp.tar.age"
redis_stopped=false

cleanup() {
  local exit_code=$?
  if [[ $redis_stopped == true ]]; then
    (cd "$deploy_dir" && docker compose start redis) || true
  fi
  rm -rf "$work_dir" "$plain_archive"
  exit "$exit_code"
}
trap cleanup EXIT

cd "$deploy_dir"
[[ $(docker inspect -f '{{.State.Status}}' sub2api 2>/dev/null || true) != running ]]
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$pre_switch_image_id" ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
[[ $(sha256sum "$deploy_dir/docker-compose.yml" | awk '{print $1}') == "$compose_sha256" ]]
compose_json=$(docker compose config --format json)
rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ -n $rendered_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$pre_switch_image_id" ]]
jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
jq -e '(.services.sub2api.network_mode == "host" and .services.sub2api.environment.SERVER_HOST == "127.0.0.1" and .services.sub2api.environment.SERVER_PORT == "18080") or ((.services.sub2api.ports // []) | any(.target == 8080 and (.published | tostring) == "18080" and .host_ip == "127.0.0.1"))' <<<"$compose_json" >/dev/null
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-active sub2api-backup.timer 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.service 2>/dev/null || true) == masked* ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == masked* ]]
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
redis_data_dir=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis)
[[ -n $redis_data_dir && -d $redis_data_dir ]]
migration_181_count=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM schema_migrations WHERE filename = '181_upstream_key_platform_detection.sql'")
[[ $migration_181_count == 0 ]]
platform_nullable=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT is_nullable FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_keys' AND column_name='platform'")
[[ $platform_nullable == NO ]]
detection_column_count=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_keys' AND column_name IN ('platform_source','detected_platform','platform_detection_status','platform_detected_at')")
[[ $detection_column_count == 0 ]]
install -d -m 700 "$backup_root" "$work_dir/database" "$work_dir/redis" \
  "$work_dir/config/app" "$work_dir/config/nginx" "$work_dir/config/certbot" \
  "$work_dir/metadata" "$work_dir/row-snapshot"

expected_files=(.installed config.yaml model_pricing.json model_pricing.sha256)
mapfile -t data_files < <(find "$deploy_dir/data" -maxdepth 1 -type f -printf '%f\n')
(( ${#data_files[@]} == ${#expected_files[@]} ))
for data_file in "${data_files[@]}"; do
  case "$data_file" in
    .installed|config.yaml|model_pricing.json|model_pricing.sha256) ;;
    *) exit 11 ;;
  esac
done

docker exec sub2api-postgres pg_dump -U sub2api -d sub2api -Fc -Z 6 > "$work_dir/database/sub2api.dump"
docker exec sub2api-postgres pg_dumpall -U sub2api --globals-only > "$work_dir/database/globals.sql"
test -s "$work_dir/database/sub2api.dump"

# These plaintext row snapshots exist only inside the temporary work directory
# and are removed immediately after the encrypted artifact is verified remotely.
docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d sub2api -c "\copy (
  SELECT * FROM upstream_configs
  WHERE deleted_at IS NULL
    AND lower(regexp_replace(site_url, '/+$', '')) IN ('https://api.sunai.lol', 'https://www.codexapis.com')
) TO STDOUT WITH (FORMAT csv, HEADER true)" > "$work_dir/row-snapshot/upstream-configs.csv"
docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d sub2api -c "\copy (
  SELECT k.* FROM upstream_keys k
  JOIN upstream_configs c ON c.id = k.upstream_config_id
  WHERE k.deleted_at IS NULL AND c.deleted_at IS NULL
    AND lower(regexp_replace(c.site_url, '/+$', '')) IN ('https://api.sunai.lol', 'https://www.codexapis.com')
) TO STDOUT WITH (FORMAT csv, HEADER true)" > "$work_dir/row-snapshot/upstream-keys.csv"
docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d sub2api -c "\copy (
  SELECT a.* FROM accounts a
  JOIN upstream_keys k ON k.id = a.upstream_key_id
  JOIN upstream_configs c ON c.id = k.upstream_config_id
  WHERE a.deleted_at IS NULL AND k.deleted_at IS NULL AND c.deleted_at IS NULL
    AND lower(regexp_replace(c.site_url, '/+$', '')) IN ('https://api.sunai.lol', 'https://www.codexapis.com')
) TO STDOUT WITH (FORMAT csv, HEADER true)" > "$work_dir/row-snapshot/accounts.csv"

redis_password=$(awk -F= '$1=="REDIS_PASSWORD"{print substr($0,index($0,"=")+1)}' "$deploy_dir/.env")
redis_cli=(docker exec)
if [[ -n $redis_password ]]; then
  redis_cli+=(-e "REDISCLI_AUTH=$redis_password")
fi
redis_cli+=(sub2api-redis redis-cli --no-auth-warning)
"${redis_cli[@]}" BGSAVE >/dev/null || {
  [[ $("${redis_cli[@]}" INFO persistence | tr -d '\r' | awk -F: '$1=="rdb_bgsave_in_progress"{print $2}') == 1 ]]
}
for _ in {1..120}; do
  persistence=$("${redis_cli[@]}" INFO persistence | tr -d '\r')
  in_progress=$(awk -F: '$1=="rdb_bgsave_in_progress"{print $2}' <<<"$persistence")
  last_status=$(awk -F: '$1=="rdb_last_bgsave_status"{print $2}' <<<"$persistence")
  [[ $in_progress == 0 ]] && break
  sleep 1
done
[[ $in_progress == 0 && $last_status == ok ]]
{
  printf 'refresh_token='
  "${redis_cli[@]}" --scan --pattern 'refresh_token:*' | wc -l
  printf 'token_family='
  "${redis_cli[@]}" --scan --pattern 'token_family:*' | wc -l
} > "$work_dir/metadata/redis-critical-counts.txt"

docker compose stop -t 30 redis
redis_stopped=true
cp -a "$redis_data_dir/dump.rdb" "$work_dir/redis/"
if [[ -d $redis_data_dir/appendonlydir ]]; then
  cp -a "$redis_data_dir/appendonlydir" "$work_dir/redis/"
fi
docker compose start redis
redis_stopped=false
for _ in {1..60}; do
  [[ $(docker inspect sub2api-redis --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}') == healthy ]] && break
  sleep 1
done
[[ $(docker inspect sub2api-redis --format '{{.State.Health.Status}}') == healthy ]]

install -m 600 "$deploy_dir/.env" "$work_dir/config/app/.env"
install -m 600 "$deploy_dir/docker-compose.yml" "$work_dir/config/app/docker-compose.yml"
for file in "${expected_files[@]}"; do
  install -m 600 "$deploy_dir/data/$file" "$work_dir/config/app/$file"
done
nginx -T > "$work_dir/config/nginx/nginx-T.txt" 2>&1
cp -a /etc/nginx/nginx.conf /etc/nginx/sites-enabled "$work_dir/config/nginx/"
cp -aL /etc/letsencrypt/live "$work_dir/config/certbot/"
cp -a /etc/letsencrypt/archive /etc/letsencrypt/renewal "$work_dir/config/certbot/"

docker inspect sub2api sub2api-postgres sub2api-redis --format '{{.Name}} {{.Config.Image}} {{.Image}}' > "$work_dir/metadata/images.txt"
docker exec sub2api-postgres psql -U sub2api -d sub2api -Atqc "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename;" > "$work_dir/metadata/postgres.txt"
"${redis_cli[@]}" INFO server persistence keyspace > "$work_dir/metadata/redis.txt"
docker exec sub2api-postgres psql -U sub2api -d sub2api -Atqc "SELECT 'accounts='||count(*) FROM accounts; SELECT 'upstream_configs='||count(*) FROM upstream_configs; SELECT 'upstream_keys='||count(*) FROM upstream_keys; SELECT 'users='||count(*) FROM users; SELECT 'api_keys='||count(*) FROM api_keys;" > "$work_dir/metadata/core-counts.txt"
printf 'restore_point_utc=%s\nrelease=181\napplication_commit=%s\napplication_version=%s\ncandidate_image_id=%s\npre_switch_image_id=%s\ncompose_sha256=%s\npre_migration_181_absent=true\nwrites_frozen=true\n' \
  "$timestamp" "$release_commit" "$release_version" "$candidate_image_id" \
  "$pre_switch_image_id" "$compose_sha256" > "$work_dir/metadata/manifest.txt"
(cd "$work_dir" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)

tar -C "$work_dir" -cf "$plain_archive" .
age -R "$recipient_file" -o "$encrypted_archive" "$plain_archive"
archive_sha=$(sha256sum "$encrypted_archive" | awk '{print $1}')
remote_result=$(ssh -i "$upload_key" -o BatchMode=yes -o StrictHostKeyChecking=yes "$upload_target" "upload daily $transport_name $archive_sha" < "$encrypted_archive")
grep -Fq "OK $transport_name $archive_sha" <<<"$remote_result"
[[ $(docker inspect -f '{{.State.Status}}' sub2api 2>/dev/null || true) != running ]]

printf 'artifact=%s\n' "$(basename "$encrypted_archive")"
printf 'transport_artifact=%s\n' "$transport_name"
printf 'artifact_size=%s\n' "$(stat -c %s "$encrypted_archive")"
printf 'artifact_sha256=%s\n' "$archive_sha"
printf 'config_rows=%s\n' "$(( $(wc -l < "$work_dir/row-snapshot/upstream-configs.csv") - 1 ))"
printf 'key_rows=%s\n' "$(( $(wc -l < "$work_dir/row-snapshot/upstream-keys.csv") - 1 ))"
printf 'account_rows=%s\n' "$(( $(wc -l < "$work_dir/row-snapshot/accounts.csv") - 1 ))"
printf 'pre_migration_181_absent=true\n'
printf 'writes_frozen=true\nno_restart_path_proven=true\n'

trap - EXIT
rm -rf "$work_dir" "$plain_archive"
