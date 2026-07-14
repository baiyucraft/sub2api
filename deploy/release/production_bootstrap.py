from __future__ import annotations

from .manifest import sha256_file
from .profiles import get_profile
from .ssh import SSHRunner


def bootstrap_production(profile_name: str, runner: SSHRunner | None = None) -> dict[str, str]:
    profile = get_profile(profile_name)
    runner = runner or SSHRunner()
    trusted_key = __import__("pathlib").Path(__file__).resolve().parent / "trust" / "vm-gate-ed25519.pub"
    trust_sha = sha256_file(trusted_key)
    script = f"""
set -Eeuo pipefail
trust=/opt/sub2api-release-trust/vm-gate-ed25519.pub
test -f "$trust" && test ! -L "$trust" && test "$(sha256sum "$trust" | awk '{{print $1}}')" = {trust_sha}
test ! -e /opt/sub2api/releases/.active-release && test ! -L /opt/sub2api/releases/.active-release
for container in sub2api sub2api-postgres sub2api-redis; do test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$container")" = healthy; done
test "$(systemctl is-active nginx)" = active
test "$(systemctl is-active sub2api-backup.service 2>/dev/null || true)" != active
install -d -m 700 /opt/sub2api/releases /opt/sub2api/backups/release-state /root/.config/sub2api-release
canary=/root/.config/sub2api-release/canary-api-key
db_key=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT key FROM api_keys WHERE id={int(profile.get('canary_api_key_id', 2))} AND status='active' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > NOW()) AND (quota <= 0 OR quota_used < quota) LIMIT 1" | tr -d '\\r\\n')
[[ $db_key == sk-* && ${{#db_key}} -ge 16 ]]
if test -e "$canary"; then
  test -f "$canary" && test ! -L "$canary" && test "$(stat -c '%a' "$canary")" = 600 && grep -Eq '^sk-.{{12,}}$' "$canary"
  [[ $(tr -d '\\r\\n' < "$canary") == "$db_key" ]]
else
  umask 077
  printf '%s\n' "$db_key" > "$canary"
fi
wrapper=/usr/local/libexec/sub2api-backup-global-lock
dropin=/etc/systemd/system/sub2api-backup.service.d/10-global-lock.conf
current=$(systemctl show sub2api-backup.service -p ExecStart --value)
current_path=$(awk 'match($0,/path=[^ ;}}]+/) {{ print substr($0,RSTART+5,RLENGTH-5); exit }}' <<<"$current")
[[ $current_path == "$wrapper" ]]
test -f "$wrapper" && test ! -L "$wrapper" && grep -Fq '/run/lock/sub2api-backup-global.lock' "$wrapper"
test -f "$dropin" && test ! -L "$dropin"
[[ $(grep -Fxc 'ExecStart=/usr/local/libexec/sub2api-backup-global-lock' "$dropin") == 1 ]]
printf 'production_bootstrap=true\ntrust_ready=true\ncanary_ready=true\nbackup_lock_ready=true\n'
"""
    return runner.run(
        "racknerd",
        script,
        {"production_bootstrap", "trust_ready", "canary_ready", "backup_lock_ready"},
    ).values
