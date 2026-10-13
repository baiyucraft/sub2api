"""Finalize an already-consumed release without rewriting its failed worker.

The original signed Gate proves the application.  The independently committed
control helper and its checks prove post-deploy completion; neither is a new
Gate, claim, migration, deployment or disaster-recovery baseline.
"""

from __future__ import annotations

import argparse
import ctypes
import hashlib
import os
import re
import shlex
import time
from pathlib import Path
from typing import Any

from . import supervisor
from .atomic import canonical_json
from .doctor import ReleaseDoctor
from .manifest import _git_output, release_asset_checksums, runner_checksum, validate_commit, validate_image_id
from .migration_planner import plan_migrations
from .paths import WORKSPACE
from .production_snapshot import decode_snapshot
from .profiles import get_profile
from .ssh import SSHRunner
from .state import RunLock
from .vm_lifecycle import FINALIZATION_PRESERVE, FINALIZATION_SHUTDOWN, RetainedVMFinalization, finalization_mode, verify_retained_finalization


ORIGINAL_FILES = (
    "manifest.json", "runner.json", "state.json", "release-state.json", "vm-lifecycle.json",
    "gate/gate.json", "gate/gate.sig", "gate/production-result.json", "gate/candidate.tar.gz",
)
DOCTOR_FIELDS = {
    "local_ready", "host_keys_ready", "vm_ready", "vm_free_bytes", "vm_database_bytes", "vm_release_unit_status",
    "racknerd_ready", "racknerd_free_bytes", "backup_protocol_ready", "nginx_ingress_policy",
    "production_migration_status", "production_current_image_id", "production_current_commit_sha",
    "dmit_ready", "proxy_v2_ready", "release_proxy_ready", "legacy_proxy_disabled",
    "backup_ready", "backup_free_bytes", "dmit_external_health",
}
DOCTOR_REQUIRED = {
    "local_ready": "true", "host_keys_ready": "true", "vm_ready": "true", "vm_release_unit_status": "verified",
    "racknerd_ready": "true", "backup_protocol_ready": "true", "nginx_ingress_policy": "ready",
    "production_migration_status": "verified", "dmit_ready": "true", "proxy_v2_ready": "true",
    "release_proxy_ready": "true", "legacy_proxy_disabled": "true", "backup_ready": "true", "dmit_external_health": "pass",
}
REMOTE_REQUIRED = {
    "consumed_marker_verified": "true", "active_claim": "absent", "application_health": "pass",
    "direct_health": "pass", "dmit_route_health": "pass", "backup_units_restored": "true", "plaintext_state_removed": "true",
}


def _process_identity(pid: int) -> str | None:
    """Distinguish a dead PID from inaccessible or malformed process state."""
    if os.name == "nt":
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = (ctypes.c_ulong, ctypes.c_int, ctypes.c_ulong)
        kernel.OpenProcess.restype = ctypes.c_void_p
        kernel.GetProcessTimes.argtypes = (ctypes.c_void_p,) + (ctypes.POINTER(ctypes.c_ulonglong),) * 4
        kernel.CloseHandle.argtypes = (ctypes.c_void_p,)
        handle = kernel.OpenProcess(0x1000, False, pid)
        if not handle:
            if ctypes.get_last_error() == 87:
                return None
            raise RuntimeError("finalization worker identity query is uncertain")
        try:
            times = [ctypes.c_ulonglong() for _ in range(4)]
            if not kernel.GetProcessTimes(handle, *(ctypes.byref(value) for value in times)):
                raise RuntimeError("finalization worker identity query is uncertain")
            return f"win:{times[0].value}"
        finally:
            kernel.CloseHandle(handle)
    try:
        content = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
    except FileNotFoundError:
        return None
    try:
        fields = content.rsplit(")", 1)[1].split()
        boot = Path("/proc/sys/kernel/random/boot_id").read_text(encoding="ascii").strip()
        return f"linux:{boot}:{int(fields[19])}"
    except (OSError, IndexError, UnicodeError, ValueError):
        raise RuntimeError("finalization worker identity query is uncertain") from None


