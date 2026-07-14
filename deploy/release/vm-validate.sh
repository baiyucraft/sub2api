#!/usr/bin/env bash
set -Eeuo pipefail

manifest=${1:?manifest path is required}
output_dir=${2:?output directory is required}
source_dir=/opt/sub2api-src
deploy_dir=/opt/sub2api-deploy
data_dir="$deploy_dir/data-dev"
state_root="$deploy_dir/release-gates"
[[ $(id -u) == 0 ]]
[[ -f $manifest && ! -L $manifest ]]
commit=$(jq -er '.commit_sha' "$manifest")
release_id=$(jq -er '.release_id' "$manifest")
version=0.1.153-baiyu
tag="sub2api:baiyu-$version-$commit"
[[ $commit =~ ^[0-9a-f]{40}$ ]]
[[ $release_id =~ ^182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $(jq -er '.profile' "$manifest") == 182 ]]
[[ $(jq -er '.vm_identity' "$manifest") == sub2api-dev ]]
[[ $(jq -er '.origin' "$manifest") == https://github.com/baiyucraft/sub2api.git ]]
[[ $(jq -er '.vm_validator_sha256' "$manifest") == "$(sha256sum "$0" | awk '{print $1}')" ]]
state_dir="$state_root/$release_id"
[[ $output_dir == "$state_dir/output" ]]
[[ ! -e $state_dir && ! -L $state_dir ]]
install -d -m 700 "$state_root"
exec 9>"$state_root/release.lock"
flock -n 9
install -d -m 700 "$state_dir" "$state_dir/backup" "$output_dir"
install -m 400 "$manifest" "$state_dir/manifest.json"
manifest="$state_dir/manifest.json"

cd "$source_dir"
[[ -f .sub2api-deploy-worktree ]]
[[ $(git remote get-url origin) == https://github.com/baiyucraft/sub2api.git ]]
[[ -z $(git status --porcelain --untracked-files=all | grep -v '^?? .sub2api-deploy-worktree$' || true) ]]
git fetch origin main >/dev/null 2>&1
[[ $(git rev-parse origin/main) == "$commit" ]]
git reset --hard "$commit" >/dev/null
while IFS=$'\t' read -r relative expected; do
  [[ $relative =~ ^deploy/(release\.py|release/([^/]+|trust/[^/]+)|maintenance/release/[^/]+|maintenance/181/(mask-backup-units|restore-backup-units)\.sh)$ ]]
  [[ -f $source_dir/$relative && ! -L $source_dir/$relative ]]
  [[ $(sha256sum "$source_dir/$relative" | awk '{print $1}') == "$expected" ]]
done < <(jq -r '.release_asset_sha256 | to_entries[] | [.key,.value] | @tsv' "$manifest")
[[ -d $data_dir && ! -L $data_dir ]]
old_image_id=$(docker inspect -f '{{.Image}}' sub2api-dev)
old_image_ref=$(docker inspect -f '{{.Config.Image}}' sub2api-dev)
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]]
extract_config() {
  local section=$1 field=$2
  sed -n "/^${section}:/,/^[^[:space:]]/p" "$data_dir/config.yaml" | sed -n "s/^[[:space:]]*${field}:[[:space:]]*//p" | head -n1 | tr -d '"'
}
db_host=$(extract_config database host)
redis_host=$(extract_config redis host)
server_port=$(extract_config server port)
[[ $db_host =~ ^(127\.0\.0\.1|localhost)$ ]]
[[ $redis_host =~ ^(127\.0\.0\.1|localhost)$ ]]
[[ $server_port =~ ^[0-9]+$ ]]
[[ $(ss -ltn | awk '$4 ~ /:5432$/ {n++} END{print n+0}') -ge 1 ]]
[[ $(ss -ltn | awk '$4 ~ /:6379$/ {n++} END{print n+0}') -ge 1 ]]
[[ $(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT COUNT(*) FROM schema_migrations WHERE filename='"'"'182_upstream_actual_rate_multiplier.sql'"'"'"') == 0 ]]

free_before=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
[[ $free_before -gt 2684354560 ]]
export DOCKER_BUILDKIT=1
docker build --network=host --progress=plain \
  --build-arg NODE_IMAGE=docker.m.daocloud.io/library/node:24-alpine \
  --build-arg GOLANG_IMAGE=docker.m.daocloud.io/library/golang:1.26.5-alpine \
  --build-arg ALPINE_IMAGE=docker.m.daocloud.io/library/alpine:3.21 \
  --build-arg POSTGRES_IMAGE=docker.m.daocloud.io/library/postgres:18-alpine \
  --build-arg COMMIT="$commit" --build-arg VERSION="$version" \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" -t "$tag" . >/dev/null 2>&1
candidate_image_id=$(docker image inspect -f '{{.Id}}' "$tag")
candidate_size=$(docker image inspect -f '{{.Size}}' "$tag")
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
free_after_build=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
[[ $free_after_build -gt 2147483648 ]]
docker save "$candidate_image_id" | gzip -1 > "$state_dir/candidate.tar.gz"
candidate_archive_sha=$(sha256sum "$state_dir/candidate.tar.gz" | awk '{print $1}')

docker run --rm --network host -v "$source_dir:/src:ro" -v sub2api-vm-go-mod:/go/pkg/mod -v sub2api-vm-go-build:/root/.cache/go-build -w /src/backend \
  docker.m.daocloud.io/library/golang:1.26.5-alpine sh -lc \
  'go test ./internal/service -run "TestNormalizeUpstreamActualRate|TestUpstreamKeyRateDTOJSONUsesSingleActualRateContract" -count=1' >/dev/null 2>&1

restore_required=false
redis_source=""
restore_vm() (
  set -Eeuo pipefail
  if docker inspect sub2api-dev-pre-release >/dev/null 2>&1; then
    docker rm -f sub2api-dev >/dev/null 2>&1 || true
    docker rename sub2api-dev-pre-release sub2api-dev
  else
    docker stop sub2api-dev >/dev/null 2>&1 || true
  fi
  docker exec sub2api-postgres sh -lc 'psql -X -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='"'"'sub2api_dev'"'"' AND pid <> pg_backend_pid();" >/dev/null'
  docker exec sub2api-postgres sh -lc 'dropdb --if-exists -U "${POSTGRES_USER:-postgres}" sub2api_dev && createdb -U "${POSTGRES_USER:-postgres}" sub2api_dev'
  docker exec -i sub2api-postgres sh -lc 'pg_restore --exit-on-error --no-owner -U "${POSTGRES_USER:-postgres}" -d sub2api_dev' < "$state_dir/backup/postgres.dump"
  docker stop sub2api-redis >/dev/null
  find "$redis_source" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -a "$state_dir/backup/redis-data/." "$redis_source/"
  (cd "$redis_source" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$state_dir/backup/redis-data-restored.sha256"
  diff -u "$state_dir/backup/redis-data.sha256" "$state_dir/backup/redis-data-restored.sha256" >/dev/null
  docker start sub2api-redis >/dev/null
  for _ in $(seq 1 60); do
    [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
    sleep 1
  done
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
  [[ $(docker exec sub2api-redis sh -lc 'export REDISCLI_AUTH="${REDIS_PASSWORD:-}"; redis-cli --no-auth-warning DBSIZE' | tr -d '\r') == "$(<"$state_dir/backup/redis-dbsize")" ]]
  find "$data_dir" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
  cp -a "$state_dir/backup/data-dev/." "$data_dir/"
  (cd "$data_dir" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$state_dir/backup/data-dev-restored.sha256"
  diff -u "$state_dir/backup/data-dev.sha256" "$state_dir/backup/data-dev-restored.sha256" >/dev/null
  docker start sub2api-dev >/dev/null
  for _ in $(seq 1 90); do
    [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]] && break
    sleep 2
  done
  [[ $(docker inspect -f '{{.Image}}' sub2api-dev) == "$old_image_id" ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
  [[ $(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$server_port/health") == 200 ]]
  [[ $(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT COUNT(*) FROM schema_migrations WHERE filename='"'"'182_upstream_actual_rate_multiplier.sql'"'"'"') == 0 ]]
  docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT '"'"'accounts='"'"'||count(*) FROM accounts UNION ALL SELECT '"'"'users='"'"'||count(*) FROM users UNION ALL SELECT '"'"'upstream_configs='"'"'||count(*) FROM upstream_configs UNION ALL SELECT '"'"'upstream_keys='"'"'||count(*) FROM upstream_keys"' > "$state_dir/backup/core-counts-restored.txt"
  diff -u "$state_dir/backup/core-counts.txt" "$state_dir/backup/core-counts-restored.txt" >/dev/null
  docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT '"'"'accounts='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM accounts t UNION ALL SELECT '"'"'users='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM users t UNION ALL SELECT '"'"'upstream_configs='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM upstream_configs t UNION ALL SELECT '"'"'upstream_keys='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM upstream_keys t"' > "$state_dir/backup/core-content-digests-restored.txt"
  diff -u "$state_dir/backup/core-content-digests.txt" "$state_dir/backup/core-content-digests-restored.txt" >/dev/null
)
resume_vm_without_restore() (
  set -Eeuo pipefail
  docker start sub2api-redis >/dev/null 2>&1 || true
  for _ in $(seq 1 60); do
    [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
    sleep 1
  done
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
  docker start sub2api-dev >/dev/null 2>&1 || true
  for _ in $(seq 1 90); do
    [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]] && break
    sleep 2
  done
  [[ $(docker inspect -f '{{.Image}}' sub2api-dev) == "$old_image_id" ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
)
on_failure() {
  code=$?
  trap - ERR INT TERM
  if [[ $restore_required == true ]]; then
    restore_vm || exit 125
  else
    resume_vm_without_restore || exit 125
  fi
  rm -rf "$state_dir/backup"
  rm -f "$state_dir/candidate.tar.gz"
  exit "$code"
}
trap on_failure ERR INT TERM

docker stop sub2api-dev >/dev/null
docker exec sub2api-postgres sh -lc 'pg_dump -Fc -Z 6 -U "${POSTGRES_USER:-postgres}" -d sub2api_dev' > "$state_dir/backup/postgres.dump"
[[ -s $state_dir/backup/postgres.dump ]]
docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT '"'"'accounts='"'"'||count(*) FROM accounts UNION ALL SELECT '"'"'users='"'"'||count(*) FROM users UNION ALL SELECT '"'"'upstream_configs='"'"'||count(*) FROM upstream_configs UNION ALL SELECT '"'"'upstream_keys='"'"'||count(*) FROM upstream_keys"' > "$state_dir/backup/core-counts.txt"
docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT '"'"'accounts='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM accounts t UNION ALL SELECT '"'"'users='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM users t UNION ALL SELECT '"'"'upstream_configs='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM upstream_configs t UNION ALL SELECT '"'"'upstream_keys='"'"'||md5(COALESCE(string_agg(md5(row_to_json(t)::text),'"'"''"'"' ORDER BY id),'"'"''"'"')) FROM upstream_keys t"' > "$state_dir/backup/core-content-digests.txt"
docker exec sub2api-redis sh -lc 'export REDISCLI_AUTH="${REDIS_PASSWORD:-}"; redis-cli --no-auth-warning BGSAVE >/dev/null || true; for i in $(seq 1 120); do value=$(redis-cli --no-auth-warning INFO persistence | tr -d "\r"); progress=$(printf "%s\n" "$value" | awk -F: '"'"'$1=="rdb_bgsave_in_progress"{print $2}'"'"'); status=$(printf "%s\n" "$value" | awk -F: '"'"'$1=="rdb_last_bgsave_status"{print $2}'"'"'); [ "$progress" = 0 ] && [ "$status" = ok ] && exit 0; sleep 1; done; exit 1'
redis_source=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' sub2api-redis)
[[ -n $redis_source && -d $redis_source ]]
docker exec sub2api-redis sh -lc 'export REDISCLI_AUTH="${REDIS_PASSWORD:-}"; redis-cli --no-auth-warning DBSIZE' | tr -d '\r' > "$state_dir/backup/redis-dbsize"
docker stop sub2api-redis >/dev/null
cp -a "$redis_source/." "$state_dir/backup/redis-data"
(cd "$state_dir/backup/redis-data" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$state_dir/backup/redis-data.sha256"
docker start sub2api-redis >/dev/null
for _ in $(seq 1 60); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]] && break
  sleep 1
done
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-redis) == healthy ]]
cp -a "$data_dir" "$state_dir/backup/data-dev"
(cd "$data_dir" && find . -type f -print0 | sort -z | xargs -0 sha256sum) > "$state_dir/backup/data-dev.sha256"
printf '%s\n' "$old_image_id" > "$state_dir/backup/old-image-id"
printf '%s\n' "$old_image_ref" > "$state_dir/backup/old-image-ref"
(cd "$state_dir/backup" && find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS)

restore_required=true
docker rename sub2api-dev sub2api-dev-pre-release
docker run --rm --network host -v "$data_dir:/app/data" "$candidate_image_id" /app/sub2api --migrate-only >/dev/null 2>&1
docker run -d --name sub2api-dev --restart unless-stopped --network host \
  --health-cmd "wget -q -T 5 -O /dev/null http://127.0.0.1:$server_port/health || exit 1" \
  --health-interval 30s --health-timeout 10s --health-start-period 10s --health-retries 3 \
  -v "$data_dir:/app/data" "$candidate_image_id" >/dev/null
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-dev) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api-dev) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
migration_checksum=$(jq -er '.migration_sha256["182_upstream_actual_rate_multiplier.sql"]' "$manifest")
recorded_checksum=$(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT checksum FROM schema_migrations WHERE filename='"'"'182_upstream_actual_rate_multiplier.sql'"'"'"')
[[ $recorded_checksum == "$migration_checksum" ]]
[[ $(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT COUNT(*) FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.upstream_key_id IS NOT NULL AND (a.rate_multiplier IS DISTINCT FROM k.rate_multiplier OR a.priority IS DISTINCT FROM ROUND(k.rate_multiplier*100)::int)"') == 0 ]]
[[ $(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT COUNT(*) FROM accounts WHERE extra ?| ARRAY['"'"'upstream_rate_multiplier'"'"','"'"'upstream_source_rate_multiplier'"'"','"'"'upstream_recharge_rate'"'"','"'"'upstream_effective_cost_multiplier'"'"','"'"'sub2api_upstream_rate_multiplier'"'"']"') == 0 ]]
integration_verified=true

trap - ERR INT TERM
restore_vm || exit 125
restore_required=false
vm_restore_verified=true

jq -n --slurpfile manifest "$manifest" \
  --arg candidate_image_id "$candidate_image_id" \
  --arg candidate_archive_sha256 "$candidate_archive_sha" \
  --argjson candidate_size "$candidate_size" \
  '{manifest:$manifest[0],evidence:{candidate_image_id:$candidate_image_id,candidate_archive_sha256:$candidate_archive_sha256,candidate_size:$candidate_size,integration_verified:true,vm_restore_verified:true,vm_database_boundary:true,vm_redis_boundary:true,data_dev_boundary:true}}' \
  | jq -cS . > "$output_dir/gate.json.tmp"
chmod 400 "$output_dir/gate.json.tmp"
mv -T -- "$output_dir/gate.json.tmp" "$output_dir/gate.json"
/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json" "$output_dir/gate.sig"
ln "$state_dir/candidate.tar.gz" "$output_dir/candidate.tar.gz"
sha256sum "$output_dir/gate.json" "$output_dir/gate.sig" "$output_dir/candidate.tar.gz" > "$output_dir/SHA256SUMS"
chmod 400 "$output_dir/gate.json" "$output_dir/gate.sig" "$output_dir/candidate.tar.gz" "$output_dir/SHA256SUMS"
rm -rf "$state_dir/backup"
rm -f "$state_dir/candidate.tar.gz"
printf 'gate_status=verified\n'
printf 'candidate_image_id=%s\n' "$candidate_image_id"
printf 'candidate_archive_sha256=%s\n' "$candidate_archive_sha"
