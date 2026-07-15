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
recovery="$state_dir/recovery"
rm -rf "$recovery"
install -d -m 700 "$recovery"
cleanup_recovery() { rm -rf "$recovery"; }
fail_closed() {
  code=$?
  set +e
  systemctl stop nginx >/dev/null 2>&1
  docker stop sub2api >/dev/null 2>&1
  cleanup_recovery
  exit "$code"
}
trap fail_closed ERR INT TERM
trap cleanup_recovery EXIT
systemctl stop nginx
docker rm -f sub2api >/dev/null 2>&1 || true
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
age -d -i /root/.config/sub2api-backup/age-identity.txt "$state_dir/recovery-point.age" | tar -C "$recovery" -xf -
(cd "$recovery" && sha256sum -c SHA256SUMS >/dev/null)
docker exec sub2api-postgres psql -X -v ON_ERROR_STOP=1 -U sub2api -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='sub2api' AND pid<>pg_backend_pid();" >/dev/null
docker exec sub2api-postgres dropdb --if-exists -U sub2api sub2api
docker exec sub2api-postgres createdb -U sub2api -O sub2api sub2api
docker exec -i sub2api-postgres pg_restore --exit-on-error --no-owner -U sub2api -d sub2api < "$recovery/database/sub2api.dump"
redis_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis)
docker stop sub2api-redis >/dev/null
find "$redis_source" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a "$recovery/redis/." "$redis_source/"
(cd "$redis_source" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$recovery/metadata/redis-files-restored.sha256"
diff -u "$recovery/metadata/redis-files.sha256" "$recovery/metadata/redis-files-restored.sha256" >/dev/null
docker start sub2api-redis >/dev/null
for _ in $(seq 1 60); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
  sleep 1
done
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
redis_dbsize=$(docker exec sub2api-redis sh -lc 'export REDISCLI_AUTH="${REDIS_PASSWORD:-}"; redis-cli --no-auth-warning DBSIZE' | tr -d '\r')
[[ $redis_dbsize == "$(<"$recovery/metadata/redis-dbsize.txt")" ]]
cp -a "$recovery/config/app/.env" "$deploy_dir/.env"
cp -a "$recovery/config/app/docker-compose.yml" "$deploy_dir/docker-compose.yml"
if [[ -f $recovery/config/app/docker-compose.release-active.yml && ! -L $recovery/config/app/docker-compose.release-active.yml ]]; then
  cp -a "$recovery/config/app/docker-compose.release-active.yml" "$deploy_dir/docker-compose.release-active.yml"
else
  [[ -f $recovery/config/app/no-release-active-override && ! -L $recovery/config/app/no-release-active-override ]]
  rm -f "$deploy_dir/docker-compose.release-active.yml"
fi
find "$deploy_dir/data" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a "$recovery/config/app/data/." "$deploy_dir/data/"
(cd "$deploy_dir/data" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$recovery/metadata/data-restored.sha256"
diff -u "$recovery/metadata/data.sha256" "$recovery/metadata/data-restored.sha256" >/dev/null
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||count(*) FROM accounts UNION ALL SELECT 'users='||count(*) FROM users UNION ALL SELECT 'api_keys='||count(*) FROM api_keys UNION ALL SELECT 'upstream_configs='||count(*) FROM upstream_configs UNION ALL SELECT 'upstream_keys='||count(*) FROM upstream_keys" > "$recovery/metadata/core-counts-restored.txt"
diff -u "$recovery/metadata/core-counts.txt" "$recovery/metadata/core-counts-restored.txt" >/dev/null
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT 'accounts='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM accounts t UNION ALL SELECT 'users='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM users t UNION ALL SELECT 'api_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM api_keys t UNION ALL SELECT 'upstream_configs='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_configs t UNION ALL SELECT 'upstream_keys='||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'' ORDER BY id),'')) FROM upstream_keys t" > "$recovery/metadata/core-content-digests-restored.txt"
diff -u "$recovery/metadata/core-content-digests.txt" "$recovery/metadata/core-content-digests-restored.txt" >/dev/null
docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT version(); SELECT datcollate||'|'||datctype FROM pg_database WHERE datname=current_database(); SELECT extname||'|'||extversion FROM pg_extension ORDER BY 1; SELECT filename||'|'||checksum FROM schema_migrations ORDER BY filename" > "$recovery/metadata/postgres-restored.txt"
diff -u "$recovery/metadata/postgres.txt" "$recovery/metadata/postgres-restored.txt" >/dev/null
cd "$deploy_dir"
compose_image=$(docker compose config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$(<"$state_dir/pre-image-id")" ]]
docker compose up -d --no-deps sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$(<"$state_dir/pre-image-id")" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
systemctl start nginx
cleanup_recovery
trap - ERR INT TERM EXIT
printf 'coordinated_restore=verified\n'
printf 'restored_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
printf 'application_health=pass\n'
