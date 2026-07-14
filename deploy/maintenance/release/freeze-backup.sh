#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
exec 9>/run/lock/sub2api-backup-global.lock
flock -n 9
STATE_ROOT=/opt/sub2api/backups/release-state STATE_DIR="$state_dir" "$assets_dir/mask-backup-units.sh"
RELEASE_LOCK_HELD=true RELEASE_DIR="$release_dir" "$assets_dir/freeze.sh"
RELEASE_LOCK_HELD=true RELEASE_DIR="$release_dir" "$assets_dir/backup.sh"
