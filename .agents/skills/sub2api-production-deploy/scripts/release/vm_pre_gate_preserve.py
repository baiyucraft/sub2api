"""Close only a failed release that stopped before creating any Gate state."""

from __future__ import annotations

import hashlib
import re
import shlex
import stat
import time
from pathlib import Path
from typing import Any

from . import supervisor, vm_lifecycle
from .atomic import atomic_write, canonical_json
from .doctor import ReleaseDoctor
from .finalization import _helper_identity
from .manifest import RELEASE_ID
from .production_snapshot import decode_snapshot, snapshot_script, snapshot_sha256
from .recovery_evidence import strict_json
from .ssh import SSHRunner
from .state import RunLock


CHECKS_NAME = "vm-pre-gate-preserve-checks.json"
RESULT_NAME = "vm-pre-gate-preserve-result.json"
VM_GATE_ROOT = "/opt/sub2api-deploy/release-gates"
VM_UNIT_LOCK = "/usr/local/libexec/.sub2api-release-unit.lock"
VM_GATE_LOCK = f"{VM_GATE_ROOT}/release.lock"
PRODUCTION_RELEASE_ROOT = "/opt/sub2api/releases"
PRODUCTION_STATE_ROOT = "/opt/sub2api/backups/release-state"
PRODUCTION_ACTIVE_SLOT = "/opt/sub2api/active-app"

_ORIGINAL_NAMES = ("manifest.json", "runner.json", "state.json", "vm-lifecycle.json")
_LOCAL_SIDE_EFFECTS = (
    "gate",
    "release-state.json",
    "production-result.json",
    "runner-terminal-receipt.json",
    "finalization-result.json",
    "finalization-checks.json",
    "vm-preserve-result.json",
    "vm-failed-preserve-checks.json",
    "vm-failed-preserve-result.json",
    "pre-state-claim-checks.json",
    "pre-state-claim-result.json",
    "vm-fast-fixture-preserve-checks.json",
    "vm-fast-fixture-preserve-result.json",
    "vm-restore-probe-preserve-checks.json",
    "vm-restore-probe-preserve-result.json",
)


def _sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _regular_absent(path: Path) -> bool:
    try:
        path.lstat()
    except FileNotFoundError:
        return True
    return False


