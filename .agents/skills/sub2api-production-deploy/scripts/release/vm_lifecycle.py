"""Owned, fail-closed power management for the local release VM."""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import subprocess
import time
from typing import Callable
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

import yaml
import paramiko

from .atomic import atomic_write, canonical_json
from .process import run_hidden
from .ssh import SSH_CONFIG, SSHRunner
from .state import RunLock


class VMLifecycleError(RuntimeError):
    pass


@dataclass(frozen=True)
class VMSettings:
    vmrun_path: Path
    vmx_path: Path
    startup_timeout_seconds: int = 180
    shutdown_timeout_seconds: int = 180

    @property
    def identity(self) -> str:
        return hashlib.sha256(os.path.normcase(str(self.vmx_path)).encode()).hexdigest()


def load_settings() -> VMSettings | None:
    if not SSH_CONFIG.exists():
        return None
    try:
        document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
        if not isinstance(document, dict):
            raise ValueError
        config = document.get("vm_lifecycle")
        if config is None:
            return None
        if not isinstance(config, dict) or type(config.get("enabled")) is not bool:
            raise ValueError
        if set(config) - {"enabled", "vmrun_path", "vmx_path", "startup_timeout_seconds", "shutdown_timeout_seconds"}:
            raise ValueError
        if not config["enabled"]:
            return None
        paths = []
        for key in ("vmrun_path", "vmx_path"):
            value = config.get(key)
            if not isinstance(value, str) or not value or any(ord(c) < 32 for c in value):
                raise ValueError
            path = Path(value)
            if not path.is_absolute() or not path.is_file() or path.is_symlink():
                raise ValueError
            paths.append(path.resolve(strict=True))
        if paths[1].suffix.lower() != ".vmx":
            raise ValueError
        timeouts = []
        for key in ("startup_timeout_seconds", "shutdown_timeout_seconds"):
            value = config.get(key, 180)
            if type(value) is not int or not 30 <= value <= 600:
                raise ValueError
            timeouts.append(value)
        return VMSettings(*paths, *timeouts)
    except (OSError, ValueError, yaml.YAMLError) as error:
        raise VMLifecycleError("vm_lifecycle_config_invalid") from None


def _shared_dir(settings: VMSettings) -> Path:
    root = Path(os.environ.get("LOCALAPPDATA") or (Path.home() / ".local" / "state"))
    return root / "sub2api-release" / "vm-lifecycle" / settings.identity


def _read_state(path: Path) -> dict:
    if path.is_symlink() or path.parent.is_symlink():
        raise VMLifecycleError("vm_lifecycle_state_unsafe")
    if not path.exists():
        return {}
    try:
        if not path.is_file() or path.stat().st_size > 16384:
            raise ValueError
        value = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(value, dict) or value.get("schema") != 1:
            raise ValueError
        return value
    except (OSError, ValueError) as error:
        raise VMLifecycleError("vm_lifecycle_state_invalid") from None


def lifecycle_view(run_dir: Path) -> dict:
    state = _read_state(run_dir / "vm-lifecycle.json")
    power = state.get("vm_power_status", "unmanaged")
    cleanup = state.get("vm_cleanup_status", "not_applicable")
    return {
        "vm_power_status": power if isinstance(power, str) and power in {"unmanaged", "checking", "starting", "waiting_for_ssh", "running", "stopping", "stopped", "unknown"} else "unknown",
        "vm_started_by_release": state.get("vm_started_by_release") is True,
        "vm_cleanup_status": cleanup if isinstance(cleanup, str) and cleanup in {"not_applicable", "pending", "preserved", "running", "verified", "retained_failure", "shutdown_reply_uncertain", "failed"} else "unknown",
    }


def _assert_free(shared: Path) -> None:
    owner = _read_state(shared / "owner.json")
    if owner and owner.get("lease_status") != "released":
        raise VMLifecycleError("vm_lifecycle_reconciliation_required")


@contextmanager
def vm_guard():
    """Protect manual VM consumers without changing power or ownership."""
    settings = load_settings()
    if settings is None:
        yield
        return
    shared = _shared_dir(settings)
    if shared.is_symlink():
        raise VMLifecycleError("vm_lifecycle_state_unsafe")
    with RunLock(shared / "lease.lock"):
        _assert_free(shared)
        yield


