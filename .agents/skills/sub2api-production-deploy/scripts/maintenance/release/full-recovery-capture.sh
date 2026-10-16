#!/usr/bin/env bash
set -Eeuo pipefail

# Pre-Gate or postrelease encrypted baseline capture. This helper never switches traffic or
# changes release claims. It reads the live stores into a private temporary
# directory while holding the existing backup lock, then emits only a digest
# receipt. The caller must perform the independent VM Gate and recovery review.

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_id=${RELEASE_ID:?RELEASE_ID is required}
recipient_file=${AGE_RECIPIENT_FILE:?AGE_RECIPIENT_FILE is required}
output_dir=${CAPTURE_OUTPUT_DIR:?CAPTURE_OUTPUT_DIR is required}
work_root=${CAPTURE_WORK_ROOT:-/var/lib/sub2api/recovery-capture}
age_bin=${AGE_BIN:-age}
expected_image=${EXPECTED_PRE_IMAGE_ID:?EXPECTED_PRE_IMAGE_ID is required}
active_container=${ACTIVE_CONTAINER:?ACTIVE_CONTAINER is required}
capture_purpose=${CAPTURE_PURPOSE:-pre_gate}
assets_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
umask 077
[[ $(id -u) == 0 ]]
[[ $expected_image =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $active_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $capture_purpose == pre_gate || $capture_purpose == postrelease ]]
[[ $release_id =~ ^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ -d $deploy_dir && ! -L $deploy_dir && $(realpath -e -- "$deploy_dir") == "$deploy_dir" ]]
[[ -f $recipient_file && ! -L $recipient_file && $(stat -c '%u:%g:%a:%h' "$recipient_file") == 0:0:600:1 ]]
recipient=$(tr -d '\r\n' < "$recipient_file")
[[ $recipient =~ ^age1[0-9a-z]{10,120}$ ]]
command -v "$age_bin" >/dev/null
[[ -d $output_dir && ! -L $output_dir && $(realpath -e -- "$output_dir") == "$output_dir" ]]
[[ $(stat -c '%u:%g:%a' "$output_dir") == 0:0:700 ]]
[[ $work_root == /var/lib/sub2api/recovery-capture && ! -L $work_root ]]
[[ -d /var/lib/sub2api && ! -L /var/lib/sub2api && $(realpath -e /var/lib/sub2api) == /var/lib/sub2api ]]
if [[ ! -e $work_root ]]; then mkdir -m 700 "$work_root"; fi
[[ -d $work_root && $(realpath -e -- "$work_root") == "$work_root" && $(stat -c '%u:%g:%a' "$work_root") == 0:0:700 ]]
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9

stamp=$(date -u +%Y%m%dT%H%M%SZ)
work="$work_root/.full-$release_id-$stamp"
plain="$work_root/.full-$release_id-$stamp.tar"
artifact="$output_dir/sub2api-$stamp.tar.age"
receipt="$output_dir/$release_id-capture-receipt.json"
[[ ! -e $work && ! -L $work && ! -e $plain && ! -L $plain && ! -e $artifact && ! -L $artifact && ! -e $receipt && ! -L $receipt && ! -e $receipt.tmp && ! -L $receipt.tmp ]]
mkdir -m 700 "$work"
snapshot_in=
snapshot_pid=
redis_path=/tmp/sub2api-full-recovery-$release_id.rdb
redis_owned=false
capture_stage=preflight
capture_failed=false
capture_error() {
  local failed_line=$1 failed_code=$2
  if [[ $capture_failed == false ]]; then
    capture_failed=true
    printf 'capture_stage=%s\nfailed_line=%s\nexit_code=%s\n' "$capture_stage" "$failed_line" "$failed_code" >&2
  fi
  return "$failed_code"
}
trap 'capture_error "$LINENO" "$?"' ERR
cleanup() {
  code=$?
  if [[ -n $snapshot_in ]]; then
    printf 'ROLLBACK;\n\\q\n' >&"$snapshot_in" 2>/dev/null || true
    exec {snapshot_in}>&-
  fi
  if [[ -n $snapshot_pid ]]; then wait "$snapshot_pid" >/dev/null 2>&1 || true; fi
  if [[ $redis_owned == true ]]; then docker exec sub2api-redis rm -f -- "$redis_path" >/dev/null 2>&1 || true; fi
  [[ $work == "$work_root/.full-$release_id-$stamp" && ! -L $work ]]
  rm -rf -- "$work" "$plain"
  if (( code != 0 )); then rm -f -- "$artifact" "$receipt" "$receipt.tmp"; fi
  exit "$code"
}
trap cleanup EXIT

