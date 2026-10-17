#!/usr/bin/env bash
set -Eeuo pipefail

gate_dir=${1:?gate directory is required}
candidate_archive=${2:?candidate archive is required}
candidate_image_id=${3:?candidate image ID is required}
release_id=${4:?release ID is required}
deploy_dir=/opt/sub2api-deploy
compose_file="$deploy_dir/docker-compose.local.yml"
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
state_dir="$deploy_dir/release-gates/$release_id"
override="$state_dir/vm-only-override.yml"
result="$state_dir/switch-result"
compose_backup="$state_dir/docker-compose.local.yml.before"

{
  required_query_1=$(id -u)
  [[ ${required_query_1} == 0 ]]
}
[[ $gate_dir == "/opt/sub2api-deploy/release-gates/$release_id/output" ]]
[[ -f "$gate_dir/gate.json" && ! -L "$gate_dir/gate.json" ]]
[[ -f "$gate_dir/gate.sig" && ! -L "$gate_dir/gate.sig" ]]
[[ -f "$candidate_archive" && ! -L "$candidate_archive" ]]
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ -d $deploy_dir && ! -L $deploy_dir && -f $compose_file && ! -L $compose_file ]]
[[ -d $state_dir && ! -L $state_dir ]]
[[ ! -e $compose_backup && ! -L $compose_backup ]]
{
  required_query_2=$(stat -c '%U:%G:%a:%h' "$unit_lock")
  [[ -f $unit_lock && ! -L $unit_lock && ${required_query_2} == root:root:600:1 ]]
}
exec 8<>"$unit_lock"
flock -n 8

public_key=/opt/sub2api-release-signer/vm-gate-ed25519.pub
[[ -f $public_key && ! -L $public_key ]]
openssl pkeyutl -verify -pubin -inkey "$public_key" -rawin -in "$gate_dir/gate.json" -sigfile "$gate_dir/gate.sig" >/dev/null 2>&1
{
  required_query_3=$(jq -er '.manifest.scope' "$gate_dir/gate.json")
  [[ ${required_query_3} == vm-only ]]
}
{
  required_query_4=$(jq -er '.manifest.release_id' "$gate_dir/gate.json")
  [[ ${required_query_4} == "$release_id" ]]
}
{
  required_query_5=$(jq -er '.evidence.candidate_image_id' "$gate_dir/gate.json")
  [[ ${required_query_5} == "$candidate_image_id" ]]
}
{
  required_query_6=$(sha256sum "$candidate_archive" | awk '{print $1}')
  required_query_7=$(jq -er '.evidence.candidate_archive_sha256' "$gate_dir/gate.json")
  [[ ${required_query_6} == "${required_query_7}" ]]
}

{
  required_query_8=$(docker inspect -f '{{.Name}}' sub2api-dev)
  [[ ${required_query_8} == /sub2api-dev ]]
}
{
  required_query_9=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev)
  [[ ${required_query_9} == healthy ]]
}
{
  required_query_10=$(docker inspect -f '{{.HostConfig.NetworkMode}}' sub2api-dev)
  [[ ${required_query_10} == host ]]
}
ss -H -ltn | awk '$4 ~ /:8211$/ {found=1} END {exit !found}'
{
  required_query_11=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' sub2api-dev)
  [[ ${required_query_11} == /opt/sub2api-deploy/data-dev ]]
}
old_image_id=$(docker inspect -f '{{.Image}}' sub2api-dev)

loaded_image=$(gzip -dc "$candidate_archive" | docker load | sed -n 's/^Loaded image ID: //p' | tail -n1)
if [[ -n $loaded_image && $loaded_image != "$candidate_image_id" ]]; then
  printf 'failure=loaded_image_mismatch\n' >"$result"
  exit 1
fi
{
  required_query_12=$(docker image inspect -f '{{.Id}}' "$candidate_image_id")
  [[ ${required_query_12} == "$candidate_image_id" ]]
}

cat >"$override" <<YAML
services:
  sub2api-dev:
    image: $candidate_image_id
YAML
chmod 600 "$override"
cp -p -- "$compose_file" "$compose_backup"
awk -v candidate="$candidate_image_id" '
  /^  sub2api-dev:[[:space:]]*$/ { in_app=1 }
  in_app && /^  [^[:space:]][^:]*:/ && $0 !~ /^  sub2api-dev:[[:space:]]*$/ { in_app=0 }
  in_app && /^    image:[[:space:]]*/ { $0="    image: " candidate; replaced=1 }
  { print }
  END { if (!replaced) exit 1 }
' "$compose_file" >"$compose_file.vm-only.tmp"
chmod --reference="$compose_file" "$compose_file.vm-only.tmp"
mv -f -- "$compose_file.vm-only.tmp" "$compose_file"

rollback() {
  code=$?
  if (( code != 0 )); then
    printf 'failure=rollback_started\nold_image_id=%s\n' "$old_image_id" >"$result"
    cp -p -- "$compose_backup" "$compose_file"
    sed "s#image: .*#image: $old_image_id#" "$override" >"$override.rollback"
    docker compose -f "$compose_file" -f "$override.rollback" up -d --no-deps --force-recreate sub2api-dev >/dev/null 2>&1 || {
      printf 'failure=rollback_failed\nold_image_id=%s\n' "$old_image_id" >"$result"
      exit 98
    }
    for _ in $(seq 1 60); do
      if probe_health=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev 2>/dev/null) && [[ $probe_health == healthy ]]; then
        break
      fi
      sleep 2
    done
    {
      required_query_13=$(docker inspect -f '{{.Image}}' sub2api-dev) || return $?
      [[ ${required_query_13} == "$old_image_id" ]]
    }
    {
      required_query_14=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev) || return $?
      [[ ${required_query_14} == healthy ]]
    }
    printf 'failure=switched_candidate_failed_rolled_back\nold_image_id=%s\n' "$old_image_id" >"$result"
  fi
  rm -f -- "$override" "$override.rollback"
  exit "$code"
}
trap rollback EXIT

docker compose -f "$compose_file" -f "$override" up -d --no-deps --force-recreate sub2api-dev >/dev/null 2>&1
for _ in $(seq 1 90); do
  if probe_health=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev 2>/dev/null) && [[ $probe_health == healthy ]]; then
    break
  fi
  sleep 2
done
{
  required_query_15=$(docker inspect -f '{{.Image}}' sub2api-dev)
  [[ ${required_query_15} == "$candidate_image_id" ]]
}
{
  required_query_16=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev)
  [[ ${required_query_16} == healthy ]]
}
{
  required_query_17=$(curl -fsS --max-time 10 -o /dev/null -w '%{http_code}' http://192.168.31.199:8211/health)
  [[ ${required_query_17} == 200 ]]
}
{
  required_query_18=$(curl -fsS --max-time 10 -o /dev/null -w '%{http_code}' http://192.168.31.199:8211/)
  [[ ${required_query_18} == 200 ]]
}
{
  required_query_19=$(docker ps -a --format '{{.Names}}' | grep -Fc 'sub2api-dev')
  [[ ${required_query_19} == 1 ]]
}
printf 'switch=verified\nold_image_id=%s\ncandidate_image_id=%s\nport=8211\nhealth=200\npage=200\n' "$old_image_id" "$candidate_image_id" >"$result"
trap - EXIT
rm -f -- "$override"
printf 'switch=verified\nold_image_id=%s\ncandidate_image_id=%s\nport=8211\nhealth=200\npage=200\n' "$old_image_id" "$candidate_image_id"