def _hash_file(path: Path) -> str:
    # JSON shape/path guards are shared with the runner, but archives/signatures
    # are binary and need an independent regular-file, one-link guard.
    root = supervisor.RUN_ROOT.resolve()
    current = path
    while current != root and current != current.parent:
        if current.is_symlink():
            raise RuntimeError("finalization evidence path is unsafe")
        current = current.parent
    if root not in path.resolve().parents or not path.is_file() or path.stat().st_nlink != 1:
        raise RuntimeError("finalization evidence is not a regular single-link file")
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _original_hashes(run_dir: Path) -> dict[str, str]:
    return {name: _hash_file(run_dir / name) for name in ORIGINAL_FILES}


def _helper_identity() -> dict[str, Any]:
    commit = validate_commit(_git_output(["git", "rev-parse", "--verify", "HEAD^{commit}"], cwd=WORKSPACE, text=True).strip())
    assets = release_asset_checksums(commit)
    # Require exact committed bytes; local doctor separately checks the entire
    # worktree and origin/main ancestry.  Mode and Git blob identity are bound
    # as well as SHA-256 so executable-bit or blob drift cannot reuse a record.
    for name, digest in assets.items():
        path = WORKSPACE / name
        if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
            raise RuntimeError("finalization helper does not match its committed assets")
    output = _git_output(["git", "ls-tree", "-r", "-z", commit, "--", *assets], cwd=WORKSPACE)
    blobs = {}
    for entry in output.split(b"\0"):
        if entry:
            identity, name = entry.decode("utf-8").split("\t", 1)
            mode, kind, blob = identity.split(" ")
            if kind != "blob" or mode not in {"100644", "100755"}:
                raise RuntimeError("finalization helper asset mode is unsafe")
            blobs[name] = {"mode": mode, "blob": blob, "sha256": assets[name]}
    if set(blobs) != set(assets):
        raise RuntimeError("finalization helper blob inventory is incomplete")
    return {"commit_sha": commit, "runner_sha256": runner_checksum(), "assets": blobs}


def _failed_worker(run_dir: Path, manifest: dict[str, Any]) -> dict[str, Any]:
    runner = supervisor._read_json(run_dir / "runner.json", required=True) or {}
    pid, token = runner.get("pid"), runner.get("process_token")
    if (
        runner.get("schema") != 1 or runner.get("release_id") != manifest.get("release_id")
        or runner.get("profile") != manifest.get("profile") or runner.get("commit") != manifest.get("commit_sha")
        or runner.get("runner_sha256") != manifest.get("runner_sha256")
        or runner.get("status") != "failed" or type(runner.get("exit_code")) is not int or runner["exit_code"] == 0
        or type(pid) is not int or pid <= 0 or not isinstance(token, str) or not token
    ):
        raise RuntimeError("finalization requires an identified failed terminal worker")
    if _process_identity(pid) == token:
        raise RuntimeError("finalization refuses a live original worker")
    return runner


