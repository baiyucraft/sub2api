"""Preserve the originally running VM after the frozen profile-274 fast-fixture failure.

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


INCIDENT_ID = "274-3c945e40fbb2-1792561040-1cdc7201"
INCIDENT_COMMIT = "3c945e40fbb2f388efb0a28b47b9e280f6da671d"
INCIDENT_VALIDATOR = "e59f504d4c3a5f11323355fafc64662dca05399d8244db1f6fbdc13dfbcd5b33"
ORIGINAL_SHA256 = {
    'manifest.json': 'db817de9cd5f327da28f024f958a467b79358b336bb9b0869330fedbcbce0b07',
    'runner.json': 'cf70de3946d5d9360eb4fb8afcf398c6330d6c301bf1a80d9a667cb1d05f3781',
    'state.json': 'eeda54facfd09b3659c6ebfcc353b6f7b3eea2b761cabb9a2d9412dbb0dad68d',
    'vm-lifecycle.json': '2a5d71076ecdab5ee8021af680be447bafc0752878412040727e7948b44aeb97',
}
REMOTE_SHA256 = {
    'manifest.json': 'db817de9cd5f327da28f024f958a467b79358b336bb9b0869330fedbcbce0b07',
    'stage': '3dcb552fcc5d970010083422a4e1752ac76eedd8e4fac3f4420cc7af754bb965',
    'failure-category': '8104b95c59f764f9ead9abce86a04406d1c180570a84bc0e95580d6fdef6f0de',
    'failure-line': 'f9e053f236ae5f012657110017573529c39bbfd0fc0fccc774737ed083bee5ef',
    'failure-detail': '4a107292501486ab00d0111ec16e104f8a9a07a5af06ae61d64e0e70d141b50c',
    'plan-before.exit': '9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa',
    'plan-actual.exit': '9a271f2a916b0b6ee6cecb2426f0b3206ef074578be55d9bc94f6f3fe3ab86aa',
    'plan-before.json': 'bde12cb995a26216fce89c95294c1fd76dd4d5f309973c9240005600cedf1ccc',
    'plan-actual.json': '6952a150d6d30de1e62865fd772e7db92beeab36e6bb107c34308600a2551653',
    'fixture-bootstrap-before.json': '6952a150d6d30de1e62865fd772e7db92beeab36e6bb107c34308600a2551653',
    'fixture-bootstrap.log': '63f8b7a9c30b907562c443c3354e6353fc21baeaabff7a4a7ac81d2c964930be',
    'production-snapshot.json': 'a0b2218e43ccc1342bae5deabb4b71df89108d892cd7de17af74a2faffcd62e6',
    'candidate-identity': '264de172cf6ccd8f4702fcfff03b325630bdc4159ac85bd29ae882d50e870ee7',
    'candidate-cli-contract': 'b694c8a9a58634876d84fe88fe1b1b507d2cded1e4541702b19d07897f62854c',
    'logs/vm-validate.raw.log': 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    'validator.stderr': 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
}
ABSENT = (
    "probe-data", "probe-redis-data", "production-recovery", "candidate.tar.gz",
    "migration-plan-apply.json", "migrate-candidate.log", "plan-after.json", "old-image-health.log",
    "output/gate.json", "output/gate.sig", "output/candidate.tar.gz", "output/SHA256SUMS",
    "migration-hook-filename", "migration-hook-phase", "migration-hook-status",
    "migration-hook-context.sh", "migration-hook.stdout", "migration-hook.stderr", "hook-results.json",
    "fixture-bootstrap.exit", "fixture-bootstrap-after.exit", "fixture-bootstrap-after.json",
    "fixture-bootstrap-after.log", "gate.json", "gate.sig", "SHA256SUMS", "build-failure-code", "build-failure-phase", "build-failure-role",
    "build-failure-method", "build-retry-count",
)
CHECKS_NAME = "vm-fast-fixture-preserve-checks.json"
RESULT_NAME = "vm-fast-fixture-preserve-result.json"


def _acquire_locks(specification):
    """Acquire existing safe lock inodes without creating or replacing them."""
    import fcntl
    import os
    import stat

    handles = []
    try:
        for name, modes in specification:
            descriptor = os.open(name, os.O_RDWR | os.O_NOFOLLOW)
            handles.append(descriptor)
            value = os.fstat(descriptor)
            current = os.lstat(name)
            if (not stat.S_ISREG(value.st_mode) or value.st_uid != 0 or value.st_gid != 0
                    or stat.S_IMODE(value.st_mode) not in modes or value.st_nlink != 1
                    or (value.st_dev, value.st_ino) != (current.st_dev, current.st_ino)):
                raise RuntimeError("vm_fast_fixture_preserve_unsafe_lock")
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            current = os.lstat(name)
            if (value.st_dev, value.st_ino) != (current.st_dev, current.st_ino):
                raise RuntimeError("vm_fast_fixture_preserve_unsafe_lock")
        return handles
    except BaseException:
        for descriptor in handles:
            os.close(descriptor)
        raise


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
                raise RuntimeError("vm_fast_fixture_preserve_unsafe_directory")
        value = current.stat()
        if not stat.S_ISDIR(value.st_mode) or value.st_uid != 0 or value.st_gid != 0 or stat.S_IMODE(value.st_mode) != 0o700:
            raise RuntimeError("vm_fast_fixture_preserve_unsafe_directory")

    def file(path, mode, digest):
        path = Path(path)
        # Inspect the opened inode, not a path that could have been exchanged
        # after lstat; reject both symlink and hardlink evidence.
        descriptor = os.open(str(path), os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, "rb") as handle:
            value = os.fstat(handle.fileno())
            current = path.lstat()
            if (not stat.S_ISREG(value.st_mode) or value.st_uid != 0 or value.st_gid != 0
                    or stat.S_IMODE(value.st_mode) != mode or value.st_nlink != 1
                    or (value.st_dev, value.st_ino) != (current.st_dev, current.st_ino)):
                raise RuntimeError("vm_fast_fixture_preserve_remote_evidence_changed")
            actual = hashlib.sha256()
            for chunk in iter(lambda: handle.read(65536), b""):
                actual.update(chunk)
            if actual.hexdigest() != digest:
                raise RuntimeError("vm_fast_fixture_preserve_remote_evidence_changed")

    root, raw = Path(root), Path(raw)
    for path in (root, root / "output", root / "logs", raw):
        directory(path)
    for name, digest in expected.items():
        mode = (0o600 if name in ("stage", "logs/vm-validate.raw.log", "validator.stderr", "fixture-bootstrap.log")
                else 0o444 if name in ("fixture-bootstrap-before.json", "production-snapshot.json") else 0o400)
        file(root / name, mode, digest)
    file(raw / "vm-validate.raw.log", 0o600, expected["logs/vm-validate.raw.log"])
    for name in absent:
        if os.path.lexists(root / name):
            raise RuntimeError("vm_fast_fixture_preserve_later_stage_or_residue")
    if Path(boot_path).read_text().strip() != boot_id:
        raise RuntimeError("vm_fast_fixture_preserve_boot_changed")

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
                or any(".sub2api-release-unit." in arg and "/sub2api-sign-" in arg for arg in args)
                or "release.vm_validate" in args
                or (executable == "docker" and len(args) > 1 and args[1] == "run"
                    and any(arg.startswith("--migration-") for arg in args))):
            raise RuntimeError("vm_fast_fixture_preserve_worker_present")

    names = run(["docker", "ps", "-a", "--format", "{{.Names}}"]).splitlines()
    if any(name.startswith(("sub2api-v2-", "sub2api-probe-", "sub2api-preview-")) for name in names):
        raise RuntimeError("vm_fast_fixture_preserve_probe_container_present")
    networks = run(["docker", "network", "ls", "--format", "{{.Name}}"]).splitlines()
    if any(name.startswith(("sub2api-v2-net-", "sub2api-probe-")) for name in networks):
        raise RuntimeError("vm_fast_fixture_preserve_probe_network_present")
    sql = "BEGIN READ ONLY; SELECT count(*) FROM pg_database WHERE left(datname,11)='sub2api_v2_'; COMMIT;"
    # No isolated migration clone may remain, including another release's clone.
    command = 'exec psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d postgres -c "' + sql + '"'
    if run(["docker", "exec", "sub2api-postgres", "sh", "-lc", command]).strip() != "0":
        raise RuntimeError("vm_fast_fixture_preserve_probe_database_present")
    if run(["docker", "inspect", "-f", "{{.State.Health.Status}}", "sub2api-dev"]).strip() != "healthy":
        raise RuntimeError("vm_fast_fixture_preserve_dev_unhealthy")


def vm_script(boot_id: str) -> str:
    if not re.fullmatch(r"[0-9a-f-]{36}", boot_id):
        raise RuntimeError("vm_fast_fixture_preserve_boot_unproven")
    suffix = "".join(char for char in INCIDENT_ID if char.isalnum())
    arguments = repr((f"/opt/sub2api-deploy/release-gates/{INCIDENT_ID}",
                      f"/opt/sub2api-deploy/release-logs/{INCIDENT_ID}", boot_id,
                      REMOTE_SHA256, ABSENT, "sub2api_v2_" + suffix[:24], suffix[:12]))
    return """set -Eeuo pipefail
