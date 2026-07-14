#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
expected_image_id=${EXPECTED_IMAGE_ID:?EXPECTED_IMAGE_ID is required}
expected_auto_sync=${EXPECTED_AUTO_SYNC:?EXPECTED_AUTO_SYNC is required}
expected_host_port=${EXPECTED_HOST_PORT:-18080}
cd "$deploy_dir"

compose_json=$(docker compose config --format json)
rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ -n $rendered_image ]]
[[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$expected_image_id" ]]
jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
jq -e --arg port "$expected_host_port" '(.services.sub2api.network_mode == "host" and .services.sub2api.environment.SERVER_HOST == "127.0.0.1" and (.services.sub2api.environment.SERVER_PORT | tostring) == $port) or ((.services.sub2api.ports // []) | any(.target == 8080 and (.published | tostring) == $port and .host_ip == "127.0.0.1"))' <<<"$compose_json" >/dev/null
[[ $(jq -r '.services.sub2api.environment.UPSTREAM_SYNC_AUTO_ENABLED // empty' <<<"$compose_json") == "$expected_auto_sync" ]]

printf 'compose_image_id_verified=true\n'
printf 'compose_loopback_verified=true\n'
printf 'compose_data_mount_verified=true\n'
printf 'compose_auto_sync=%s\n' "$expected_auto_sync"