source "$assets_dir/recovery-compose.sh"
source "$assets_dir/nginx-ingress-contract.sh"
[[ -f $deploy_dir/active-app && ! -L $deploy_dir/active-app ]]
[[ $(stat -c '%u:%g:%a:%h' "$deploy_dir/active-app") == 0:0:600:1 ]]
[[ $(sed -n 's/^container=//p' "$deploy_dir/active-app") == "$active_container" ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]


[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-postgres) == healthy ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
pre_image_id=$(docker inspect -f '{{.Image}}' "$active_container")
[[ $pre_image_id == "$expected_image" ]]
compose_root="$work/config/app"
install -d -o root -g root -m 700 "$work/database" "$work/redis" "$compose_root" "$work/config/nginx" "$work/config/certbot" "$work/metadata"

# Each store is internally consistent. PostgreSQL export and Redis RDB are
# deliberately labelled per-store; this capture does not claim a cross-store
# write freeze while the production application is serving traffic.
# Keep the exported read-only transaction alive until every PostgreSQL
# observer has imported precisely the same snapshot. No data values leave the
# private work directory; hashes and counts are only used by the restore.
capture_stage=postgres_snapshot
coproc PG_SNAPSHOT { docker exec -i sub2api-postgres psql -X -q -A -t -v ON_ERROR_STOP=1 -U sub2api -d sub2api; }
snapshot_pid=$PG_SNAPSHOT_PID
exec {snapshot_in}>&"${PG_SNAPSHOT[1]}"
printf "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET LOCAL idle_in_transaction_session_timeout='15min'; SELECT pg_export_snapshot();\n" >&"$snapshot_in"
IFS= read -r -t 30 snapshot_id <&"${PG_SNAPSHOT[0]}"
[[ $snapshot_id =~ ^[0-9A-F]{8}-[0-9A-F]{8}-[0-9]+$ ]]
timeout 900 docker exec sub2api-postgres pg_dump -U sub2api -d sub2api --snapshot="$snapshot_id" -Fc -Z 6 > "$work/database/sub2api.dump"
docker exec sub2api-postgres pg_dumpall -U sub2api --globals-only > "$work/database/globals.sql"
[[ -s $work/database/sub2api.dump && -s $work/database/globals.sql ]]
pg_snapshot_query() {
  docker exec sub2api-postgres psql -X -q -A -t -v ON_ERROR_STOP=1 -U sub2api -d sub2api -c "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET TRANSACTION SNAPSHOT '$snapshot_id'; $1; COMMIT;"
}
pg_snapshot_query "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$work/metadata/postgres.txt"
pg_snapshot_query "SELECT 'accounts='||count(*) FROM accounts UNION ALL SELECT 'users='||count(*) FROM users UNION ALL SELECT 'api_keys='||count(*) FROM api_keys UNION ALL SELECT 'upstream_configs='||count(*) FROM upstream_configs UNION ALL SELECT 'upstream_keys='||count(*) FROM upstream_keys" > "$work/metadata/core-counts.txt"
pg_snapshot_query "SELECT 'accounts='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM accounts t UNION ALL SELECT 'users='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM users t UNION ALL SELECT 'api_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM api_keys t UNION ALL SELECT 'upstream_configs='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_configs t UNION ALL SELECT 'upstream_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_keys t" > "$work/metadata/core-content-digests.txt"
pg_snapshot_query "SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$work/metadata/migrations.txt"
printf 'ROLLBACK;\n\\q\n' >&"$snapshot_in"
exec {snapshot_in}>&-
snapshot_in=
wait "$snapshot_pid"
snapshot_pid=
capture_stage=redis_snapshot
redis_password=$(docker inspect sub2api-redis | jq -er '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $args | ($args | index("--requirepass")) as $index | if $index != null and ($index + 1) < ($args | length) then $args[$index + 1] else ([ $args[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first) end')
[[ -n $redis_password ]]
docker exec sub2api-redis test ! -e "$redis_path"
redis_owned=true
printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec redis-cli --no-auth-warning --rdb "$1"' sh "$redis_path" >/dev/null
rdb_check=$(docker exec sub2api-redis redis-check-rdb "$redis_path")
redis_keys=$(sed -n 's/^\[info\] \([0-9][0-9]*\) keys read$/\1/p' <<<"$rdb_check")
redis_expires=$(sed -n 's/^\[info\] \([0-9][0-9]*\) expires$/\1/p' <<<"$rdb_check")
redis_expired=$(sed -n 's/^\[info\] \([0-9][0-9]*\) already expired$/\1/p' <<<"$rdb_check")
[[ $redis_keys =~ ^[0-9]+$ && $redis_expires =~ ^[0-9]+$ && $redis_expired =~ ^[0-9]+$ ]]
[[ $redis_expired -le $redis_expires && $redis_expires -le $redis_keys ]]
printf 'keys=%s\nexpires=%s\nalready_expired=%s\n' "$redis_keys" "$redis_expires" "$redis_expired" > "$work/metadata/redis-rdb-counts.txt"
docker cp "sub2api-redis:$redis_path" "$work/redis/dump.rdb" >/dev/null
docker exec sub2api-redis rm -f -- "$redis_path" >/dev/null
redis_owned=false
[[ -s $work/redis/dump.rdb ]]
(cd "$work/redis" && sha256sum ./dump.rdb > "$work/metadata/redis-files.sha256")

capture_stage=compose_snapshot
recovery_compose_snapshot "$deploy_dir" "$work/config/app" >/dev/null
cp -a "$deploy_dir/data" "$work/config/app/data"
# The live bind-mount root is application-owned 0777. The isolated archive
# boundary never needs that write permission: tighten only our copied root,
# retain its actual data file ownership and bytes, and never chmod production.
[[ -d $work/config/app/data && ! -L $work/config/app/data ]]
chmod 700 "$work/config/app/data"
(cd "$work/config/app/data" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > "$work/metadata/data.sha256")
capture_stage=nginx_snapshot
nginx -T > "$work/config/nginx/nginx-T.txt" 2>&1
install -d -m 700 "$work/config/nginx/sites-enabled" "$work/config/nginx/conf.d" "$work/config/nginx/snippets" "$work/config/nginx/release-backups" "$work/config/nginx/logrotate"
[[ -f /etc/nginx/nginx.conf && ! -L /etc/nginx/nginx.conf ]]
[[ -d /etc/nginx/sites-enabled && ! -L /etc/nginx/sites-enabled ]]
[[ -d /etc/nginx/conf.d && ! -L /etc/nginx/conf.d ]]
[[ -d /etc/nginx/snippets && ! -L /etc/nginx/snippets ]]
cp -p /etc/nginx/nginx.conf "$work/config/nginx/nginx.conf"
[[ $(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf) == root:root:600:1 ||
   $(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf) == root:root:640:1 ||
   $(stat -c '%U:%G:%a:%h' /etc/nginx/nginx.conf) == root:root:644:1 ]]
managed_site=$(find_managed_nginx_site)
managed_site_stat=$(stat -c '%U:%G:%a:%h' "$managed_site")
case "$managed_site_stat" in root:root:600:1|root:root:640:1|root:root:644:1) ;; *) exit 1 ;; esac
cp -p "$managed_site" "$work/config/nginx/sites-enabled/$(basename -- "$managed_site")"
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
    [[ $(stat -c '%U:%G:%a:%h' "$release_conf") == root:root:600:1 ]]
    cp -p "$release_conf" "$work/config/nginx/conf.d/"
  done