def _retained_recovery_script(manifest: dict[str, Any], backup: dict[str, Any]) -> str:
    """Prove the exact encrypted-only closure kept by cleanup-state.sh."""
    identifier = manifest["release_id"]
    pre_image = str(backup.get("pre_switch_image_id", ""))
    validate_image_id(pre_image)
    if manifest.get("schema") == 2 and pre_image != manifest.get("production_current_image_id"):
        raise RuntimeError("retained recovery pre-image differs from signed baseline")
    expected = {}
    for field in ("artifact", "transport_artifact"):
        value = str(backup.get(field, ""))
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.-]{0,200}\.tar\.age", value):
            raise RuntimeError("retained recovery artifact identity is unproven")
        expected[field] = value
    size, digest = str(backup.get("artifact_size", "")), str(backup.get("artifact_sha256", ""))
    if not re.fullmatch(r"[1-9][0-9]*", size) or not re.fullmatch(r"[0-9a-f]{64}", digest) or backup.get("release_sha256") != digest:
        raise RuntimeError("retained recovery backup checksum identity is unproven")
    expected.update(artifact_size=size, artifact_sha256=digest)
    for field, value in (("traffic_preserved", "true"), ("redis_backup_mode", "rdb"), ("no_restart_path_proven", "true"), ("local_restore_point_ready", "true")):
        if backup.get(field) != value:
            raise RuntimeError("retained recovery backup evidence is incomplete")
        expected[field] = value
    result_text = "\n".join(f"{key}={value}" for key, value in expected.items())
    result_sha = hashlib.sha256((result_text + "\n").encode()).hexdigest()
    entries = "\n".join(sorted(("recovery-point.age", "recovery-point.age.sha256", "pre-image-id", "backup-result", "backup-result.sha256")))
    return f"""state={shlex.quote('/opt/sub2api/backups/release-state/' + identifier)}
test ! -L "$state"
if test -e "$state"; then
  test -d "$state"
  test "$(realpath "$state")" = {shlex.quote('/opt/sub2api/backups/release-state/' + identifier)}
  test "$(stat -c '%U:%G:%a' "$state")" = root:root:700
  test -z "$(find "$state" -mindepth 1 -maxdepth 1 ! -type f -print -quit)"
  expected_entries={shlex.quote(entries)}
  if test -e "$state/backup-failure"; then
    test ! -L "$state/backup-failure"
    test "$(stat -c '%U:%G:%a:%h' "$state/backup-failure")" = root:root:400:1
    test "$(wc -l < "$state/backup-failure")" = 3
    test "$(grep -c '^attempt_id=' "$state/backup-failure")" = 1
    test "$(grep -c '^stage=' "$state/backup-failure")" = 1
    test "$(grep -c '^exit_code=' "$state/backup-failure")" = 1
    grep -Eq {shlex.quote('^attempt_id=' + re.escape(identifier) + '-[123]$')} "$state/backup-failure"
    grep -Eq '^stage=(initializing|preflight|database|redis|config|metadata|archive|encrypt|upload|local_restore_point|commit_result)$' "$state/backup-failure"
    failure_code=$(sed -n 's/^exit_code=//p' "$state/backup-failure")
    [[ $failure_code =~ ^[1-9][0-9]{{0,2}}$ ]]
    (( 10#$failure_code <= 255 ))
    expected_entries=$(printf '%s\\nbackup-failure\\n' "$expected_entries" | LC_ALL=C sort)
  fi
  test "$(find "$state" -mindepth 1 -maxdepth 1 -printf '%f\\n' | LC_ALL=C sort)" = "$expected_entries"
  for file in recovery-point.age recovery-point.age.sha256; do
    test "$(stat -c '%U:%G:%a:%h' "$state/$file")" = root:root:600:1
  done
  for file in backup-result backup-result.sha256; do
    test "$(stat -c '%U:%G:%a:%h' "$state/$file")" = root:root:400:1
  done
  case "$(stat -c '%U:%G:%a:%h' "$state/pre-image-id")" in root:root:600:1|root:root:644:1) ;; *) exit 1 ;; esac
  test "$(cat "$state/pre-image-id")" = {shlex.quote(pre_image)}
  test "$(cat "$state/backup-result")" = {shlex.quote(result_text)}
  test "$(cat "$state/backup-result.sha256")" = {shlex.quote(result_sha + '  backup-result')}
  test "$(cat "$state/recovery-point.age.sha256")" = {shlex.quote(digest + '  recovery-point.age')}
  test "$(stat -c '%s' "$state/recovery-point.age")" = {shlex.quote(size)}
  (cd "$state"; sha256sum --strict -c backup-result.sha256 >/dev/null; sha256sum --strict -c recovery-point.age.sha256 >/dev/null)
fi
"""


def _health_header_script(identifier: str) -> str:
    instance_pattern = re.escape(identifier + "-active")
    return f"""health_headers=$(mktemp /tmp/sub2api-finalization-health.XXXXXX)
chmod 600 "$health_headers"
trap 'rm -f -- "$health_headers"' EXIT
assert_health_headers() {{
  test "$(grep -Eic '^x-sub2api-instance:' "$health_headers")" = 1
  test "$(grep -Eic '^x-sub2api-instance:[[:space:]]*{instance_pattern}[[:space:]]*$' "$health_headers")" = 1
  test "$(grep -Eic '^x-sub2api-background-ready:' "$health_headers")" = 1
  test "$(grep -Eic '^x-sub2api-background-ready:[[:space:]]*true[[:space:]]*$' "$health_headers")" = 1
}}
"""