def _vm_probe_script(release_id: str, expected_boot: str) -> str:
    suffix = hashlib.sha256(release_id.encode("ascii")).hexdigest()
    probe_db = "sub2api_v2_" + suffix[:24]
    probe_db_alt = "sub2api_probe_" + suffix[:24]
    probe_suffix = suffix[:12]
    probe_names = (
        f"sub2api-candidate-{release_id}", f"sub2api-vm-only-{release_id}",
        f"sub2api-v2-app-{probe_suffix}", f"sub2api-v2-old-{probe_suffix}",
        f"sub2api-v2-redis-{probe_suffix}", f"sub2api-probe-redis-{probe_suffix}",
        f"sub2api-probe-app-{probe_suffix}", f"sub2api-probe-old-{probe_suffix}",
    )
    networks = (f"sub2api-v2-net-{probe_suffix}", f"sub2api-probe-net-{probe_suffix}")
    return f'''set -Eeuo pipefail
python3 -B - <<'VM_PRE_GATE'
import fcntl, os, stat, subprocess
from pathlib import Path
if os.getuid() != 0:
    raise RuntimeError("vm_pre_gate_requires_root")
handles=[]
for name, modes in (({VM_UNIT_LOCK!r}, (0o600,)), ({VM_GATE_LOCK!r}, (0o600, 0o644))):
    fd=os.open(name, os.O_RDWR | os.O_NOFOLLOW)
    handles.append(fd)
    current=os.lstat(name); opened=os.fstat(fd)
    if (not stat.S_ISREG(opened.st_mode) or opened.st_uid != 0 or opened.st_gid != 0
        or opened.st_nlink != 1 or stat.S_IMODE(opened.st_mode) not in modes
        or (opened.st_dev,opened.st_ino)!=(current.st_dev,current.st_ino)):
        raise RuntimeError("vm_pre_gate_unsafe_lock")
    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
    current=os.lstat(name)
    if (opened.st_dev,opened.st_ino)!=(current.st_dev,current.st_ino):
        raise RuntimeError("vm_pre_gate_unsafe_lock")
boot=Path('/proc/sys/kernel/random/boot_id').read_text().strip()
if boot != {expected_boot!r}:
    raise RuntimeError("vm_pre_gate_boot_changed")
for path in ({(VM_GATE_ROOT + '/' + release_id)!r},
             {('/opt/sub2api-deploy/release-logs/' + release_id + '/vm-validate.raw.log')!r}):
    try:
        os.lstat(path)
    except FileNotFoundError:
        pass
    else:
        raise RuntimeError("vm_pre_gate_gate_present")
ancestors=set(); pid=os.getpid()
while pid and pid not in ancestors:
    ancestors.add(pid)
    try: pid=int((Path('/proc')/str(pid)/'stat').read_text().rsplit(')',1)[1].split()[1])
    except (FileNotFoundError,ProcessLookupError): break
needles=('vm-space-clean.sh','sub2api-vm-validate','run-validator.sh','buildctl',
         'sub2api-sign-gate','sub2api-sign-dr-evidence')
count=0
for entry in Path('/proc').iterdir():
    if not entry.name.isdigit() or int(entry.name) in ancestors: continue
    try: argv=[part.decode(errors='replace') for part in (entry/'cmdline').read_bytes().split(bytes([0])) if part]
    except (FileNotFoundError,ProcessLookupError): continue
    if not argv: continue
    if (any(Path(part).name in needles for part in argv)
        or (Path(argv[0]).name == 'docker' and len(argv)>1 and argv[1] in ('build','buildx'))
        or any('release-input/' in part and '/bootstrap' in part for part in argv)):
        count+=1
if count: raise RuntimeError("vm_pre_gate_worker_present")
run=lambda args: subprocess.run(args,check=True,capture_output=True,text=True,timeout=30).stdout
containers=run(['docker','ps','-a','--format','{{{{.Names}}}}']).splitlines()
if any(name in containers for name in {probe_names!r}):
    raise RuntimeError("vm_pre_gate_probe_container_present")
networks=run(['docker','network','ls','--format','{{{{.Name}}}}']).splitlines()
if any(name in networks for name in {networks!r}):
    raise RuntimeError("vm_pre_gate_probe_network_present")
db_sql="BEGIN READ ONLY; SELECT count(*) FROM pg_database WHERE datname IN ({probe_db!r},{probe_db_alt!r}); COMMIT;"
db_command='exec psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${{POSTGRES_USER:-postgres}}" -d postgres -c "'+db_sql+'"'
if run(['docker','exec','sub2api-postgres','sh','-lc',db_command]).strip() != '0':
    raise RuntimeError("vm_pre_gate_probe_database_present")
dev_health=run(['docker','inspect','-f','{{{{.State.Health.Status}}}}','sub2api-dev']).strip()
if dev_health != 'healthy': raise RuntimeError("vm_pre_gate_dev_unhealthy")
print('vm_pre_gate_preserve=verified')
print('vm_boot_id='+boot)
print('vm_process_count=0')
print('vm_candidate_absent=true')
print('vm_probe_resources_absent=true')
print('vm_health='+dev_health)
VM_PRE_GATE
'''


