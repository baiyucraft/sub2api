"""Preserve the originally running VM after the frozen profile-274 restore-probe failure.

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


INCIDENT_ID = "274-12d4cf67d1c6-1792572302-7b82c271"
INCIDENT_COMMIT = "12d4cf67d1c666a661458d282fd2284fa3d543eb"
INCIDENT_VALIDATOR = "905262cc12f1fcce28e3eded91ac59f92653390f52d526c8b8139832fc54af66"
ORIGINAL_SHA256 = {
    "manifest.json": "66abc06a5ec6cc23dec43e0dca3165479d15a1c67dec4042f277027c97200654",
    "runner.json": "74c16047a4d6d7ec34de3f7c5d8dadcd3180c95e795cd92bde70bd00741e5eb1",
    "state.json": "2751c81b2092a61af5314402eb8f789bccbc8896482e1132c663884f3582ee16",
    "vm-lifecycle.json": "9241e3c0b689cbfe2941d3978a9e5d39a53146ce3eb676761d56d01ae7f08db4",
}
# Filled only from the independently captured immutable incident metadata.
REMOTE_SHA256 = {'build.log': 'aea3d9ca1e06864d5a4d34cac8272b2f7f6b133dc0f5e0e280df162478647c5f',
 'candidate-cli-contract': 'b694c8a9a58634876d84fe88fe1b1b507d2cded1e4541702b19d07897f62854c',
 'candidate-help.log': '8b2ed3f297d43107c65daf0e45f5d51edd4e00014ad54ae186281ef1496858b0',
 'candidate-identity': '264de172cf6ccd8f4702fcfff03b325630bdc4159ac85bd29ae882d50e870ee7',
 'candidate-version.log': '1d34886b9fa0655c8be07fad8a46036a7407010b3f70cb9d62a73f27a327f23c',
 'failure-category': '2b1f37a02dea059ea38554f1eb6cdd43a9185304fa29771924d81e9d8763d332',
 'failure-detail': 'eb4853e01e3662fa1f7160626e81eeb49807fc2f32fa07c94beed3db2e034401',
 'failure-line': '5ced0deaf966775a8b592f6bd95a33a67053b242a7c13654896cd9f2d36a0e37',
 'logs/vm-validate.raw.log': 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
 'manifest.json': '66abc06a5ec6cc23dec43e0dca3165479d15a1c67dec4042f277027c97200654',
 'post-build-space-dry-run': 'ec02c8b7017e253617827e0e8475f7167de746dc716d298bb605b3f970dd1981',
 'release-assets-inventory.tsv': '5db699d8eb2dced97bf2df54df1b19677b8b09cb13052bb8d6e0df45b5f270d0',
 'stage': '64bb11bb02776106c43c382c27bba74780e157e0a6618a11ff68b95a41e82a3a',
 'validator.stderr': 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'}
REMOTE_MODES = {'build.log': 384,
 'candidate-cli-contract': 256,
 'candidate-help.log': 384,
 'candidate-identity': 256,
 'candidate-version.log': 384,
 'failure-category': 256,
 'failure-detail': 256,
 'failure-line': 256,
 'logs/vm-validate.raw.log': 384,
 'manifest.json': 256,
 'post-build-space-dry-run': 256,
 'release-assets-inventory.tsv': 384,
 'stage': 384,
 'validator.stderr': 384}
ABSENT = ('probe-data',
 'probe-redis-data',
 'production-recovery',
 'candidate.tar.gz',
 'migration-plan-apply.json',
 'migrate-candidate.log',
 'plan-after.json',
 'old-image-health.log',
 'output/gate.json',
 'output/gate.sig',
 'output/candidate.tar.gz',
 'output/SHA256SUMS',
 'migration-hook-filename',
 'migration-hook-phase',
 'migration-hook-status',
 'migration-hook-context.sh',
 'migration-hook.stdout',
 'migration-hook.stderr',
 'hook-results.json',
 'fixture-bootstrap.exit',
 'fixture-bootstrap-after.exit',
 'fixture-bootstrap-after.json',
 'fixture-bootstrap-after.log',
 'gate.json',
 'gate.sig',
 'SHA256SUMS',
 'build-failure-code',
 'build-failure-phase',
 'build-failure-role',
 'build-failure-method',
 'build-retry-count',
 'fixture-bootstrap-before.json',
 'fixture-bootstrap.log',
 'plan-before.exit',
 'plan-actual.exit',
 'plan-before.json',
 'plan-actual.json',
 'production-snapshot.json',
 'cli-contract',
 'post-build-space-apply',
 'post-build-space-verified')

CHECKS_NAME = "vm-restore-probe-preserve-checks.json"
RESULT_NAME = "vm-restore-probe-preserve-result.json"


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
                raise RuntimeError("vm_restore_probe_preserve_unsafe_lock")
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            current = os.lstat(name)
            if (value.st_dev, value.st_ino) != (current.st_dev, current.st_ino):
                raise RuntimeError("vm_restore_probe_preserve_unsafe_lock")
        return handles
    except BaseException:
        for descriptor in handles:
            os.close(descriptor)
        raise


def _vm_probe(root, raw, boot_id, expected, absent, probe_db, probe_suffix, *, run, modes=None,
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
                raise RuntimeError("vm_restore_probe_preserve_unsafe_directory")
        value = current.stat()
        if not stat.S_ISDIR(value.st_mode) or value.st_uid != 0 or value.st_gid != 0 or stat.S_IMODE(value.st_mode) != 0o700:
            raise RuntimeError("vm_restore_probe_preserve_unsafe_directory")

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
                raise RuntimeError("vm_restore_probe_preserve_remote_evidence_changed")
            actual = hashlib.sha256()
            for chunk in iter(lambda: handle.read(65536), b""):
                actual.update(chunk)
            if actual.hexdigest() != digest:
                raise RuntimeError("vm_restore_probe_preserve_remote_evidence_changed")

    root, raw = Path(root), Path(raw)
    for path in (root, root / "output", root / "logs", raw):
        directory(path)
    if not expected or set(expected) != set(modes or {}):
        raise RuntimeError("vm_restore_probe_preserve_remote_contract_unproven")
    for name, digest in expected.items():
        file(root / name, modes[name], digest)
    file(raw / "vm-validate.raw.log", 0o600, expected["logs/vm-validate.raw.log"])
    for name in absent:
        if os.path.lexists(root / name):
            raise RuntimeError("vm_restore_probe_preserve_later_stage_or_residue")
    if Path(boot_path).read_text().strip() != boot_id:
        raise RuntimeError("vm_restore_probe_preserve_boot_changed")

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
            raise RuntimeError("vm_restore_probe_preserve_worker_present")

    names = run(["docker", "ps", "-a", "--format", "{{.Names}}"]).splitlines()
    if any(name.startswith(("sub2api-v2-", "sub2api-probe-", "sub2api-preview-")) for name in names):
        raise RuntimeError("vm_restore_probe_preserve_probe_container_present")
    networks = run(["docker", "network", "ls", "--format", "{{.Name}}"]).splitlines()
    if any(name.startswith(("sub2api-v2-net-", "sub2api-probe-")) for name in networks):
        raise RuntimeError("vm_restore_probe_preserve_probe_network_present")
    sql = ("BEGIN READ ONLY; SELECT count(*) FROM pg_database WHERE "
           "left(datname,11)='sub2api_v2_' OR left(datname,14)='sub2api_probe_'; COMMIT;")
    # No isolated migration clone may remain, including another release's clone.
    command = 'exec psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d postgres -c "' + sql + '"'
    if run(["docker", "exec", "sub2api-postgres", "sh", "-lc", command]).strip() != "0":
        raise RuntimeError("vm_restore_probe_preserve_probe_database_present")
    if run(["docker", "inspect", "-f", "{{.State.Health.Status}}", "sub2api-dev"]).strip() != "healthy":
        raise RuntimeError("vm_restore_probe_preserve_dev_unhealthy")


def vm_script(boot_id: str) -> str:
    if not re.fullmatch(r"[0-9a-f-]{36}", boot_id):
        raise RuntimeError("vm_restore_probe_preserve_boot_unproven")
    suffix = "".join(char for char in INCIDENT_ID if char.isalnum())
    arguments = repr((f"/opt/sub2api-deploy/release-gates/{INCIDENT_ID}",
                      f"/opt/sub2api-deploy/release-logs/{INCIDENT_ID}", boot_id,
                      REMOTE_SHA256, ABSENT, "sub2api_v2_" + suffix[:24], suffix[:12]))
    return """set -Eeuo pipefail