def _remote_consumed(ssh: SSHRunner, manifest: dict[str, Any], candidate: str, originals: dict[str, str], *, backup_evidence: dict[str, Any] | None = None) -> dict[str, str]:
    identifier = manifest["release_id"]
    profile = get_profile(manifest["profile"])
    consumed = f"/opt/sub2api/releases/{identifier}/.consumed"
    asset_checksums = {}
    for path, digest in manifest["release_asset_sha256"].items():
        if "/maintenance/release/" in path or path.endswith(("/maintenance/181/mask-backup-units.sh", "/maintenance/181/restore-backup-units.sh", "/release/backfill-activity-reward-costs.sh")):
            name = path.rsplit("/", 1)[1]
            if not re.fullmatch(r"[A-Za-z0-9_.-]{1,160}", name) or not re.fullmatch(r"[0-9a-f]{64}", digest) or name in asset_checksums:
                raise RuntimeError("finalization signed claim asset inventory is unsafe")
            asset_checksums[name] = digest
    if not asset_checksums:
        raise RuntimeError("finalization signed claim asset inventory is missing")
    claim_checksums = {f"assets/{name}": digest for name, digest in asset_checksums.items()}
    claim_checksums.update({name: originals[f"gate/{name}"] for name in ("gate.json", "gate.sig", "candidate.tar.gz")})
    expected_checksums = "\n".join(f"{digest}  {name}" for name, digest in sorted(claim_checksums.items()))
    expected_files = "\n".join(sorted(claim_checksums))
    if backup_evidence is None:
        production = supervisor._read_json(supervisor._run_dir(identifier) / "gate/production-result.json", required=True) or {}
        backup_evidence = supervisor._final_evidence(production)
    retained_recovery = _retained_recovery_script(manifest, backup_evidence)
    # Recheck the remote immutable claim, rather than treating a directory or a
    # previous production JSON field as proof of consumption.
    script = f"""set -Eeuo pipefail
test "$(id -u)" = 0
exec 8>/run/lock/sub2api-production-release.lock
flock -xn 8
claim={shlex.quote(consumed)}
test -d "$claim"
test ! -L "$claim"
test "$(realpath "$claim")" = {shlex.quote(consumed)}
test "$(stat -c '%U:%G:%a' "$claim")" = root:root:700
test ! -e /opt/sub2api/releases/.active-release
test ! -L /opt/sub2api/releases/.active-release
test ! -e {shlex.quote(consumed.rsplit('/', 1)[0] + '/.recovered')}
test ! -L {shlex.quote(consumed.rsplit('/', 1)[0] + '/.recovered')}
{retained_recovery}
for file in marker release_id gate.json gate.sig candidate.tar.gz CLAIM_SHA256SUMS plaintext-cleaned; do
  test -f "$claim/$file"
  test ! -L "$claim/$file"
  test "$(stat -c '%U:%G:%a:%h' "$claim/$file")" = root:root:400:1
done
test -d "$claim/assets"
test ! -L "$claim/assets"
test "$(stat -c '%U:%G:%a' "$claim/assets")" = root:root:700
test -z "$(find "$claim/assets" -mindepth 1 ! -type f -print -quit)"
test "$(cat "$claim/CLAIM_SHA256SUMS")" = {shlex.quote(expected_checksums)}
test "$(cd "$claim" && find gate.json gate.sig candidate.tar.gz assets -type f -print | LC_ALL=C sort)" = {shlex.quote(expected_files)}
for file in "$claim/assets"/*; do test "$(stat -c '%U:%G:%a:%h' "$file")" = root:root:500:1; done
grep -Fxq {shlex.quote('release_id=' + identifier)} "$claim/marker"
grep -Fxq {shlex.quote('candidate_image_id=' + candidate)} "$claim/marker"
grep -Fxq {shlex.quote('release_id=' + identifier)} "$claim/release_id"
(cd "$claim" && sha256sum --strict -c CLAIM_SHA256SUMS >/dev/null)
test "$(sha256sum "$claim/gate.json" | awk '{{print $1}}')" = {shlex.quote(originals['gate/gate.json'])}
test "$(sha256sum "$claim/gate.sig" | awk '{{print $1}}')" = {shlex.quote(originals['gate/gate.sig'])}
openssl pkeyutl -verify -pubin -inkey /opt/sub2api-release-trust/vm-gate-ed25519.pub -rawin -in "$claim/gate.json" -sigfile "$claim/gate.sig" >/dev/null 2>&1
slot=/opt/sub2api/active-app
test -f "$slot"
test ! -L "$slot"
for field in container port image_id release_id instance_id; do test "$(grep -c "^$field=" "$slot")" = 1; done
container=$(sed -n 's/^container=//p' "$slot")
port=$(sed -n 's/^port=//p' "$slot")
[[ $container =~ ^[A-Za-z0-9_.-]{{1,80}}$ ]]
test "$container" = sub2api
[[ $port == 18080 || $port == 18081 ]]
test "$(sed -n 's/^release_id=//p' "$slot")" = {shlex.quote(identifier)}
test "$(sed -n 's/^instance_id=//p' "$slot")" = {shlex.quote(identifier + '-active')}
test "$(sed -n 's/^image_id=//p' "$slot")" = {shlex.quote(candidate)}
test "$(docker inspect -f '{{{{.Image}}}}' "$container")" = {shlex.quote(candidate)}
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$container")" = healthy
{_health_header_script(identifier)}
test "$(curl -sS --max-time 15 -D "$health_headers" -o /dev/null -w '%{{http_code}}' http://127.0.0.1:$port/health)" = 200
assert_health_headers
test "$(curl -sS --resolve {profile['public_domain']}:443:{profile['rack_public_ip']} --max-time 15 -D "$health_headers" -o /dev/null -w '%{{http_code}}' https://{profile['public_domain']}/health)" = 200
assert_health_headers
grep -Fq "server 127.0.0.1:$port;" /etc/nginx/conf.d/sub2api-release-upstream.conf
test "$(systemctl is-active nginx)" = active
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
test "$(systemctl is-active sub2api-backup.timer)" = active
if backup_service_state=$(systemctl is-active sub2api-backup.service 2>/dev/null); then
  backup_service_code=0
else
  backup_service_code=$?
fi
test "$backup_service_code" = 3
test "$backup_service_state" = inactive
printf 'consumed_marker_verified=true\\nactive_claim=absent\\napplication_health=pass\\ndirect_health=pass\\nbackup_units_restored=true\\nplaintext_state_removed=true\\nrunning_image_id=%s\\n' {shlex.quote(candidate)}
"""
    rack_required = {key: value for key, value in REMOTE_REQUIRED.items() if key != "dmit_route_health"}
    values = ssh.run("racknerd", script, {*rack_required, "running_image_id"}, timeout=300).values
    if any(values.get(key) != expected for key, expected in rack_required.items()) or values.get("running_image_id") != candidate:
        raise RuntimeError("consumed release remote evidence is incomplete")
    dmit_script = f"""set -Eeuo pipefail
{_health_header_script(identifier)}
test "$(curl -sS --resolve {profile['public_domain']}:443:{profile['dmit_public_ip']} --max-time 15 -D "$health_headers" -o /dev/null -w '%{{http_code}}' https://{profile['public_domain']}/health)" = 200
assert_health_headers
printf 'dmit_route_health=pass\\n'
"""
    dmit = ssh.run("backup", dmit_script, {"dmit_route_health"}, timeout=60).values
    values = {key: value for key, value in values.items() if key in {*rack_required, "running_image_id"}}
    values["dmit_route_health"] = dmit.get("dmit_route_health", "unknown")
    if any(values.get(key) != expected for key, expected in REMOTE_REQUIRED.items()) or values.get("running_image_id") != candidate:
        raise RuntimeError("consumed release remote evidence is incomplete")
    return values


