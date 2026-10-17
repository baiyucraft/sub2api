#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
if [[ -n ${RECOVERY_ASSETS_DIR:-} ]]; then
  source "$RECOVERY_ASSETS_DIR/context.sh"
else
  source /opt/sub2api/releases/.active-release/assets/context.sh
fi
[[ -f /opt/sub2api/active-app && ! -L /opt/sub2api/active-app ]]
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
_required_1=$(docker inspect -f '{{.State.Health.Status}}' "$active_container")
[[ ${_required_1} == healthy ]]
_required_2=$(systemctl is-active nginx)
[[ ${_required_2} == active ]]
managed_upstream=${NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}
active_port=$(sed -n 's/^port=//p' /opt/sub2api/active-app)
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
_required_3=$(sub2api_required_systemctl is-active sub2api-backup.service 2>/dev/null)
[[ ${_required_3} != active ]]
_required_4=$(sub2api_required_systemctl is-enabled sub2api-backup.timer 2>/dev/null)
[[ ${_required_4} == enabled ]]
[[ -f $active_claim/plaintext-cleaned && ! -L $active_claim/plaintext-cleaned ]]
if [[ $active_claim != "$release_dir/.recovered" ]]; then
  _required_arg_1=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  printf 'release_id=%s\nrecovered_at=%s\n' "$release_id" "${_required_arg_1}" > "$active_claim/marker"
  chmod 400 "$active_claim/marker"
  [[ ! -e $release_dir/.recovered && ! -L $release_dir/.recovered ]]
  mv -T -- "$active_claim" "$release_dir/.recovered"
fi
[[ -d $release_dir/.recovered && ! -L $release_dir/.recovered &&
   ! -e /opt/sub2api/releases/.active-release && ! -L /opt/sub2api/releases/.active-release ]]
printf 'release_claim_reconciled=true\n'
