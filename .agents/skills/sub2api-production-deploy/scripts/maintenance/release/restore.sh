#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
bundle_assets_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
RECOVERY_ASSETS_DIR=$bundle_assets_dir
source "$bundle_assets_dir/context.sh"
assets_dir=$bundle_assets_dir
source "$assets_dir/compose-contract.sh"
source "$assets_dir/recovery-compose.sh"
source "$assets_dir/recovery-checkpoint.sh"
if [[ -n ${RECOVERY_INTERRUPT_AFTER_PHASE:-} ]]; then
  source "$assets_dir/recovery-isolation.sh"
fi
source "$assets_dir/nginx-ingress-contract.sh"
unset COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR DOCKER_HOST DOCKER_CONTEXT
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
pre_image_id=$(<"$state_dir/pre-image-id")
recovery_point_sha256=$(sha256sum "$state_dir/recovery-point.tar" | awk '{print $1}')
recovery_checkpoint_init "$release_dir/recovery-checkpoints" "$release_id" "$recovery_point_sha256" "$pre_image_id" "${RECOVERY_HELPER_BUNDLE_SHA256:?verified helper bundle SHA is required}"
recovery="$state_dir/recovery"
if [[ -e $recovery || -L $recovery ]]; then
  [[ -d $recovery && ! -L $recovery ]]
  rm -rf -- "$recovery"
