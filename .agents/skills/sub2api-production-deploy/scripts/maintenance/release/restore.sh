#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
(cd "$state_dir" && sha256sum -c recovery-point.age.sha256 >/dev/null)
[[ -f $state_dir/recovery-point.tar && ! -L $state_dir/recovery-point.tar ]]
[[ -f $state_dir/recovery-point.tar.sha256 && ! -L $state_dir/recovery-point.tar.sha256 ]]
(cd "$state_dir" && sha256sum -c recovery-point.tar.sha256 >/dev/null)
recovery="$state_dir/recovery"
rm -rf "$recovery"
install -d -m 700 "$recovery"
cleanup_recovery() { rm -rf "$recovery"; }
fail_closed() {
  code=$?
  local failed=0 app_status nginx_status container_names
  trap - ERR INT TERM EXIT
  set +e
  systemctl stop nginx >/dev/null 2>&1 || failed=1
  docker stop "$active_container" >/dev/null 2>&1 || true
  cleanup_recovery || failed=1
  nginx_status=$(systemctl is-active nginx 2>/dev/null)
  case "$nginx_status" in
    inactive|failed) ;;
    *) failed=1 ;;
  esac
  if ! docker info >/dev/null 2>&1; then
    failed=1
  elif docker inspect "$active_container" >/dev/null 2>&1; then
    app_status=$(docker inspect -f '{{.State.Status}}' "$active_container" 2>/dev/null) || failed=1
    [[ -n $app_status && $app_status != running ]] || failed=1
  else
    if ! container_names=$(docker ps -a --format '{{.Names}}' 2>/dev/null); then
      failed=1
    elif grep -Fxq "$active_container" <<<"$container_names"; then
      failed=1
    fi
  fi
  (( failed == 0 )) || exit 125
  exit "$code"
}
trap fail_closed ERR INT TERM
trap cleanup_recovery EXIT
systemctl stop nginx
if docker inspect "$candidate_container" >/dev/null 2>&1; then
  docker stop -t 30 "$candidate_container" >/dev/null 2>&1 || true
  docker rm "$candidate_container" >/dev/null 2>&1 || true