python3 -B - <<'VM_FAST_FIXTURE_PRESERVE'
import os, subprocess
if os.getuid() != 0:
    raise RuntimeError('vm_fast_fixture_preserve_requires_root')
""" + inspect.getsource(_acquire_locks) + "\n" + inspect.getsource(_vm_probe) + "\n" + f"""
handles = _acquire_locks((('/usr/local/libexec/.sub2api-release-unit.lock', (0o600,)),
                         ('/opt/sub2api-deploy/release-gates/release.lock', (0o600, 0o644))))
def run(args):
    result = subprocess.run(args, check=True, capture_output=True, text=True, timeout=30)
    return result.stdout
_vm_probe(*{arguments}, run=run)
print('vm_fast_fixture_preserve=verified')
VM_FAST_FIXTURE_PRESERVE
"""


def production_script() -> str:
    return """set -Eeuo pipefail
python3 -B - <<'VM_FAST_FIXTURE_PRODUCTION'
import os, subprocess
if os.getuid() != 0:
    raise RuntimeError('vm_fast_fixture_preserve_requires_root')
""" + inspect.getsource(_acquire_locks) + "\n" + f"""
handles = _acquire_locks((('/run/lock/sub2api-production-release.lock', (0o600, 0o644)),))
for name in ('/opt/sub2api/releases/{INCIDENT_ID}', '/opt/sub2api/releases/.active-release'):
    if os.path.lexists(name):
        raise RuntimeError('vm_fast_fixture_preserve_production_started')
