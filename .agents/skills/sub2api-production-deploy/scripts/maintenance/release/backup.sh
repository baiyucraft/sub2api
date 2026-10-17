#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
backup_attempt_id=${BACKUP_ATTEMPT_ID:?BACKUP_ATTEMPT_ID is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
source "$assets_dir/nginx-ingress-contract.sh"
source "$assets_dir/recovery-compose.sh"
[[ $backup_attempt_id == "$release_id-1" || $backup_attempt_id == "$release_id-2" || $backup_attempt_id == "$release_id-3" ]]
backup_root=${BACKUP_ROOT:-$deploy_dir/backups/automated}
recipient_file=${AGE_RECIPIENT_FILE:-/root/.config/sub2api-backup/age-recipient.txt}
review_schema=$(jq -er '.manifest.recovery_gate.schema // 1' "$release_dir/gate.json")
if [[ $review_schema == 2 ]]; then
  # The current full review proves possession and actual recovery with this
  # exact dedicated recipient. Bind release backup encryption to the same
  # public key without changing the daily recipient or copying any identity.
  [[ $recipient_file == "$release_dir/backup-recipient.txt" ]]
  _required_1=$(realpath -e -- "$recipient_file")
  [[ -f $recipient_file && ! -L $recipient_file && ${_required_1} == "$recipient_file" ]]
  _required_2=$(stat -c '%U:%G:%a:%h' "$recipient_file")
  [[ ${_required_2} == root:root:400:1 ]]
  approved_recipient=$(jq -er '.manifest.recovery_gate.full_recovery_evidence.payload.source | select(.recipient_type == "age1") | .approved_recipient_sha256' "$release_dir/gate.json")
  [[ $approved_recipient =~ ^[0-9a-f]{64}$ ]]
  observed_recipient=$(python3 - "$recipient_file" <<'PY'
import hashlib, pathlib, re, sys
raw=pathlib.Path(sys.argv[1]).read_bytes()
if re.fullmatch(rb'age1[023456789acdefghjklmnpqrstuvwxyz]{58}\n', raw) is None: raise SystemExit(1)
print(hashlib.sha256(raw[:-1]).hexdigest())
PY
)
  [[ $observed_recipient == "$approved_recipient" ]]
fi
upload_key=${BACKUP_UPLOAD_KEY:-/root/.ssh/sub2api_backup_upload}
upload_target=${BACKUP_UPLOAD_TARGET:-sub2api-backup@47.85.205.94}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
umask 077
work="$backup_root/.release-$release_id-$timestamp"
plain="$backup_root/sub2api-$release_id-$timestamp.tar"
encrypted="$plain.age"
transport="sub2api-$timestamp.tar.age"
backup_stage=initializing
result_file="$state_dir/backup-result"
result_checksum="$state_dir/backup-result.sha256"
failure_file="$state_dir/backup-failure"
redis_password=$(docker inspect sub2api-redis | jq -er '
  ((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $args
  | ($args | index("--requirepass")) as $index
  | if $index != null and ($index + 1) < ($args | length)
    then $args[$index + 1]
    else ([ $args[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first)
    end
')
[[ -n $redis_password ]]

cleanup() {
  code=$?
  if (( code != 0 )); then
    printf 'attempt_id=%s\nstage=%s\nexit_code=%s\n' "$backup_attempt_id" "$backup_stage" "$code" > "$failure_file.tmp"
    chmod 400 "$failure_file.tmp"
    mv -f "$failure_file.tmp" "$failure_file"
  fi
  docker exec sub2api-redis rm -f /tmp/sub2api-release.rdb >/dev/null 2>&1 || true
  rm -rf "$work" "$plain"
  exit "$code"
}
trap cleanup EXIT
rm -f "$result_file" "$result_checksum" "$failure_file" "$failure_file.tmp"
backup_stage=preflight
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
cd "$deploy_dir"
load_release_compose_files "$deploy_dir"
_required_3=$(docker inspect -f '{{.State.Health.Status}}' "$active_container")
[[ ${_required_3} == healthy ]]
_required_4=$(systemctl is-active nginx)
[[ ${_required_4} == active ]]
_required_5=$(sub2api_required_systemctl is-active sub2api-backup.service 2>/dev/null)
[[ ${_required_5} != active ]]
_required_6=$(sub2api_required_systemctl is-active sub2api-backup.timer 2>/dev/null)
[[ ${_required_6} != active ]]
_required_7=$(sub2api_required_systemctl is-enabled sub2api-backup.service 2>/dev/null)
[[ ${_required_7} == masked ]]
_required_8=$(sub2api_required_systemctl is-enabled sub2api-backup.timer 2>/dev/null)
[[ ${_required_8} == masked ]]
_required_9=$(docker image inspect -f '{{.Id}}' "$candidate_image_id")
[[ ${_required_9} == "$candidate_image_id" ]]
_required_10=$(docker inspect -f '{{.Image}}' "$active_container")
[[ ${_required_10} == "$(<"$state_dir/pre-image-id")" ]]
compose_image=$(_recovery_compose_config "$deploy_dir" "${release_compose_args[@]}" | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
_required_11=$(docker image inspect -f '{{.Id}}' "$compose_image")
[[ ${_required_11} == "$(<"$state_dir/pre-image-id")" ]]
if [[ ${RELEASE_LOCK_HELD:-false} != true ]]; then
  exec 9>/run/lock/sub2api-backup-global.lock
  flock -n 9
fi
install -d -m 700 "$work/database" "$work/redis" "$work/config/app" "$work/config/nginx" "$work/config/certbot" "$work/metadata"
backup_stage=database
docker exec sub2api-postgres pg_dump -U sub2api -d sub2api -Fc -Z 6 > "$work/database/sub2api.dump"
docker exec sub2api-postgres pg_dumpall -U sub2api --globals-only > "$work/database/globals.sql"
[[ -s $work/database/sub2api.dump ]]
backup_stage=redis
redis_command() {
  printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c '
    IFS= read -r REDISCLI_AUTH
    export REDISCLI_AUTH
    exec redis-cli --no-auth-warning "$@"
  ' sh "$@"
}
docker exec sub2api-redis rm -f /tmp/sub2api-release.rdb
redis_command --rdb /tmp/sub2api-release.rdb >/dev/null
redis_rdb_check=$(docker exec sub2api-redis redis-check-rdb /tmp/sub2api-release.rdb)
redis_rdb_keys=$(sed -n 's/^\[info\] \([0-9][0-9]*\) keys read$/\1/p' <<<"$redis_rdb_check")
redis_rdb_expires=$(sed -n 's/^\[info\] \([0-9][0-9]*\) expires$/\1/p' <<<"$redis_rdb_check")
redis_rdb_already_expired=$(sed -n 's/^\[info\] \([0-9][0-9]*\) already expired$/\1/p' <<<"$redis_rdb_check")
[[ $redis_rdb_keys =~ ^[0-9]+$ && $redis_rdb_expires =~ ^[0-9]+$ && $redis_rdb_already_expired =~ ^[0-9]+$ ]]
[[ $redis_rdb_already_expired -le $redis_rdb_expires && $redis_rdb_expires -le $redis_rdb_keys ]]
printf 'keys=%s\nexpires=%s\nalready_expired=%s\n' "$redis_rdb_keys" "$redis_rdb_expires" "$redis_rdb_already_expired" > "$work/metadata/redis-rdb-counts.txt"
docker cp sub2api-redis:/tmp/sub2api-release.rdb "$work/redis/dump.rdb" >/dev/null
docker exec sub2api-redis rm -f /tmp/sub2api-release.rdb
[[ -s $work/redis/dump.rdb ]]
_required_12=$(docker inspect -f '{{.State.Health.Status}}' sub2api-redis)
[[ ${_required_12} == healthy ]]
backup_stage=config
recovery_compose_snapshot "$deploy_dir" "$work/config/app" >/dev/null
cp -a "$deploy_dir/data" "$work/config/app/data"
(cd "$work/config/app/data" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > "$work/metadata/data.sha256")
nginx -T > "$work/config/nginx/nginx-T.txt" 2>&1
install -d -m 700 "$work/config/nginx/sites-enabled" "$work/config/nginx/conf.d" "$work/config/nginx/snippets" "$work/config/nginx/release-backups" "$work/config/nginx/logrotate"
[[ -f /etc/nginx/nginx.conf && ! -L /etc/nginx/nginx.conf ]]
[[ -d /etc/nginx/sites-enabled && ! -L /etc/nginx/sites-enabled ]]
[[ -d /etc/nginx/conf.d && ! -L /etc/nginx/conf.d ]]
[[ -d /etc/nginx/snippets && ! -L /etc/nginx/snippets ]]
cp -p /etc/nginx/nginx.conf "$work/config/nginx/nginx.conf"
_required_15=$(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf)
_required_14=$(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf)
_required_13=$(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf)
[[ ${_required_15} == root:root:600:1 ||
   ${_required_14} == root:root:640:1 ||
   ${_required_13} == root:root:644:1 ]]
managed_site=$(find_managed_nginx_site)
managed_site_stat=$(stat -c '%U:%G:%a:%h' "$managed_site")
case "$managed_site_stat" in root:root:600:1|root:root:640:1|root:root:644:1) ;; *) exit 1 ;; esac
_required_arg_1=$(basename -- "$managed_site")
cp -p "$managed_site" "$work/config/nginx/sites-enabled/${_required_arg_1}"
printf '%s\n' "$managed_site" > "$work/metadata/nginx-managed-site"
chmod 600 "$work/metadata/nginx-managed-site"
shopt -s nullglob
release_confs=(/etc/nginx/conf.d/sub2api-release-*.conf)
if [[ ${#release_confs[@]} == 0 ]]; then
  : > "$work/config/nginx/conf.d/.none"
else
  for release_conf in "${release_confs[@]}"; do
    [[ $release_conf =~ ^/etc/nginx/conf.d/sub2api-release-[A-Za-z0-9._-]{1,160}\.conf$ ]]
    [[ -f $release_conf && ! -L $release_conf ]]
    _required_16=$(stat -c '%U:%G:%a:%h' "$release_conf")
    [[ ${_required_16} == root:root:600:1 ]]
    cp -p "$release_conf" "$work/config/nginx/conf.d/"
  done
fi
if [[ -e $NGINX_INGRESS_SNIPPET || -L $NGINX_INGRESS_SNIPPET ]]; then
  [[ -f $NGINX_INGRESS_SNIPPET && ! -L $NGINX_INGRESS_SNIPPET ]]
  _required_17=$(stat -c '%U:%G:%a:%h' "$NGINX_INGRESS_SNIPPET")
  [[ ${_required_17} == root:root:600:1 ]]
  _required_arg_2=$(basename -- "$NGINX_INGRESS_SNIPPET")
  cp -p -- "$NGINX_INGRESS_SNIPPET" "$work/config/nginx/snippets/${_required_arg_2}"
else
  : > "$work/config/nginx/snippets/.absent"
fi
if [[ -e $NGINX_OBSERVABILITY_CONF || -L $NGINX_OBSERVABILITY_CONF ]]; then
  [[ -f $NGINX_OBSERVABILITY_CONF && ! -L $NGINX_OBSERVABILITY_CONF ]]
  _required_18=$(stat -c '%U:%G:%a:%h' "$NGINX_OBSERVABILITY_CONF")
  [[ ${_required_18} == root:root:600:1 ]]
  cp -p -- "$NGINX_OBSERVABILITY_CONF" "$work/config/nginx/observability.conf"
else
  : > "$work/config/nginx/observability.absent"
fi
if [[ -e $NGINX_SITE_BACKUP_DIR || -L $NGINX_SITE_BACKUP_DIR ]]; then
  [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]]
  _required_19=$(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR")
  [[ ${_required_19} == root:root:700 ]]
  _required_20=$(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 ! -type f -print -quit)
  [[ -z ${_required_20} ]]
  _required_inventory_1=()
  sub2api_capture_nul _required_inventory_1 find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f -print0
  for backup_file in "${_required_inventory_1[@]}"; do
    name=$(basename -- "$backup_file")
    [[ $name =~ ^[A-Za-z0-9._-]{1,240}$ ]]
    _required_21=$(stat -c '%U:%G:%a:%h' "$backup_file")
    [[ ${_required_21} == root:root:600:1 ]]
    cp -p -- "$backup_file" "$work/config/nginx/release-backups/$name"
  done
else
  : > "$work/config/nginx/release-backups/.absent"
fi
if [[ -e $NGINX_UPSTREAM_LOGROTATE || -L $NGINX_UPSTREAM_LOGROTATE ]]; then
  [[ -f $NGINX_UPSTREAM_LOGROTATE && ! -L $NGINX_UPSTREAM_LOGROTATE ]]
  _required_22=$(stat -c '%U:%G:%a:%h' "$NGINX_UPSTREAM_LOGROTATE")
  [[ ${_required_22} == root:root:644:1 ]]
  # The managed release policy uses rotate 14; preserve the source file byte-for-byte.
  cp -p "$NGINX_UPSTREAM_LOGROTATE" "$work/config/nginx/logrotate/"
else
  : > "$work/config/nginx/logrotate/.absent"
fi
shopt -u nullglob
cp -aL /etc/letsencrypt/live "$work/config/certbot/"
cp -a /etc/letsencrypt/archive /etc/letsencrypt/renewal "$work/config/certbot/"
docker inspect "$active_container" sub2api-postgres sub2api-redis --format '{{.Name}} {{.Config.Image}} {{.Image}}' > "$work/metadata/images.txt"
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$work/metadata/postgres.txt"
redis_command INFO keyspace > "$work/metadata/redis.txt"
redis_command DBSIZE | tr -d '\r' > "$work/metadata/redis-dbsize.txt"
redis_command --raw SCAN 0 COUNT 1 >/dev/null
backup_stage=metadata
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||count(*) FROM accounts UNION ALL SELECT 'users='||count(*) FROM users UNION ALL SELECT 'api_keys='||count(*) FROM api_keys UNION ALL SELECT 'upstream_configs='||count(*) FROM upstream_configs UNION ALL SELECT 'upstream_keys='||count(*) FROM upstream_keys" > "$work/metadata/core-counts.txt"
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM accounts t UNION ALL SELECT 'users='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM users t UNION ALL SELECT 'api_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM api_keys t UNION ALL SELECT 'upstream_configs='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_configs t UNION ALL SELECT 'upstream_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_keys t" > "$work/metadata/core-content-digests.txt"
(cd "$work/redis" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > "$work/metadata/redis-files.sha256")
printf 'release_id=%s\ncandidate_image_id=%s\npre_switch_image_id=%s\ntraffic_preserved=true\nredis_backup_mode=rdb\n' "$release_id" "$candidate_image_id" "$(<"$state_dir/pre-image-id")" > "$work/metadata/manifest.txt"
(cd "$work" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS)
backup_stage=archive
tar -C "$work" -cf "$plain" .
backup_stage=encrypt
age -R "$recipient_file" -o "$encrypted" "$plain"
artifact_sha=$(sha256sum "$encrypted" | awk '{print $1}')
backup_stage=upload
remote_result=$(ssh -i "$upload_key" -o BatchMode=yes -o StrictHostKeyChecking=yes "$upload_target" "upload daily $transport $artifact_sha" < "$encrypted")
grep -Fq "OK $transport $artifact_sha" <<<"$remote_result"
_required_23=$(docker inspect -f '{{.State.Health.Status}}' "$active_container")
[[ ${_required_23} == healthy ]]
backup_stage=local_restore_point
cp -a "$encrypted" "$state_dir/recovery-point.age"
printf '%s  recovery-point.age\n' "$artifact_sha" > "$state_dir/recovery-point.age.sha256"
install -m 600 "$plain" "$state_dir/recovery-point.tar"
plain_sha=$(sha256sum "$state_dir/recovery-point.tar" | awk '{print $1}')
printf '%s  recovery-point.tar\n' "$plain_sha" > "$state_dir/recovery-point.tar.sha256"
(cd "$state_dir" && sha256sum -c recovery-point.tar.sha256 >/dev/null)
tar -tf "$state_dir/recovery-point.tar" >/dev/null
backup_stage=commit_result
result_tmp="$result_file.tmp"
_required_arg_4=$(basename "$encrypted")
_required_arg_3=$(stat -c %s "$encrypted")
printf 'artifact=%s\ntransport_artifact=%s\nartifact_size=%s\nartifact_sha256=%s\ntraffic_preserved=true\nredis_backup_mode=rdb\nno_restart_path_proven=true\nlocal_restore_point_ready=true\n' \
  "${_required_arg_4}" "$transport" "${_required_arg_3}" "$artifact_sha" > "$result_tmp"
chmod 400 "$result_tmp"
mv -f "$result_tmp" "$result_file"
(cd "$state_dir" && sha256sum backup-result > backup-result.sha256.tmp)
chmod 400 "$result_checksum.tmp"
mv -f "$result_checksum.tmp" "$result_checksum"
rm -f "$failure_file"
cat "$result_file"
trap - EXIT
rm -rf "$work" "$plain"