python3 -B - <<'VM_RESTORE_PROBE_PRESERVE'
import os, subprocess
if os.getuid() != 0:
    raise RuntimeError('vm_restore_probe_preserve_requires_root')
""" + inspect.getsource(_acquire_locks) + "\n" + inspect.getsource(_vm_probe) + "\n" + f"""
handles = _acquire_locks((('/usr/local/libexec/.sub2api-release-unit.lock', (0o600,)),
                         ('/opt/sub2api-deploy/release-gates/release.lock', (0o600, 0o644))))
def run(args):
    result = subprocess.run(args, check=True, capture_output=True, text=True, timeout=30)
    return result.stdout
_vm_probe(*{arguments}, run=run, modes={REMOTE_MODES!r})
print('vm_restore_probe_preserve=verified')
VM_RESTORE_PROBE_PRESERVE
"""


def production_script() -> str:
    return """set -Eeuo pipefail
python3 -B - <<'VM_RESTORE_PROBE_PRODUCTION'
import os, subprocess
if os.getuid() != 0:
    raise RuntimeError('vm_restore_probe_preserve_requires_root')
""" + inspect.getsource(_acquire_locks) + "\n" + f"""
handles = _acquire_locks((('/run/lock/sub2api-production-release.lock', (0o600, 0o644)),))
for name in ('/opt/sub2api/releases/{INCIDENT_ID}', '/opt/sub2api/releases/.active-release'):
    if os.path.lexists(name):
        raise RuntimeError('vm_restore_probe_preserve_production_started')