def _doctor_checks(manifest: dict[str, Any], helper: dict[str, Any], ssh: SSHRunner, *, include_vm: bool = True) -> dict[str, str]:
    local = ReleaseDoctor(manifest["profile"], helper["commit_sha"], runner=ssh).run(("local",))
    nodes = ("vm", "racknerd", "dmit", "backup") if include_vm else ("racknerd", "dmit", "backup")
    remote = ReleaseDoctor(manifest["profile"], manifest["commit_sha"], runner=ssh).run(nodes)
    values = {key: value for key, value in {**local, **remote}.items() if key in DOCTOR_FIELDS}
    required = dict(DOCTOR_REQUIRED)
    if not include_vm:
        required.pop("vm_ready")
        required.pop("vm_release_unit_status")
    if any(values.get(key) != value for key, value in required.items()):
        raise RuntimeError("finalization post-deploy doctor evidence is incomplete")
    gate = supervisor._read_json(supervisor._run_dir(manifest["release_id"]) / "gate/gate.json", required=True) or {}
    candidate = gate["evidence"]["candidate_image_id"]
    if values.get("production_current_image_id") != candidate or values.get("production_current_commit_sha") != manifest["commit_sha"]:
        raise RuntimeError("finalization doctor observed a different application identity")
    if manifest.get("schema") == 2:
        snapshot = decode_snapshot(remote.get("production_snapshot_b64", ""))
        if snapshot["current_image_id"] != candidate or snapshot["production_current_commit_sha"] != manifest["commit_sha"]:
            raise RuntimeError("finalization migration snapshot identity differs")
        plan = plan_migrations(manifest["migration_catalog"], snapshot["schema_migrations"])
        if plan["pending"] or not plan["existing_checksums_verified"]:
            raise RuntimeError("finalization migration checksums or pending state differ")
        values["migration_catalog_verified"] = "true"
    return values