fi
docker rm -f "$active_container" >/dev/null 2>&1 || true
[[ "$active_container" == sub2api ]] || docker rm -f sub2api >/dev/null 2>&1 || true
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
tar -C "$recovery" -xf "$state_dir/recovery-point.tar"
(cd "$recovery" && sha256sum -c SHA256SUMS >/dev/null)
docker cp "$recovery/redis/dump.rdb" sub2api-redis:/tmp/sub2api-restore.rdb >/dev/null
redis_rdb_check=$(docker exec sub2api-redis redis-check-rdb /tmp/sub2api-restore.rdb)
docker exec sub2api-redis rm -f /tmp/sub2api-restore.rdb
redis_backup_dbsize=$(sed -n 's/^\[info\] \([0-9][0-9]*\) keys read$/\1/p' <<<"$redis_rdb_check")
redis_backup_expiring=$(sed -n 's/^\[info\] \([0-9][0-9]*\) expires$/\1/p' <<<"$redis_rdb_check")
redis_already_expired=$(sed -n 's/^\[info\] \([0-9][0-9]*\) already expired$/\1/p' <<<"$redis_rdb_check")
[[ $redis_backup_dbsize =~ ^[0-9]+$ && $redis_backup_expiring =~ ^[0-9]+$ && $redis_already_expired =~ ^[0-9]+$ ]]
[[ $redis_already_expired -le $redis_backup_expiring && $redis_backup_expiring -le $redis_backup_dbsize ]]
docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='sub2api' AND pid<>pg_backend_pid();" >/dev/null
docker exec sub2api-postgres dropdb --if-exists -U sub2api sub2api
docker exec sub2api-postgres createdb -U sub2api -O sub2api sub2api
docker exec -i sub2api-postgres pg_restore --exit-on-error --no-owner -U sub2api -d sub2api < "$recovery/database/sub2api.dump"
redis_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis)
redis_appendonly=$(docker inspect sub2api-redis | jq -r '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--appendonly")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--appendonly=")) | ltrimstr("--appendonly=") ] | first // "no") end')
docker stop sub2api-redis >/dev/null
find "$redis_source" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a "$recovery/redis/." "$redis_source/"
(cd "$redis_source" && [[ -f dump.rdb && ! -L dump.rdb ]] && [[ $(find . -mindepth 1 -maxdepth 1 -type f | wc -l) == 1 ]])
(cd "$redis_source" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$recovery/metadata/redis-files-restored.sha256"
diff -u "$recovery/metadata/redis-files.sha256" "$recovery/metadata/redis-files-restored.sha256" >/dev/null
if [[ ${redis_appendonly,,} == yes ]]; then
  # Redis 7 prefers multipart AOF over dump.rdb. Seed its base RDB from the
  # verified recovery point so enabling AOF cannot start an empty database.
  redis_data_uid=$(stat -c %u "$redis_source/dump.rdb")
  redis_data_gid=$(stat -c %g "$redis_source/dump.rdb")
  install -d -o "$redis_data_uid" -g "$redis_data_gid" -m 700 "$redis_source/appendonlydir"
  install -o "$redis_data_uid" -g "$redis_data_gid" -m 600 "$redis_source/dump.rdb" "$redis_source/appendonlydir/appendonly.aof.1.base.rdb"
  : > "$redis_source/appendonlydir/appendonly.aof.1.incr.aof"
  printf 'file appendonly.aof.1.base.rdb seq 1 type b\nfile appendonly.aof.1.incr.aof seq 1 type i startoffset 0\n' > "$redis_source/appendonlydir/appendonly.aof.manifest"
  chown "$redis_data_uid:$redis_data_gid" "$redis_source/appendonlydir/appendonly.aof.1.incr.aof" "$redis_source/appendonlydir/appendonly.aof.manifest"
  chmod 600 "$redis_source/appendonlydir/appendonly.aof.1.incr.aof" "$redis_source/appendonlydir/appendonly.aof.manifest"
fi
docker start sub2api-redis >/dev/null
for _ in $(seq 1 60); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
  sleep 1
done
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
redis_password=$(docker inspect sub2api-redis | jq -r '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--requirepass")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first // "") end')
redis_dbsize=$(printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning DBSIZE' | tr -d '\r')
redis_keyspace=$(printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning INFO keyspace' | tr -d '\r')
redis_restored_expiring=$(printf '%s\n' "$redis_keyspace" | sed -n 's/^db[0-9]*:keys=[0-9]*,expires=\([0-9]*\).*/\1/p' | awk '{sum += $1} END {print sum + 0}')
[[ $redis_dbsize =~ ^[0-9]+$ && $redis_backup_dbsize =~ ^[0-9]+$ ]]
[[ $redis_backup_expiring =~ ^[0-9]+$ && $redis_restored_expiring =~ ^[0-9]+$ ]]
[[ $redis_backup_dbsize -ge $redis_dbsize ]]
[[ $redis_backup_expiring -ge $redis_restored_expiring ]]
[[ $((redis_backup_dbsize - redis_dbsize)) -eq $((redis_backup_expiring - redis_restored_expiring)) ]]
[[ $((redis_backup_dbsize - redis_dbsize)) -ge $redis_already_expired ]]
load_release_compose_files "$recovery/config/app"
cp -a "$recovery/config/app/.env" "$deploy_dir/.env"
for compose_file in "${release_compose_files[@]}"; do
  cp -a "$recovery/config/app/$compose_file" "$deploy_dir/$compose_file"
done
if [[ " ${release_compose_files[*]} " != *" docker-compose.release-active.yml "* ]]; then
  [[ -f $recovery/config/app/no-release-active-override && ! -L $recovery/config/app/no-release-active-override ]]
  rm -f "$deploy_dir/docker-compose.release-active.yml"
fi
restore_override_tmp="$deploy_dir/docker-compose.release-active.yml.restore.$$"
cat > "$restore_override_tmp" <<EOF
services:
  sub2api:
    image: $(<"$state_dir/pre-image-id")
EOF
chmod 600 "$restore_override_tmp"
mv -T -- "$restore_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
env_tmp="$deploy_dir/.env.restore.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\n' "$(<"$state_dir/pre-image-id")" >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
find "$deploy_dir/data" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a "$recovery/config/app/data/." "$deploy_dir/data/"
(cd "$deploy_dir/data" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$recovery/metadata/data-restored.sha256"
diff -u "$recovery/metadata/data.sha256" "$recovery/metadata/data-restored.sha256" >/dev/null
# The dump is the authoritative online snapshot. Live-table digests are
# collected later for observability and can legitimately differ while traffic
# remains enabled; pg_restore and the archive checksum prove restored content.
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$recovery/metadata/postgres-restored.txt"
diff -u "$recovery/metadata/postgres.txt" "$recovery/metadata/postgres-restored.txt" >/dev/null
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
compose_image=$(docker compose "${release_compose_args[@]}" config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$(<"$state_dir/pre-image-id")" ]]
docker compose "${release_compose_args[@]}" up -d --no-deps sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$(<"$state_dir/pre-image-id")" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
systemctl start nginx
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=sub2api\nport=18080\nimage_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
cleanup_recovery
trap - ERR INT TERM EXIT
printf 'coordinated_restore=verified\n'
printf 'restored_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
printf 'application_health=pass\n'
