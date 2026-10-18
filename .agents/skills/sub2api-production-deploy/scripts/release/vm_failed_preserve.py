"""Preserve the running VM after one frozen, pre-production Gate incident.

Original incident files are immutable. Only an independent reconciliation
record and, as the last commit marker, the shared released owner are written.
No remote operation changes files, data, containers or VM power.
"""
from __future__ import annotations

import argparse
import hashlib
import inspect
import re
import time
from pathlib import Path

from . import supervisor, vm_lifecycle
from .atomic import atomic_write, canonical_json, hash_replaceable_file
from .doctor import ReleaseDoctor
from .finalization import _helper_identity, _process_identity
from .ssh import SSHRunner
from .state import RunLock


INCIDENT_ID = "273-1396fc89fe73-1792295582-80609bce"
INCIDENT_COMMIT = "1396fc89fe733a169a00e1ca0c3a8d24defa6557"
INCIDENT_VALIDATOR = "3f3f533428463f1bcc16f133b50270e72fdd594090103da7e9573b8bfd82e47c"
ORIGINAL_SHA256 = {
    "manifest.json": "a21f1ea49ec757e5602e1b9e3c5ad2f9761c8ad0fc083c7cf7e1821678e8d317",
    "runner.json": "7521170c6736a42c148e82aa858e2ec953cff72841eff5ef15cdf6aaadee7771",
    "state.json": "6938d0a5ca6e0c48608022db2b345976801371a74e04dad8ebc766fd2192408a",
    "vm-lifecycle.json": "0deca31b81080141cabc2795db8ccae1aa882f4801f202e1e3c49bdc6e301940",
}
REMOTE_SHA256 = {
    "manifest.json": ORIGINAL_SHA256["manifest.json"],
    "stage": "3c69b538979a1fcfd83d91a8615c384ef1b4148db702ff20d4cd8dafc552bf45",
    "failure-category": "80fa64c69aa7268a06d6b0573c0b05bd7c6c6e98353b895ae3e7f1c584895ee5",
    "failure-line": "ee3ddb7cc831f149b0acaa8fc48d3872abef4bcf5f4f2e5b832cab797cb09330",
    "failure-detail": "0efecfeea2a1658224af7885994a199b1ace6e1b5746588e982de80306a85bd2",
    "plan-before.exit": "9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa",
    "plan-actual.exit": "9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa",
    "plan-before.json": "25fa3b3964e6a777e7c190e0ddefd136e8c4f01cc0deee143ccba3ec3e38b20f",
    "plan-actual.json": "7788b96c163d69dcb9d6401aed0d82188aaf385fc8dbe5d267e5a731922f07d1",
    "logs/vm-validate.raw.log": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "validator.stderr": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
}
ABSENT = (
    "probe-data", "probe-redis-data", "production-recovery", "candidate.tar.gz",
    "migration-plan-apply.json", "migrate-candidate.log", "plan-after.json", "old-image-health.log",
    "output/gate.json", "output/gate.sig", "output/candidate.tar.gz", "output/SHA256SUMS",
    "migration-hook-filename", "migration-hook-phase", "migration-hook-status",
    "migration-hook-context.sh", "migration-hook.stdout", "migration-hook.stderr", "hook-results.json",
)
CHECKS_NAME = "vm-failed-preserve-checks.json"
RESULT_NAME = "vm-failed-preserve-result.json"


