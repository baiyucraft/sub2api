from __future__ import annotations

import pathlib
import subprocess
import time
from pathlib import Path

import paramiko
import yaml

from .manifest import _git_output, migration_checksums, sha256_file, validate_commit
from .migration_planner import discover_migration_catalog
from .paths import RELEASE_PACKAGE_ROOT, TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .profiles import get_profile
from .ssh import KNOWN_HOSTS, SSH_CONFIG, SSHRunner


ROOT = WORKSPACE
TRUSTED_KEY = TRUSTED_VM_PUBLIC_KEY
VALIDATOR = RELEASE_PACKAGE_ROOT / "vm-validate.sh"
GATE_SIGNER = RELEASE_PACKAGE_ROOT / "sign-gate.sh"
DR_SIGNER = RELEASE_PACKAGE_ROOT / "sign-dr-evidence.sh"
NODES = ("local", "vm", "racknerd", "dmit", "backup")
RACKNERD_READONLY_RETRY_DELAYS = (0, 2, 5)


def import_trusted_host_keys() -> None:
    document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
    private_hosts = paramiko.HostKeys()
    if KNOWN_HOSTS.exists():
        private_hosts.load(str(KNOWN_HOSTS))

    missing = []
    for config in document["servers"].values():
        host = str(config["host"])
        port = int(config.get("port", 22))
        candidates = [host] if port == 22 else [f"[{host}]:{port}", host]
        if not private_hosts.lookup(candidates[0]):
            missing.append(candidates)

    if not missing:
        return

    user_hosts = paramiko.HostKeys()
    user_hosts.load(str(pathlib.Path.home() / ".ssh" / "known_hosts"))
    for candidates in missing:
        trusted = next((user_hosts.lookup(candidate) for candidate in candidates if user_hosts.lookup(candidate)), None)
        if not trusted:
            raise RuntimeError("an SSH host key is not present in the trusted user known_hosts file")
        for key_type, key in trusted.items():
            private_hosts.add(candidates[0], key_type, key)
    KNOWN_HOSTS.parent.mkdir(parents=True, exist_ok=True)
    private_hosts.save(str(KNOWN_HOSTS))