subprocess.run(['systemctl','is-active','--quiet','nginx'], check=True, capture_output=True, timeout=20)
subprocess.run(['systemctl','is-enabled','--quiet','sub2api-backup.timer'], check=True, capture_output=True, timeout=20)
print('production_not_started=verified')
VM_RESTORE_PROBE_PRODUCTION
"""


def _hash_originals(run_dir: Path) -> dict[str, str]:
    result = {}
    for name in ORIGINAL_SHA256:
        path = run_dir / name
        if path.is_symlink() or not path.is_file() or path.stat().st_nlink != 1:
            raise RuntimeError("vm_restore_probe_preserve_unsafe_original")
        result[name] = hash_replaceable_file(path)
    if result != ORIGINAL_SHA256:
        raise RuntimeError("vm_restore_probe_preserve_original_changed")
    return result


def reconcile(args: argparse.Namespace) -> dict:
    """Explicit control entry point, including a read-only verified replay."""
    identifier = args.release_id
    if identifier != INCIDENT_ID:
        raise RuntimeError("vm_restore_probe_preserve_unknown_incident")
    run_dir = supervisor._run_dir(identifier)
    settings = vm_lifecycle.load_settings()
    if settings is None:
        raise RuntimeError("vm_restore_probe_preserve_requires_managed_vm")
    shared = vm_lifecycle._shared_dir(settings)
    if (run_dir.is_symlink() or not run_dir.is_dir()
            or any(parent.is_symlink() for parent in run_dir.parents)
            or shared.is_symlink() or not shared.is_dir()
            or any(parent.is_symlink() for parent in shared.parents)):
        raise RuntimeError("vm_restore_probe_preserve_unsafe_owner")
    with RunLock(supervisor.RUN_ROOT / ".release.lock"), vm_lifecycle.shared_lock(settings, shared):
        originals = _hash_originals(run_dir)
        manifest = supervisor._read_json(run_dir / "manifest.json", required=True) or {}
        runner = supervisor._read_json(run_dir / "runner.json", required=True) or {}
        state = supervisor._read_json(run_dir / "state.json", required=True) or {}
        lease = vm_lifecycle._read_state(run_dir / "vm-lifecycle.json")
        owner = vm_lifecycle._read_state(shared / "owner.json")
        owner_snapshot, owner_bytes = vm_lifecycle._state_snapshot(shared / "owner.json")
        if owner_snapshot != owner:
            raise RuntimeError("vm_restore_probe_preserve_local_evidence_changed")
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
            raise RuntimeError("vm_restore_probe_preserve_original_identity_unproven")
        supervisor.validate_image_id(manifest.get("production_current_image_id"))
        history = state.get("history")
        if not isinstance(history, list) or any(not isinstance(item, dict) or item.get("stage") in supervisor.DANGEROUS_STAGES for item in history):
            raise RuntimeError("vm_restore_probe_preserve_production_state_present")
        for name in ("gate", "release-state.json", "finalization-result.json", "vm-preserve-result.json",
                     "runner-terminal-receipt.json",
                     "vm-failed-preserve-checks.json", "vm-failed-preserve-result.json",
                     "vm-fast-fixture-preserve-checks.json", "vm-fast-fixture-preserve-result.json"):
            path = run_dir / name
            if path.exists() or path.is_symlink():
                raise RuntimeError("vm_restore_probe_preserve_production_state_present")
        helper = _helper_identity()
        module_asset = ".agents/skills/sub2api-production-deploy/scripts/release/vm_restore_probe_preserve.py"
        module = (helper.get("assets") or {}).get(module_asset)
        if not isinstance(module, dict) or module.get("sha256") != hashlib.sha256(Path(__file__).read_bytes()).hexdigest():
            raise RuntimeError("vm_restore_probe_preserve_helper_module_uncommitted")
        local = ReleaseDoctor("274", helper["commit_sha"]).run(("local",))
        if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
            raise RuntimeError("vm_restore_probe_preserve_helper_unproven")
        for name in (CHECKS_NAME, RESULT_NAME):
            path = run_dir / name
            if path.is_symlink() or (path.exists() and (not path.is_file() or path.stat().st_nlink != 1)):
                raise RuntimeError("vm_restore_probe_preserve_unsafe_record")
        checks, checks_raw = supervisor._json_snapshot(run_dir / CHECKS_NAME)
        result, result_raw = supervisor._json_snapshot(run_dir / RESULT_NAME)
        binding = {"schema": 1, "scope": "restore-probe-failed-gate-vm-preserve", "release_id": identifier,
                   "application_commit": INCIDENT_COMMIT, "helper": helper,
                   "original_sha256": originals, "remote_sha256": REMOTE_SHA256, "remote_modes": REMOTE_MODES,
                   "remote_absent": list(ABSENT), "failed_stage": "restore_probe",
                   "original_owner_sha256": originals["vm-lifecycle.json"],
                   "vm_identity": settings.identity, "vm_boot_id": lease.get("vm_boot_id"),
                   "production_not_started": True, "shutdown_requested": False}
        if checks is not None and checks != binding:
            raise RuntimeError("vm_restore_probe_preserve_checks_changed")
        checks_sha = hashlib.sha256(canonical_json(binding) + b"\n").hexdigest()
        if checks is not None and hashlib.sha256(checks_raw).hexdigest() != checks_sha:
            raise RuntimeError("vm_restore_probe_preserve_checks_changed")
        if result is not None:
            expected = {"schema": 1, "scope": binding["scope"], "release_id": identifier, "status": "verified",
                        "checks_sha256": checks_sha, "helper_commit": helper["commit_sha"],
                        "vm_power_status": "running", "vm_cleanup_status": "preserved",
                        "original_runner_status": "failed", "original_runner_exit": 1,
                        "reconciled_at": result.get("reconciled_at")}
            if (result != expected or type(result.get("reconciled_at")) is not int or result["reconciled_at"] <= 0 or checks is None
                    or result_raw != canonical_json(result) + b"\n"):
                raise RuntimeError("vm_restore_probe_preserve_result_changed")
        released = {**lease, "lease_status": "released", "vm_power_status": "running", "vm_cleanup_status": "preserved"}
        if result is not None:
            released["updated_at"] = result["reconciled_at"]
        already_released = result is not None and owner == released
        if not already_released and owner != lease:
            raise RuntimeError("vm_restore_probe_preserve_owner_changed")
        if (not already_released and hashlib.sha256(owner_bytes).hexdigest() != originals["vm-lifecycle.json"]
                or already_released and owner_bytes != canonical_json(released) + b"\n"):
            raise RuntimeError("vm_restore_probe_preserve_owner_changed")
        if getattr(args, "verify_only", False) and not already_released:
            raise RuntimeError("vm_restore_probe_preserve_not_committed")
        vm = vm_lifecycle.VMLease(run_dir, identifier)
        ssh = SSHRunner()

        def preflight():
            if _process_identity(runner["pid"]) == runner["process_token"]:
                raise RuntimeError("vm_restore_probe_preserve_worker_alive")
            if not vm._running():
                raise RuntimeError("vm_restore_probe_preserve_power_unproven")
            if ssh.run("local_vm", vm_script(lease["vm_boot_id"]), {"vm_restore_probe_preserve"}, timeout=90).values != {"vm_restore_probe_preserve": "verified"}:
                raise RuntimeError("vm_restore_probe_preserve_vm_unproven")
            if ssh.run("racknerd", production_script(), {"production_not_started"}, timeout=90).values != {"production_not_started": "verified"}:
                raise RuntimeError("vm_restore_probe_preserve_production_started")
            production = ReleaseDoctor("274", INCIDENT_COMMIT).run(("racknerd",), require_ingress_policy=False)
            if production.get("racknerd_ready") != "true" or production.get("production_current_image_id") != manifest["production_current_image_id"]:
                raise RuntimeError("vm_restore_probe_preserve_production_changed")
            if (_hash_originals(run_dir) != originals or vm_lifecycle._read_state(shared / "owner.json") != owner
                    or vm_lifecycle._state_snapshot(shared / "owner.json")[1] != owner_bytes
                    or _process_identity(runner["pid"]) == runner["process_token"] or not vm._running()):
                raise RuntimeError("vm_restore_probe_preserve_local_evidence_changed")
            # The helper/checkout must remain the reviewed committed version.
            if _helper_identity() != helper:
                raise RuntimeError("vm_restore_probe_preserve_helper_changed")
            local = ReleaseDoctor("274", helper["commit_sha"]).run(("local",))
            if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
                raise RuntimeError("vm_restore_probe_preserve_helper_unproven")

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
                raise RuntimeError("vm_restore_probe_preserve_written_evidence_changed")
            preflight()
            released["updated_at"] = result["reconciled_at"]
            atomic_write(shared / "owner.json", canonical_json(released) + b"\n")
        value = {"release_id": identifier, "status": "verified", "helper_commit": helper["commit_sha"],
                 "original_runner_status": "failed", "original_runner_exit": 1,
                 "vm_power_status": "running", "vm_cleanup_status": "preserved", "lease_status": "released"}
    print(canonical_json(value).decode("ascii"))
    return value