class VMLease:
    def __init__(self, run_dir: Path, release_id: str, ssh=None, *, process_token: str | None = None, commit: str | None = None):
        self.run_dir = run_dir
        self.release_id = release_id
        self.ssh = ssh
        self.process_token = process_token
        self.settings = load_settings()
        self.enabled = self.settings is not None
        self.lock = None
        self.shared = None
        self.ready = False
        self.completed = False
        self.state = {
            "schema": 1, "release_id": release_id, "pid": os.getpid(),
            "source_commit": commit,
            "process_token": process_token, "lease_token": secrets.token_hex(16),
            "vm_power_status": "unmanaged", "vm_started_by_release": False,
            "vm_cleanup_status": "not_applicable", "lease_status": "active",
        }

    def _save(self, *, shared_owner: bool = True, **changes) -> None:
        self.state.update(changes, updated_at=int(time.time()))
        atomic_write(self.run_dir / "vm-lifecycle.json", canonical_json(self.state) + b"\n")
        if shared_owner and self.shared is not None:
            atomic_write(self.shared / "owner.json", canonical_json(self.state) + b"\n")

    def _owns_owner(self, owner: dict) -> bool:
        identity_keys = ("vm_identity", "release_id", "source_commit", "pid", "process_token", "lease_token", "initially_running", "vm_started_by_release")
        return bool(owner) and all(owner.get(key) == self.state.get(key) for key in identity_keys)

    def __enter__(self):
        if self.settings is None:
            return self
        self.shared = _shared_dir(self.settings)
        if self.shared.is_symlink():
            raise VMLifecycleError("vm_lifecycle_state_unsafe")
        self.lock = RunLock(self.shared / "lease.lock")
        self.lock.__enter__()
        try:
            _assert_free(self.shared)
            self.state["vm_identity"] = self.settings.identity
            self._save(vm_power_status="checking", vm_cleanup_status="pending")
        except BaseException:
            self.lock.__exit__(None, None, None)
            self.lock = None
            raise
        return self

    def _vmrun(self, *arguments: str, timeout: int = 30) -> str:
        assert self.settings is not None
        try:
            result = run_hidden(
                [str(self.settings.vmrun_path), "-T", "ws", *arguments],
                capture_output=True, text=True, timeout=timeout, check=False,
            )
        except (OSError, subprocess.SubprocessError):
            raise VMLifecycleError("vm_lifecycle_vmware_command_uncertain") from None
        if result.returncode != 0:
            raise VMLifecycleError("vm_lifecycle_vmware_command_failed")
        return result.stdout

    def _running(self) -> bool:
        assert self.settings is not None
        lines = self._vmrun("list").strip().splitlines()
        if not lines or not re.fullmatch(r"Total running VMs: [0-9]+", lines[0]):
            raise VMLifecycleError("vm_lifecycle_vmware_state_unknown")
        count = int(lines[0].rsplit(" ", 1)[1])
        if len(lines) != count + 1 or any(not Path(line).is_absolute() for line in lines[1:]):
            raise VMLifecycleError("vm_lifecycle_vmware_state_unknown")
        return any(os.path.normcase(str(Path(line).resolve())) == os.path.normcase(str(self.settings.vmx_path)) for line in lines[1:])

    def ensure_ready(self) -> None:
        if not self.enabled or self.ready:
            return
        assert self.settings is not None and self.lock is not None
        if self.ssh is None:
            self.ssh = SSHRunner()
        initially_running = self._running()
        self._save(initially_running=initially_running)
        deadline = time.monotonic() + self.settings.startup_timeout_seconds
        if not initially_running:
            # Record intent before the side effect; a crash is never a free lease.
            self._save(vm_power_status="starting", vm_started_by_release=True)
            self._vmrun("start", str(self.settings.vmx_path), "nogui", timeout=60)
        if not self._running():
            raise VMLifecycleError("vm_lifecycle_start_not_verified")
        self._save(vm_power_status="waiting_for_ssh")
        while True:
            try:
                result = self.ssh.run(
                    "local_vm",
                    "set -Eeuo pipefail\ntest -d /opt/sub2api-deploy\nprintf 'vm_ssh_ready=true\\nvm_boot_id=%s\\n' \"$(cat /proc/sys/kernel/random/boot_id)\"",
                    {"vm_ssh_ready", "vm_boot_id"}, timeout=10,
                )
                if result.values.get("vm_ssh_ready") != "true":
                    raise VMLifecycleError("vm_lifecycle_identity_not_verified")
                boot_id = result.values.get("vm_boot_id", "")
                if not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", boot_id):
                    raise VMLifecycleError("vm_lifecycle_identity_not_verified")
                self._save(vm_boot_id=boot_id)
                break
            except (paramiko.AuthenticationException, paramiko.BadHostKeyException):
                raise VMLifecycleError("vm_lifecycle_ssh_identity_failed") from None
            except (OSError, EOFError, paramiko.SSHException):
                if time.monotonic() >= deadline:
                    raise VMLifecycleError("vm_lifecycle_ssh_ready_timeout") from None
                time.sleep(2)
        if self.state["vm_started_by_release"]:
            self.ssh.run("local_vm", """set -Eeuo pipefail
state=$(docker inspect -f '{{.State.Status}}' sub2api-dev)
case "$state" in
  exited|created) docker start sub2api-dev >/dev/null ;;
  running) ;;
  *) exit 1 ;;
esac
printf 'vm_app_started=true\\n'
""", {"vm_app_started"}, timeout=30)
            while True:
                result = self.ssh.run("local_vm", """set -Eeuo pipefail
health=$(docker inspect -f '{{.State.Health.Status}}' sub2api-dev)
printf 'vm_app_health=%s\\n' "$health"
""", {"vm_app_health"}, timeout=10)
                if result.values.get("vm_app_health") == "healthy":
                    break
                if result.values.get("vm_app_health") not in {"starting", "unhealthy"} or time.monotonic() >= deadline:
                    raise VMLifecycleError("vm_lifecycle_app_ready_timeout")
                time.sleep(2)
        self.ready = True
        self._save(vm_power_status="running")

    def complete(self) -> None:
        if not self.enabled or self.completed:
            return
        assert self.settings is not None and self.shared is not None
        owner = _read_state(self.shared / "owner.json")
        if not self._owns_owner(owner) or not self.ready:
            raise VMLifecycleError("vm_lifecycle_owner_mismatch")
        if not self.state["vm_started_by_release"]:
            self._save(vm_power_status="running" if self._running() else "stopped", vm_cleanup_status="preserved", lease_status="released")
            self.completed = True
            return
        self._save(vm_power_status="stopping", vm_cleanup_status="running")
        try:
            # The validator holds this shared lock for its entire remote lifetime.
            # Exclusive acquisition rejects even an orphaned Gate worker.
            result = self.ssh.run("local_vm", """set -Eeuo pipefail
test "$(id -u)" = 0
test "$(cat /proc/sys/kernel/random/boot_id)" = BOOT_ID
lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$lock" && test ! -L "$lock"
test "$(stat -c '%U:%G:%a:%h' "$lock")" = root:root:600:1
exec 8<>"$lock"
flock -xn 8
test -z "$(docker ps -a --format '{{.Names}}' | grep -E '^sub2api-(v2-|probe-|preview-)' || true)"
if test "$(docker inspect -f '{{.State.Running}}' sub2api-dev)" = true; then
  docker stop -t 30 sub2api-dev >/dev/null
fi
test "$(docker inspect -f '{{.State.Status}}' sub2api-dev)" = exited
! ss -ltnH '( sport = :8211 )' | grep -q .
shutdown -h +1 >/dev/null 2>&1
printf 'vm_shutdown_scheduled=true\\n'
""".replace("BOOT_ID", self.state["vm_boot_id"]), {"vm_shutdown_scheduled"}, timeout=60)
            if result.values.get("vm_shutdown_scheduled") != "true":
                raise VMLifecycleError("vm_lifecycle_shutdown_preflight_failed")
        except RuntimeError as error:
            if "exit code -1" not in str(error):
                self._save(vm_power_status="running", vm_cleanup_status="failed")
                raise VMLifecycleError("vm_lifecycle_shutdown_preflight_failed") from None
            self._save(vm_cleanup_status="shutdown_reply_uncertain")
        except (OSError, EOFError, paramiko.SSHException):
            # A lost SSH reply is ambiguous; never resend a shutdown request.
            self._save(vm_cleanup_status="shutdown_reply_uncertain")
        deadline = time.monotonic() + self.settings.shutdown_timeout_seconds
        try:
            while self._running():
                if time.monotonic() >= deadline:
                    raise VMLifecycleError("vm_lifecycle_shutdown_timeout")
                time.sleep(2)
        except VMLifecycleError:
            self._save(vm_power_status="unknown", vm_cleanup_status="failed")
            raise
        self._save(vm_power_status="stopped", vm_cleanup_status="verified", lease_status="released")
        self.completed = True

    def __exit__(self, exc_type, exc, traceback):
        try:
            if self.enabled and not self.completed:
                shared_owner = False
                if self.shared is not None:
                    try:
                        shared_owner = self._owns_owner(_read_state(self.shared / "owner.json"))
                    except (VMLifecycleError, OSError):
                        pass
                self._save(shared_owner=shared_owner, lease_status="retained", vm_cleanup_status="retained_failure" if self.state["vm_cleanup_status"] == "pending" else self.state["vm_cleanup_status"])
        finally:
            if self.lock is not None:
                self.lock.__exit__(exc_type, exc, traceback)
                self.lock = None


