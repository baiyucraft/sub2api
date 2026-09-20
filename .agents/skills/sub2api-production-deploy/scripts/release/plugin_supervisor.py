from __future__ import annotations

import argparse
import ctypes
import json
import os
import re
import secrets
import sys
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .paths import ENTRYPOINT, PLUGIN_RUN_ROOT, RUN_ROOT, SCRIPTS_ROOT
from .plugin_api import PluginAPIClient, PluginApplyResult
from .plugin_credentials import load_plugin_admin_credentials
from .plugin_package import (
    PLUGIN_ID,
    PackageIdentity,
    build_packages,
    find_previous_identities,
    find_previous_identity,
    package_manifest,
    validate_source_commit,
)
from .plugin_state import PLUGIN_TERMINAL_STATES, PluginRunState
from .process import popen_detached_worker
from .state import RunLock


PLUGIN_RELEASE_ID = re.compile(r"^[a-z0-9][a-z0-9.-]{0,127}$")
PLUGIN_STATUS_FIELDS = (
    "release_id",
    "plugin_id",
    "source_commit",
    "target_version",
    "runner_status",
    "runner_alive",
    "runner_exit",
    "stage",
    "status",
    "operation",
    "instances_expected",
    "instances_verified",
    "updated_at",
)


def _run_dir(identifier: str) -> Path:
    if not PLUGIN_RELEASE_ID.fullmatch(identifier):
        raise ValueError("invalid plugin release ID")
    root = PLUGIN_RUN_ROOT.resolve(strict=False)
    path = PLUGIN_RUN_ROOT / identifier
    if path.is_symlink() or path.resolve(strict=False).parent != root:
        raise RuntimeError("plugin release directory is unsafe")
    return path


def _write_json(path: Path, value: dict[str, Any]) -> None:
    atomic_write(path, canonical_json(value) + b"\n", 0o600)


def _read_json(path: Path, required: bool = False) -> dict[str, Any] | None:
    if not path.exists():
        if required:
            raise RuntimeError(f"missing plugin release file: {path.name}")
        return None
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 2 * 1024 * 1024:
        raise RuntimeError(f"invalid plugin release file: {path.name}")
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise RuntimeError(f"invalid plugin release document: {path.name}")
    return value


def _process_token(pid: int) -> str | None:
    if pid <= 0:
        return None
    if os.name == "nt":
        handle = ctypes.windll.kernel32.OpenProcess(0x1000, False, pid)
        if not handle:
            return None
        try:
            creation = ctypes.c_ulonglong()
            exit_time = ctypes.c_ulonglong()
            kernel = ctypes.c_ulonglong()
            user = ctypes.c_ulonglong()
            ok = ctypes.windll.kernel32.GetProcessTimes(
                handle, ctypes.byref(creation), ctypes.byref(exit_time), ctypes.byref(kernel), ctypes.byref(user)
            )
            return f"win:{creation.value}" if ok else None
        finally:
            ctypes.windll.kernel32.CloseHandle(handle)
    try:
        fields = Path(f"/proc/{pid}/stat").read_text(encoding="ascii").rsplit(")", 1)[1].split()
        boot_id = Path("/proc/sys/kernel/random/boot_id").read_text(encoding="ascii").strip()
        return f"linux:{boot_id}:{fields[19]}"
    except (OSError, IndexError, UnicodeError):
        return None


def _runner_alive(runner: dict[str, Any]) -> bool:
    if runner.get("status") not in {"starting", "waiting_for_lock", "running"}:
        return False
    pid = runner.get("pid")
    token = runner.get("process_token")
    return isinstance(pid, int) and isinstance(token, str) and _process_token(pid) == token


