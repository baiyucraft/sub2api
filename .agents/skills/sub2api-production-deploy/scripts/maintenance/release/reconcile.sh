#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ -f /opt/sub2api/active-app && ! -L /opt/sub2api/active-app ]]
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
[[ $(docker inspect -f '{{.State.Health.Status}}' "$active_container") == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
managed_upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
active_port=$(sed -n 's/^port=//p' /opt/sub2api/active-app)
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
[[ -f $active_claim/plaintext-cleaned && ! -L $active_claim/plaintext-cleaned ]]
printf 'release_id=%s\nrecovered_at=%s\n' "$release_id" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/marker"
chmod 400 "$active_claim/marker"
mv -T -- "$active_claim" "$release_dir/.recovered"
[[ -d $release_dir/.recovered && ! -L $release_dir/.recovered && ! -e $active_claim ]]
printf 'release_claim_reconciled=true\n'
