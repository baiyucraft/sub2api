#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
[[ -d $state_dir && ! -L $state_dir ]]
(cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
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
compose_image=$(docker compose "${release_compose_args[@]}" config --format json | jq -r '.services.sub2api.image // empty')
[[ -n $compose_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$compose_image") == "$(<"$state_dir/pre-image-id")" ]]
docker compose "${release_compose_args[@]}" up -d --no-deps --force-recreate sub2api >/dev/null 2>&1
for _ in $(seq 1 90); do
  [[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api) == healthy ]] && break
  sleep 2
done
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$(<"$state_dir/pre-image-id")" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
systemctl start nginx
slot_tmp="$active_slot_file.tmp.$$"
printf 'container=sub2api\nport=18080\nimage_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot_file"
printf 'old_application_resumed=true\n'
printf 'running_image_id=%s\n' "$(docker inspect -f '{{.Image}}' sub2api)"