def _worker_env() -> dict[str, str]:
    allowed = {
        "APPDATA", "COMSPEC", "HOME", "HOMEDRIVE", "HOMEPATH", "LOCALAPPDATA", "NUMBER_OF_PROCESSORS",
        "OS", "PATH", "PATHEXT", "PROGRAMDATA", "PROGRAMFILES", "PROGRAMFILES(X86)", "SYSTEMDRIVE",
        "SYSTEMROOT", "TEMP", "TMP", "USERPROFILE", "WINDIR",
    }
    return {key: value for key, value in os.environ.items() if key.upper() in allowed}


def _open_log(path: Path):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.parent.is_symlink() or path.is_symlink():
        raise RuntimeError("plugin runner log path is unsafe")
    descriptor = os.open(path, os.O_APPEND | os.O_CREAT | os.O_WRONLY | getattr(os, "O_BINARY", 0), 0o600)
    return os.fdopen(descriptor, "ab", buffering=0)


def _update_runner(run_dir: Path, **changes: Any) -> dict[str, Any]:
    runner = _read_json(run_dir / "runner.json", required=True) or {}
    runner.update(changes)
    runner["updated_at"] = int(time.time())
    _write_json(run_dir / "runner.json", runner)
    return runner


def _release_id(commit: str) -> str:
    return f"codex-state-{commit[:12]}-{int(time.time())}-{secrets.token_hex(4)}"


def start(args: argparse.Namespace, *, announce: bool = True) -> str:
    commit = validate_source_commit(args.commit)
    identifier = _release_id(commit)
    run_dir = _run_dir(identifier)
    run_dir.mkdir(parents=True, mode=0o700)
    manifest = {
        "schema": 1,
        "release_id": identifier,
        "plugin_id": PLUGIN_ID,
        "source_commit": commit,
        "created_at": int(time.time()),
    }
    _write_json(run_dir / "manifest.json", manifest)
    PluginRunState.create(run_dir / "state.json", identifier, PLUGIN_ID, commit)
    now = int(time.time())
    _write_json(
        run_dir / "runner.json",
        {
            "schema": 1,
            "release_id": identifier,
            "plugin_id": PLUGIN_ID,
            "source_commit": commit,
            "pid": None,
            "process_token": None,
            "status": "starting",
            "exit_code": None,
            "started_at": now,
            "updated_at": now,
            "stdout": "logs/runner.stdout.log",
            "stderr": "logs/runner.stderr.log",
        },
    )
    command = [sys.executable, str(ENTRYPOINT), "_plugin-deploy-worker", "--release-id", identifier, "--commit", commit]
    try:
        with _open_log(run_dir / "logs" / "runner.stdout.log") as stdout, _open_log(run_dir / "logs" / "runner.stderr.log") as stderr:
            process = popen_detached_worker(
                command,
                cwd=SCRIPTS_ROOT,
                stdout=stdout,
                stderr=stderr,
                env=_worker_env(),
            )
    except BaseException:
        _update_runner(run_dir, status="failed", exit_code=1, finished_at=int(time.time()))
        raise
    token = None
    for _ in range(20):
        token = _process_token(process.pid)
        if token:
            break
        time.sleep(0.05)
    _update_runner(run_dir, pid=process.pid, process_token=token)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        runner = _read_json(run_dir / "runner.json", required=True) or {}
        if runner.get("status") in {"waiting_for_lock", "running", "awaiting_authorization", "verified"}:
            if announce:
                print(f"plugin_release_id={identifier} runner=started")
            return identifier
        if runner.get("status") == "failed":
            raise RuntimeError("plugin release worker failed during startup")
        time.sleep(0.1)
    raise RuntimeError(f"plugin release worker did not complete startup handshake: {identifier}")