def _cleanup_vm_tasks(ssh: SSHRunner, manifest: dict[str, Any], original_sha: str, *, boot_id: str) -> dict[str, str]:
    if not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", boot_id):
        raise RuntimeError("finalization cleanup VM boot identity is unproven")
    identifier = manifest["release_id"]
    suffix = re.sub(r"[^a-zA-Z0-9]", "", identifier)
    # Completed validators already remove their probe containers/databases.
    # Refuse any remaining process/runtime instead of destroying ambiguous work;
    # only leftover plaintext directories from this exact release may be removed.
    script = f"""set -Eeuo pipefail
test "$(id -u)" = 0
test "$(cat /proc/sys/kernel/random/boot_id)" = {shlex.quote(boot_id)}
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$unit_lock"
test ! -L "$unit_lock"
test "$(stat -c '%U:%G:%a:%h' "$unit_lock")" = root:root:600:1
exec 8<>"$unit_lock"
flock -xn 8
root=/opt/sub2api-deploy/release-gates
state="$root/{identifier}"
test -d "$root"
test ! -L "$root"
test -d "$state"
test ! -L "$state"
test "$(realpath "$state")" = "$root/{identifier}"
test "$(stat -c '%U:%G:%a' "$state")" = root:root:700
test -f "$state/manifest.json"
test ! -L "$state/manifest.json"
test "$(sha256sum "$state/manifest.json" | awk '{{print $1}}')" = {shlex.quote(original_sha)}
if test -e "$root/release.lock"; then
  test -f "$root/release.lock"
  test ! -L "$root/release.lock"
  exec 9<>"$root/release.lock"; flock -xn 9
fi
names=$(docker ps -a --format '{{{{.Names}}}}')
! grep -Eq '^sub2api-(v2-|probe-|preview-)' <<<"$names"
networks=$(docker network ls --format '{{{{.Name}}}}')
! grep -Eq '^sub2api-(v2-|probe-)' <<<"$networks"
pg_user=$(docker inspect sub2api-postgres | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_USER=")) | ltrimstr("POSTGRES_USER=")')
[[ $pg_user =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]
databases=$(docker exec sub2api-postgres psql -X -A -t -U "$pg_user" -d postgres -c "SELECT datname FROM pg_database")
! grep -Fxq {shlex.quote('sub2api_v2_' + suffix[:24])} <<<"$databases"
! grep -Fxq {shlex.quote('sub2api_probe_' + suffix[:24])} <<<"$databases"
for name in probe-data production-recovery probe-redis-data; do
  target="$state/$name"
  test ! -L "$target"
  if test -e "$target"; then
    test -d "$target"
    test "$(realpath "$target")" = "$state/$name"
    test "$(stat -c '%U:%G:%a' "$target")" = root:root:700
    rm -rf -- "$target"
  fi
  test ! -e "$target"
  test ! -L "$target"
done
printf 'vm_tasks_cleanup=verified\\n'
"""
    values = ssh.run("local_vm", script, {"vm_tasks_cleanup"}, timeout=300).values
    if values != {"vm_tasks_cleanup": "verified"}:
        raise RuntimeError("finalization VM isolated cleanup is unverified")
    return values


