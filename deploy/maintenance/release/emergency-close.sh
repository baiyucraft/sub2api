#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
systemctl stop nginx
[[ $(systemctl is-active nginx 2>/dev/null || true) != active ]]
[[ -d $active_claim && ! -L $active_claim ]]
printf 'public_traffic_closed=true\n'