def worker(args: argparse.Namespace) -> None:
    run_dir = _run_dir(args.release_id)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    if manifest.get("release_id") != args.release_id or manifest.get("source_commit") != args.commit:
        raise RuntimeError("plugin worker identity mismatch")
    os.environ["SUB2API_RELEASE_ID"] = args.release_id
    os.environ["SUB2API_DEPLOYMENT_MODE"] = "plugin-package"
    os.environ["SUB2API_EVENT_LOG"] = str(run_dir / "logs" / "events.jsonl")
    exit_code = 1
    status = "failed"
    try:
        _update_runner(run_dir, status="waiting_for_lock")
        with RunLock(RUN_ROOT / ".release.lock"):
            _update_runner(run_dir, status="running")
            artifacts = run_dir / "artifacts"
            identities = build_packages(args.commit, artifacts)
            state = PluginRunState.load(run_dir / "state.json")
            state.transition("package_built", evidence={"arches": sorted(identities), "version": identities["amd64"].version})
            package = package_manifest(identities)
            _write_json(run_dir / "package.json", package)
            state.transition(
                "local_verified",
                evidence={
                    "version": package["version"],
                    "signature_key_id": package["signature_key_id"],
                    "package_sha256": {arch: item["package_sha256"] for arch, item in package["arches"].items()},
                },
            )
            state.transition("awaiting_vm_authorization", evidence={"remote_write_performed": False})
        authorize(argparse.Namespace(release_id=args.release_id))
        authorize(argparse.Namespace(release_id=args.release_id))
        exit_code = 0
        status = "verified"
    except BaseException as error:
        current = PluginRunState.load(run_dir / "state.json")
        if current.value.get("status") not in PLUGIN_TERMINAL_STATES:
            current.fail(str(current.value.get("stage") or "worker"), evidence={"error_type": type(error).__name__})
        raise
    finally:
        _update_runner(run_dir, status=status, exit_code=exit_code, finished_at=int(time.time()))


def _identity(run_dir: Path, arch: str = "amd64") -> PackageIdentity:
    package = _read_json(run_dir / "package.json", required=True) or {}
    details = (package.get("arches") or {}).get(arch)
    if not isinstance(details, dict):
        raise RuntimeError("plugin package architecture is unavailable")
    return PackageIdentity(
        path=run_dir / "artifacts" / str(details["file"]),
        arch=arch,
        version=str(package["version"]),
        package_sha256=str(details["package_sha256"]),
        binary_sha256=str(details["binary_sha256"]),
        key_id=str(package["signature_key_id"]),
        signature_status=str(details["signature_status"]),
    )


def _previous_packages(run_dir: Path) -> list[PackageIdentity]:
    return find_previous_identities("amd64", exclude_release_id=run_dir.name, limit=10)


def _wipe(data: bytearray) -> None:
    for index in range(len(data)):
        data[index] = 0


