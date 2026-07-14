from __future__ import annotations

import pathlib
import subprocess

import paramiko
import yaml

from .manifest import migration_checksums, sha256_file, validate_commit
from .profiles import get_profile
from .ssh import KNOWN_HOSTS, SSH_CONFIG, SSHRunner


ROOT = pathlib.Path(__file__).resolve().parents[2]
TRUSTED_KEY = ROOT / "deploy" / "release" / "trust" / "vm-gate-ed25519.pub"
NODES = ("local", "vm", "racknerd", "dmit", "backup")


def import_trusted_host_keys() -> None:
    document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
    user_hosts = paramiko.HostKeys()
    user_hosts.load(str(pathlib.Path.home() / ".ssh" / "known_hosts"))
    private_hosts = paramiko.HostKeys()
    if KNOWN_HOSTS.exists():
        private_hosts.load(str(KNOWN_HOSTS))
    for config in document["servers"].values():
        host = str(config["host"])
        port = int(config.get("port", 22))
        candidates = [host] if port == 22 else [f"[{host}]:{port}", host]
        existing = private_hosts.lookup(candidates[0])
        if existing:
            continue
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
        status = subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True)
        if status.strip():
            raise RuntimeError("tracked workspace changes must be committed before deployment")
        origin = subprocess.check_output(["git", "remote", "get-url", "origin"], cwd=ROOT, text=True).strip()
        if origin != self.profile["origin"]:
            raise RuntimeError("local origin does not match the release profile")
        if self.commit:
            head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
            if head != self.commit:
                raise RuntimeError("deployment commit must be the checked-out HEAD")
            subprocess.run(["git", "merge-base", "--is-ancestor", self.commit, "origin/main"], cwd=ROOT, check=True)
        return {"local_ready": "true", "host_keys_ready": "true"}

    def _ssh(self) -> SSHRunner:
        if self.runner is None:
            self.runner = SSHRunner()
        return self.runner

    def check_vm(self) -> dict[str, str]:
        profile = self.profile
        script = f"""
set -Eeuo pipefail
for command in docker git jq df; do command -v "$command" >/dev/null; done
test -d {profile['vm_source']} && test -d {profile['vm_deploy']} && test -d {profile['vm_data']}
test ! -L {profile['vm_source']} && test ! -L {profile['vm_deploy']} && test ! -L {profile['vm_data']}
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api-dev)" = healthy
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{{print $4}}' || df -PB1 / | awk 'NR==2{{print $4}}')
db_bytes=$(docker exec sub2api-postgres sh -lc 'psql -X -A -t -U "${{POSTGRES_USER:-postgres}}" -d postgres -c "SELECT pg_database_size('"'"'sub2api_dev'"'"')"' | tr -d '[:space:]')
printf 'vm_ready=true\nvm_free_bytes=%s\nvm_database_bytes=%s\n' "$free_bytes" "$db_bytes"
"""
        return self._ssh().run("local_vm", script, {"vm_ready", "vm_free_bytes", "vm_database_bytes"}).values

    def check_racknerd(self) -> dict[str, str]:
        profile = self.profile
        trust_sha = sha256_file(TRUSTED_KEY)
        migration_name = profile["migrations"][0]
        migration_sha = migration_checksums(profile)[migration_name]
        script = f"""
set -Eeuo pipefail
for command in docker jq age flock nginx curl ssh; do command -v "$command" >/dev/null; done
test -d /opt/sub2api/releases && test ! -L /opt/sub2api/releases
test -d /opt/sub2api/backups/release-state && test ! -L /opt/sub2api/backups/release-state
test -f /opt/sub2api-release-trust/vm-gate-ed25519.pub
test "$(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}')" = {trust_sha}
test -f /root/.config/sub2api-release/canary-api-key && test ! -L /root/.config/sub2api-release/canary-api-key
test "$(stat -c '%a' /root/.config/sub2api-release/canary-api-key)" = 600
test ! -e /opt/sub2api/releases/.active-release && test ! -L /opt/sub2api/releases/.active-release
for container in sub2api sub2api-postgres sub2api-redis; do test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$container")" = healthy; done
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
migration_row=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='{migration_name}'")
if [[ -z $migration_row ]]; then production_migration_status=absent; else [[ $migration_row == '{migration_name}|{migration_sha}' ]] && production_migration_status=verified; fi
test -r /etc/nginx/nginx.conf && test -r /etc/letsencrypt/live/{profile['public_domain']}/fullchain.pem
test -f /root/.ssh/sub2api_backup_upload && test ! -L /root/.ssh/sub2api_backup_upload
set +e
ssh -i /root/.ssh/sub2api_backup_upload -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 -o LogLevel=ERROR sub2api-backup@47.85.205.94 doctor-probe </dev/null >/dev/null 2>&1
backup_ssh_code=$?
set -e
[[ $backup_ssh_code != 255 ]]
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{{print $4}}' || df -PB1 / | awk 'NR==2{{print $4}}')
test "$free_bytes" -ge {profile['minimum_rack_free_bytes']}
printf 'racknerd_ready=true\nracknerd_free_bytes=%s\nbackup_protocol_ready=true\nproduction_migration_status=%s\n' "$free_bytes" "$production_migration_status"
"""
        return self._ssh().run("racknerd", script, {"racknerd_ready", "racknerd_free_bytes", "backup_protocol_ready", "production_migration_status"}, timeout=300).values

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
