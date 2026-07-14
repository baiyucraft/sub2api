#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ $(docker inspect -f '{{.Image}}' sub2api) == "$candidate_image_id" ]]
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
nginx -t >/dev/null 2>&1
systemctl start nginx
[[ $(systemctl is-active nginx) == active ]]
printf 'public_traffic_enabled=true\n'