fi
if [[ -e $NGINX_INGRESS_SNIPPET || -L $NGINX_INGRESS_SNIPPET ]]; then
  [[ -f $NGINX_INGRESS_SNIPPET && ! -L $NGINX_INGRESS_SNIPPET ]]
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_INGRESS_SNIPPET") == root:root:600:1 ]]
  cp -p -- "$NGINX_INGRESS_SNIPPET" "$work/config/nginx/snippets/$(basename -- "$NGINX_INGRESS_SNIPPET")"
else
  : > "$work/config/nginx/snippets/.absent"
fi
if [[ -e $NGINX_OBSERVABILITY_CONF || -L $NGINX_OBSERVABILITY_CONF ]]; then
  [[ -f $NGINX_OBSERVABILITY_CONF && ! -L $NGINX_OBSERVABILITY_CONF ]]
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_OBSERVABILITY_CONF") == root:root:600:1 ]]
  cp -p -- "$NGINX_OBSERVABILITY_CONF" "$work/config/nginx/observability.conf"
else
  : > "$work/config/nginx/observability.absent"
fi
if [[ -e $NGINX_SITE_BACKUP_DIR || -L $NGINX_SITE_BACKUP_DIR ]]; then
  [[ -d $NGINX_SITE_BACKUP_DIR && ! -L $NGINX_SITE_BACKUP_DIR ]]
  [[ $(stat -c '%U:%G:%a' "$NGINX_SITE_BACKUP_DIR") == root:root:700 ]]
  [[ -z $(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 ! -type f -print -quit) ]]
  while IFS= read -r -d '' backup_file; do
    name=$(basename -- "$backup_file")
    [[ $name =~ ^[A-Za-z0-9._-]{1,240}$ ]]
    [[ $(stat -c '%U:%G:%a:%h' "$backup_file") == root:root:600:1 ]]
    cp -p -- "$backup_file" "$work/config/nginx/release-backups/$name"
  done < <(find "$NGINX_SITE_BACKUP_DIR" -mindepth 1 -maxdepth 1 -type f -print0)
else
  : > "$work/config/nginx/release-backups/.absent"