def authorize(args: argparse.Namespace) -> None:
    run_dir = _run_dir(args.release_id)
    state = PluginRunState.load(run_dir / "state.json")
    identity = _identity(run_dir)
    api = PluginAPIClient()
    stage = state.value.get("stage")
    if stage == "awaiting_vm_authorization":
        credentials = load_plugin_admin_credentials("vm")
        lock = RunLock(RUN_ROOT / ".release.lock")
        try:
            lock.__enter__()
        except BaseException:
            _wipe(credentials)
            raise
        try:
            try:
                result = api.apply(
                    node="local_vm",
                    identity=identity,
                    credentials=credentials,
                    restore_after=True,
                    previous_packages=_previous_packages(run_dir),
                )
            except BaseException as error:
                state.fail("awaiting_vm_authorization", blocked=True, evidence={"error_type": type(error).__name__})
                raise
        except BaseException as error:
            raise
        finally:
            lock.__exit__(None, None, None)
        if result.write_uncertain:
            state.fail("awaiting_vm_authorization", blocked=True, evidence={"write_uncertain": True})
            raise RuntimeError("VM plugin operation requires reconciliation")
        expected = int(result.values["instances_expected"])
        verified = int(result.values["instances_verified"])
        if expected < 1 or verified != expected or result.values["restoration_status"] not in {"removed", "restored", "unchanged"}:
            state.fail("awaiting_vm_authorization", evidence={"instances_expected": expected, "instances_verified": verified})
            raise RuntimeError("VM plugin Gate failed")
        _write_json(run_dir / "gate" / "vm-result.json", result.values)
        state.transition("vm_gate_verified", evidence={"operation": result.operation, "instances_verified": verified})
        state.transition("awaiting_production_authorization", evidence={"remote_write_performed": False})
        print(f"plugin_release_id={args.release_id} stage=awaiting_production_authorization")
        return
    if stage != "awaiting_production_authorization":
        raise RuntimeError(f"plugin release is not awaiting authorization: {stage}")
    credentials = load_plugin_admin_credentials("production")
    lock = RunLock(RUN_ROOT / ".release.lock")
    try:
        lock.__enter__()
    except BaseException:
        _wipe(credentials)
        raise
    try:
        state.transition("production_preflight_verified", evidence={"package_sha256": identity.package_sha256})
        state.transition("install_or_upgrade_started", evidence={"target_version": identity.version})
        try:
            result = api.apply(
                node="racknerd",
                identity=identity,
                credentials=credentials,
                restore_after=False,
            )
        except BaseException as error:
            state.fail("install_or_upgrade_started", blocked=True, evidence={"error_type": type(error).__name__})
            raise
    finally:
        lock.__exit__(None, None, None)
    if result.write_uncertain:
        state.fail("install_or_upgrade_started", blocked=True, evidence={"write_uncertain": True})
        raise RuntimeError("production plugin write requires reconciliation")
    state.transition(
        "installation_committed",
        evidence={"operation": result.operation, "installation_id": int(result.values["installation_id"])},
    )
    expected = int(result.values["instances_expected"])
    verified = int(result.values["instances_verified"])
    invariants = (
        result.values["signature_status"] == "trusted"
        and result.values["binary_sha256"] == identity.binary_sha256
        and result.values["config_revision_preserved"] == "true"
        and result.values["managed_scope_digest_preserved"] == "true"
        and expected >= 1
        and verified == expected
    )
    if not invariants:
        state.fail("installation_committed", blocked=True, evidence={"instances_expected": expected, "instances_verified": verified})
        raise RuntimeError("production plugin verification failed after commit")
    state.transition("instances_verified", evidence={"instances_expected": expected, "instances_verified": verified})
    _write_json(run_dir / "production-result.json", {"schema": 1, "status": "verified", **result.values})
    state.transition("verified", "verified", evidence={"operation": result.operation, "instances_verified": verified})
    _update_runner(run_dir, status="verified", exit_code=0, finished_at=int(time.time()))
    print(f"plugin_release_id={args.release_id} status=verified operation={result.operation}")


def status_view(identifier: str) -> dict[str, Any]:
    run_dir = _run_dir(identifier)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner = _read_json(run_dir / "runner.json") or {}
    state = _read_json(run_dir / "state.json") or {}
    package = _read_json(run_dir / "package.json") or {}
    production = _read_json(run_dir / "production-result.json") or {}
    updated = max(
        (int(path.stat().st_mtime) for path in (run_dir / "runner.json", run_dir / "state.json", run_dir / "production-result.json") if path.exists()),
        default=int(run_dir.stat().st_mtime),
    )
    view = {
        "release_id": identifier,
        "plugin_id": manifest.get("plugin_id"),
        "source_commit": manifest.get("source_commit"),
        "target_version": package.get("version"),
        "runner_status": runner.get("status"),
        "runner_alive": _runner_alive(runner),
        "runner_exit": runner.get("exit_code"),
        "stage": state.get("stage"),
        "status": state.get("status"),
        "operation": production.get("operation"),
        "instances_expected": production.get("instances_expected"),
        "instances_verified": production.get("instances_verified"),
        "updated_at": updated,
    }
    return {field: view.get(field) for field in PLUGIN_STATUS_FIELDS}