fi
install -d -m 700 "$recovery"
cleanup_recovery() {
  if [[ -e $recovery || -L $recovery ]]; then
    [[ -d $recovery && ! -L $recovery ]] || return 1
    rm -rf -- "$recovery" || return 1
  fi
}
assert_root_file() {
  local path=${1:?path is required}
  local kind=${2:?kind is required}
  [[ -f $path && ! -L $path ]] || return 1
  local metadata
  metadata=$(stat -c '%U:%G:%a:%h' "$path") || return 1
  case "$kind" in
    nginx) case "$metadata" in root:root:600:1|root:root:640:1|root:root:644:1) ;; *) return 1 ;; esac ;;
    recovery-config) case "$metadata" in root:root:400:1|root:root:600:1|root:root:640:1|root:root:644:1) ;; *) return 1 ;; esac ;;
    600) [[ $metadata == root:root:600:1 ]] ;;
    644) [[ $metadata == root:root:644:1 ]] ;;
    *) return 1 ;;
  esac
}
assert_safe_file_target() {
  local target=${1:?target is required}
  local kind=${2:?kind is required}
  local parent=${target%/*}
  [[ -d $parent && ! -L $parent ]] || return 1
  local canonical_parent
  canonical_parent=$(realpath -e -- "$parent") || return 1
  [[ $canonical_parent == "$parent" ]] || return 1
  if [[ -e $target || -L $target ]]; then
    assert_root_file "$target" "$kind" || return 1
  fi
}
replace_from_snapshot() {
  local source=${1:?source is required}
  local target=${2:?target is required}
  local kind=${3:?kind is required}
  local tmp
  assert_root_file "$source" "$kind" || return 1
  assert_safe_file_target "$target" "$kind" || return 1
  tmp="$target.restore.$$"
  [[ ! -e $tmp && ! -L $tmp ]] || return 1
  cp -p -- "$source" "$tmp" || { rm -f -- "$tmp"; return 1; }
  mv -T -- "$tmp" "$target" || { rm -f -- "$tmp"; return 1; }
}
snapshot_entries() {
  local directory=${1:?directory is required}
  [[ -d $directory && ! -L $directory ]] || return 1
  mapfile -d '' SNAPSHOT_ENTRIES < <(find "$directory" -mindepth 1 -maxdepth 1 -printf '%f\0') || return 1
  local scan_pid=$!
  wait "$scan_pid" || return 1
}
assert_observability_snapshot() {
  local directory=${1:?directory is required}
  local -a entries=()
  [[ -d $directory && ! -L $directory ]] || return 1
  mapfile -d '' entries < <(find "$directory" -mindepth 1 -maxdepth 1 -name 'observability*' -printf '%f\0') || return 1
  local scan_pid=$!
  wait "$scan_pid" || return 1
  [[ ${#entries[@]} == 1 ]] || return 1
  case "${entries[0]}" in
    observability.absent|observability.conf) ;;
    *) return 1 ;;
  esac
  assert_root_file "$directory/${entries[0]}" 600 || return 1
}
assert_single_snapshot() {
  local directory=${1:?directory is required}
  local expected=${2:?expected is required}
  local kind=${3:?kind is required}
  [[ -d $directory && ! -L $directory ]] || return 1
  [[ $expected =~ ^[A-Za-z0-9._-]{1,240}$ && $expected != . && $expected != .. ]] || return 1
  snapshot_entries "$directory" || return 1
  [[ ${#SNAPSHOT_ENTRIES[@]} == 1 && ${SNAPSHOT_ENTRIES[0]} == "$expected" ]] || return 1
  assert_root_file "$directory/$expected" "$kind" || return 1
}
restore_nginx_recovery() {
  local nginx_backup="$recovery/config/nginx"
  local managed_site managed_name source target dump
  local unexpected_entries scan_pid copied_count
  [[ -d $nginx_backup && ! -L $nginx_backup ]] || return 1
  [[ -d $nginx_backup/sites-enabled && ! -L $nginx_backup/sites-enabled ]] || return 1
  [[ -d $nginx_backup/conf.d && ! -L $nginx_backup/conf.d ]] || return 1
  [[ -d $nginx_backup/snippets && ! -L $nginx_backup/snippets ]] || return 1
  [[ -d $nginx_backup/release-backups && ! -L $nginx_backup/release-backups ]] || return 1
  [[ -d $nginx_backup/logrotate && ! -L $nginx_backup/logrotate ]] || return 1
  assert_root_file "$nginx_backup/nginx.conf" nginx || return 1
  replace_from_snapshot "$nginx_backup/nginx.conf" /etc/nginx/nginx.conf nginx || return 1
  [[ -f $recovery/metadata/nginx-managed-site && ! -L $recovery/metadata/nginx-managed-site ]] || return 1
  managed_site=$(<"$recovery/metadata/nginx-managed-site") || return 1
  [[ $managed_site =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}$ ]] || return 1
  managed_name=$(basename -- "$managed_site") || return 1
  source="$nginx_backup/sites-enabled/$managed_name"
  assert_single_snapshot "$nginx_backup/sites-enabled" "$managed_name" nginx || return 1
  replace_from_snapshot "$source" "$managed_site" nginx || return 1

  shopt -s nullglob || return 1
  local -a current_confs=(/etc/nginx/conf.d/sub2api-release-*.conf)
  local -a backup_confs=("$nginx_backup"/conf.d/sub2api-release-*.conf)
  snapshot_entries "$nginx_backup/conf.d" || return 1
  if [[ -f $nginx_backup/conf.d/.none ]]; then
    assert_single_snapshot "$nginx_backup/conf.d" .none 600 || return 1
    [[ ${#backup_confs[@]} == 0 ]] || return 1
  else
    [[ ${#backup_confs[@]} -gt 0 ]] || return 1
    [[ ! -e $nginx_backup/conf.d/.none && ! -L $nginx_backup/conf.d/.none ]] || return 1
    [[ ${#SNAPSHOT_ENTRIES[@]} == ${#backup_confs[@]} ]] || return 1
    for source in "${backup_confs[@]}"; do
      managed_name=$(basename -- "$source") || return 1
      [[ $managed_name =~ ^sub2api-release-[A-Za-z0-9._-]{1,160}\.conf$ ]] || return 1
      assert_root_file "$source" 600 || return 1
    done
  fi
  for target in "${current_confs[@]}"; do
    [[ $target =~ ^/etc/nginx/conf.d/sub2api-release-[A-Za-z0-9._-]{1,160}\.conf$ ]] || return 1
    assert_root_file "$target" 600 || return 1
    rm -f -- "$target" || return 1
  done
  if [[ -f $nginx_backup/conf.d/.none ]]; then
    :
  else
    for source in "${backup_confs[@]}"; do
      managed_name=$(basename -- "$source") || return 1
      replace_from_snapshot "$source" "/etc/nginx/conf.d/$managed_name" 600 || return 1
    done
  fi
  local -a live_confs=(/etc/nginx/conf.d/sub2api-release-*.conf)
  [[ ${#live_confs[@]} == ${#backup_confs[@]} ]] || return 1
  for source in "${backup_confs[@]}"; do
    managed_name=$(basename -- "$source") || return 1
    assert_root_file "/etc/nginx/conf.d/$managed_name" 600 || return 1
  done
  shopt -u nullglob || return 1

  if [[ -e $nginx_backup/snippets/.absent || -L $nginx_backup/snippets/.absent ]]; then
    assert_single_snapshot "$nginx_backup/snippets" .absent 600 || return 1
    assert_safe_file_target "$NGINX_INGRESS_SNIPPET" 600 || return 1
    rm -f -- "$NGINX_INGRESS_SNIPPET" || return 1
  else
    source="$nginx_backup/snippets/$(basename -- "$NGINX_INGRESS_SNIPPET")" || return 1
    assert_single_snapshot "$nginx_backup/snippets" "$(basename -- "$NGINX_INGRESS_SNIPPET")" 600 || return 1
    replace_from_snapshot "$source" "$NGINX_INGRESS_SNIPPET" 600 || return 1
  fi

  assert_observability_snapshot "$nginx_backup" || return 1
  if [[ -e $nginx_backup/observability.absent || -L $nginx_backup/observability.absent ]]; then
    assert_root_file "$nginx_backup/observability.absent" 600 || return 1
    [[ ! -e $nginx_backup/observability.conf && ! -L $nginx_backup/observability.conf ]] || return 1
    assert_safe_file_target "$NGINX_OBSERVABILITY_CONF" 600 || return 1
    rm -f -- "$NGINX_OBSERVABILITY_CONF" || return 1
  else
    [[ ! -e $nginx_backup/observability.absent && ! -L $nginx_backup/observability.absent ]] || return 1
    assert_root_file "$nginx_backup/observability.conf" 600 || return 1
    replace_from_snapshot "$nginx_backup/observability.conf" "$NGINX_OBSERVABILITY_CONF" 600 || return 1
  fi

  if [[ -e $NGINX_SITE_BACKUP_DIR || -L $NGINX_SITE_BACKUP_DIR ]]; then
    [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]] || return 1
    unexpected_entries=$(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 ! -type f -print -quit) || return 1
    [[ -z $unexpected_entries ]] || return 1
    [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]] || return 1
    find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f -delete || return 1
    rmdir "$NGINX_SITE_BACKUP_DIR" || return 1
  fi
  snapshot_entries "$nginx_backup/release-backups" || return 1
  if [[ -e $nginx_backup/release-backups/.absent || -L $nginx_backup/release-backups/.absent ]]; then
    assert_single_snapshot "$nginx_backup/release-backups" .absent 600 || return 1
    [[ ! -e $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]] || return 1
  else
    [[ ! -e $nginx_backup/release-backups/.absent && ! -L $nginx_backup/release-backups/.absent ]] || return 1
    [[ ${#SNAPSHOT_ENTRIES[@]} -gt 0 ]] || return 1
    for managed_name in "${SNAPSHOT_ENTRIES[@]}"; do
      [[ $managed_name =~ ^[A-Za-z0-9._-]{1,240}$ ]] || return 1
      assert_root_file "$nginx_backup/release-backups/$managed_name" 600 || return 1
    done
    install -d -o root -g root -m 700 "$NGINX_SITE_BACKUP_DIR" || return 1
    while IFS= read -r -d '' source; do
      managed_name=$(basename -- "$source") || return 1
      replace_from_snapshot "$source" "$NGINX_SITE_BACKUP_DIR/$managed_name" 600 || return 1
    done < <(find "$nginx_backup/release-backups" -mindepth 1 -maxdepth 1 -type f -print0)
    scan_pid=$!
    wait "$scan_pid" || return 1
    copied_count=$(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f | wc -l) || return 1
    [[ $copied_count == ${#SNAPSHOT_ENTRIES[@]} ]] || return 1
    while IFS= read -r -d '' source; do
      managed_name=$(basename -- "$source") || return 1
      assert_root_file "$NGINX_SITE_BACKUP_DIR/$managed_name" 600 || return 1
    done < <(find "$nginx_backup/release-backups" -mindepth 1 -maxdepth 1 -type f -print0)
    scan_pid=$!
    wait "$scan_pid" || return 1
  fi

  if [[ -e $nginx_backup/logrotate/.absent || -L $nginx_backup/logrotate/.absent ]]; then
    assert_single_snapshot "$nginx_backup/logrotate" .absent 600 || return 1
    assert_safe_file_target "$NGINX_UPSTREAM_LOGROTATE" 644 || return 1
    rm -f -- "$NGINX_UPSTREAM_LOGROTATE" || return 1
  else
    source="$nginx_backup/logrotate/$(basename -- "$NGINX_UPSTREAM_LOGROTATE")" || return 1
    assert_single_snapshot "$nginx_backup/logrotate" "$(basename -- "$NGINX_UPSTREAM_LOGROTATE")" 644 || return 1
    replace_from_snapshot "$source" "$NGINX_UPSTREAM_LOGROTATE" 644 || return 1
  fi

  nginx -t >/dev/null 2>&1 || return 1
  dump=$(nginx -T 2>&1) || return 1
  grep -Fq "# configuration file $managed_site:" <<<"$dump" || return 1
  for source in "${backup_confs[@]}"; do
    managed_name=$(basename -- "$source") || return 1
    grep -Fq "# configuration file /etc/nginx/conf.d/$managed_name:" <<<"$dump" || return 1
  done
  if [[ ! -f $nginx_backup/observability.absent ]]; then
    grep -Fq "# configuration file $NGINX_OBSERVABILITY_CONF:" <<<"$dump" || return 1
  fi
}
fail_closed() {
  code=$?
  local failed=0 app_status nginx_status container_names
  trap - ERR INT TERM EXIT
  set +e
  systemctl stop nginx >/dev/null 2>&1 || failed=1
  docker stop "$active_container" >/dev/null 2>&1 || true
  [[ $active_container == sub2api ]] || docker stop sub2api >/dev/null 2>&1 || true
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
  if docker inspect sub2api >/dev/null 2>&1; then
    [[ $(docker inspect -f '{{.State.Status}}' sub2api 2>/dev/null) != running ]] || failed=1
  fi
  (( failed == 0 )) || exit 125
  exit "$code"
}
trap cleanup_recovery EXIT
# Validate and render the entire recovery closure before removing a container,
# overwriting data, or issuing a database mutation.
tar -C "$recovery" -xf "$state_dir/recovery-point.tar"
(cd "$recovery" && sha256sum -c SHA256SUMS >/dev/null)
install -d -o root -g root -m 700 "$recovery/compose-restored"
recovery_compose_restore_verify "$recovery/config/app" "$recovery/compose-restored" "$pre_image_id"

apply_compose_recovery() {
  _recovery_compose_inventory "$recovery/config/app" || return 1
  local compose_file
  for compose_file in "${RECOVERY_COMPOSE_FILES[@]}"; do
    assert_safe_file_target "$deploy_dir/$compose_file" recovery-config || return 1
    [[ ! -e $deploy_dir/$compose_file.restore.$$ && ! -L $deploy_dir/$compose_file.restore.$$ ]] || return 1
    install -o root -g root -m 600 -- "$recovery/compose-restored/$compose_file" "$deploy_dir/$compose_file.restore.$$" || return 1
    mv -T -- "$deploy_dir/$compose_file.restore.$$" "$deploy_dir/$compose_file" || return 1
  done
  load_release_compose_files "$recovery/config/app" || return 1
  if [[ " ${release_compose_files[*]} " != *" docker-compose.release-active.yml "* ]]; then
    [[ -f $recovery/config/app/no-release-active-override && ! -L $recovery/config/app/no-release-active-override ]] || return 1
    assert_safe_file_target "$deploy_dir/docker-compose.release-active.yml" 600 || return 1
    rm -f -- "$deploy_dir/docker-compose.release-active.yml" || return 1
  fi
}
verify_compose_recovery() {
  recovery_compose_verify_live "$recovery/config/app" "$deploy_dir" "$pre_image_id" || return 1
  cd -- "$deploy_dir" || return 1
  restore_network_mode=$(assert_sub2api_compose_closure "$deploy_dir" "$old_port" "$pre_image_id" "$old_instance_id") || return 1
  [[ $restore_network_mode == host || $restore_network_mode == bridge ]] || return 1
}
postgres_metadata() {
  docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -A -t -U sub2api -d sub2api -c "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename"
}
postgres_fingerprint() (
  set -Eeuo pipefail
  # Hash the actual restored rows, never the traffic-time observability digest.
  # Data values stay inside this pipe; only the final SHA is persisted.
  local tables schema table digest
  tables=$(docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -A -t -U sub2api -d sub2api -c "SELECT schemaname||'|'||tablename FROM pg_tables WHERE schemaname='public' ORDER BY schemaname,tablename") || return 1
  [[ -n $tables ]] || return 1
  {
    while IFS='|' read -r schema table; do
      [[ $schema =~ ^[A-Za-z_][A-Za-z0-9_]*$ && $table =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || return 1
      digest=$(docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d sub2api -c "COPY (SELECT row_to_json(t)::text FROM \"$schema\".\"$table\" t ORDER BY row_to_json(t)::text COLLATE \"C\") TO STDOUT" | sha256sum | awk '{print $1}') || return 1
      [[ $digest =~ ^[0-9a-f]{64}$ ]] || return 1
      printf '%s|%s|%s\n' "$schema" "$table" "$digest" || return 1
    done <<<"$tables"
  } | sha256sum | awk '{print $1}'
)
assert_recovery_container_absent() {
  local container=${1:?container is required} names
  [[ $container =~ ^[A-Za-z0-9_.-]{1,100}$ ]] || return 1
  names=$(docker ps -a --format '{{.Names}}') || return 1
  if grep -Fxq -- "$container" <<<"$names"; then
    return 1
  else
    local match_status=$?
    [[ $match_status == 1 ]] || return 1
  fi
}
assert_recovery_container_stopped() {
  local container=${1:?container is required} status
  status=$(docker inspect -f '{{.State.Status}}' "$container") || return 1
  case "$status" in exited|created|dead) ;; *) return 1 ;; esac
}
apply_postgres_recovery() {
  systemctl stop nginx || return 1
  if docker inspect "$candidate_container" >/dev/null 2>&1; then
    docker stop -t 30 "$candidate_container" >/dev/null 2>&1 || return 1
    assert_recovery_container_stopped "$candidate_container" || return 1
    docker rm "$candidate_container" >/dev/null 2>&1 || return 1
  else
    assert_recovery_container_absent "$candidate_container" || return 1
  fi
  assert_recovery_container_absent "$candidate_container" || return 1
  if docker inspect "$active_container" >/dev/null 2>&1; then
    docker rm -f "$active_container" >/dev/null 2>&1 || return 1
  fi
  assert_recovery_container_absent "$active_container" || return 1
  if [[ $active_container != sub2api ]]; then
    if docker inspect sub2api >/dev/null 2>&1; then docker rm -f sub2api >/dev/null 2>&1 || return 1; fi
    assert_recovery_container_absent sub2api || return 1
  fi
  local nginx_status metadata_sha
  nginx_status=$(systemctl is-active nginx 2>/dev/null || true)
  case "$nginx_status" in inactive|failed) ;; *) return 1 ;; esac
  [[ -d $deploy_dir/data && ! -L $deploy_dir/data ]] || return 1
  find "$deploy_dir/data" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + || return 1
  cp -a "$recovery/config/app/data/." "$deploy_dir/data/" || return 1
  (cd "$deploy_dir/data" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$recovery/metadata/data-restored.sha256" || return 1
  diff -u "$recovery/metadata/data.sha256" "$recovery/metadata/data-restored.sha256" >/dev/null || return 1
  docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='sub2api' AND pid<>pg_backend_pid();" >/dev/null || return 1
  docker exec sub2api-postgres dropdb --if-exists -U sub2api sub2api || return 1
  docker exec sub2api-postgres createdb -U sub2api -O sub2api sub2api || return 1
  docker exec -i sub2api-postgres pg_restore --exit-on-error --no-owner -U sub2api -d sub2api < "$recovery/database/sub2api.dump" || return 1
  postgres_metadata > "$recovery/metadata/postgres-restored.txt" || return 1
  diff -u "$recovery/metadata/postgres.txt" "$recovery/metadata/postgres-restored.txt" >/dev/null || return 1
  metadata_sha=$(sha256sum "$recovery/metadata/postgres-restored.txt" | awk '{print $1}') || return 1
  recovery_checkpoint_store postgres.metadata "$metadata_sha" || return 1
  local restored_fingerprint
  restored_fingerprint=$(postgres_fingerprint) || return 1
  recovery_checkpoint_store postgres.fingerprint "$restored_fingerprint" || return 1
}
verify_postgres_recovery() {
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/postgres.metadata" || return 1
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/postgres.fingerprint" || return 1
  local current_metadata current_fingerprint
  current_metadata=$(postgres_metadata | sha256sum | awk '{print $1}') || return 1
  [[ $current_metadata == "$(<"$RECOVERY_CHECKPOINT_ROOT/postgres.metadata")" ]] || return 1
  # A healthy application may legitimately write after its checkpoint. At that
  # point revalidate schema/migrations and the immutable restoration identity;
  # never roll back those new writes by replaying the recovery dump.
  if ! recovery_checkpoint_completed app_healthy; then
    current_fingerprint=$(postgres_fingerprint) || return 1
    [[ $current_fingerprint == "$(<"$RECOVERY_CHECKPOINT_ROOT/postgres.fingerprint")" ]] || return 1
  fi
}
apply_redis_recovery() {
local redis_health
redis_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis) || return 1
[[ $redis_source == /* && $redis_source != / && -d $redis_source && ! -L $redis_source ]] || return 1
[[ $(realpath -e -- "$redis_source") == "$redis_source" ]] || return 1
redis_appendonly=$(docker inspect sub2api-redis | jq -r '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--appendonly")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--appendonly=")) | ltrimstr("--appendonly=") ] | first // "no") end') || return 1
docker stop sub2api-redis >/dev/null || return 1
assert_recovery_container_stopped sub2api-redis || return 1
find "$redis_source" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + || return 1
cp -a "$recovery/redis/." "$redis_source/" || return 1
(cd "$redis_source" && [[ -f dump.rdb && ! -L dump.rdb ]] && [[ $(find . -mindepth 1 -maxdepth 1 -type f | wc -l) == 1 ]]) || return 1
(cd "$redis_source" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum) > "$recovery/metadata/redis-files-restored.sha256" || return 1
diff -u "$recovery/metadata/redis-files.sha256" "$recovery/metadata/redis-files-restored.sha256" >/dev/null || return 1
if [[ ${redis_appendonly,,} == yes ]]; then
  # Redis 7 prefers multipart AOF over dump.rdb. Seed its base RDB from the
  # verified recovery point so enabling AOF cannot start an empty database.
  redis_data_uid=$(stat -c %u "$redis_source/dump.rdb") || return 1
  redis_data_gid=$(stat -c %g "$redis_source/dump.rdb") || return 1
  install -d -o "$redis_data_uid" -g "$redis_data_gid" -m 700 "$redis_source/appendonlydir" || return 1
  install -o "$redis_data_uid" -g "$redis_data_gid" -m 600 "$redis_source/dump.rdb" "$redis_source/appendonlydir/appendonly.aof.1.base.rdb" || return 1
  : > "$redis_source/appendonlydir/appendonly.aof.1.incr.aof" || return 1
  printf 'file appendonly.aof.1.base.rdb seq 1 type b\nfile appendonly.aof.1.incr.aof seq 1 type i startoffset 0\n' > "$redis_source/appendonlydir/appendonly.aof.manifest" || return 1
  chown "$redis_data_uid:$redis_data_gid" "$redis_source/appendonlydir/appendonly.aof.1.incr.aof" "$redis_source/appendonlydir/appendonly.aof.manifest" || return 1
  chmod 600 "$redis_source/appendonlydir/appendonly.aof.1.incr.aof" "$redis_source/appendonlydir/appendonly.aof.manifest" || return 1
fi
redis_restore_health_started=$SECONDS
redis_restore_health_deadline=$((redis_restore_health_started + 180)) || return 1
docker start sub2api-redis >/dev/null || return 1
# Large verified RDB/AOF recovery points can take longer than the ordinary
# container health start period. Keep the wait bounded, but allow enough time
# for Redis to finish loading before fail-closed recovery stops the stack.
for _ in {1..180}; do
  ((SECONDS < redis_restore_health_deadline)) || break
  [[ $(timeout 5 docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
  sleep 1 || return 1
done
redis_health=$(timeout 5 docker inspect -f '{{.State.Health.Status}}' sub2api-redis) || return 1
[[ $redis_health == healthy ]] || return 1
((SECONDS <= redis_restore_health_deadline)) || return 1
recovery_checkpoint_store redis.health-wait-seconds "$((SECONDS - redis_restore_health_started))" || return 1
  recovery_checkpoint_store redis.persistent "$redis_backup_persistent" || return 1
}
verify_redis_recovery() {
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]] || return 1
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/redis.persistent" || return 1
redis_password=$(docker inspect sub2api-redis | jq -r '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--requirepass")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first // "") end') || return 1
redis_dbsize=$(printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning DBSIZE' | tr -d '\r') || return 1
redis_keyspace=$(printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning INFO keyspace' | tr -d '\r') || return 1
redis_restored_expiring=$(printf '%s\n' "$redis_keyspace" | sed -n 's/^db[0-9]*:keys=[0-9]*,expires=\([0-9]*\).*/\1/p' | awk '{sum += $1} END {print sum + 0}') || return 1
redis_restored_keyspace_keys=$(printf '%s\n' "$redis_keyspace" | sed -n 's/^db[0-9]*:keys=\([0-9]*\),expires=.*/\1/p' | awk '{sum += $1} END {print sum + 0}') || return 1
[[ $redis_dbsize =~ ^[0-9]+$ && $redis_backup_dbsize =~ ^[0-9]+$ ]] || return 1
[[ $redis_backup_expiring =~ ^[0-9]+$ && $redis_restored_expiring =~ ^[0-9]+$ ]] || return 1
if ! recovery_checkpoint_completed app_healthy; then [[ $redis_backup_dbsize -ge $redis_dbsize ]] || return 1; fi
if ! recovery_checkpoint_completed app_healthy; then [[ $redis_backup_expiring -ge $redis_restored_expiring ]] || return 1; fi
# Redis expires keys lazily. A healthy instance can therefore still count a
# logically expired key in DBSIZE briefly after loading the exact verified RDB.
# The file checksum and redis-check-rdb checks above prove the restored source;
# the live check must only ensure that no persistent (non-TTL) keys disappeared.
redis_backup_persistent=$((redis_backup_dbsize - redis_backup_expiring))
redis_restored_persistent_floor=$((redis_dbsize - redis_restored_expiring))
[[ $redis_restored_persistent_floor -ge $redis_backup_persistent ]] || return 1
  [[ $(<"$RECOVERY_CHECKPOINT_ROOT/redis.persistent") == "$redis_backup_persistent" ]] || return 1
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/redis.health-wait-seconds" || return 1
  redis_restore_health_wait_seconds=$(<"$RECOVERY_CHECKPOINT_ROOT/redis.health-wait-seconds")
  [[ $redis_restore_health_wait_seconds =~ ^[0-9]+$ ]] || return 1
  printf 'redis_restore_health_wait_seconds=%s\n' "$redis_restore_health_wait_seconds" || return 1
  printf 'redis_backup_dbsize=%s\nredis_backup_expiring=%s\n' "$redis_backup_dbsize" "$redis_backup_expiring" || return 1
  printf 'redis_restored_dbsize=%s\nredis_restored_expiring=%s\n' "$redis_dbsize" "$redis_restored_expiring" || return 1
  printf 'redis_restored_keyspace_keys=%s\n' "$redis_restored_keyspace_keys" || return 1
  printf 'redis_ttl_reconciliation=verified\n' || return 1
}
apply_application_recovery() {
  cd -- "$deploy_dir" || return 1
  load_release_compose_files "$deploy_dir" || return 1
  docker compose "${release_compose_args[@]}" up -d --no-deps sub2api >/dev/null 2>&1 || return 1
  local app_health
  for _ in {1..90}; do
    app_health=$(docker inspect -f '{{.State.Health.Status}}' sub2api) || return 1
    [[ $app_health == healthy ]] && break
    sleep 2 || return 1
  done
  [[ $app_health == healthy ]] || return 1
}
verify_application_recovery() {
  assert_sub2api_runtime_contract sub2api "$pre_image_id" "$restore_network_mode" "$old_port" || return 1
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] || return 1
}
verify_nginx_snapshot() {
  nginx -t >/dev/null 2>&1 || return 1
  local current_nginx_fingerprint
  current_nginx_fingerprint=$(nginx -T 2>&1 | sha256sum | awk '{print $1}') || return 1
  [[ $current_nginx_fingerprint == "$(sha256sum "$recovery/config/nginx/nginx-T.txt" | awk '{print $1}')" ]] || return 1
  local source target managed_name
  local nginx_backup="$recovery/config/nginx"
  assert_root_file /etc/nginx/nginx.conf nginx || return 1
  target=$(<"$recovery/metadata/nginx-managed-site")
  [[ $target =~ ^/etc/nginx/sites-enabled/[A-Za-z0-9._-]{1,160}$ ]] || return 1
  assert_root_file "$target" nginx || return 1
  cmp -s -- "$nginx_backup/sites-enabled/$(basename -- "$target")" "$target" || return 1
  while IFS= read -r -d '' source; do
    [[ ${source##*/} == .none ]] && continue
    target="/etc/nginx/conf.d/${source##*/}"
    assert_root_file "$target" 600 || return 1
    cmp -s -- "$source" "$target" || return 1
  done < <(find "$nginx_backup/conf.d" -mindepth 1 -maxdepth 1 -type f -print0)
  if [[ -f $nginx_backup/snippets/.absent ]]; then
    [[ ! -e $NGINX_INGRESS_SNIPPET && ! -L $NGINX_INGRESS_SNIPPET ]] || return 1
  else
    assert_root_file "$NGINX_INGRESS_SNIPPET" 600 || return 1
    cmp -s -- "$nginx_backup/snippets/$(basename -- "$NGINX_INGRESS_SNIPPET")" "$NGINX_INGRESS_SNIPPET" || return 1
  fi
  if [[ -f $nginx_backup/observability.absent ]]; then
    [[ ! -e $NGINX_OBSERVABILITY_CONF && ! -L $NGINX_OBSERVABILITY_CONF ]] || return 1
  else
    assert_root_file "$NGINX_OBSERVABILITY_CONF" 600 || return 1
    cmp -s -- "$nginx_backup/observability.conf" "$NGINX_OBSERVABILITY_CONF" || return 1
  fi
  if [[ -f $nginx_backup/release-backups/.absent ]]; then
    [[ ! -e $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]] || return 1
  else
    [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]] || return 1
    [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]] || return 1
    diff -qr -- "$nginx_backup/release-backups" "$NGINX_SITE_BACKUP_DIR" >/dev/null || return 1
    while IFS= read -r -d '' source; do assert_root_file "$source" 600 || return 1; done < <(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -print0)
  fi
  if [[ -f $nginx_backup/logrotate/.absent ]]; then
    [[ ! -e $NGINX_UPSTREAM_LOGROTATE && ! -L $NGINX_UPSTREAM_LOGROTATE ]] || return 1
  else
    assert_root_file "$NGINX_UPSTREAM_LOGROTATE" 644 || return 1
    cmp -s -- "$nginx_backup/logrotate/$(basename -- "$NGINX_UPSTREAM_LOGROTATE")" "$NGINX_UPSTREAM_LOGROTATE" || return 1
  fi
}
apply_nginx_recovery() {
  restore_nginx_recovery || return 1
  verify_nginx_snapshot || return 1
  local nginx_fingerprint actual_image nginx_status restored_at
  nginx_fingerprint=$(sha256sum "$recovery/config/nginx/nginx-T.txt" | awk '{print $1}') || return 1
  recovery_checkpoint_store nginx.fingerprint "$nginx_fingerprint" || return 1
  systemctl start nginx || return 1
  nginx_status=$(systemctl is-active nginx) || return 1
  [[ $nginx_status == active ]] || return 1
  restored_at=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  printf 'restored_at=%s\n' "$restored_at" > "$state_dir/nginx-recovery-restored.tmp" || return 1
  chmod 400 "$state_dir/nginx-recovery-restored.tmp" || return 1
  mv -T -- "$state_dir/nginx-recovery-restored.tmp" "$state_dir/nginx-recovery-restored" || return 1
  slot_tmp="$active_slot_file.tmp.$$"
  actual_image=$(docker inspect -f '{{.Image}}' sub2api) || return 1
  [[ $actual_image == "$pre_image_id" ]] || return 1
  printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s\n' "$old_port" "$actual_image" "$old_release_id" "$old_instance_id" > "$slot_tmp" || return 1
  chmod 600 "$slot_tmp" || return 1
  mv -T -- "$slot_tmp" "$active_slot_file" || return 1
}
verify_nginx_recovery() {
  verify_nginx_snapshot || return 1
  recovery_checkpoint_assert_file "$RECOVERY_CHECKPOINT_ROOT/nginx.fingerprint" || return 1
  [[ $(sha256sum "$recovery/config/nginx/nginx-T.txt" | awk '{print $1}') == "$(<"$RECOVERY_CHECKPOINT_ROOT/nginx.fingerprint")" ]] || return 1
  [[ $(systemctl is-active nginx) == active ]] || return 1
  [[ -f $state_dir/nginx-recovery-restored && ! -L $state_dir/nginx-recovery-restored ]] || return 1
  [[ -f $active_slot_file && ! -L $active_slot_file ]] || return 1
  [[ $(<"$active_slot_file") == "$(printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s' "$old_port" "$pre_image_id" "$old_release_id" "$old_instance_id")" ]] || return 1
  verify_application_recovery || return 1
}
docker cp "$recovery/redis/dump.rdb" sub2api-redis:/tmp/sub2api-restore.rdb >/dev/null
redis_rdb_check=$(docker exec sub2api-redis redis-check-rdb /tmp/sub2api-restore.rdb)
docker exec sub2api-redis rm -f /tmp/sub2api-restore.rdb
redis_backup_dbsize=$(sed -n 's/^\[info\] \([0-9][0-9]*\) keys read$/\1/p' <<<"$redis_rdb_check")
redis_backup_expiring=$(sed -n 's/^\[info\] \([0-9][0-9]*\) expires$/\1/p' <<<"$redis_rdb_check")
redis_already_expired=$(sed -n 's/^\[info\] \([0-9][0-9]*\) already expired$/\1/p' <<<"$redis_rdb_check")
[[ $redis_backup_dbsize =~ ^[0-9]+$ && $redis_backup_expiring =~ ^[0-9]+$ && $redis_already_expired =~ ^[0-9]+$ ]]
[[ $redis_already_expired -le $redis_backup_expiring && $redis_backup_expiring -le $redis_backup_dbsize ]]
redis_backup_persistent=$((redis_backup_dbsize - redis_backup_expiring))
trap fail_closed ERR INT TERM
recovery_checkpoint_run compose_restored verify_compose_recovery apply_compose_recovery
recovery_checkpoint_run postgres_restored verify_postgres_recovery apply_postgres_recovery
recovery_checkpoint_run redis_restored verify_redis_recovery apply_redis_recovery
recovery_checkpoint_run app_healthy verify_application_recovery apply_application_recovery
recovery_checkpoint_run nginx_restored verify_nginx_recovery apply_nginx_recovery
cleanup_recovery
trap - ERR INT TERM EXIT
printf 'coordinated_restore=verified\n'
printf 'restored_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
printf 'application_health=pass\n'
printf 'recovery_helper_bundle_sha256=%s\n' "$RECOVERY_HELPER_BUNDLE_SHA256"