fi
if [[ -e $NGINX_UPSTREAM_LOGROTATE || -L $NGINX_UPSTREAM_LOGROTATE ]]; then
  [[ -f $NGINX_UPSTREAM_LOGROTATE && ! -L $NGINX_UPSTREAM_LOGROTATE ]]
  [[ $(stat -c '%U:%G:%a:%h' "$NGINX_UPSTREAM_LOGROTATE") == root:root:644:1 ]]
  # The managed release policy uses rotate 14; preserve the source file byte-for-byte.
  cp -p "$NGINX_UPSTREAM_LOGROTATE" "$work/config/nginx/logrotate/"
else
  : > "$work/config/nginx/logrotate/.absent"
fi
shopt -u nullglob
capture_stage=certificate_snapshot
cp -aL /etc/letsencrypt/live "$work/config/certbot/"
cp -a /etc/letsencrypt/archive /etc/letsencrypt/renewal "$work/config/certbot/"

image_manifest=$(docker inspect "$active_container" sub2api-postgres sub2api-redis --format '{{.Name}} {{.Config.Image}} {{.Image}}')
printf '%s\n' "$image_manifest" > "$work/metadata/images.txt"
printf 'release_id=%s\ncandidate_image_id=%s\npre_switch_image_id=%s\ncompose_render_sha256=%s\npostgres_consistency=single_export_snapshot\npostgres_globals_consistency=independent_catalog_read\nredis_consistency=single_rdb_snapshot\ncross_store_writes_frozen=false\n' "$release_id" "$pre_image_id" "$pre_image_id" "$(<"$compose_root/.recovery-compose-render.sha256")" > "$work/metadata/manifest.txt"
# Historical archive readers call the restoration target pre_switch_image_id.
# State the actual capture purpose/image separately; postrelease never claims
# that its current candidate was the application's earlier production image.
printf 'capture_purpose=%s\ncaptured_image_id=%s\n' "$capture_purpose" "$pre_image_id" >> "$work/metadata/manifest.txt"
printf 'pg_dump_sha256=%s\nredis_rdb_sha256=%s\n' "$(sha256sum "$work/database/sub2api.dump" | awk '{print $1}')" "$(sha256sum "$work/redis/dump.rdb" | awk '{print $1}')" > "$work/metadata/store-digests.txt"
[[ $(docker inspect -f '{{.Image}}' "$active_container") == "$expected_image" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
capture_stage=archive_preflight
[[ -z $(find "$work" -mindepth 1 ! -type f ! -type d -print -quit) ]]
[[ -z $(find "$work" -type f -links +1 -print -quit) ]]
[[ -z $(find "$work" -perm /7022 -print -quit) ]]
(cd "$work" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS)
capture_stage=archive
tar -C "$work" -cf "$plain" .
plain_sha=$(sha256sum "$plain" | awk '{print $1}')
capture_stage=encryption
"$age_bin" -R "$recipient_file" -o "$artifact" "$plain" >/dev/null
chmod 600 "$artifact"
artifact_sha=$(sha256sum "$artifact" | awk '{print $1}')
artifact_size=$(stat -c '%s' "$artifact")
capture_stage=receipt
python3 - "$receipt" "$release_id" "$pre_image_id" "$plain_sha" "$artifact_sha" "$artifact_size" "$stamp" "$(<"$compose_root/.recovery-compose-render.sha256")" "$(sha256sum "$work/database/sub2api.dump" | awk '{print $1}')" "$(sha256sum "$work/redis/dump.rdb" | awk '{print $1}')" "$capture_purpose" <<'PY'
import json, os, sys
path, release, image, plain, artifact, size, stamp, compose, pg, redis, purpose = sys.argv[1:]
doc = {"schema": 1, "kind": "encrypted-production-baseline-capture", "release_id": release, "pre_switch_image_id": image, "recovery_point_sha256": plain, "artifact_sha256": artifact, "artifact_bytes": int(size), "captured_at": stamp, "compose_render_sha256": compose, "postgres_dump_sha256": pg, "redis_rdb_sha256": redis, "postgres_consistency": "single_export_snapshot", "postgres_globals_consistency": "independent_catalog_read", "redis_consistency": "single_rdb_snapshot", "cross_store_writes_frozen": False}
doc.update(capture_purpose=purpose, captured_image_id=image)
tmp = path + ".tmp"
fd = os.open(tmp, os.O_WRONLY|os.O_CREAT|os.O_EXCL, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as stream:
    json.dump(doc, stream, sort_keys=True, separators=(",", ":")); stream.write("\n"); stream.flush(); os.fsync(stream.fileno())
os.replace(tmp, path)
PY
# Leave EXIT cleanup armed through the final receipt; it owns only this
# capture's new paths, not the existing timer, claim or backup assets.
rm -rf -- "$work" "$plain"
capture_stage=completed
printf 'capture_receipt=%s\nartifact_sha256=%s\nartifact_bytes=%s\n' "$receipt" "$artifact_sha" "$artifact_size"