def status(args: argparse.Namespace) -> None:
    print(canonical_json(status_view(args.release_id)).decode("ascii"))


def wait(args: argparse.Namespace) -> int:
    deadline = None if args.timeout <= 0 else time.monotonic() + args.timeout
    while True:
        view = status_view(args.release_id)
        if view["status"] in PLUGIN_TERMINAL_STATES:
            print(canonical_json(view).decode("ascii"))
            return 0 if view["status"] == "verified" else 2
        if deadline is not None and time.monotonic() >= deadline:
            print(canonical_json(view).decode("ascii"))
            return 3
        time.sleep(1)


def verify_result(args: argparse.Namespace) -> None:
    run_dir = _run_dir(args.release_id)
    state = PluginRunState.load(run_dir / "state.json").value
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    production = _read_json(run_dir / "production-result.json", required=True) or {}
    package = _read_json(run_dir / "package.json", required=True) or {}
    if state.get("status") != "verified" or state.get("stage") != "verified" or production.get("status") != "verified":
        raise RuntimeError("plugin release is not verified")
    if int(production.get("instances_expected", 0)) < 1 or production.get("instances_expected") != production.get("instances_verified"):
        raise RuntimeError("plugin instance verification is incomplete")
    amd64 = (package.get("arches") or {}).get("amd64") or {}
    if (
        state.get("release_id") != manifest.get("release_id")
        or state.get("plugin_id") != manifest.get("plugin_id")
        or state.get("source_commit") != manifest.get("source_commit")
        or production.get("signature_status") != "trusted"
        or production.get("target_version") != package.get("version")
        or production.get("binary_sha256") != amd64.get("binary_sha256")
    ):
        raise RuntimeError("plugin package identity differs from production result")
    print(canonical_json({"release_id": args.release_id, "status": "verified", "plugin_id": PLUGIN_ID, "version": package["version"]}).decode("ascii"))


def rollback_start(args: argparse.Namespace) -> None:
    run_dir = _run_dir(args.release_id)
    production = _read_json(run_dir / "production-result.json", required=True) or {}
    previous_version = production.get("previous_version")
    previous_binary = production.get("previous_binary_sha256")
    if not isinstance(previous_version, str) or previous_version == "not_installed":
        raise RuntimeError("no previous installed plugin version is available for rollback")
    if not isinstance(previous_binary, str):
        raise RuntimeError("previous plugin binary identity is unavailable for rollback")
    previous_identity = find_previous_identity(
        previous_version,
        "amd64",
        binary_sha256=previous_binary,
        exclude_release_id=args.release_id,
    )
    if previous_identity is None:
        raise RuntimeError("trusted previous plugin package is unavailable")
    credentials = load_plugin_admin_credentials("production")
    lock = RunLock(RUN_ROOT / ".release.lock")
    try:
        lock.__enter__()
    except BaseException:
        _wipe(credentials)
        raise
    try:
        result = PluginAPIClient().apply(
            node="racknerd",
            identity=previous_identity,
            credentials=credentials,
            restore_after=False,
        )
    finally:
        lock.__exit__(None, None, None)
    if result.write_uncertain:
        raise RuntimeError("plugin rollback requires reconciliation")
    expected = int(result.values["instances_expected"])
    verified = int(result.values["instances_verified"])
    if (
        result.values["binary_sha256"] != previous_identity.binary_sha256
        or result.values["signature_status"] != "trusted"
        or result.values["config_revision_preserved"] != "true"
        or result.values["managed_scope_digest_preserved"] != "true"
        or expected < 1
        or verified != expected
    ):
        raise RuntimeError("plugin rollback verification failed")
    rollback_dir = run_dir / "rollback"
    rollback_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    _write_json(rollback_dir / f"{int(time.time())}.json", {"schema": 1, "status": "verified", **result.values})
    print(f"plugin_release_id={args.release_id} rollback=verified version={previous_version}")
