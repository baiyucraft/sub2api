"""Owned, fail-closed power management for the local release VM."""

from __future__ import annotations

import hashlib
import json
import os
import re
import secrets
import subprocess
import time
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