def _production_probe_script(release_id: str) -> str:
    release_root = shlex.quote(PRODUCTION_RELEASE_ROOT)
    state_root = shlex.quote(PRODUCTION_STATE_ROOT)
    active_slot = shlex.quote(PRODUCTION_ACTIVE_SLOT)
    release = shlex.quote(release_id)
    return f'''set -Eeuo pipefail
production_lock=/run/lock/sub2api-production-release.lock
test "$(id -u)" = 0
test -f "$production_lock"
test ! -L "$production_lock"
exec 9<"$production_lock"
lock_path=$(stat -c '%u:%g:%a:%h:%d:%i' "$production_lock")
case "$lock_path" in 0:0:600:1:*|0:0:644:1:*) ;; *) exit 1 ;; esac
flock -n 9
test "$(stat -Lc '%u:%g:%a:%h:%d:%i' /proc/self/fd/9)" = "$lock_path"
test "$(stat -c '%u:%g:%a:%h:%d:%i' "$production_lock")" = "$lock_path"
release_root={release_root}
state_root={state_root}
release_dir="$release_root/{release}"
state_dir="$state_root/{release}"
active_claim="$release_root/.active-release"
for root in "$release_root" "$state_root"; do
  test -d "$root"
  test ! -L "$root"
  test "$(realpath -e -- "$root")" = "$root"
done
for path in "$release_dir" "$state_dir" "$active_claim"; do
  test ! -e "$path"
  test ! -L "$path"
done
for staging in "$state_root/.{release}.staging."*; do
  test ! -e "$staging"
  test ! -L "$staging"
done
container_names=$(docker ps -a --format '{{{{.Names}}}}')
candidate_container=sub2api-candidate-{release}
if grep -Fxq -- sub2api-migrate-{release} <<<"$container_names"; then exit 1; else test "$?" = 1; fi
if grep -Fxq -- "$candidate_container" <<<"$container_names"; then
  candidate_absent=false
else
  grep_code=$?
  test "$grep_code" = 1 || exit "$grep_code"
  candidate_absent=true
fi
test "$candidate_absent" = true
slot={active_slot}
test -f "$slot"
test ! -L "$slot"
test -f /etc/nginx/conf.d/sub2api-release-upstream.conf
test ! -L /etc/nginx/conf.d/sub2api-release-upstream.conf
active_container=$(sed -n 's/^container=//p' "$slot")
active_port=$(sed -n 's/^port=//p' "$slot")
active_release=$(sed -n 's/^release_id=//p' "$slot")
[[ "$active_container" =~ ^[A-Za-z0-9_.-]{{1,80}}$ ]]
[[ "$active_port" == 18080 || "$active_port" == 18081 ]]
test "$active_release" != {release}
grep -Fxq -- "$active_container" <<<"$container_names"
app_health=$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container")
test "$app_health" = healthy
running_image_id=$(docker inspect -f '{{{{.Image}}}}' "$active_container")
[[ "$running_image_id" =~ ^sha256:[0-9a-f]{{64}}$ ]]
test "$(grep -Fc "server 127.0.0.1:$active_port;" /etc/nginx/conf.d/sub2api-release-upstream.conf)" = 1
route_sha256=$(sha256sum /etc/nginx/conf.d/sub2api-release-upstream.conf | awk '{{print $1}}')
active_slot_sha256=$(sha256sum "$slot" | awk '{{print $1}}')
nginx_state=$(systemctl is-active nginx)
test "$nginx_state" = active
backup_timer_state=$(systemctl is-enabled sub2api-backup.timer)
test "$backup_timer_state" = enabled
{snapshot_script()}
printf 'production_pre_gate_preserve=verified\\nproduction_claim_absent=true\\nproduction_state_absent=true\\nproduction_candidate_absent=%s\\nproduction_health=%s\\nproduction_image_id=%s\\nproduction_route_sha256=%s\\nproduction_active_slot_sha256=%s\\nproduction_nginx=%s\\nproduction_backup_timer=%s\\n' "$candidate_absent" "$app_health" "$running_image_id" "$route_sha256" "$active_slot_sha256" "$nginx_state" "$backup_timer_state"
'''


def _read_originals(run_dir: Path) -> tuple[dict[str, dict[str, Any]], dict[str, bytes]]:
    values: dict[str, dict[str, Any]] = {}
    raw: dict[str, bytes] = {}
    for name in _ORIGINAL_NAMES:
        path = run_dir / name
        _assert_safe_file(path)
        _, content = supervisor._json_snapshot(path, required=True)
        values[name] = strict_json(content)
        raw[name] = content
    return values, raw


def _assert_safe_file(path: Path) -> None:
    metadata = path.lstat()
    if (not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1
            or getattr(metadata, "st_file_attributes", 0) & 0x400):
        raise RuntimeError("vm_pre_gate_unsafe_local_file")


