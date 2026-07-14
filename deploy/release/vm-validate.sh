#!/usr/bin/env bash
set -Eeuo pipefail

required_commands=(awk chmod cp curl date df diff docker find flock git grep gzip head id install jq ln mkdir mv rm sed seq sha256sum sleep sort ss stat tr xargs)
for command_name in "${required_commands[@]}"; do
  command -v "$command_name" >/dev/null 2>&1 || exit 127
done
docker info >/dev/null 2>&1
git --version >/dev/null 2>&1

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
test_tag="sub2api:vm-test-$commit"
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
mark_stage() {
  printf '%s\n' "$1" > "$state_dir/stage.tmp"
  mv -T -- "$state_dir/stage.tmp" "$state_dir/stage"
}
mark_stage preflight

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
mark_stage candidate_build
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
probe_suffix=${release_id//[^a-zA-Z0-9]/}
probe_db="sub2api_probe_${probe_suffix:0:24}"
probe_dir="$state_dir/probe-data"
probe_redis="sub2api-probe-redis-${probe_suffix:0:12}"
probe_app="sub2api-probe-app-${probe_suffix:0:12}"
probe_network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' sub2api-postgres)
redis_image=$(docker inspect -f '{{.Config.Image}}' sub2api-redis)
database_owner=$(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d postgres -c "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname='"'"'sub2api_dev'"'"'"' | tr -d '\r')
cleanup_probe() {
  docker rm -f "$probe_app" "$probe_redis" >/dev/null 2>&1 || true
  docker exec sub2api-postgres sh -lc "dropdb --if-exists -U \"\${POSTGRES_USER:-postgres}\" $probe_db" >/dev/null 2>&1 || true
  rm -rf "$probe_dir" "$state_dir/probe.dump"
}
on_failure() {
  code=$?
  trap - ERR INT TERM
  cleanup_probe
  rm -f "$state_dir/candidate.tar.gz"
  docker image rm "$tag" >/dev/null 2>&1 || true
  exit "$code"
}
trap on_failure ERR INT TERM

mark_stage isolated_database
install -d -m 700 "$probe_dir"
docker exec sub2api-postgres sh -lc 'pg_dump -Fc -Z 1 -U "${POSTGRES_USER:-postgres}" -d sub2api_dev' > "$state_dir/probe.dump"
docker exec -i -e DB_OWNER="$database_owner" sub2api-postgres sh -lc 'psql -X -v ON_ERROR_STOP=1 -v db_owner="$DB_OWNER" -U "${POSTGRES_USER:-postgres}" -d postgres' >/dev/null <<SQL
SELECT format('CREATE DATABASE %I OWNER %I', '$probe_db', :'db_owner') \gexec
SQL
docker exec -i sub2api-postgres sh -lc "pg_restore --exit-on-error -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db" < "$state_dir/probe.dump" >/dev/null
cp -a "$data_dir/." "$probe_dir/"
sed -i "/^database:/,/^[^[:space:]]/ s/^[[:space:]]*dbname:[[:space:]]*.*/  dbname: $probe_db/" "$probe_dir/config.yaml"
sed -i '/^database:/,/^[^[:space:]]/ s/^[[:space:]]*host:[[:space:]]*.*/  host: sub2api-postgres/' "$probe_dir/config.yaml"

mark_stage isolated_redis
docker run -d --name "$probe_redis" --network "$probe_network" --network-alias probe-redis "$redis_image" redis-server --save '' --appendonly no >/dev/null
for _ in $(seq 1 30); do
  [[ $(docker exec "$probe_redis" redis-cli PING 2>/dev/null | tr -d '\r') == PONG ]] && break
  sleep 1
done
[[ $(docker exec "$probe_redis" redis-cli PING 2>/dev/null | tr -d '\r') == PONG ]]
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*host:[[:space:]]*.*/  host: probe-redis/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*port:[[:space:]]*.*/  port: 6379/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*password:[[:space:]]*.*/  password: ""/' "$probe_dir/config.yaml"
sed -i '/^redis:/,/^[^[:space:]]/ s/^[[:space:]]*db:[[:space:]]*.*/  db: 0/' "$probe_dir/config.yaml"

mark_stage migrate_candidate
docker run --rm --network "$probe_network" -v "$probe_dir:/app/data" "$candidate_image_id" /app/sub2api --migrate-only >/dev/null 2>&1
mark_stage candidate_health
docker run -d --name "$probe_app" --network "$probe_network" \
  --health-cmd "wget -q -T 5 -O /dev/null http://127.0.0.1:$server_port/health || exit 1" \
  --health-interval 5s --health-timeout 5s --health-start-period 5s --health-retries 6 \
  -v "$probe_dir:/app/data" "$candidate_image_id" >/dev/null
for _ in $(seq 1 30); do
  [[ $(docker inspect -f '{{.State.Health.Status}}' "$probe_app") == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' "$probe_app") == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' "$probe_app") == healthy ]]

mark_stage migration_assertions
migration_checksum=$(jq -er '.migration_sha256["182_upstream_actual_rate_multiplier.sql"]' "$manifest")
recorded_checksum=$(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT checksum FROM schema_migrations WHERE filename='182_upstream_actual_rate_multiplier.sql'\"")
[[ $recorded_checksum == "$migration_checksum" ]]
[[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.upstream_key_id IS NOT NULL AND (a.rate_multiplier IS DISTINCT FROM k.rate_multiplier OR a.priority IS DISTINCT FROM ROUND(k.rate_multiplier*100)::int)\"") == 0 ]]
[[ $(docker exec sub2api-postgres sh -lc "psql -X -A -t -U \"\${POSTGRES_USER:-postgres}\" -d $probe_db -c \"SELECT COUNT(*) FROM accounts WHERE extra ?| ARRAY['upstream_rate_multiplier','upstream_source_rate_multiplier','upstream_recharge_rate','upstream_effective_cost_multiplier','sub2api_upstream_rate_multiplier']\"") == 0 ]]

mark_stage isolated_cleanup
cleanup_probe
[[ $(docker inspect -f '{{.Image}}' sub2api-dev) == "$old_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) == healthy ]]
[[ $(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${POSTGRES_USER:-postgres}" -d sub2api_dev -c "SELECT COUNT(*) FROM schema_migrations WHERE filename='"'"'182_upstream_actual_rate_multiplier.sql'"'"'"') == 0 ]]
integration_verified=true
vm_restore_verified=true

docker save "$candidate_image_id" | gzip -1 > "$state_dir/candidate.tar.gz"
candidate_archive_sha=$(sha256sum "$state_dir/candidate.tar.gz" | awk '{print $1}')
trap - ERR INT TERM

mark_stage gate_signing
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
mark_stage verified
printf 'gate_status=verified\n'
printf 'candidate_image_id=%s\n' "$candidate_image_id"
printf 'candidate_archive_sha256=%s\n' "$candidate_archive_sha"
