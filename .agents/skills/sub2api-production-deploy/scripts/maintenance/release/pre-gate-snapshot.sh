#!/usr/bin/env bash
set -Eeuo pipefail

output_dir=${OUTPUT_DIR:?OUTPUT_DIR is required}
expected_image_id=${EXPECTED_IMAGE_ID:?EXPECTED_IMAGE_ID is required}
deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
[[ $output_dir =~ ^/opt/sub2api/releases/pre-gate\.[A-Za-z0-9]+$ ]]
[[ $expected_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ -d $output_dir && ! -L $output_dir && $(stat -c '%U:%G:%a' "$output_dir") == root:root:700 ]]
exec 9>/run/lock/sub2api-production-release.lock
flock -n 9
exec 8>/run/lock/sub2api-backup-global.lock
flock -n 8

active_slot=/opt/sub2api/active-app
[[ -f $active_slot && ! -L $active_slot ]]
active_container=$(sed -n 's/^container=//p' "$active_slot")
active_image=$(sed -n 's/^image_id=//p' "$active_slot")
[[ $active_container =~ ^[A-Za-z0-9_.-]{1,80}$ ]]
[[ $active_image == "$expected_image_id" ]]
[[ $(docker inspect -f '{{.Image}}' "$active_container") == "$expected_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-postgres) == healthy ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]

work="$output_dir/work"
recovery="$output_dir/production-recovery.tar"
compatibility="$output_dir/production-current-image.tar.gz"
cleanup() {
  code=$?
  docker exec sub2api-redis rm -f /tmp/sub2api-pre-gate.rdb >/dev/null 2>&1 || true
  rm -rf "$work"
  if (( code != 0 )); then rm -f "$recovery" "$compatibility"; fi
  exit "$code"
}
trap cleanup EXIT
install -d -m 700 "$work/database" "$work/redis" "$work/config"
docker exec sub2api-postgres pg_dump -U sub2api -d sub2api -Fc -Z 1 > "$work/database/sub2api.dump"
[[ -s $work/database/sub2api.dump ]]

redis_password=$(docker inspect sub2api-redis | jq -er '
  ((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $args
  | ($args | index("--requirepass")) as $index
  | if $index != null and ($index + 1) < ($args | length)
    then $args[$index + 1]
    else ([ $args[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first)
    end
')
[[ -n $redis_password ]]
docker exec sub2api-redis rm -f /tmp/sub2api-pre-gate.rdb
printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c '
  IFS= read -r REDISCLI_AUTH
  export REDISCLI_AUTH
  exec redis-cli --no-auth-warning --rdb /tmp/sub2api-pre-gate.rdb
' >/dev/null 2>&1
redis_rdb_check=$(docker exec sub2api-redis redis-check-rdb /tmp/sub2api-pre-gate.rdb 2>&1)
redis_rdb_keys=$(sed -n 's/^\[info\] \([0-9][0-9]*\) keys read$/\1/p' <<<"$redis_rdb_check")
redis_rdb_expires=$(sed -n 's/^\[info\] \([0-9][0-9]*\) expires$/\1/p' <<<"$redis_rdb_check")
redis_rdb_already_expired=$(sed -n 's/^\[info\] \([0-9][0-9]*\) already expired$/\1/p' <<<"$redis_rdb_check")
[[ $redis_rdb_keys =~ ^[0-9]+$ && $redis_rdb_expires =~ ^[0-9]+$ && $redis_rdb_already_expired =~ ^[0-9]+$ ]]
docker cp sub2api-redis:/tmp/sub2api-pre-gate.rdb "$work/redis/dump.rdb" >/dev/null 2>&1
docker exec sub2api-redis rm -f /tmp/sub2api-pre-gate.rdb
[[ -s $work/redis/dump.rdb ]]

[[ -d $deploy_dir/data && ! -L $deploy_dir/data ]]
cp -a "$deploy_dir/data" "$work/config/data"
printf 'current_image_id=%s\nredis_keys=%s\nredis_expires=%s\nredis_already_expired=%s\n' \
  "$active_image" "$redis_rdb_keys" "$redis_rdb_expires" "$redis_rdb_already_expired" > "$work/manifest"
(cd "$work" && find . -type f ! -name SHA256SUMS -print0 | LC_ALL=C sort -z | xargs -0 sha256sum > SHA256SUMS)
tar -C "$work" -cf "$recovery" .
tar -tf "$recovery" >/dev/null
docker save "$expected_image_id" 2>/dev/null | gzip -1 > "$compatibility"
[[ -s $compatibility ]]
chmod 600 "$recovery" "$compatibility"

[[ $(docker inspect -f '{{.Image}}' "$active_container") == "$expected_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
printf 'production_image_id=%s\n' "$expected_image_id"
printf 'recovery_sha256=%s\n' "$(sha256sum "$recovery" | awk '{print $1}')"
printf 'compatibility_sha256=%s\n' "$(sha256sum "$compatibility" | awk '{print $1}')"
printf 'restore_points_verified=true\n'
trap - EXIT
rm -rf "$work"