def _validate_originals(identifier: str, run_dir: Path, docs: dict[str, dict[str, Any]],
                        original_raw: dict[str, bytes], vm_identity: str) -> tuple[str, str]:
    manifest, runner, state, lifecycle = (docs[name] for name in _ORIGINAL_NAMES)
    commit = manifest.get("commit_sha")
    profile = manifest.get("profile")
    try:
        supervisor.validate_commit(commit)
    except (TypeError, ValueError):
        raise RuntimeError("vm_pre_gate_release_identity_unproven") from None
    if (manifest.get("schema") != 2 or manifest.get("release_id") != identifier
            or not isinstance(profile, str) or not re.fullmatch(r"[0-9]{3}", profile)
            or not identifier.startswith(f"{profile}-{commit[:12]}-")
            or any(manifest.get(field) is not None for field in
                   ("candidate_image_id", "production_current_image_id", "production_snapshot_sha256"))
            or not re.fullmatch(r"[0-9a-f]{64}", str(manifest.get("runner_sha256", "")))):
        raise RuntimeError("vm_pre_gate_release_identity_unproven")
    if (runner.get("schema") != 1 or runner.get("release_id") != identifier
            or runner.get("commit") != commit or runner.get("profile") != profile
            or runner.get("runner_sha256") != manifest["runner_sha256"]
            or runner.get("status") != "failed" or type(runner.get("exit_code")) is not int
            or runner["exit_code"] == 0 or type(runner.get("pid")) is not int or runner["pid"] <= 0
            or not isinstance(runner.get("process_token"), str) or not runner["process_token"]
            or type(runner.get("started_at")) is not int or type(runner.get("finished_at")) is not int
            or runner["started_at"] <= 0 or runner["finished_at"] < runner["started_at"]
            or not supervisor._runner_exit_proven(runner)):
        raise RuntimeError("vm_pre_gate_worker_exit_unproven")
    from .terminal_receipt import terminal_receipt_status

    if terminal_receipt_status(run_dir, runner, original_raw["runner.json"]) not in {"legacy_schema_1", "not_required"}:
        raise RuntimeError("vm_pre_gate_runner_receipt_invalid")
    if (state.get("schema") != 1 or state.get("release_id") != identifier
            or state.get("stage") != "init" or state.get("status") != "not_started"
            or state.get("history") != []):
        raise RuntimeError("vm_pre_gate_state_started")
    for name in _LOCAL_SIDE_EFFECTS:
        if not _regular_absent(run_dir / name):
            raise RuntimeError("vm_pre_gate_local_side_effect_present")
    if (lifecycle.get("schema") != 1 or lifecycle.get("release_id") != identifier
            or lifecycle.get("source_commit") != commit
            or lifecycle.get("vm_identity") != vm_identity
            or lifecycle.get("pid") != runner["pid"]
            or lifecycle.get("process_token") != runner["process_token"]
            or not lifecycle.get("lease_token") or not isinstance(lifecycle["lease_token"], str)
            or lifecycle.get("lease_status") != "retained"
            or lifecycle.get("vm_cleanup_status") != "retained_failure"
            or lifecycle.get("vm_power_status") != "running"
            or lifecycle.get("initially_running") is not False
            or lifecycle.get("vm_started_by_release") is not True
            or not isinstance(lifecycle.get("vm_boot_id"), str)
            or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", lifecycle["vm_boot_id"])):
        raise RuntimeError("vm_pre_gate_lifecycle_unproven")
    return commit, profile


def _original_sha(raw: dict[str, bytes]) -> dict[str, str]:
    return {name: _sha256(raw[name]) for name in _ORIGINAL_NAMES}


def _capture_local(run_dir: Path, shared: Path, expected_raw: dict[str, bytes], expected_owner_raw: bytes) -> None:
    if _read_originals(run_dir)[1] != expected_raw:
        raise RuntimeError("vm_pre_gate_local_evidence_changed")
    if any(not _regular_absent(run_dir / name) for name in _LOCAL_SIDE_EFFECTS):
        raise RuntimeError("vm_pre_gate_local_side_effect_present")
    _assert_safe_file(shared / "owner.json")
    if vm_lifecycle._state_snapshot(shared / "owner.json")[1] != expected_owner_raw:
        raise RuntimeError("vm_pre_gate_owner_changed")


def _remote_snapshot(identifier: str, lifecycle: dict, profile: str, commit: str,
                     vm: vm_lifecycle.VMLease, settings: vm_lifecycle.VMSettings) -> dict[str, Any]:
    if vm_lifecycle.load_settings() != settings or vm.settings != settings or not vm._running():
        raise RuntimeError("vm_pre_gate_vm_identity_or_power_unproven")
    vm_fields = {"vm_pre_gate_preserve", "vm_boot_id", "vm_process_count", "vm_candidate_absent",
                 "vm_probe_resources_absent", "vm_health"}
    ssh = SSHRunner(require_environment=True)
    environment = ssh.deployment_environment
    if environment is None:
        raise RuntimeError("vm_pre_gate_config_changed")
    vm_values = ssh.run("local_vm", _vm_probe_script(identifier, lifecycle["vm_boot_id"]), vm_fields,
                        timeout=90).values
    expected_vm = {
        "vm_pre_gate_preserve": "verified",
        "vm_boot_id": lifecycle["vm_boot_id"],
        "vm_process_count": "0",
        "vm_candidate_absent": "true",
        "vm_probe_resources_absent": "true",
        "vm_health": "healthy",
    }
    if vm_values != expected_vm:
        raise RuntimeError("vm_pre_gate_remote_vm_unproven")
    production_fields = {
        "production_pre_gate_preserve", "production_claim_absent", "production_state_absent",
        "production_candidate_absent", "production_health", "production_image_id",
        "production_route_sha256", "production_active_slot_sha256", "production_nginx", "production_backup_timer",
        "snapshot_b64",
    }
    script = _production_probe_script(identifier)
    production = ssh.run(environment.direct_route["target_node"], script, production_fields, timeout=90).values
    expected = {
        "production_pre_gate_preserve": "verified",
        "production_claim_absent": "true",
        "production_state_absent": "true",
        "production_candidate_absent": "true",
        "production_health": "healthy",
        "production_nginx": "active",
        "production_backup_timer": "enabled",
    }
    if (any(production.get(key) != value for key, value in expected.items())
            or not re.fullmatch(r"sha256:[0-9a-f]{64}", production.get("production_image_id", ""))
            or not re.fullmatch(r"[0-9a-f]{64}", production.get("production_route_sha256", ""))
            or not re.fullmatch(r"[0-9a-f]{64}", production.get("production_active_slot_sha256", ""))):
        raise RuntimeError("vm_pre_gate_remote_production_unproven")
    doctor = ReleaseDoctor(profile, commit, runner=ssh).run(("racknerd",), require_ingress_policy=False)
    if doctor.get("racknerd_ready") != "true" or doctor.get("production_current_image_id") != production["production_image_id"]:
        raise RuntimeError("vm_pre_gate_production_health_unproven")
    try:
        snapshot = decode_snapshot(production["snapshot_b64"])
        doctor_snapshot = decode_snapshot(doctor["production_snapshot_b64"])
    except (KeyError, RuntimeError):
        raise RuntimeError("vm_pre_gate_production_snapshot_unproven") from None
    if (snapshot != doctor_snapshot or snapshot.get("current_image_id") != production["production_image_id"]
            or not snapshot.get("production_current_commit_sha")):
        raise RuntimeError("vm_pre_gate_production_snapshot_unproven")
    production_observation = {
        **{key: value for key, value in production.items() if key != "snapshot_b64"},
        "migration_snapshot_sha256": snapshot_sha256(snapshot),
        "production_commit": snapshot.get("production_current_commit_sha", ""),
    }
    ssh.assert_configuration_current()
    if vm_lifecycle.load_settings() != settings or not vm._running():
        raise RuntimeError("vm_pre_gate_remote_state_changed")
    return {"vm": vm_values, "production": production_observation}


def _read_record(path: Path) -> tuple[dict[str, Any] | None, bytes]:
    if _regular_absent(path):
        return None, b""
    _assert_safe_file(path)
    _, raw = supervisor._json_snapshot(path, required=True)
    return strict_json(raw), raw


def reconcile(args) -> dict[str, Any]:
    identifier = args.release_id
    if not RELEASE_ID.fullmatch(identifier):
        raise RuntimeError("vm_pre_gate_release_identity_unproven")
    run_dir = supervisor._run_dir(identifier)
    if run_dir.is_symlink() or not run_dir.is_dir():
        raise RuntimeError("vm_pre_gate_run_dir_unproven")
    settings = vm_lifecycle.load_settings()
    if settings is None:
        raise RuntimeError("vm_pre_gate_requires_managed_vm")
    shared = vm_lifecycle._shared_dir(settings)
    if shared.is_symlink() or not shared.is_dir():
        raise RuntimeError("vm_pre_gate_shared_dir_unproven")

    with RunLock(supervisor.RUN_ROOT / ".release.lock"), vm_lifecycle.shared_lock(settings, shared):
        docs, original_raw = _read_originals(run_dir)
        commit, profile = _validate_originals(identifier, run_dir, docs, original_raw, settings.identity)
        lifecycle = docs["vm-lifecycle.json"]
        lease, lease_raw = vm_lifecycle._state_snapshot(run_dir / "vm-lifecycle.json")
        _assert_safe_file(shared / "owner.json")
        owner, owner_raw = vm_lifecycle._state_snapshot(shared / "owner.json")
        if lease != lifecycle or lease_raw != original_raw["vm-lifecycle.json"]:
            raise RuntimeError("vm_pre_gate_lifecycle_changed")
        vm = vm_lifecycle.VMLease(run_dir, identifier)
        if vm.settings != settings:
            raise RuntimeError("vm_pre_gate_config_changed")

        checks_path = run_dir / CHECKS_NAME
        result_path = run_dir / RESULT_NAME
        existing_checks, checks_raw = _read_record(checks_path)
        existing_result, result_raw = _read_record(result_path)
        if (existing_checks is None) != (not checks_raw) or (existing_result is None) != (not result_raw):
            raise RuntimeError("vm_pre_gate_record_invalid")
        if existing_result is not None and existing_checks is None:
            raise RuntimeError("vm_pre_gate_result_changed")

        first_owner = owner == lease and owner_raw == lease_raw
        replay_owner = False
        if not first_owner:
            if existing_checks is None or existing_result is None:
                raise RuntimeError("vm_pre_gate_owner_unproven")
            replay_owner = True
        if getattr(args, "verify_only", False) and not replay_owner:
            raise RuntimeError("vm_pre_gate_not_committed")

        helper = _helper_identity()
        local = ReleaseDoctor(profile, helper["commit_sha"]).run(("local",))
        if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
            raise RuntimeError("vm_pre_gate_helper_unproven")
        _capture_local(run_dir, shared, original_raw, owner_raw)
        remote = _remote_snapshot(identifier, lifecycle, profile, commit, vm, settings)
        original_sha = _original_sha(original_raw)
        checks = {
            "schema": 1,
            "type": "sub2api-vm-pre-gate-preserve-checks",
            "release_id": identifier,
            "application_commit": commit,
            "profile": profile,
            "failed_stage": "before_gate",
            "original_sha256": original_sha,
            "original_owner_sha256": _sha256(lease_raw),
            "worker_pid": lifecycle["pid"],
            "worker_process_token_sha256": _sha256(lifecycle["process_token"].encode("utf-8")),
            "vm_identity": settings.identity,
            "vm_boot_id": lifecycle["vm_boot_id"],
            "vm_started_by_release": True,
            "helper": helper,
            "remote": remote,
        }
        checks_bytes = canonical_json(checks) + b"\n"
        if existing_checks is not None:
            if (existing_checks != checks or checks_raw != checks_bytes):
                raise RuntimeError("vm_pre_gate_checks_changed")
        else:
            atomic_write(checks_path, checks_bytes, 0o600)
            persisted, persisted_raw = _read_record(checks_path)
            if persisted != checks or persisted_raw != checks_bytes:
                raise RuntimeError("vm_pre_gate_checks_write_unproven")
            existing_checks, checks_raw = persisted, persisted_raw

        checks_sha = _sha256(checks_raw)
        if existing_result is None:
            result = {
                "schema": 1,
                "type": "sub2api-vm-pre-gate-preserve-result",
                "release_id": identifier,
                "status": "verified",
                "application_deployed": False,
                "original_runner_status": "failed",
                "original_runner_exit": docs["runner.json"]["exit_code"],
                "vm_power_status": "running",
                "vm_started_by_release": True,
                "vm_cleanup_status": "preserved",
                "lease_status": "released",
                "checks_sha256": checks_sha,
                "reconciled_at": int(time.time()),
            }
            result_bytes = canonical_json(result) + b"\n"
            atomic_write(result_path, result_bytes, 0o600)
            persisted, persisted_raw = _read_record(result_path)
            if persisted != result or persisted_raw != result_bytes:
                raise RuntimeError("vm_pre_gate_result_write_unproven")
            existing_result, result_raw = persisted, persisted_raw
        else:
            result = existing_result
            if (result.get("schema") != 1
                    or result.get("type") != "sub2api-vm-pre-gate-preserve-result"
                    or result.get("release_id") != identifier
                    or result.get("status") != "verified"
                    or result.get("application_deployed") is not False
                    or result.get("original_runner_status") != "failed"
                    or result.get("original_runner_exit") != docs["runner.json"]["exit_code"]
                    or result.get("vm_power_status") != "running"
                    or result.get("vm_started_by_release") is not True
                    or result.get("vm_cleanup_status") != "preserved"
                    or result.get("lease_status") != "released"
                    or result.get("checks_sha256") != checks_sha
                    or type(result.get("reconciled_at")) is not int
                    or result["reconciled_at"] <= 0
                    or result_raw != canonical_json(result) + b"\n"):
                raise RuntimeError("vm_pre_gate_result_changed")

        released = {
            **lease,
            "lease_status": "released",
            "vm_power_status": "running",
            "vm_cleanup_status": "preserved",
            "updated_at": result["reconciled_at"],
            "pre_gate_preserve_result_sha256": _sha256(result_raw),
        }
        current_owner, current_owner_raw = vm_lifecycle._state_snapshot(shared / "owner.json")
        if replay_owner:
            if current_owner != released or current_owner_raw != canonical_json(released) + b"\n":
                raise RuntimeError("vm_pre_gate_owner_changed")
        elif current_owner != lease or current_owner_raw != lease_raw:
            raise RuntimeError("vm_pre_gate_owner_changed")

        _capture_local(run_dir, shared, original_raw, current_owner_raw)
        final_remote = _remote_snapshot(identifier, lifecycle, profile, commit, vm, settings)
        if final_remote != remote:
            raise RuntimeError("vm_pre_gate_remote_state_changed")
        if _helper_identity() != helper:
            raise RuntimeError("vm_pre_gate_helper_changed")
        local = ReleaseDoctor(profile, helper["commit_sha"]).run(("local",))
        if local.get("local_ready") != "true" or local.get("host_keys_ready") != "true":
            raise RuntimeError("vm_pre_gate_helper_unproven")
        _capture_local(run_dir, shared, original_raw, current_owner_raw)
        for path, expected in ((checks_path, checks_raw), (result_path, result_raw)):
            _, actual = _read_record(path)
            if actual != expected:
                raise RuntimeError("vm_pre_gate_record_changed")
        if not supervisor._runner_exit_proven(docs["runner.json"]):
            raise RuntimeError("vm_pre_gate_worker_exit_unproven")

        if not replay_owner:
            # The shared owner is the last write; original release evidence stays immutable.
            atomic_write(shared / "owner.json", canonical_json(released) + b"\n", 0o600)
            committed, committed_raw = vm_lifecycle._state_snapshot(shared / "owner.json")
            if committed != released or committed_raw != canonical_json(released) + b"\n":
                raise RuntimeError("vm_pre_gate_owner_commit_unproven")

        value = {
            "release_id": identifier,
            "status": "verified",
            "application_deployed": False,
            "runner_status": "failed",
            "runner_exit": docs["runner.json"]["exit_code"],
            "vm_power_status": "running",
            "vm_started_by_release": True,
            "vm_cleanup_status": "preserved",
            "lease_status": "released",
            "checks_sha256": checks_sha,
        }
    print(canonical_json(value).decode("ascii"))
    return value