def _validate_checks(run_dir: Path, checks: dict[str, Any], manifest: dict[str, Any], helper: dict[str, Any], originals: dict[str, str]) -> None:
    preserve = finalization_mode(checks) == FINALIZATION_PRESERVE
    fields = {"schema", "scope", "release_id", "profile", "application_commit", "helper", "original_sha256", "production", "doctor", "vm_tasks_cleanup", "checked_at"}
    if preserve:
        fields.add("mode")
    if (
        set(checks) != fields or checks.get("scope") != "consumed-release-finalization"
        or checks.get("release_id") != manifest["release_id"] or checks.get("profile") != manifest["profile"]
        or checks.get("application_commit") != manifest["commit_sha"] or checks.get("helper") != helper
        or checks.get("original_sha256") != originals or checks.get("vm_tasks_cleanup") != "verified"
        or type(checks.get("checked_at")) is not int
    ):
        raise RuntimeError("finalization checks identity or original evidence differs")
    doctor, production = checks.get("doctor"), checks.get("production")
    if not isinstance(doctor, dict) or set(doctor) - (DOCTOR_FIELDS | {"migration_catalog_verified"}) or not isinstance(production, dict) or set(production) != {*REMOTE_REQUIRED, "running_image_id"}:
        raise RuntimeError("finalization checks field allowlist differs")
    if any(doctor.get(key) != value for key, value in DOCTOR_REQUIRED.items()) or any(production.get(key) != value for key, value in REMOTE_REQUIRED.items()):
        raise RuntimeError("finalization required checks are incomplete")
    gate = supervisor._read_json(run_dir / "gate/gate.json", required=True) or {}
    candidate = gate["evidence"]["candidate_image_id"]
    if production.get("running_image_id") != candidate or doctor.get("production_current_image_id") != candidate or doctor.get("production_current_commit_sha") != manifest["commit_sha"]:
        raise RuntimeError("finalization recorded application identity differs")
    if manifest.get("schema") == 2 and doctor.get("migration_catalog_verified") != "true":
        raise RuntimeError("finalization migration evidence is incomplete")


def finalized_result_view(identifier: str, *, check_live: bool = True) -> dict[str, Any]:
    run_dir = supervisor._run_dir(identifier)
    manifest = supervisor._read_json(run_dir / "manifest.json", required=True) or {}
    runner = _failed_worker(run_dir, manifest)
    original = supervisor._verified_evidence_view(identifier, allow_historical_runner=True)
    result = supervisor._read_json(run_dir / "finalization-result.json", required=True) or {}
    helper, originals = _helper_identity(), _original_hashes(run_dir)
    checks = supervisor._read_json(run_dir / "finalization-checks.json", required=True) or {}
    _validate_checks(run_dir, checks, manifest, helper, originals)
    mode = finalization_mode(checks)
    if finalization_mode(result) != mode:
        raise RuntimeError("finalization mode mismatch")
    checks_sha = _hash_file(run_dir / "finalization-checks.json")
    fields = {"schema", "scope", "release_id", "status", "application_commit", "helper_commit", "original_runner_status", "original_runner_exit", "original_sha256", "checks_sha256", "vm_finalization_sha256", "finalized_at"}
    if mode == FINALIZATION_PRESERVE:
        fields.add("mode")
    if (
        set(result) != fields or result.get("scope") != "consumed-release-finalization"
        or result.get("release_id") != identifier or result.get("status") != "verified"
        or result.get("application_commit") != manifest["commit_sha"] or result.get("helper_commit") != helper["commit_sha"]
        or result.get("original_runner_status") != runner["status"] or result.get("original_runner_exit") != runner["exit_code"]
        or result.get("original_sha256") != originals or result.get("checks_sha256") != checks_sha
        or result.get("vm_finalization_sha256") != _hash_file(run_dir / "vm-finalization.json")
        or type(result.get("finalized_at")) is not int
    ):
        raise RuntimeError("finalization result identity or evidence checksum differs")
    vm = verify_retained_finalization(run_dir, identifier, manifest["commit_sha"], evidence_sha256=checks_sha)
    if finalization_mode(vm) != mode:
        raise RuntimeError("finalization VM mode mismatch")
    if check_live:
        ssh = SSHRunner()
        _doctor_checks(manifest, helper, ssh, include_vm=mode == FINALIZATION_PRESERVE)
        _remote_consumed(ssh, manifest, original["candidate_image_id"], originals)
    return {
        **original, "finalization_status": "verified", "application_commit": manifest["commit_sha"],
        "helper_commit": helper["commit_sha"], "original_runner_status": runner["status"], "original_runner_exit": runner["exit_code"],
        "checks_sha256": checks_sha, "vm_power_status": vm["vm_power_status"], "vm_cleanup_status": vm["vm_cleanup_status"],
    }