class ReleaseDoctor:
    def __init__(self, profile_name: str, commit: str | None = None, runner: SSHRunner | None = None) -> None:
        self.profile = get_profile(profile_name)
        self.commit = validate_commit(commit) if commit else None
        self.runner = runner

    def check_local(self) -> dict[str, str]:
        if not SSH_CONFIG.is_file() or not TRUSTED_KEY.is_file():
            raise RuntimeError("release SSH config or VM trust key is missing")
        import_trusted_host_keys()
        status = _git_output(["git", "status", "--porcelain"], cwd=ROOT, text=True)
        if status.strip():
            raise RuntimeError("tracked workspace changes must be committed before deployment")
        origin = _git_output(["git", "remote", "get-url", "origin"], cwd=ROOT, text=True).strip()
        if origin != self.profile["origin"]:
            raise RuntimeError("local origin does not match the release profile")
        if self.commit:
            head = _git_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
            if head != self.commit:
                raise RuntimeError("deployment commit must be the checked-out HEAD")
            _git_output(["git", "merge-base", "--is-ancestor", self.commit, "origin/main"], cwd=ROOT)
        return {"local_ready": "true", "host_keys_ready": "true"}

    def _ssh(self) -> SSHRunner:
        if self.runner is None:
            self.runner = SSHRunner()
        return self.runner

    def _run_racknerd_readonly(self, script: str, allowed: set[str], timeout: int) -> dict[str, str]:
        """Retry only the idempotent RackNerd doctor probe after transport loss."""

        last_error: BaseException | None = None
        for delay in RACKNERD_READONLY_RETRY_DELAYS:
            if delay:
                time.sleep(delay)
            try:
                return self._ssh().run("racknerd", script, allowed, timeout=timeout).values
            except (EOFError, OSError, paramiko.SSHException) as error:
                last_error = error
            except RuntimeError as error:
                # Paramiko reports a channel that vanished before an exit status
                # as -1. Do not retry ordinary non-zero remote command failures.
                if "exit code -1" not in str(error):
                    raise
                last_error = error
        assert last_error is not None
        raise last_error

    def check_vm(self) -> dict[str, str]:
        profile = self.profile
        trust_sha = sha256_file(TRUSTED_KEY)
        validator_sha = sha256_file(VALIDATOR)
        gate_signer_sha = sha256_file(GATE_SIGNER)
        dr_signer_sha = sha256_file(DR_SIGNER)
        script = f"""
set -Eeuo pipefail
for command in docker git jq df openssl sha256sum stat cmp flock; do command -v "$command" >/dev/null; done
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$unit_lock" && test ! -L "$unit_lock" && test "$(stat -c '%U:%G:%a:%h' "$unit_lock")" = root:root:600:1
exec 8<>"$unit_lock"
test "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)" = root:root:600:1
flock -s 8
test -d {profile['vm_source']} && test -d {profile['vm_deploy']} && test -d {profile['vm_data']}
test ! -L {profile['vm_source']} && test ! -L {profile['vm_deploy']} && test ! -L {profile['vm_data']}
test "$(stat -c '%U:%G:%a' /opt/sub2api-release-signer/vm-gate-ed25519.pem)" = root:root:600
test "$(stat -c '%U:%G:%a' /opt/sub2api-release-signer/vm-gate-ed25519.pub)" = root:root:644
for asset in /usr/local/libexec/sub2api-vm-validate /usr/local/libexec/sub2api-sign-gate /usr/local/libexec/sub2api-sign-dr-evidence; do test -f "$asset" && test ! -L "$asset" && test "$(stat -c '%U:%G:%a' "$asset")" = root:root:700; done
for asset in /opt/sub2api-release-signer/vm-gate-ed25519.pem /opt/sub2api-release-signer/vm-gate-ed25519.pub; do test -f "$asset" && test ! -L "$asset"; done
test "$(sha256sum /opt/sub2api-release-signer/vm-gate-ed25519.pub | awk '{{print $1}}')" = {trust_sha}
test "$(sha256sum /usr/local/libexec/sub2api-vm-validate | awk '{{print $1}}')" = {validator_sha}
test "$(sha256sum /usr/local/libexec/sub2api-sign-gate | awk '{{print $1}}')" = {gate_signer_sha}
test "$(sha256sum /usr/local/libexec/sub2api-sign-dr-evidence | awk '{{print $1}}')" = {dr_signer_sha}
derived_public=$(mktemp)
trap 'rm -f -- "$derived_public"' EXIT
openssl pkey -in /opt/sub2api-release-signer/vm-gate-ed25519.pem -pubout -out "$derived_public"
cmp -s "$derived_public" /opt/sub2api-release-signer/vm-gate-ed25519.pub
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api-dev)" = healthy
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{{print $4}}' || df -PB1 / | awk 'NR==2{{print $4}}')
vm_pg_user=$(docker inspect sub2api-postgres | jq -r '.[0].Config.Env[]? | select(startswith("POSTGRES_USER=")) | ltrimstr("POSTGRES_USER=")' | head -n1)
if [[ ! $vm_pg_user =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then vm_pg_user=sub2api; fi
db_bytes=$(docker exec sub2api-postgres psql -X -A -t -U "$vm_pg_user" -d postgres -c "SELECT pg_database_size('sub2api_dev')" | tr -d '[:space:]')
printf 'vm_ready=true\nvm_free_bytes=%s\nvm_database_bytes=%s\nvm_release_unit_status=verified\n' "$free_bytes" "$db_bytes"
"""
        return self._ssh().run("local_vm", script, {"vm_ready", "vm_free_bytes", "vm_database_bytes", "vm_release_unit_status"}).values

    def check_racknerd(self) -> dict[str, str]:
        profile = self.profile
        trust_sha = sha256_file(TRUSTED_KEY)
        snapshot_query = r'''snapshot_rows=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COALESCE(json_agg(json_build_object('filename',filename,'checksum',checksum) ORDER BY filename),'[]'::json) FROM schema_migrations" | tr -d '\r\n')
printf '%s' "$snapshot_rows" | jq -e 'type == "array" and all(.[]; (type == "object" and (.filename|type)=="string" and (.checksum|type)=="string"))' >/dev/null
snapshot_payload=$(jq -cn --arg image "$active_image" --argjson rows "$snapshot_rows" '{current_image_id:$image,schema_migrations:$rows}')
snapshot_b64=$(printf '%s' "$snapshot_payload" | base64 -w0)
'''
        if profile.get("gate_schema") == 2:
            migration_checks = ""
        else:
            migration_sha_by_name = migration_checksums(profile, self.commit)
            migration_checks = "\n".join(
                f'''migration_row=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='{name}'")
if [[ -z $migration_row ]]; then production_migration_status=absent; else [[ $migration_row == '{name}|{migration_sha_by_name[name]}' ]]; fi'''
                for name in profile["migrations"]
            )
        script = f"""
set -Eeuo pipefail
for command in docker jq age flock nginx curl ssh; do command -v "$command" >/dev/null; done
test -d /opt/sub2api/releases && test ! -L /opt/sub2api/releases
test -d /opt/sub2api/backups/release-state && test ! -L /opt/sub2api/backups/release-state
test -f /opt/sub2api-release-trust/vm-gate-ed25519.pub
test "$(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}')" = {trust_sha}
test ! -e /opt/sub2api/releases/.active-release && test ! -L /opt/sub2api/releases/.active-release
active_slot=/opt/sub2api/active-app
test -f "$active_slot" && test ! -L "$active_slot"
test "$(grep -c '^container=' "$active_slot")" = 1 && test "$(grep -c '^port=' "$active_slot")" = 1 && test "$(grep -c '^image_id=' "$active_slot")" = 1
active_container=$(sed -n 's/^container=//p' "$active_slot")
active_port=$(sed -n 's/^port=//p' "$active_slot")
active_image=$(sed -n 's/^image_id=//p' "$active_slot")
[[ $active_container =~ ^[A-Za-z0-9_.-]{{1,80}}$ ]]
[[ $active_port == 18080 || $active_port == 18081 ]]
[[ $active_image =~ ^sha256:[0-9a-f]{{64}}$ ]]
for container in "$active_container" sub2api-postgres sub2api-redis; do test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$container")" = healthy; done
test "$(docker inspect -f '{{{{.Image}}}}' "$active_container")" = "$active_image"
grep -Fq "server 127.0.0.1:$active_port;" /etc/nginx/conf.d/sub2api-release-upstream.conf
test "$(systemctl is-active nginx)" = active
test "$(systemctl is-active sub2api-backup.service 2>/dev/null || true)" != active
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
test "$(systemctl is-active sub2api-backup.timer)" = active
backup_exec=$(systemctl show sub2api-backup.service -p ExecStart --value)
backup_path=$(awk 'match($0,/path=[^ ;}}]+/) {{ print substr($0,RSTART+5,RLENGTH-5); exit }}' <<<"$backup_exec")
test -f "$backup_path" && test ! -L "$backup_path"
grep -Fq '/run/lock/sub2api-backup-global.lock' "$backup_path"
redis_password=$(docker inspect sub2api-redis | jq -er '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--requirepass")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first) end')
test -n "$redis_password"
printf '%s\n' "$redis_password" | docker exec -i sub2api-redis sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning PING' | grep -Fxq PONG
docker exec sub2api-postgres pg_dump -U sub2api -d sub2api -Fc --schema-only >/dev/null
production_current_image_id="$active_image"
{snapshot_query}
production_migration_status=verified
{migration_checks}
test -r /etc/nginx/nginx.conf && test -r /etc/letsencrypt/live/{profile['public_domain']}/fullchain.pem
test -f /root/.ssh/sub2api_backup_upload && test ! -L /root/.ssh/sub2api_backup_upload
if ssh -i /root/.ssh/sub2api_backup_upload -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 -o LogLevel=ERROR sub2api-backup@47.85.205.94 doctor-probe </dev/null >/dev/null 2>&1; then
  backup_ssh_code=0
else
  backup_ssh_code=$?
fi
[[ $backup_ssh_code != 255 ]]
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{{print $4}}' || df -PB1 / | awk 'NR==2{{print $4}}')
test "$free_bytes" -ge {profile['minimum_rack_free_bytes']}
printf 'racknerd_ready=true\nracknerd_free_bytes=%s\nbackup_protocol_ready=true\nproduction_migration_status=%s\nproduction_current_image_id=%s\nproduction_snapshot_b64=%s\n' "$free_bytes" "$production_migration_status" "$active_image" "$snapshot_b64"
"""
        return self._run_racknerd_readonly(
            script,
            {"racknerd_ready", "racknerd_free_bytes", "backup_protocol_ready", "production_migration_status", "production_current_image_id", "production_snapshot_b64"},
            timeout=300,
        )

    def check_dmit(self) -> dict[str, str]:
        script = """
set -Eeuo pipefail
command -v haproxy >/dev/null
test "$(systemctl is-active haproxy)" = active
haproxy -c -f /etc/haproxy/haproxy.cfg >/dev/null
ss -H -ltn sport = :443 | grep -q .
grep -Eq 'send-proxy-v2|send-proxy' /etc/haproxy/haproxy.cfg
printf 'dmit_ready=true\nproxy_v2_ready=true\n'
"""
        return self._ssh().run("dmit", script, {"dmit_ready", "proxy_v2_ready"}).values

    def check_backup(self) -> dict[str, str]:
        profile = self.profile
        script = f"""
set -Eeuo pipefail
test -d /srv/sub2api-backups && test ! -L /srv/sub2api-backups && test -w /srv/sub2api-backups
free_bytes=$(df -PB1 /srv/sub2api-backups | awk 'NR==2{{print $4}}')
test "$free_bytes" -ge {profile['minimum_backup_free_bytes']}
test "$(curl -sS --resolve {profile['public_domain']}:443:{profile['dmit_public_ip']} --max-time 15 -o /dev/null -w '%{{http_code}}' https://{profile['public_domain']}/health)" = 200
public_ip=$(curl -fsS --max-time 15 https://api.ipify.org)
[[ $public_ip =~ ^[0-9a-fA-F:.]+$ ]]
printf 'backup_ready=true\nbackup_free_bytes=%s\ndmit_external_health=pass\nbackup_public_ip=%s\n' "$free_bytes" "$public_ip"
"""
        return self._ssh().run("backup", script, {"backup_ready", "backup_free_bytes", "dmit_external_health", "backup_public_ip"}).values

    def run(self, nodes: tuple[str, ...] = NODES) -> dict[str, str]:
        evidence: dict[str, str] = {}
        for node in nodes:
            method = getattr(self, f"check_{node}")
            try:
                evidence.update(method())
            except BaseException as error:
                raise RuntimeError(f"doctor.{node} failed") from error
        return evidence