def _vm_probe(root, raw, boot_id, expected, absent, probe_db, probe_suffix, *, run,
              proc_root="/proc", boot_path="/proc/sys/kernel/random/boot_id"):
    """Read-only Linux proof. The CLI supplies fixed production paths only.

    The explicit IO parameters also permit the identical proof to run against
    a private Linux fixture without creating or deleting real VM resources.
    """
    import hashlib
    import os
    import stat
    from pathlib import Path

    def directory(path):
        current = Path(path)
        for parent in (current, *current.parents):
            if parent.is_symlink():
                raise RuntimeError("vm_failed_preserve_unsafe_directory")
        value = current.stat()
        if not stat.S_ISDIR(value.st_mode) or value.st_uid != 0 or value.st_gid != 0 or stat.S_IMODE(value.st_mode) != 0o700:
            raise RuntimeError("vm_failed_preserve_unsafe_directory")

    def file(path, mode, digest):
        path = Path(path)
        value = path.lstat()
        if (not stat.S_ISREG(value.st_mode) or value.st_uid != 0 or value.st_gid != 0
                or stat.S_IMODE(value.st_mode) != mode or value.st_nlink != 1
                or hashlib.sha256(path.read_bytes()).hexdigest() != digest):
            raise RuntimeError("vm_failed_preserve_remote_evidence_changed")

    root, raw = Path(root), Path(raw)
    for path in (root, root / "output", root / "logs", raw):
        directory(path)
    for name, digest in expected.items():
        mode = 0o600 if name in ("stage", "logs/vm-validate.raw.log", "validator.stderr") else 0o400
        file(root / name, mode, digest)
    file(raw / "vm-validate.raw.log", 0o600, expected["logs/vm-validate.raw.log"])
    for name in absent:
        if os.path.lexists(root / name):
            raise RuntimeError("vm_failed_preserve_later_stage_or_residue")
    if Path(boot_path).read_text().strip() != boot_id:
        raise RuntimeError("vm_failed_preserve_boot_changed")

    ancestors = set()
    pid = os.getpid()
    while pid and pid not in ancestors:
        ancestors.add(pid)
        try:
            pid = int((Path(proc_root) / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()[1])
        except FileNotFoundError:
            break
    for entry in Path(proc_root).iterdir():
        if not entry.name.isdigit() or int(entry.name) in ancestors:
            continue
        try:
            args = [value.decode(errors="replace") for value in (entry / "cmdline").read_bytes().split(bytes([0])) if value]
        except FileNotFoundError:
            continue
        if not args:
            continue
        executable = Path(args[0]).name
        if (any(Path(arg).name in ("vm-space-clean.sh", "sub2api-vm-validate", "run-validator.sh", "buildctl") for arg in args)
                or (executable == "docker" and len(args) > 1 and args[1] in ("build", "buildx"))
                or any("release-input/" in arg and "/bootstrap" in arg for arg in args)
                or any(".sub2api-release-unit." in arg and "/sub2api-sign-" in arg for arg in args)):
            raise RuntimeError("vm_failed_preserve_worker_present")

    names = run(["docker", "ps", "-a", "--format", "{{.Names}}"]).splitlines()
    if any(name.startswith(("sub2api-v2-", "sub2api-probe-", "sub2api-preview-")) for name in names):
        raise RuntimeError("vm_failed_preserve_probe_container_present")
    networks = run(["docker", "network", "ls", "--format", "{{.Name}}"]).splitlines()
    if "sub2api-v2-net-" + probe_suffix in networks:
        raise RuntimeError("vm_failed_preserve_probe_network_present")
    sql = "BEGIN READ ONLY; SELECT count(*) FROM pg_database WHERE datname='" + probe_db + "'; COMMIT;"
    # probe_db is derived solely from the frozen alphanumeric incident ID.
    command = 'exec psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d postgres -c "' + sql + '"'
    if run(["docker", "exec", "sub2api-postgres", "sh", "-lc", command]).strip() != "0":
        raise RuntimeError("vm_failed_preserve_probe_database_present")
    if run(["docker", "inspect", "-f", "{{.State.Health.Status}}", "sub2api-dev"]).strip() != "healthy":
        raise RuntimeError("vm_failed_preserve_dev_unhealthy")


def vm_script(boot_id: str) -> str:
    if not re.fullmatch(r"[0-9a-f-]{36}", boot_id):
        raise RuntimeError("vm_failed_preserve_boot_unproven")
    suffix = "".join(char for char in INCIDENT_ID if char.isalnum())
    arguments = repr((f"/opt/sub2api-deploy/release-gates/{INCIDENT_ID}",
                      f"/opt/sub2api-deploy/release-logs/{INCIDENT_ID}", boot_id,
                      REMOTE_SHA256, ABSENT, "sub2api_v2_" + suffix[:24], suffix[:12]))
    return """set -Eeuo pipefail
python3 -B - <<'VM_FAILED_PRESERVE'
import fcntl, os, stat, subprocess
if os.getuid() != 0:
    raise RuntimeError('vm_failed_preserve_requires_root')
handles = []
for name, modes in (('/usr/local/libexec/.sub2api-release-unit.lock', (0o600,)),
                    ('/opt/sub2api-deploy/release-gates/release.lock', (0o600, 0o644))):
    descriptor = os.open(name, os.O_RDWR | os.O_NOFOLLOW)
    handles.append(descriptor)
    value = os.fstat(descriptor)
    current = os.lstat(name)
    if (not stat.S_ISREG(value.st_mode) or value.st_uid != 0 or value.st_gid != 0
            or stat.S_IMODE(value.st_mode) not in modes or value.st_nlink != 1
            or (value.st_dev, value.st_ino) != (current.st_dev, current.st_ino)):
        raise RuntimeError('vm_failed_preserve_unsafe_lock')
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
""" + inspect.getsource(_vm_probe) + "\n" + f"""
def run(args):
    result = subprocess.run(args, check=True, capture_output=True, text=True, timeout=30)
    return result.stdout
_vm_probe(*{arguments}, run=run)
print('vm_failed_preserve=verified')
VM_FAILED_PRESERVE
"""


def production_script() -> str:
    return f"""set -Eeuo pipefail
python3 -B - <<'VM_FAILED_PRODUCTION'
import fcntl, os, stat, subprocess
from pathlib import Path
name='/run/lock/sub2api-production-release.lock'
descriptor=os.open(name, os.O_RDWR | os.O_NOFOLLOW)
value=os.fstat(descriptor)
current=os.lstat(name)
if (not stat.S_ISREG(value.st_mode) or value.st_uid != 0 or value.st_gid != 0
        or value.st_nlink != 1 or stat.S_IMODE(value.st_mode) & 0o022
        or (value.st_dev,value.st_ino) != (current.st_dev,current.st_ino)):
    raise RuntimeError('vm_failed_preserve_unsafe_production_lock')
fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
for name in ('/opt/sub2api/releases/{INCIDENT_ID}', '/opt/sub2api/releases/.active-release'):
    if os.path.lexists(name):
        raise RuntimeError('vm_failed_preserve_production_started')
subprocess.run(['systemctl','is-active','--quiet','nginx'], check=True, capture_output=True, timeout=20)
subprocess.run(['systemctl','is-enabled','--quiet','sub2api-backup.timer'], check=True, capture_output=True, timeout=20)
print('production_not_started=verified')
VM_FAILED_PRODUCTION
"""


def _hash_originals(run_dir: Path) -> dict[str, str]:
    result = {}
    for name in ORIGINAL_SHA256:
        path = run_dir / name
        if path.is_symlink() or not path.is_file() or path.stat().st_nlink != 1:
            raise RuntimeError("vm_failed_preserve_unsafe_original")
        result[name] = hash_replaceable_file(path)
    if result != ORIGINAL_SHA256:
        raise RuntimeError("vm_failed_preserve_original_changed")
    return result


def reconcile(args: argparse.Namespace) -> dict:
    """Explicit control entry point, including a read-only verified replay."""
    identifier = args.release_id
    if identifier != INCIDENT_ID:
        raise RuntimeError("vm_failed_preserve_unknown_incident")
    run_dir = supervisor._run_dir(identifier)
    settings = vm_lifecycle.load_settings()
    if settings is None:
        raise RuntimeError("vm_failed_preserve_requires_managed_vm")
    shared = vm_lifecycle._shared_dir(settings)
    if shared.is_symlink() or not shared.is_dir():
        raise RuntimeError("vm_failed_preserve_unsafe_owner")
    with RunLock(supervisor.RUN_ROOT / ".release.lock"), RunLock(shared / "lease.lock"):
        originals = _hash_originals(run_dir)
        manifest = supervisor._read_json(run_dir / "manifest.json", required=True) or {}
        runner = supervisor._read_json(run_dir / "runner.json", required=True) or {}
        state = supervisor._read_json(run_dir / "state.json", required=True) or {}
        lease = vm_lifecycle._read_state(run_dir / "vm-lifecycle.json")
        owner = vm_lifecycle._read_state(shared / "owner.json")
        owner_snapshot, owner_bytes = vm_lifecycle._state_snapshot(shared / "owner.json")
        if owner_snapshot != owner:
            raise RuntimeError("vm_failed_preserve_local_evidence_changed")
        if (manifest.get("schema") != 2 or manifest.get("release_id") != identifier
                or manifest.get("commit_sha") != INCIDENT_COMMIT or manifest.get("profile") != "273"
                or manifest.get("vm_validator_sha256") != INCIDENT_VALIDATOR
                or runner.get("schema") != 1 or runner.get("release_id") != identifier
                or runner.get("commit") != INCIDENT_COMMIT or runner.get("profile") != "273"
                or runner.get("runner_sha256") != manifest.get("runner_sha256")
                or runner.get("status") != "failed" or type(runner.get("exit_code")) is not int or runner["exit_code"] != 1
                or type(runner.get("pid")) is not int or runner["pid"] <= 0
                or not isinstance(runner.get("process_token"), str) or not runner["process_token"]
                or state.get("schema") != 1 or state.get("release_id") != identifier
                or state.get("stage") != "vm_validate" or state.get("status") != "failed"
                or lease.get("vm_identity") != settings.identity or lease.get("source_commit") != INCIDENT_COMMIT
                or lease.get("release_id") != identifier or lease.get("pid") != runner["pid"]
                or lease.get("process_token") != runner["process_token"]
                or lease.get("initially_running") is not False or lease.get("vm_started_by_release") is not True
                or lease.get("lease_status") != "retained" or lease.get("vm_cleanup_status") != "retained_failure"
                or lease.get("vm_power_status") != "running"):
            raise RuntimeError("vm_failed_preserve_original_identity_unproven")
        supervisor.validate_image_id(manifest.get("production_current_image_id"))
        history = state.get("history")
        if not isinstance(history, list) or any(not isinstance(item, dict) or item.get("stage") in supervisor.DANGEROUS_STAGES for item in history):
            raise RuntimeError("vm_failed_preserve_production_state_present")
        for name in ("gate", "release-state.json", "finalization-result.json", "vm-preserve-result.json"):
            path = run_dir / name
            if path.exists() or path.is_symlink():
                raise RuntimeError("vm_failed_preserve_production_state_present")
        helper = _helper_identity()
        local = ReleaseDoctor("273", helper["commit_sha"]).run(("local",))
        if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
            raise RuntimeError("vm_failed_preserve_helper_unproven")
        checks, checks_raw = supervisor._json_snapshot(run_dir / CHECKS_NAME)
        result, result_raw = supervisor._json_snapshot(run_dir / RESULT_NAME)
        binding = {"schema": 1, "scope": "failed-gate-vm-preserve", "release_id": identifier,
                   "application_commit": INCIDENT_COMMIT, "helper": helper,
                   "original_sha256": originals, "remote_sha256": REMOTE_SHA256,
                   "original_owner_sha256": originals["vm-lifecycle.json"],
                   "vm_identity": settings.identity, "vm_boot_id": lease.get("vm_boot_id"),
                   "production_not_started": True, "shutdown_requested": False}
        if checks is not None and checks != binding:
            raise RuntimeError("vm_failed_preserve_checks_changed")
        checks_sha = hashlib.sha256(canonical_json(binding) + b"\n").hexdigest()
        if checks is not None and hashlib.sha256(checks_raw).hexdigest() != checks_sha:
            raise RuntimeError("vm_failed_preserve_checks_changed")
        if result is not None:
            expected = {"schema": 1, "scope": binding["scope"], "release_id": identifier, "status": "verified",
                        "checks_sha256": checks_sha, "helper_commit": helper["commit_sha"],
                        "vm_power_status": "running", "vm_cleanup_status": "preserved",
                        "original_runner_status": "failed", "original_runner_exit": 1,
                        "reconciled_at": result.get("reconciled_at")}
            if (result != expected or type(result.get("reconciled_at")) is not int or checks is None
                    or result_raw != canonical_json(result) + b"\n"):
                raise RuntimeError("vm_failed_preserve_result_changed")
        released = {**lease, "lease_status": "released", "vm_power_status": "running", "vm_cleanup_status": "preserved"}
        if result is not None:
            released["updated_at"] = result["reconciled_at"]
        already_released = result is not None and owner == released
        if not already_released and owner != lease:
            raise RuntimeError("vm_failed_preserve_owner_changed")
        if (not already_released and hashlib.sha256(owner_bytes).hexdigest() != originals["vm-lifecycle.json"]
                or already_released and owner_bytes != canonical_json(released) + b"\n"):
            raise RuntimeError("vm_failed_preserve_owner_changed")
        if getattr(args, "verify_only", False) and not already_released:
            raise RuntimeError("vm_failed_preserve_not_committed")
        vm = vm_lifecycle.VMLease(run_dir, identifier)
        ssh = SSHRunner()

        def preflight():
            if _process_identity(runner["pid"]) == runner["process_token"]:
                raise RuntimeError("vm_failed_preserve_worker_alive")
            if not vm._running():
                raise RuntimeError("vm_failed_preserve_power_unproven")
            if ssh.run("local_vm", vm_script(lease["vm_boot_id"]), {"vm_failed_preserve"}, timeout=90).values != {"vm_failed_preserve": "verified"}:
                raise RuntimeError("vm_failed_preserve_vm_unproven")
            if ssh.run("racknerd", production_script(), {"production_not_started"}, timeout=90).values != {"production_not_started": "verified"}:
                raise RuntimeError("vm_failed_preserve_production_started")
            production = ReleaseDoctor("273", INCIDENT_COMMIT).run(("racknerd",), require_ingress_policy=False)
            if production.get("racknerd_ready") != "true" or production.get("production_current_image_id") != manifest["production_current_image_id"]:
                raise RuntimeError("vm_failed_preserve_production_changed")
            if (_hash_originals(run_dir) != originals or vm_lifecycle._read_state(shared / "owner.json") != owner
                    or vm_lifecycle._state_snapshot(shared / "owner.json")[1] != owner_bytes
                    or _process_identity(runner["pid"]) == runner["process_token"] or not vm._running()):
                raise RuntimeError("vm_failed_preserve_local_evidence_changed")
            # The helper/checkout must remain the reviewed committed version.
            if _helper_identity() != helper:
                raise RuntimeError("vm_failed_preserve_helper_changed")
            local = ReleaseDoctor("273", helper["commit_sha"]).run(("local",))
            if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
                raise RuntimeError("vm_failed_preserve_helper_unproven")

        preflight()
        preflight()
        if not already_released:
            if checks is None:
                atomic_write(run_dir / CHECKS_NAME, canonical_json(binding) + b"\n")
            if result is None:
                result = {"schema": 1, "scope": binding["scope"], "release_id": identifier, "status": "verified",
                          "checks_sha256": checks_sha, "helper_commit": helper["commit_sha"],
                          "vm_power_status": "running", "vm_cleanup_status": "preserved",
                          "original_runner_status": "failed", "original_runner_exit": 1, "reconciled_at": int(time.time())}
                atomic_write(run_dir / RESULT_NAME, canonical_json(result) + b"\n")
            # Verify durable independent records before the final owner commit.
            written_checks, written_checks_raw = supervisor._json_snapshot(run_dir / CHECKS_NAME, required=True)
            written_result, written_result_raw = supervisor._json_snapshot(run_dir / RESULT_NAME, required=True)
            if (written_checks != binding or written_result != result
                    or written_checks_raw != canonical_json(binding) + b"\n"
                    or written_result_raw != canonical_json(result) + b"\n"):
                raise RuntimeError("vm_failed_preserve_written_evidence_changed")
            preflight()
            released["updated_at"] = result["reconciled_at"]
            atomic_write(shared / "owner.json", canonical_json(released) + b"\n")
        value = {"release_id": identifier, "status": "verified", "helper_commit": helper["commit_sha"],
                 "original_runner_status": "failed", "original_runner_exit": 1,
                 "vm_power_status": "running", "vm_cleanup_status": "preserved", "lease_status": "released"}
    print(canonical_json(value).decode("ascii"))
    return value