_FINALIZATION_IDENTITY = (
    "schema", "vm_identity", "release_id", "source_commit", "pid", "process_token",
    "lease_token", "initially_running", "vm_started_by_release", "vm_boot_id",
)


def _file_sha256(path: Path) -> str:
    _read_state(path)
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _finalization_owner(original: dict, record: dict) -> dict:
    return {
        **original, "lease_status": "released", "vm_power_status": "stopped",
        "vm_cleanup_status": "verified", "updated_at": record["finished_at"],
        "finalization_sha256": hashlib.sha256(canonical_json(record) + b"\n").hexdigest(),
    }


def _validate_finalization_record(run_dir: Path, settings: VMSettings, release_id: str, commit: str) -> tuple[dict, dict]:
    record = _read_state(run_dir / "vm-finalization.json")
    original = _read_state(run_dir / "vm-finalization-owner.json")
    lifecycle = _read_state(run_dir / "vm-lifecycle.json")
    runner = _read_state(run_dir / "runner.json")
    if (
        not record or not original or record.get("release_id") != release_id
        or record.get("source_commit") != commit or record.get("vm_identity") != settings.identity
        or record.get("status") != "verified" or record.get("vm_power_status") != "stopped"
        or record.get("vm_cleanup_status") != "verified" or record.get("vm_started_by_release") is not True
        or record.get("shutdown_requested") is not True or record.get("boot_verified") is not True
        or type(record.get("finished_at")) is not int
        or not re.fullmatch(r"[0-9a-f]{64}", str(record.get("evidence_sha256", "")))
        or original.get("lease_status") != "retained"
        or lifecycle.get("lease_status") != "retained"
        or original.get("initially_running") is not False or original.get("vm_started_by_release") is not True
        or any(original.get(key) != lifecycle.get(key) for key in _FINALIZATION_IDENTITY)
        or original.get("release_id") != release_id or original.get("source_commit") != commit
        or original.get("vm_identity") != settings.identity or record.get("vm_boot_id") != original.get("vm_boot_id")
        or runner.get("release_id") != release_id or runner.get("commit") != commit
        or runner.get("status") != "failed" or type(runner.get("exit_code")) is not int or runner["exit_code"] == 0
        or runner.get("pid") != original.get("pid") or runner.get("process_token") != original.get("process_token")
        or record.get("original_owner_sha256") != _file_sha256(run_dir / "vm-finalization-owner.json")
        or record.get("original_lifecycle_sha256") != _file_sha256(run_dir / "vm-lifecycle.json")
        or record.get("original_runner_sha256") != _file_sha256(run_dir / "runner.json")
    ):
        raise VMLifecycleError("vm_finalization_record_unproven")
    return record, original