subprocess.run(['systemctl','is-active','--quiet','nginx'], check=True, capture_output=True, timeout=20)
subprocess.run(['systemctl','is-enabled','--quiet','sub2api-backup.timer'], check=True, capture_output=True, timeout=20)
print('production_not_started=verified')
VM_FAST_FIXTURE_PRODUCTION
"""


def _hash_originals(run_dir: Path) -> dict[str, str]:
    result = {}
    for name in ORIGINAL_SHA256:
        path = run_dir / name
        if path.is_symlink() or not path.is_file() or path.stat().st_nlink != 1:
            raise RuntimeError("vm_fast_fixture_preserve_unsafe_original")
        result[name] = hash_replaceable_file(path)
    if result != ORIGINAL_SHA256:
        raise RuntimeError("vm_fast_fixture_preserve_original_changed")
    return result


def reconcile(args: argparse.Namespace) -> dict:
    """Explicit control entry point, including a read-only verified replay."""
    identifier = args.release_id
    if identifier != INCIDENT_ID:
        raise RuntimeError("vm_fast_fixture_preserve_unknown_incident")
    run_dir = supervisor._run_dir(identifier)
    settings = vm_lifecycle.load_settings()
    if settings is None:
        raise RuntimeError("vm_fast_fixture_preserve_requires_managed_vm")
    shared = vm_lifecycle._shared_dir(settings)
    if (run_dir.is_symlink() or not run_dir.is_dir()
            or any(parent.is_symlink() for parent in run_dir.parents)
            or shared.is_symlink() or not shared.is_dir()
            or any(parent.is_symlink() for parent in shared.parents)):
        raise RuntimeError("vm_fast_fixture_preserve_unsafe_owner")
    with RunLock(supervisor.RUN_ROOT / ".release.lock"), RunLock(shared / "lease.lock"):
        originals = _hash_originals(run_dir)
        manifest = supervisor._read_json(run_dir / "manifest.json", required=True) or {}
        runner = supervisor._read_json(run_dir / "runner.json", required=True) or {}
        state = supervisor._read_json(run_dir / "state.json", required=True) or {}
        lease = vm_lifecycle._read_state(run_dir / "vm-lifecycle.json")
        owner = vm_lifecycle._read_state(shared / "owner.json")
        owner_snapshot, owner_bytes = vm_lifecycle._state_snapshot(shared / "owner.json")
        if owner_snapshot != owner:
            raise RuntimeError("vm_fast_fixture_preserve_local_evidence_changed")
        if (manifest.get("schema") != 2 or manifest.get("release_id") != identifier
                or manifest.get("commit_sha") != INCIDENT_COMMIT or manifest.get("profile") != "274"
                or manifest.get("vm_validator_sha256") != INCIDENT_VALIDATOR
                or runner.get("schema") != 1 or runner.get("release_id") != identifier
                or runner.get("commit") != INCIDENT_COMMIT or runner.get("profile") != "274"
                or runner.get("runner_sha256") != manifest.get("runner_sha256")
                or runner.get("status") != "failed" or type(runner.get("exit_code")) is not int or runner["exit_code"] != 1
                or type(runner.get("pid")) is not int or runner["pid"] <= 0
                or not isinstance(runner.get("process_token"), str) or not runner["process_token"]
                or state.get("schema") != 1 or state.get("release_id") != identifier
                or state.get("stage") != "vm_validate" or state.get("status") != "failed"
                or lease.get("vm_identity") != settings.identity or lease.get("source_commit") != INCIDENT_COMMIT
                or lease.get("release_id") != identifier or lease.get("pid") != runner["pid"]
                or lease.get("process_token") != runner["process_token"]
                or lease.get("initially_running") is not True or lease.get("vm_started_by_release") is not False
                or lease.get("lease_status") != "retained" or lease.get("vm_cleanup_status") != "retained_failure"
                or lease.get("vm_power_status") != "running"):
            raise RuntimeError("vm_fast_fixture_preserve_original_identity_unproven")
        supervisor.validate_image_id(manifest.get("production_current_image_id"))
        history = state.get("history")
        if not isinstance(history, list) or any(not isinstance(item, dict) or item.get("stage") in supervisor.DANGEROUS_STAGES for item in history):
            raise RuntimeError("vm_fast_fixture_preserve_production_state_present")
        for name in ("gate", "release-state.json", "finalization-result.json", "vm-preserve-result.json",
                     "runner-terminal-receipt.json",
                     "vm-failed-preserve-checks.json", "vm-failed-preserve-result.json"):
            path = run_dir / name
            if path.exists() or path.is_symlink():
                raise RuntimeError("vm_fast_fixture_preserve_production_state_present")
        helper = _helper_identity()
        module_asset = ".agents/skills/sub2api-production-deploy/scripts/release/vm_fast_fixture_preserve.py"
        module = (helper.get("assets") or {}).get(module_asset)
        if not isinstance(module, dict) or module.get("sha256") != hashlib.sha256(Path(__file__).read_bytes()).hexdigest():
            raise RuntimeError("vm_fast_fixture_preserve_helper_module_uncommitted")
        local = ReleaseDoctor("274", helper["commit_sha"]).run(("local",))
        if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
            raise RuntimeError("vm_fast_fixture_preserve_helper_unproven")
        for name in (CHECKS_NAME, RESULT_NAME):
            path = run_dir / name
            if path.is_symlink() or (path.exists() and (not path.is_file() or path.stat().st_nlink != 1)):
                raise RuntimeError("vm_fast_fixture_preserve_unsafe_record")
        checks, checks_raw = supervisor._json_snapshot(run_dir / CHECKS_NAME)
        result, result_raw = supervisor._json_snapshot(run_dir / RESULT_NAME)
        binding = {"schema": 1, "scope": "fast-fixture-failed-gate-vm-preserve", "release_id": identifier,
                   "application_commit": INCIDENT_COMMIT, "helper": helper,
                   "original_sha256": originals, "remote_sha256": REMOTE_SHA256,
                   "remote_absent": list(ABSENT), "failed_stage": "fast_fixture_bootstrap",
                   "original_owner_sha256": originals["vm-lifecycle.json"],
                   "vm_identity": settings.identity, "vm_boot_id": lease.get("vm_boot_id"),
                   "production_not_started": True, "shutdown_requested": False}
        if checks is not None and checks != binding:
            raise RuntimeError("vm_fast_fixture_preserve_checks_changed")
        checks_sha = hashlib.sha256(canonical_json(binding) + b"\n").hexdigest()
        if checks is not None and hashlib.sha256(checks_raw).hexdigest() != checks_sha:
            raise RuntimeError("vm_fast_fixture_preserve_checks_changed")
        if result is not None:
            expected = {"schema": 1, "scope": binding["scope"], "release_id": identifier, "status": "verified",
                        "checks_sha256": checks_sha, "helper_commit": helper["commit_sha"],
                        "vm_power_status": "running", "vm_cleanup_status": "preserved",
                        "original_runner_status": "failed", "original_runner_exit": 1,
                        "reconciled_at": result.get("reconciled_at")}
            if (result != expected or type(result.get("reconciled_at")) is not int or result["reconciled_at"] <= 0 or checks is None
                    or result_raw != canonical_json(result) + b"\n"):
                raise RuntimeError("vm_fast_fixture_preserve_result_changed")
        released = {**lease, "lease_status": "released", "vm_power_status": "running", "vm_cleanup_status": "preserved"}
        if result is not None:
            released["updated_at"] = result["reconciled_at"]
        already_released = result is not None and owner == released
        if not already_released and owner != lease:
            raise RuntimeError("vm_fast_fixture_preserve_owner_changed")
        if (not already_released and hashlib.sha256(owner_bytes).hexdigest() != originals["vm-lifecycle.json"]
                or already_released and owner_bytes != canonical_json(released) + b"\n"):
            raise RuntimeError("vm_fast_fixture_preserve_owner_changed")
        if getattr(args, "verify_only", False) and not already_released:
            raise RuntimeError("vm_fast_fixture_preserve_not_committed")
        vm = vm_lifecycle.VMLease(run_dir, identifier)
        ssh = SSHRunner()

        def preflight():
            if _process_identity(runner["pid"]) == runner["process_token"]:
                raise RuntimeError("vm_fast_fixture_preserve_worker_alive")
            if not vm._running():
                raise RuntimeError("vm_fast_fixture_preserve_power_unproven")
            if ssh.run("local_vm", vm_script(lease["vm_boot_id"]), {"vm_fast_fixture_preserve"}, timeout=90).values != {"vm_fast_fixture_preserve": "verified"}:
                raise RuntimeError("vm_fast_fixture_preserve_vm_unproven")
            if ssh.run("racknerd", production_script(), {"production_not_started"}, timeout=90).values != {"production_not_started": "verified"}:
                raise RuntimeError("vm_fast_fixture_preserve_production_started")
            production = ReleaseDoctor("274", INCIDENT_COMMIT).run(("racknerd",), require_ingress_policy=False)
            if production.get("racknerd_ready") != "true" or production.get("production_current_image_id") != manifest["production_current_image_id"]:
                raise RuntimeError("vm_fast_fixture_preserve_production_changed")
            if (_hash_originals(run_dir) != originals or vm_lifecycle._read_state(shared / "owner.json") != owner
                    or vm_lifecycle._state_snapshot(shared / "owner.json")[1] != owner_bytes
                    or _process_identity(runner["pid"]) == runner["process_token"] or not vm._running()):
                raise RuntimeError("vm_fast_fixture_preserve_local_evidence_changed")
            # The helper/checkout must remain the reviewed committed version.
            if _helper_identity() != helper:
                raise RuntimeError("vm_fast_fixture_preserve_helper_changed")
            local = ReleaseDoctor("274", helper["commit_sha"]).run(("local",))
            if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
                raise RuntimeError("vm_fast_fixture_preserve_helper_unproven")

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
                raise RuntimeError("vm_fast_fixture_preserve_written_evidence_changed")
            preflight()
            released["updated_at"] = result["reconciled_at"]
            atomic_write(shared / "owner.json", canonical_json(released) + b"\n")
        value = {"release_id": identifier, "status": "verified", "helper_commit": helper["commit_sha"],
                 "original_runner_status": "failed", "original_runner_exit": 1,
                 "vm_power_status": "running", "vm_cleanup_status": "preserved", "lease_status": "released"}
    print(canonical_json(value).decode("ascii"))
    return value
