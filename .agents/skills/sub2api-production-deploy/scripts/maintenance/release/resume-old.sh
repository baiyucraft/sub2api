#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
old_port=$(sed -n 's/^port=//p' "$state_dir/pre-active-app")
old_instance_id=$(sed -n 's/^instance_id=//p' "$state_dir/pre-active-app")
old_release_id=$(sed -n 's/^release_id=//p' "$state_dir/pre-active-app")
[[ $old_port == 18080 || $old_port == 18081 ]]
[[ -z $old_instance_id || $old_instance_id =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
[[ -z $old_release_id || $old_release_id =~ ^[A-Za-z0-9_.-]{1,128}$ ]]
load_release_compose_files "$state_dir"
systemctl stop nginx >/dev/null 2>&1 || true
cd "$deploy_dir"
docker rm -f "$active_container" >/dev/null 2>&1 || true
[[ "$active_container" == sub2api ]] || docker rm -f sub2api >/dev/null 2>&1 || true
install -m 600 "$state_dir/.env" "$deploy_dir/.env"
for compose_file in "${release_compose_files[@]}"; do
  install -m 600 "$state_dir/$compose_file" "$deploy_dir/$compose_file"
done
if [[ " ${release_compose_files[*]} " != *" docker-compose.release-active.yml "* ]]; then
  [[ -f $state_dir/no-release-active-override && ! -L $state_dir/no-release-active-override ]]
  rm -f "$deploy_dir/docker-compose.release-active.yml"
fi
load_release_compose_files "$deploy_dir"
resume_base_compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
resume_network_mode=$(sub2api_compose_network_mode "$resume_base_compose_json" "$old_port")
resume_override_tmp="$deploy_dir/docker-compose.release-active.yml.resume.$$"
write_release_active_override "$resume_override_tmp" "$(<"$state_dir/pre-image-id")" "$old_instance_id" "$old_port" "$resume_network_mode"
chmod 600 "$resume_override_tmp"
mv -T -- "$resume_override_tmp" "$deploy_dir/docker-compose.release-active.yml"
env_tmp="$deploy_dir/.env.resume.$$"
awk '!/^(COMPOSE_FILE|SUB2API_RELEASE_IMAGE|BIND_HOST|SERVER_PORT)=/' "$deploy_dir/.env" > "$env_tmp"
printf 'COMPOSE_FILE=%s\n' "$(release_compose_value_with_active_override)" >> "$env_tmp"
printf 'SUB2API_RELEASE_IMAGE=%s\nBIND_HOST=127.0.0.1\nSERVER_PORT=%s\n' "$(<"$state_dir/pre-image-id")" "$old_port" >> "$env_tmp"
chmod --reference="$deploy_dir/.env" "$env_tmp"
mv -T -- "$env_tmp" "$deploy_dir/.env"
load_release_compose_files "$deploy_dir"
compose_image=$(docker compose "${release_compose_args[@]}" config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$(<"$state_dir/pre-image-id")" ]]
[[ $(assert_sub2api_compose_closure "$deploy_dir" "$old_port" "$(<"$state_dir/pre-image-id")" "$old_instance_id") == "$resume_network_mode" ]]
docker compose "${release_compose_args[@]}" up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]] && break
  sleep 2
done
assert_sub2api_runtime_contract sub2api "$(<"$state_dir/pre-image-id")" "$resume_network_mode" "$old_port"
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
systemctl start nginx
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=sub2api\nport=%s\nimage_id=%s\nrelease_id=%s\ninstance_id=%s\n' "$old_port" "$(docker inspect -f '{{.Image}}' sub2api)" "$old_release_id" "$old_instance_id" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
printf 'old_application_resumed=true\n'
printf 'running_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
