#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
source "$assets_dir/nginx-ingress-contract.sh"
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
old_container=$(sed -n 's/^container=//p' "$state_dir/pre-active-app")
old_port=$(sed -n 's/^port=//p' "$state_dir/pre-active-app")
old_instance_id=$(sed -n 's/^instance_id=//p' "$state_dir/pre-active-app")
old_release_id=$(sed -n 's/^release_id=//p' "$state_dir/pre-active-app")
[[ $old_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $old_port == 18080 || $old_port == 18081 ]]
[[ -z $old_instance_id || $old_instance_id =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
[[ -z $old_release_id || $old_release_id =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
(cd "$state_dir" && sha256sum -c recovery-point.age.sha256 >/dev/null)
[[ -f $state_dir/recovery-point.tar && ! -L $state_dir/recovery-point.tar ]]
[[ -f $state_dir/recovery-point.tar.sha256 && ! -L $state_dir/recovery-point.tar.sha256 ]]
(cd "$state_dir" && sha256sum -c recovery-point.tar.sha256 >/dev/null)
recovery="$state_dir/recovery"
if [[ -e $recovery || -L $recovery ]]; then
  [[ -d $recovery && ! -L $recovery ]]
  rm -rf -- "$recovery"
fi
install -d -m 700 "$recovery"
cleanup_recovery() {
  if [[ -e $recovery || -L $recovery ]]; then
    [[ -d $recovery && ! -L $recovery ]]
    rm -rf -- "$recovery"
  fi
}
assert_root_file() {
  local path=${1:?path is required}
  local kind=${2:?kind is required}
  [[ -f $path && ! -L $path ]]
  case "$kind" in
    nginx) case "$(stat -c '%U:%G:%a:%h' "$path")" in root:root:600:1|root:root:640:1|root:root:644:1) ;; *) return 1 ;; esac ;;
    600) [[ $(stat -c '%U:%G:%a:%h' "$path") == root:root:600:1 ]] ;;
    644) [[ $(stat -c '%U:%G:%a:%h' "$path") == root:root:644:1 ]] ;;
    *) return 1 ;;
  esac
}
assert_safe_file_target() {
  local target=${1:?target is required}
  local kind=${2:?kind is required}
  local parent=${target%/*}
  [[ -d $parent && ! -L $parent ]]
  if [[ -e $target || -L $target ]]; then
    assert_root_file "$target" "$kind"
  fi
}
replace_from_snapshot() {
  local source=${1:?source is required}
  local target=${2:?target is required}
  local kind=${3:?kind is required}
  local tmp
  assert_root_file "$source" "$kind"
  assert_safe_file_target "$target"
  tmp="$target.restore.$$"
  [[ ! -e $tmp && ! -L $tmp ]]
  cp -p -- "$source" "$tmp"
  mv -T -- "$tmp" "$target"
}
snapshot_entries() {
  local directory=${1:?directory is required}
  mapfile -d '' SNAPSHOT_ENTRIES < <(find "$directory" -mindepth 1 -maxdepth 1 -printf '%f\0')
}
assert_single_snapshot() {
  local directory=${1:?directory is required}
  local expected=${2:?expected is required}
  local kind=${3:?kind is required}
  [[ -d $directory && ! -L $directory ]]
  snapshot_entries "$directory"
  [[ ${#SNAPSHOT_ENTRIES[@]} == 1 && ${SNAPSHOT_ENTRIES[0]} == "$expected" ]]
  assert_root_file "$directory/$expected" "$kind"
}
restore_nginx_recovery() {
  local nginx_backup="$recovery/config/nginx"
  local managed_site managed_name source target dump
  [[ -d $nginx_backup && ! -L $nginx_backup ]]
  [[ -d $nginx_backup/sites-enabled && ! -L $nginx_backup/sites-enabled ]]
  [[ -d $nginx_backup/conf.d && ! -L $nginx_backup/conf.d ]]
  [[ -d $nginx_backup/snippets && ! -L $nginx_backup/snippets ]]
  [[ -d $nginx_backup/release-backups && ! -L $nginx_backup/release-backups ]]
  [[ -d $nginx_backup/logrotate && ! -L $nginx_backup/logrotate ]]
  assert_root_file "$nginx_backup/nginx.conf" nginx
  replace_from_snapshot "$nginx_backup/nginx.conf" /etc/nginx/nginx.conf nginx
  [[ -f $recovery/metadata/nginx-managed-site && ! -L $recovery/metadata/nginx-managed-site ]]
  managed_site=$(<"$recovery/metadata/nginx-managed-site")
  [[ $managed_site =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}$ ]]
  managed_name=$(basename -- "$managed_site")
  source="$nginx_backup/sites-enabled/$managed_name"
  assert_single_snapshot "$nginx_backup/sites-enabled" "$managed_name" nginx
  replace_from_snapshot "$source" "$managed_site" nginx

  shopt -s nullglob
  local -a current_confs=(/etc/nginx/conf.d/sub2api-release-*.conf)
  local -a backup_confs=("$nginx_backup"/conf.d/sub2api-release-*.conf)
  snapshot_entries "$nginx_backup/conf.d"
  if [[ -f $nginx_backup/conf.d/.none ]]; then
    assert_single_snapshot "$nginx_backup/conf.d" .none 600
    [[ ${#backup_confs[@]} == 0 ]]
  else
    [[ ${#backup_confs[@]} -gt 0 ]]
    [[ ! -e $nginx_backup/conf.d/.none && ! -L $nginx_backup/conf.d/.none ]]
    [[ ${#SNAPSHOT_ENTRIES[@]} == ${#backup_confs[@]} ]]
    for source in "${backup_confs[@]}"; do
      managed_name=$(basename -- "$source")
      [[ $managed_name =~ ^sub2api-release-[A-Za-z0-9._-]{1,160}\.conf$ ]]
      assert_root_file "$source" 600
    done
  fi
  for target in "${current_confs[@]}"; do
    [[ $target =~ ^/etc/nginx/conf.d/sub2api-release-[A-Za-z0-9._-]{1,160}\.conf$ ]]
    assert_root_file "$target" 600
    rm -f -- "$target"
  done
  if [[ -f $nginx_backup/conf.d/.none ]]; then
    :
  else
    for source in "${backup_confs[@]}"; do
      managed_name=$(basename -- "$source")
      replace_from_snapshot "$source" "/etc/nginx/conf.d/$managed_name" 600
    done
  fi
  local -a live_confs=(/etc/nginx/conf.d/sub2api-release-*.conf)
  [[ ${#live_confs[@]} == ${#backup_confs[@]} ]]
  for source in "${backup_confs[@]}"; do
    managed_name=$(basename -- "$source")
    assert_root_file "/etc/nginx/conf.d/$managed_name" 600
  done
  shopt -u nullglob

  if [[ -e $nginx_backup/snippets/.absent || -L $nginx_backup/snippets/.absent ]]; then
    assert_single_snapshot "$nginx_backup/snippets" .absent 600
    assert_safe_file_target "$NGINX_INGRESS_SNIPPET" 600
    rm -f -- "$NGINX_INGRESS_SNIPPET"
  else
    source="$nginx_backup/snippets/$(basename -- "$NGINX_INGRESS_SNIPPET")"
    assert_single_snapshot "$nginx_backup/snippets" "$(basename -- "$NGINX_INGRESS_SNIPPET")" 600
    replace_from_snapshot "$source" "$NGINX_INGRESS_SNIPPET" 600
  fi

  if [[ -e $nginx_backup/observability.absent || -L $nginx_backup/observability.absent ]]; then
    assert_root_file "$nginx_backup/observability.absent" 600
    [[ ! -e $nginx_backup/observability.conf && ! -L $nginx_backup/observability.conf ]]
    assert_safe_file_target "$NGINX_OBSERVABILITY_CONF" 600
    rm -f -- "$NGINX_OBSERVABILITY_CONF"
  else
    [[ ! -e $nginx_backup/observability.absent && ! -L $nginx_backup/observability.absent ]]
    assert_root_file "$nginx_backup/observability.conf" 600
    replace_from_snapshot "$nginx_backup/observability.conf" "$NGINX_OBSERVABILITY_CONF" 600
  fi

  if [[ -e $NGINX_SITE_BACKUP_DIR || -L $NGINX_SITE_BACKUP_DIR ]]; then
    [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]]
    [[ -z $(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 ! -type f -print -quit) ]]
    [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]]
    find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f -delete
    rmdir "$NGINX_SITE_BACKUP_DIR"
  fi
  snapshot_entries "$nginx_backup/release-backups"
  if [[ -e $nginx_backup/release-backups/.absent || -L $nginx_backup/release-backups/.absent ]]; then
    assert_single_snapshot "$nginx_backup/release-backups" .absent 600
    [[ ! -e $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]]
  else
    [[ ! -e $nginx_backup/release-backups/.absent && ! -L $nginx_backup/release-backups/.absent ]]
    [[ ${#SNAPSHOT_ENTRIES[@]} -gt 0 ]]
    for managed_name in "${SNAPSHOT_ENTRIES[@]}"; do
      [[ $managed_name =~ ^[A-Za-z0-9._-]{1,240}$ ]]
      assert_root_file "$nginx_backup/release-backups/$managed_name" 600
    done
    install -d -o root -g root -m 700 "$NGINX_SITE_BACKUP_DIR"
    while IFS= read -r -d '' source; do
      managed_name=$(basename -- "$source")
      replace_from_snapshot "$source" "$NGINX_SITE_BACKUP_DIR/$managed_name" 600
    done < <(find "$nginx_backup/release-backups" -mindepth 1 -maxdepth 1 -type f -print0)
    [[ $(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f | wc -l) == ${#SNAPSHOT_ENTRIES[@]} ]]
    while IFS= read -r -d '' source; do
      managed_name=$(basename -- "$source")
      assert_root_file "$NGINX_SITE_BACKUP_DIR/$managed_name" 600
    done < <(find "$nginx_backup/release-backups" -mindepth 1 -maxdepth 1 -type f -print0)
  fi

  if [[ -e $nginx_backup/logrotate/.absent || -L $nginx_backup/logrotate/.absent ]]; then
    assert_single_snapshot "$nginx_backup/logrotate" .absent 600
    assert_safe_file_target "$NGINX_UPSTREAM_LOGROTATE" 644
    rm -f -- "$NGINX_UPSTREAM_LOGROTATE"
  else
    source="$nginx_backup/logrotate/$(basename -- "$NGINX_UPSTREAM_LOGROTATE")"
    assert_single_snapshot "$nginx_backup/logrotate" "$(basename -- "$NGINX_UPSTREAM_LOGROTATE")" 644
    replace_from_snapshot "$source" "$NGINX_UPSTREAM_LOGROTATE" 644
  fi

  nginx -t >/dev/null 2>&1
  dump=$(nginx -T 2>&1)
  grep -Fq "# configuration file $managed_site:" <<<"$dump"
  for source in "${backup_confs[@]}"; do
    managed_name=$(basename -- "$source")
    grep -Fq "# configuration file /etc/nginx/conf.d/$managed_name:" <<<"$dump"
  done
  if [[ ! -f $nginx_backup/observability.absent ]]; then
    grep -Fq "# configuration file $NGINX_OBSERVABILITY_CONF:" <<<"$dump"
  fi
}
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
restore_nginx_recovery
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
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
restore_base_compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
restore_network_mode=$(sub2api_compose_network_mode "$restore_base_compose_json" "$old_port")
restore_override_tmp="$deploy_dir/docker-compose.release-active.yml.restore.$$"
write_release_active_override "$restore_override_tmp" "$(<"$state_dir/pre-image-id")" "$old_instance_id" "$old_port" "$restore_network_mode"
chmod 600 "$restore_override_tmp"
mv -T -- "$restore_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
env_tmp="$deploy_dir/.env.restore.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|BIND_HOST|SERVER_PORT)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\nBIND_HOST=127.0.0.1\nSERVER_PORT=%s\n' "$(<"$state_dir/pre-image-id")" "$old_port" >> "$env_tmp"
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
[[ $(assert_sub2api_compose_closure "$deploy_dir" "$old_port" "$(<"$state_dir/pre-image-id")" "$old_instance_id") == "$restore_network_mode" ]]
docker compose "${release_compose_args[@]}" up -d --no-deps sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] && break
  sleep 2
done
assert_sub2api_runtime_contract sub2api "$(<"$state_dir/pre-image-id")" "$restore_network_mode" "$old_port"
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
systemctl start nginx
[[ $(systemctl is-active nginx) == active ]]
printf 'restored_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$state_dir/nginx-recovery-restored.tmp"
chmod 400 "$state_dir/nginx-recovery-restored.tmp"
mv -T -- "$state_dir/nginx-recovery-restored.tmp" "$state_dir/nginx-recovery-restored"
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s\n' "$old_port" "$(docker inspect -f '{{.Image}}' sub2api)" "$old_release_id" "$old_instance_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
cleanup_recovery
trap - ERR INT TERM EXIT
printf 'coordinated_restore=verified\n'
printf 'restored_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
printf 'application_health=pass\n'
