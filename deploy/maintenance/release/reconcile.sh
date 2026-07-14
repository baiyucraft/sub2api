#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
[[ -d $release_dir/.claimed && ! -L $release_dir/.claimed ]]
[[ -f $release_dir/.claimed/plaintext-cleaned && ! -L $release_dir/.claimed/plaintext-cleaned ]]
printf 'release_id=%s\nrecovered_at=%s\n' "$release_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$release_dir/.claimed/marker"
chmod 400 "$release_dir/.claimed/marker"
mv -T -- "$release_dir/.claimed" "$release_dir/.recovered"
rm -rf "$active_claim"
[[ ! -e $active_claim && ! -L $active_claim ]]
printf 'release_claim_reconciled=true\n'
