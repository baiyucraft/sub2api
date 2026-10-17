from __future__ import annotations

import shlex

from .manifest import sha256_file
from .ssh import SSHRunner


def bootstrap_production(profile_name: str, runner: SSHRunner | None = None) -> dict[str, str]:
    runner = runner or SSHRunner()
    trusted_key = __import__("pathlib").Path(__file__).resolve().parent / "trust" / "vm-gate-ed25519.pub"
    trust_sha = sha256_file(trusted_key)
    # External Bash owns errexit even when the caller tests its exit status.
    initialize = r'''
site=$1
managed_upstream=$2
active_slot=$3
transaction_id=$4
sed -i -E 's#^([[:space:]]*)proxy_pass[[:space:]]+http://127\.0\.0\.1:18080;[[:space:]]*$#\1proxy_pass http://sub2api_release_backend;#' "$site"
upstream_tmp="$managed_upstream.tmp.$transaction_id"
printf 'upstream sub2api_release_backend {\n    server 127.0.0.1:18080;\n    keepalive 128;\n}\n' > "$upstream_tmp"
chmod 600 "$upstream_tmp"
mv -T -- "$upstream_tmp" "$managed_upstream"
nginx -t >/dev/null 2>&1
systemctl reload nginx
slot_tmp="$active_slot.tmp.$transaction_id"
active_image=$(docker inspect -f '{{.Image}}' sub2api)
[[ $active_image =~ ^sha256:[0-9a-f]{64}$ ]]
printf 'container=sub2api\nport=18080\nimage_id=%s\n' "$active_image" > "$slot_tmp"
chmod 600 "$slot_tmp"
mv -T -- "$slot_tmp" "$active_slot"
'''
    recover = r'''
site_backup=$1
site=$2
managed_upstream=$3
active_slot=$4
transaction_id=$5
install -m 600 "$site_backup" "$site"
rm -f -- "$managed_upstream" "$managed_upstream.tmp.$transaction_id" "$active_slot.tmp.$transaction_id"
nginx -t >/dev/null 2>&1
systemctl reload nginx >/dev/null 2>&1
test ! -e "$active_slot"
test ! -L "$active_slot"
'''
    script = f"""
set -Eeuo pipefail
trust=/opt/sub2api-release-trust/vm-gate-ed25519.pub
test -f "$trust"
test ! -L "$trust"
required_value=$(sha256sum "$trust" | awk '{{print $1}}')
test "$required_value" = {trust_sha}
test ! -e /opt/sub2api/releases/.active-release
test ! -L /opt/sub2api/releases/.active-release
for container in sub2api-postgres sub2api-redis; do
  required_value=$(docker inspect -f '{{{{.State.Health.Status}}}}' "$container")
  test "$required_value" = healthy
done
required_value=$(systemctl is-active nginx)
test "$required_value" = active
backup_service_code=0
backup_service_state=$(systemctl is-active sub2api-backup.service 2>/dev/null) || backup_service_code=$?
case "$backup_service_state:$backup_service_code" in inactive:3|failed:3) ;; *) exit 1 ;; esac
install -d -m 700 /opt/sub2api/releases /opt/sub2api/backups/release-state
active_slot=/opt/sub2api/active-app
managed_upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
if test ! -e "$active_slot"; then
  test ! -L "$active_slot"
  required_value=$(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api)
  test "$required_value" = healthy
  test ! -e "$managed_upstream"
  test ! -L "$managed_upstream"
  nginx_text=$(nginx -T 2>&1)
  legacy_proxy_count=$(grep -Ec '^[[:space:]]*proxy_pass[[:space:]]+http://127\.0\.0\.1:18080;[[:space:]]*$' <<<"$nginx_text")
  test "$legacy_proxy_count" -ge 1
  site=$(grep -Rl --include='*' -E '^[[:space:]]*proxy_pass[[:space:]]+http://127\.0\.0\.1:18080;[[:space:]]*$' /etc/nginx/sites-enabled)
  required_value=$(wc -l <<<"$site")
  test "$required_value" = 1
  test -f "$site"
  test ! -L "$site"
  site_backup="$site.sub2api-release-backup"
  install -m 600 "$site" "$site_backup"
  if ! bash -Eeuo pipefail -c {shlex.quote(initialize)} -- "$site" "$managed_upstream" "$active_slot" "$$"; then
    if bash -Eeuo pipefail -c {shlex.quote(recover)} -- "$site_backup" "$site" "$managed_upstream" "$active_slot" "$$"; then
      exit 1
    else
      printf 'production_bootstrap_recovery=unknown\\n'
      exit 125
    fi
  fi
fi
test -f "$active_slot"
test ! -L "$active_slot"
required_value=$(stat -c '%a' "$active_slot")
test "$required_value" = 600
test -f "$managed_upstream"
test ! -L "$managed_upstream"
required_value=$(stat -c '%a' "$managed_upstream")
test "$required_value" = 600
required_value=$(grep -c '^container=' "$active_slot")
test "$required_value" = 1
required_value=$(grep -c '^port=' "$active_slot")
test "$required_value" = 1
required_value=$(grep -c '^image_id=' "$active_slot")
test "$required_value" = 1
grep -Eq '^container=[a-zA-Z0-9_.-]+$' "$active_slot"
grep -Eq '^port=(18080|18081)$' "$active_slot"
grep -Eq '^image_id=sha256:[0-9a-f]{{64}}$' "$active_slot"
active_container=$(sed -n 's/^container=//p' "$active_slot")
active_port=$(sed -n 's/^port=//p' "$active_slot")
active_image=$(sed -n 's/^image_id=//p' "$active_slot")
required_value=$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container")
test "$required_value" = healthy
required_value=$(docker inspect -f '{{{{.Image}}}}' "$active_container")
test "$required_value" = "$active_image"
grep -Fq "server 127.0.0.1:$active_port;" "$managed_upstream"
nginx -t >/dev/null 2>&1
required_value=$(curl -sS -o /dev/null -w '%{{http_code}}' http://127.0.0.1:$active_port/health)
test "$required_value" = 200
wrapper=/usr/local/libexec/sub2api-backup-global-lock
dropin=/etc/systemd/system/sub2api-backup.service.d/10-global-lock.conf
current=$(systemctl show sub2api-backup.service -p ExecStart --value)
current_path=$(awk 'match($0,/path=[^ ;}}]+/) {{ print substr($0,RSTART+5,RLENGTH-5); exit }}' <<<"$current")
[[ $current_path == "$wrapper" ]]
test -f "$wrapper"
test ! -L "$wrapper"
grep -Fq '/run/lock/sub2api-backup-global.lock' "$wrapper"
test -f "$dropin"
test ! -L "$dropin"
required_value=$(grep -Fxc 'ExecStart=/usr/local/libexec/sub2api-backup-global-lock' "$dropin")
[[ $required_value == 1 ]]
printf 'production_bootstrap=true\ntrust_ready=true\nbackup_lock_ready=true\nblue_green_ready=true\nactive_port=%s\nproduction_bootstrap_recovery=not_needed\n' "$active_port"
"""
    return runner.run(
        "racknerd",
        script,
        {"production_bootstrap", "trust_ready", "backup_lock_ready", "blue_green_ready", "active_port", "production_bootstrap_recovery"},
    ).values