def verify_retained_finalization(run_dir: Path, release_id: str, commit: str, *, evidence_sha256: str | None = None) -> dict:
    """Verify independent completion evidence without altering power or ownership."""
    settings = load_settings()
    if settings is None:
        raise VMLifecycleError("vm_finalization_config_required")
    record, original = _validate_finalization_record(run_dir, settings, release_id, commit)
    if evidence_sha256 is not None and record["evidence_sha256"] != evidence_sha256:
        raise VMLifecycleError("vm_finalization_evidence_mismatch")
    shared = _shared_dir(settings)
    if shared.is_symlink() or _read_state(shared / "owner.json") != _finalization_owner(original, record):
        raise VMLifecycleError("vm_finalization_owner_unproven")
    probe = VMLease(run_dir, release_id)
    if probe.settings != settings or probe._running():
        raise VMLifecycleError("vm_finalization_stopped_unproven")
    return record


class RetainedVMFinalization(VMLease):
    """Explicit, bounded post-consumption completion of one dead worker's lease.

    The caller must hold the global release lock and revalidate signed Gate,
    consumed production state, current image, doctor and release task cleanup.
    Original runner/lifecycle documents remain immutable throughout this API.
    """

    def __init__(self, run_dir: Path, release_id: str, commit: str, runner: dict, *, process_identity: Callable[[int], str | None], ssh=None):
        super().__init__(run_dir, release_id, ssh, commit=commit)
        self.commit = commit
        self.runner = runner
        self.process_identity = process_identity
        self.original_owner: dict = {}
        self.original_lifecycle: dict = {}
        self.record: dict = {}
        self.original_bytes: dict[Path, bytes] = {}

    def ensure_ready(self) -> None:
        raise VMLifecycleError("vm_finalization_start_forbidden")

    def _assert_original_worker_dead(self) -> None:
        pid, token = self.runner.get("pid"), self.runner.get("process_token")
        if type(pid) is not int or pid <= 0 or not isinstance(token, str) or not token:
            raise VMLifecycleError("vm_finalization_worker_unproven")
        try:
            current = self.process_identity(pid)
        except Exception:
            raise VMLifecycleError("vm_finalization_worker_unproven") from None
        if current == token:
            raise VMLifecycleError("vm_finalization_worker_alive")

    def _assert_unchanged(self) -> None:
        self._assert_original_worker_dead()
        for path, content in self.original_bytes.items():
            _read_state(path)
            if path.read_bytes() != content:
                raise VMLifecycleError("vm_finalization_original_evidence_changed")
        if _read_state(self.shared / "owner.json") != self.original_owner:
            raise VMLifecycleError("vm_finalization_owner_changed")

    def __enter__(self):
        if self.settings is None:
            raise VMLifecycleError("vm_finalization_config_required")
        if not re.fullmatch(r"[0-9a-f]{40}", self.commit):
            raise VMLifecycleError("vm_finalization_identity_unproven")
        self.shared = _shared_dir(self.settings)
        if self.shared.is_symlink():
            raise VMLifecycleError("vm_lifecycle_state_unsafe")
        self.lock = RunLock(self.shared / "lease.lock")
        self.lock.__enter__()
        try:
            self._assert_original_worker_dead()
            self.original_lifecycle = _read_state(self.run_dir / "vm-lifecycle.json")
            current_runner = _read_state(self.run_dir / "runner.json")
            if (
                current_runner != self.runner or current_runner.get("release_id") != self.release_id
                or current_runner.get("commit") != self.commit or current_runner.get("status") != "failed"
                or type(current_runner.get("exit_code")) is not int or current_runner["exit_code"] == 0
            ):
                raise VMLifecycleError("vm_finalization_worker_unproven")
            existing = _read_state(self.run_dir / "vm-finalization.json")
            if existing.get("status") == "verified":
                self.record, self.original_owner = _validate_finalization_record(self.run_dir, self.settings, self.release_id, self.commit)
                owner = _read_state(self.shared / "owner.json")
                if owner not in (self.original_owner, _finalization_owner(self.original_owner, self.record)):
                    raise VMLifecycleError("vm_finalization_owner_changed")
                if self._running():
                    raise VMLifecycleError("vm_finalization_stopped_unproven")
                self.completed = owner == _finalization_owner(self.original_owner, self.record)
                return self
            if existing.get("shutdown_requested") is True:
                # An interrupted shutdown is never permission to send it again.
                raise VMLifecycleError("vm_finalization_shutdown_reconciliation_required")
            self.original_owner = _read_state(self.shared / "owner.json")
            lifecycle = self.original_lifecycle
            if (
                not self.original_owner or self.original_owner != lifecycle
                or lifecycle.get("lease_status") != "retained"
                or lifecycle.get("release_id") != self.release_id or lifecycle.get("source_commit") != self.commit
                or lifecycle.get("vm_identity") != self.settings.identity
                or lifecycle.get("pid") != current_runner["pid"] or lifecycle.get("process_token") != current_runner["process_token"]
                or lifecycle.get("initially_running") is not False or lifecycle.get("vm_started_by_release") is not True
                or not isinstance(lifecycle.get("lease_token"), str) or not lifecycle["lease_token"]
                or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", str(lifecycle.get("vm_boot_id", "")))
            ):
                raise VMLifecycleError("vm_finalization_owner_unproven")
            if not self._running():
                raise VMLifecycleError("vm_finalization_running_unproven")
            if self.ssh is None:
                self.ssh = SSHRunner()
            result = self.ssh.run("local_vm", '''set -Eeuo pipefail
test "$(id -u)" = 0
printf 'vm_boot_id=%s\\n' "$(cat /proc/sys/kernel/random/boot_id)"
''', {"vm_boot_id"}, timeout=10)
            if result.values != {"vm_boot_id": lifecycle["vm_boot_id"]}:
                raise VMLifecycleError("vm_finalization_boot_changed")
            for path in (self.run_dir / "vm-lifecycle.json", self.run_dir / "runner.json"):
                self.original_bytes[path] = path.read_bytes()
            snapshot = self.run_dir / "vm-finalization-owner.json"
            owner_bytes = (self.shared / "owner.json").read_bytes()
            if snapshot.exists() or snapshot.is_symlink():
                _read_state(snapshot)
                if snapshot.read_bytes() != owner_bytes:
                    raise VMLifecycleError("vm_finalization_owner_snapshot_changed")
            else:
                atomic_write(snapshot, owner_bytes)
            self.original_bytes[snapshot] = owner_bytes
            self.record = {
                "schema": 1, "release_id": self.release_id, "source_commit": self.commit,
                "vm_identity": self.settings.identity, "vm_boot_id": lifecycle["vm_boot_id"],
                "original_owner_sha256": hashlib.sha256(owner_bytes).hexdigest(),
                "original_lifecycle_sha256": _file_sha256(self.run_dir / "vm-lifecycle.json"),
                "original_runner_sha256": _file_sha256(self.run_dir / "runner.json"),
                "vm_started_by_release": True, "vm_power_status": "running",
                "vm_cleanup_status": "pending", "status": "ready", "shutdown_requested": False,
                "boot_verified": True,
            }
            self._assert_unchanged()
            return self
        except BaseException:
            self.lock.__exit__(None, None, None)
            self.lock = None
            raise

    def _save_record(self, **changes) -> None:
        self.record.update(changes)
        atomic_write(self.run_dir / "vm-finalization.json", canonical_json(self.record) + b"\n")

    def complete(self, *, evidence_sha256: str) -> dict:
        if self.lock is None or self.settings is None:
            raise VMLifecycleError("vm_finalization_lock_required")
        if not re.fullmatch(r"[0-9a-f]{64}", str(evidence_sha256)):
            raise VMLifecycleError("vm_finalization_evidence_unproven")
        if self.record.get("status") == "verified":
            if self.record.get("evidence_sha256") != evidence_sha256:
                raise VMLifecycleError("vm_finalization_evidence_mismatch")
            self._assert_original_worker_dead()
            if self._running():
                raise VMLifecycleError("vm_finalization_stopped_unproven")
            _validate_finalization_record(self.run_dir, self.settings, self.release_id, self.commit)
            owner = _read_state(self.shared / "owner.json")
            if owner not in (self.original_owner, _finalization_owner(self.original_owner, self.record)):
                raise VMLifecycleError("vm_finalization_owner_changed")
            atomic_write(self.shared / "owner.json", canonical_json(_finalization_owner(self.original_owner, self.record)) + b"\n")
            self.completed = True
            return self.record
        self._assert_unchanged()
        if not self._running():
            raise VMLifecycleError("vm_finalization_running_unproven")
        self._save_record(status="finalizing", evidence_sha256=evidence_sha256, shutdown_requested=True,
                          vm_power_status="stopping", vm_cleanup_status="running")
        try:
            result = self.ssh.run("local_vm", '''set -Eeuo pipefail
test "$(id -u)" = 0
test "$(cat /proc/sys/kernel/random/boot_id)" = BOOT_ID
lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$lock"
test ! -L "$lock"
test "$(stat -c '%U:%G:%a:%h' "$lock")" = root:root:600:1
exec 8<>"$lock"
test "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)" = root:root:600:1
flock -xn 8
gate_root=/opt/sub2api-deploy/release-gates
test -d "$gate_root"
test ! -L "$gate_root"
test "$(stat -c '%U:%G:%a' "$gate_root")" = root:root:700
gate_lock="$gate_root/release.lock"
test -f "$gate_lock"
test ! -L "$gate_lock"
case "$(stat -c '%U:%G:%a:%h' "$gate_lock")" in root:root:600:1|root:root:644:1) ;; *) exit 1 ;; esac
exec 9<>"$gate_lock"
case "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9)" in root:root:600:1|root:root:644:1) ;; *) exit 1 ;; esac
flock -xn 9
processes=$(ps -eo args=)
process_count=$(awk '/vm-space-clean.sh|sub2api-vm-validate|run-validator.sh|docker (build|buildx)|buildctl/ && ! /awk/ {n++} END {print n+0}' <<<"$processes")
test "$process_count" = 0
names=$(docker ps -a --format '{{.Names}}')
while IFS= read -r name; do
  case "$name" in sub2api-v2-*|sub2api-probe-*|sub2api-preview-*) exit 1 ;; esac
done <<<"$names"
if test "$(docker inspect -f '{{.State.Running}}' sub2api-dev)" = true; then
  docker stop -t 30 sub2api-dev >/dev/null
fi
test "$(docker inspect -f '{{.State.Status}}' sub2api-dev)" = exited
listeners=$(ss -ltnH '( sport = :8211 )')
test -z "$listeners"
shutdown -h +1 >/dev/null 2>&1
printf 'vm_shutdown_scheduled=true\\n'
'''.replace("BOOT_ID", self.record["vm_boot_id"]), {"vm_shutdown_scheduled"}, timeout=60)
            if result.values != {"vm_shutdown_scheduled": "true"}:
                raise VMLifecycleError("vm_finalization_shutdown_preflight_failed")
        except RuntimeError as error:
            if "exit code -1" not in str(error):
                self._save_record(status="failed", vm_power_status="running", vm_cleanup_status="failed")
                raise VMLifecycleError("vm_finalization_shutdown_preflight_failed") from None
            self._save_record(vm_cleanup_status="shutdown_reply_uncertain")
        except (OSError, EOFError, paramiko.SSHException):
            self._save_record(vm_cleanup_status="shutdown_reply_uncertain")
        try:
            deadline = time.monotonic() + self.settings.shutdown_timeout_seconds
            while self._running():
                if time.monotonic() >= deadline:
                    raise VMLifecycleError("vm_finalization_shutdown_timeout")
                time.sleep(2)
            self._assert_unchanged()
            self._save_record(status="verified", vm_power_status="stopped", vm_cleanup_status="verified", finished_at=int(time.time()))
            # Ownership is the final commit marker. Earlier failures retain the
            # original marker, and a verified stopped record permits only this
            # last local write to be retried without another shutdown request.
            atomic_write(self.shared / "owner.json", canonical_json(_finalization_owner(self.original_owner, self.record)) + b"\n")
            self.completed = True
            return self.record
        except BaseException:
            if self.record.get("status") != "verified":
                self._save_record(status="failed", vm_power_status="unknown", vm_cleanup_status="failed")
            raise

    def __exit__(self, exc_type, exc, traceback):
        if self.lock is not None:
            self.lock.__exit__(exc_type, exc, traceback)
            self.lock = None