def finalize_consumed(args: argparse.Namespace) -> None:
    identifier = args.release_id
    preserve = getattr(args, "preserve_running_vm", False)
    if type(preserve) is not bool:
        raise RuntimeError("finalization mode unproven")
    mode = FINALIZATION_PRESERVE if preserve else FINALIZATION_SHUTDOWN
    metadata = {"schema": 2, "mode": FINALIZATION_PRESERVE} if preserve else {"schema": 1}
    run_dir = supervisor._run_dir(identifier)
    with RunLock(supervisor.RUN_ROOT / ".release.lock"):
        if (run_dir / "finalization-result.json").exists():
            existing_result = supervisor._read_json(run_dir / "finalization-result.json", required=True) or {}
            if finalization_mode(existing_result) != mode:
                raise RuntimeError("finalization mode mismatch")
            value = finalized_result_view(identifier)
        else:
            manifest = supervisor._read_json(run_dir / "manifest.json", required=True) or {}
            runner = _failed_worker(run_dir, manifest)
            original = supervisor._verified_evidence_view(identifier, allow_historical_runner=True)
            helper, originals = _helper_identity(), _original_hashes(run_dir)
            checks_path = run_dir / "finalization-checks.json"
            existing_checks = supervisor._read_json(checks_path)
            if existing_checks is not None:
                if finalization_mode(existing_checks) != mode:
                    raise RuntimeError("finalization mode mismatch")
                _validate_checks(run_dir, existing_checks, manifest, helper, originals)
            ssh = SSHRunner()
            # Local commit checks happen before any VM or production operation.
            ReleaseDoctor(manifest["profile"], helper["commit_sha"], runner=ssh).run(("local",))
            with RetainedVMFinalization(run_dir, identifier, manifest["commit_sha"], runner, process_identity=_process_identity, ssh=ssh, preserve_running_vm=preserve) as lease:
                production = _remote_consumed(ssh, manifest, original["candidate_image_id"], originals)
                vm_already_verified = lease.record.get("status") == "verified"
                doctor = _doctor_checks(manifest, helper, ssh, include_vm=preserve or not vm_already_verified)
                cleanup = {} if vm_already_verified else _cleanup_vm_tasks(ssh, manifest, originals["manifest.json"], boot_id=lease.record["vm_boot_id"])
                if _original_hashes(run_dir) != originals:
                    raise RuntimeError("original release evidence changed during finalization")
                checks = supervisor._read_json(checks_path)
                if checks is None:
                    if vm_already_verified:
                        raise RuntimeError("finalization verified VM lacks immutable pre-shutdown checks")
                    checks = {
                        **metadata, "scope": "consumed-release-finalization", "release_id": identifier,
                        "profile": manifest["profile"], "application_commit": manifest["commit_sha"],
                        "helper": helper, "original_sha256": originals, "production": production, "doctor": doctor,
                        **cleanup, "checked_at": int(time.time()),
                    }
                    _validate_checks(run_dir, checks, manifest, helper, originals)
                    supervisor._write_json(checks_path, checks)
                else:
                    _validate_checks(run_dir, checks, manifest, helper, originals)
                if finalization_mode(checks) != mode:
                    raise RuntimeError("finalization mode mismatch")
                checks_sha = _hash_file(checks_path)
                def before_owner_commit() -> None:
                    if _original_hashes(run_dir) != originals or _helper_identity() != helper or _hash_file(checks_path) != checks_sha:
                        raise RuntimeError("finalization evidence or helper changed before owner commit")

                if preserve:
                    lease.complete(evidence_sha256=checks_sha, before_owner_commit=before_owner_commit)
                else:
                    lease.complete(evidence_sha256=checks_sha)
                # Write independent success only after the strict lease commits
                # verified shutdown/preservation and terminal owner. Originals
                # remain intact; preserve binds all evidence before releasing.
                vm = verify_retained_finalization(run_dir, identifier, manifest["commit_sha"], evidence_sha256=checks_sha)
                if finalization_mode(vm) != mode:
                    raise RuntimeError("finalization VM mode mismatch")
                result = {
                    **metadata, "scope": "consumed-release-finalization", "release_id": identifier, "status": "verified",
                    "application_commit": manifest["commit_sha"], "helper_commit": helper["commit_sha"],
                    "original_runner_status": runner["status"], "original_runner_exit": runner["exit_code"],
                    "original_sha256": originals, "checks_sha256": checks_sha,
                    "vm_finalization_sha256": _hash_file(run_dir / "vm-finalization.json"), "finalized_at": int(time.time()),
                }
                supervisor._write_json(run_dir / "finalization-result.json", result)
            value = finalized_result_view(identifier)
    print(canonical_json(value).decode("ascii"))
