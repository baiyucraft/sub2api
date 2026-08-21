from __future__ import annotations

import argparse
import ctypes
import hashlib
import json
import os
import re
import shlex
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .gate import verify_gate
from .manifest import create_manifest, runner_checksum, validate_commit, validate_image_id, write_manifest_once
from .paths import ENTRYPOINT, MAINTENANCE_ROOT, RUN_ROOT, SCRIPTS_ROOT, TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .process import popen_detached_worker
from .profiles import get_profile, get_release_profile
from .ssh import SSHRunner
from .state import RunLock, RunState, TERMINAL_STATES


LOGGING_ROOT = SCRIPTS_ROOT / "logging"
if str(LOGGING_ROOT) not in sys.path:
    sys.path.insert(0, str(LOGGING_ROOT))

from release_logging import EventContext, JSONLEventLogger, LogQuery, query_events  # noqa: E402


DEPLOY_ROOT = SCRIPTS_ROOT
COORDINATED_RESTORE = MAINTENANCE_ROOT / "restore.sh"
RELEASE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$")
MAX_JSON_BYTES = 2 * 1024 * 1024
STATUS_FIELDS = (
    "release_id", "profile", "commit", "deployment_mode", "runner_status", "runner_alive", "runner_exit",
    "vm_stage", "vm_status", "production_stage", "production_status",
    "candidate_image_id", "running_image_id", "image_ids_match", "claim_final_state", "updated_at",
)
DANGEROUS_STAGES = {
    "production_preflight", "pre_switch_streaming_verified", "freeze", "freeze_verified",
    "migration_preflight", "backup", "backup_verified", "migration_and_switch",
    "candidate_internal_verified", "candidate_started", "candidate_healthy",
    "candidate_network_verified", "candidate_port_verified", "candidate_probe_started", "candidate_http_verified", "candidate_headers_verified", "active_health_verified",
    "prompt_audit_verified",
    "switch_failure_reason",
    "public_route_verification", "nginx_reloaded", "split_route_verified",
    "old_slot_draining", "old_slot_drained", "downtime_finalizing", "downtime_finalized",
    "production_verified", "production_verified_after_reconciliation",
}


def _run_dir(identifier: str) -> Path:
    if not RELEASE_ID.fullmatch(identifier):
        raise ValueError("invalid release ID")
    root = RUN_ROOT.resolve()
    path = RUN_ROOT / identifier
    if path.is_symlink() or path.resolve(strict=False).parent != root:
        raise RuntimeError("release directory is unsafe")
    return path


def _read_json(path: Path, required: bool = False) -> dict[str, Any] | None:
    root = RUN_ROOT.resolve()
    resolved = path.resolve(strict=False)
    if resolved != root and root not in resolved.parents:
        raise RuntimeError(f"unsafe state path: {path.name}")
    current = path
    unsafe_link = False
    while current != RUN_ROOT and current != current.parent:
        if current.is_symlink():
            unsafe_link = True
            break
        current = current.parent
    if unsafe_link:
        raise RuntimeError(f"unsafe state file: {path.name}")
    if not path.exists():
        if required:
            raise RuntimeError(f"missing state file: {path.name}")
        return None
    if not path.is_file() or path.stat().st_size > MAX_JSON_BYTES:
        raise RuntimeError(f"invalid state file: {path.name}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (UnicodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"malformed state file: {path.name}") from error
    if not isinstance(value, dict):
        raise RuntimeError(f"invalid state document: {path.name}")
    return value


def _write_json(path: Path, value: dict[str, Any]) -> None:
    atomic_write(path, canonical_json(value) + b"\n", 0o600)


def _process_token(pid: int) -> str | None:
    if pid <= 0:
        return None
    if os.name == "nt":
        PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
        handle = ctypes.windll.kernel32.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, False, pid)
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
    stat = Path(f"/proc/{pid}/stat")
    try:
        fields = stat.read_text(encoding="ascii").rsplit(")", 1)[1].split()
        boot_id = Path("/proc/sys/kernel/random/boot_id").read_text(encoding="ascii").strip()
        return f"linux:{boot_id}:{fields[19]}"
    except (OSError, IndexError, UnicodeError):
        return None


def _runner_alive(runner: dict[str, Any] | None) -> bool:
    if not runner or runner.get("status") not in {"starting", "waiting_for_lock", "running"}:
        return False
    pid = runner.get("pid")
    token = runner.get("process_token")
    return isinstance(pid, int) and isinstance(token, str) and _process_token(pid) == token


def _deployment_mode(run_dir: Path, fallback: str = "blue-green") -> str:
    for name in ("runner.json", "manifest.json"):
        document = _read_json(run_dir / name) or {}
        value = document.get("deployment_mode") or document.get("mode")
        if value in {"blue-green", "downtime"}:
            return str(value)
    return fallback if fallback in {"blue-green", "downtime"} else "blue-green"


def _event_logger(run_dir: Path, mode: str | None = None) -> JSONLEventLogger:
    identifier = run_dir.name
    return JSONLEventLogger(
        run_dir / "logs" / "events.jsonl",
        EventContext(identifier, mode or _deployment_mode(run_dir), "local"),
    )


def _open_raw_log(path: Path):
    """Open a local runner stream in the protected logs directory."""

    parent = path.parent
    parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    if parent.is_symlink() or path.is_symlink():
        raise RuntimeError("raw runner log path is unsafe")
    flags = os.O_APPEND | os.O_CREAT | os.O_WRONLY
    if hasattr(os, "O_BINARY"):
        flags |= os.O_BINARY
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    if os.fstat(descriptor).st_nlink != 1:
        os.close(descriptor)
        raise RuntimeError("raw runner log must have one hard link")
    if os.name != "nt" and hasattr(os, "fchmod"):
        os.fchmod(descriptor, 0o600)
        os.chmod(parent, 0o700)
    return os.fdopen(descriptor, "ab", buffering=0)


def _update_runner(run_dir: Path, **changes: Any) -> dict[str, Any]:
    path = run_dir / "runner.json"
    value = _read_json(path, required=True)
    assert value is not None
    value.update(changes)
    value["updated_at"] = int(time.time())
    _write_json(path, value)
    return value


def start(args: argparse.Namespace, *, announce: bool = True) -> str:
    from .cli import release_id, resolve_deployment_mode

    commit = validate_commit(args.commit)
    profile = get_profile(args.profile)
    identifier = release_id(args.profile, commit)
    run_dir = _run_dir(identifier)
    run_dir.mkdir(parents=True, mode=0o700)
    deployment_mode = resolve_deployment_mode(args, interactive=True)
    manifest = create_manifest(commit, profile, identifier, deployment_mode)
    write_manifest_once(run_dir / "manifest.json", manifest)
    RunState.create(run_dir / "state.json", identifier)
    now = int(time.time())
    runner = {
        "schema": 1, "release_id": identifier, "profile": args.profile, "commit": commit,
        "pid": None, "process_token": None, "status": "starting", "exit_code": None,
        "started_at": now, "updated_at": now, "stdout": "logs/runner.stdout.log",
        "stderr": "logs/runner.stderr.log", "runner_sha256": runner_checksum(),
        "deployment_mode": deployment_mode,
    }
    _write_json(run_dir / "runner.json", runner)
    logger = _event_logger(run_dir, deployment_mode)
    logger.emit(
        stage="runner", script="release.supervisor", event="runner_starting",
        message="Detached release worker is starting", details={"profile": args.profile, "commit": commit},
    )
    command = [
        sys.executable, str(ENTRYPOINT), "_deploy-worker",
        "--profile", args.profile, "--commit", commit, "--release-id", identifier, "--mode", deployment_mode,
    ]
    try:
        with _open_raw_log(run_dir / "logs" / "runner.stdout.log") as stdout, _open_raw_log(run_dir / "logs" / "runner.stderr.log") as stderr:
            process = popen_detached_worker(command, cwd=DEPLOY_ROOT, stdout=stdout, stderr=stderr)
    except BaseException as error:
        _update_runner(run_dir, status="failed", exit_code=1, finished_at=int(time.time()))
        logger.emit(
            stage="runner", script="release.supervisor", event="runner_start_failed",
            message="Detached release worker failed to start", level="error", exit_code=1,
            details={"error_type": type(error).__name__},
        )
        raise
    token = None
    for _ in range(20):
        token = _process_token(process.pid)
        if token:
            break
        time.sleep(0.05)
    _update_runner(run_dir, pid=process.pid, process_token=token)
    logger.emit(
        stage="runner", script="release.supervisor", event="runner_spawned",
        message="Detached release worker was spawned", details={"pid": process.pid, "process_token_present": bool(token)},
    )
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        current = _read_json(run_dir / "runner.json", required=True)
        assert current is not None
        if current.get("status") in {"waiting_for_lock", "running"}:
            logger.emit(stage="runner", script="release.supervisor", event="startup_handshake_verified", message="Release worker startup handshake verified")
            if announce:
                print(f"release_id={identifier} runner=started")
            return identifier
        if current.get("status") in {"failed", "verified", "recovered", "blocked_reconciliation"}:
            logger.emit(
                stage="runner", script="release.supervisor", event="startup_handshake_failed",
                message="Release worker exited during startup handshake", level="error", exit_code=current.get("exit_code"),
                details={"runner_status": current.get("status")},
            )
            raise RuntimeError(f"release worker exited during startup: {current.get('status')}")
        time.sleep(0.1)
    logger.emit(
        stage="runner", script="release.supervisor", event="startup_handshake_timeout",
        message="Release worker startup handshake timed out", level="error", exit_code=1,
    )
    raise RuntimeError(f"release worker did not complete startup handshake; inspect release_id={identifier}")


def worker(args: argparse.Namespace) -> None:
    from .cli import deploy

    run_dir = _run_dir(args.release_id)
    manifest = _read_json(run_dir / "manifest.json", required=True)
    if manifest is None or manifest.get("release_id") != args.release_id or manifest.get("profile") != args.profile or manifest.get("commit_sha") != args.commit or manifest.get("deployment_mode") != args.deployment_mode:
        raise RuntimeError("worker identity does not match immutable manifest")
    deployment_mode = _deployment_mode(run_dir)
    logger = _event_logger(run_dir, deployment_mode)
    os.environ["SUB2API_RELEASE_ID"] = args.release_id
    os.environ["SUB2API_DEPLOYMENT_MODE"] = deployment_mode
    os.environ["SUB2API_EVENT_LOG"] = str(run_dir / "logs" / "events.jsonl")
    for _ in range(100):
        runner = _read_json(run_dir / "runner.json", required=True)
        if runner and runner.get("pid") == os.getpid() and runner.get("process_token"):
            break
        time.sleep(0.05)
    else:
        logger.emit(
            stage="runner", script="release.supervisor", event="launcher_handshake_failed",
            message="Worker launcher handshake is incomplete", level="error", exit_code=1,
        )
        raise RuntimeError("worker launcher handshake is incomplete")
    exit_code = 1
    terminal = "failed"
    try:
        _update_runner(run_dir, status="waiting_for_lock")
        logger.emit(stage="runner", script="release.supervisor", event="lock_wait_started", message="Release worker is waiting for the release lock")
        with RunLock(RUN_ROOT / ".release.lock"):
            _update_runner(run_dir, status="running")
            logger.emit(stage="runner", script="release.supervisor", event="lock_acquired", message="Release worker acquired the release lock")
            deploy(
                argparse.Namespace(
                    profile=args.profile, commit=args.commit, release_id=args.release_id,
                    deployment_mode=deployment_mode,
                ),
                acquire_lock=False,
            )
        exit_code = 0
        terminal = "verified"
    except BaseException as error:
        release_state = _read_json(run_dir / "release-state.json") or {}
        production = _read_json(run_dir / "gate" / "production-result.json") or {}
        candidate = production.get("status") or release_state.get("status")
        if candidate in TERMINAL_STATES:
            terminal = str(candidate)
        logger.emit(
            stage="runner", script="release.supervisor", event="worker_failed",
            message="Release worker failed", level="error", exit_code=1,
            details={"error_type": type(error).__name__, "terminal_status": terminal},
        )
        raise
    finally:
        _update_runner(run_dir, status=terminal, exit_code=exit_code, finished_at=int(time.time()))
        logger.emit(
            stage="runner", script="release.supervisor", event="worker_finished",
            message="Release worker reached a terminal state",
            level="info" if exit_code == 0 else "error", exit_code=exit_code,
            details={"terminal_status": terminal},
        )


def status_view(identifier: str) -> dict[str, Any]:
    run_dir = _run_dir(identifier)
    if not run_dir.is_dir():
        raise RuntimeError("release does not exist")
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner = _read_json(run_dir / "runner.json") or {}
    vm = _read_json(run_dir / "state.json") or {}
    release_state = _read_json(run_dir / "release-state.json") or {}
    production = _read_json(run_dir / "gate" / "production-result.json") or {}
    gate = _read_json(run_dir / "gate" / "gate.json") or {}
    evidence = gate.get("evidence") if isinstance(gate.get("evidence"), dict) else {}
    history = production.get("history") if isinstance(production.get("history"), list) else []
    last_evidence = history[-1].get("evidence", {}) if history and isinstance(history[-1], dict) else {}
    if not isinstance(last_evidence, dict):
        last_evidence = {}
    candidate = evidence.get("candidate_image_id")
    try:
        validate_image_id(candidate)
    except (TypeError, ValueError):
        candidate = None
    running = last_evidence.get("running_image_id")
    try:
        validate_image_id(running)
    except (TypeError, ValueError):
        running = None
    claim = "unknown"
    if last_evidence.get("gate_consumed") == "true" and production.get("status") == "verified":
        claim = "consumed"
    elif production.get("status") == "recovered" or production.get("stage") in {"recovered", "recovered_after_interruption"}:
        claim = "recovered"
    elif any(isinstance(item, dict) and item.get("stage") == "stage_assets_verified" for item in history):
        claim = "claimed_or_unproven"
    updated = max(
        (int(path.stat().st_mtime) for path in (run_dir / "runner.json", run_dir / "state.json", run_dir / "release-state.json", run_dir / "gate" / "production-result.json") if path.exists()),
        default=int(run_dir.stat().st_mtime),
    )
    value = {
        "release_id": identifier, "profile": manifest.get("profile"), "commit": manifest.get("commit_sha"),
        "deployment_mode": manifest.get("deployment_mode"),
        "runner_status": runner.get("status", "unknown"), "runner_alive": _runner_alive(runner),
        "runner_exit": runner.get("exit_code"), "vm_stage": vm.get("stage", "not_started"),
        "vm_status": vm.get("status", "not_started"), "production_stage": production.get("stage", release_state.get("stage", "not_started")),
        "production_status": production.get("status", release_state.get("status", "not_started")),
        "candidate_image_id": candidate, "running_image_id": running,
        "image_ids_match": bool(candidate and running and candidate == running), "claim_final_state": claim, "updated_at": updated,
    }
    return {field: value[field] for field in STATUS_FIELDS}


def print_status(identifier: str) -> None:
    print(canonical_json(status_view(identifier)).decode("ascii"))


def _vm_gate_failure_event(ssh: SSHRunner, release_id: str, deployment_mode: str) -> dict[str, Any] | None:
    root = f"/opt/sub2api-deploy/release-gates/{release_id}"
    script = f'''set -Eeuo pipefail
root={shlex.quote(root)}
read_one() {{
  local path=$1 default=$2
  if [[ -f $path && ! -L $path ]]; then tr -d '\r\n' <"$path"; else printf '%s' "$default"; fi
}}
raw_log="$root/logs/vm-validate.raw.log"
stderr_log="$root/validator.stderr"
printf 'gate_stage=%s\n' "$(read_one "$root/stage" absent)"
printf 'gate_failure_category=%s\n' "$(read_one "$root/failure-category" absent)"
printf 'gate_failure_line=%s\n' "$(read_one "$root/failure-line" 0)"
if [[ -f $raw_log && ! -L $raw_log ]]; then raw_log_status=ok; raw_log_bytes=$(stat -c '%s' "$raw_log"); else raw_log_status=absent; raw_log_bytes=0; fi
if [[ -f $stderr_log && ! -L $stderr_log ]]; then validator_stderr_bytes=$(stat -c '%s' "$stderr_log"); else validator_stderr_bytes=0; fi
printf 'raw_log_status=%s\nraw_log_bytes=%s\nvalidator_stderr_bytes=%s\n' "$raw_log_status" "$raw_log_bytes" "$validator_stderr_bytes"
'''
    values = ssh.run(
        "local_vm",
        script,
        {"gate_stage", "gate_failure_category", "gate_failure_line", "raw_log_status", "raw_log_bytes", "validator_stderr_bytes"},
    ).values
    stage = values["gate_stage"]
    category = values["gate_failure_category"]
    line = values["gate_failure_line"]
    if category == "absent":
        return None
    if not re.fullmatch(r"[a-z0-9_]+", stage) or not re.fullmatch(r"[a-z0-9_]+", category):
        raise RuntimeError("invalid VM Gate failure evidence")
    if not line.isdigit() or values["raw_log_status"] not in {"ok", "absent"}:
        raise RuntimeError("invalid VM Gate failure evidence")
    if not values["raw_log_bytes"].isdigit() or not values["validator_stderr_bytes"].isdigit():
        raise RuntimeError("invalid VM Gate failure evidence")
    command_id = hashlib.sha256(f"{release_id}|{stage}|{category}|{line}".encode()).hexdigest()[:16]
    return {
        "schema": 1,
        "timestamp": datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z"),
        "release_id": release_id,
        "deployment_mode": deployment_mode,
        "node": "vm",
        "stage": stage,
        "script": "vm-validate.sh",
        "command_id": command_id,
        "attempt": 1,
        "stream": "event",
        "level": "error",
        "event": "gate_failure_evidence",
        "message": "VM Gate failure evidence is available on the VM",
        "exit_code": 1,
        "details": {
            "failure_category": category,
            "failure_line": int(line),
            "raw_log_status": values["raw_log_status"],
            "raw_log_bytes": int(values["raw_log_bytes"]),
            "validator_stderr_bytes": int(values["validator_stderr_bytes"]),
        },
    }


def logs_view(args: argparse.Namespace) -> dict[str, Any]:
    run_dir = _run_dir(args.release_id)
    if not run_dir.is_dir():
        raise RuntimeError("release does not exist")
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    deployment_mode = str(manifest.get("deployment_mode", "not_applicable"))
    requested = ("local", "vm", "racknerd", "dmit", "backup") if args.node == "all" else (args.node,)
    sources: list[tuple[Path, str]] = []
    failure_events: list[dict[str, Any]] = []
    external_issues: list[dict[str, Any]] = []
    with tempfile.TemporaryDirectory(prefix="sub2api-release-logs-") as temporary:
        temporary_root = Path(temporary)
        local_path = run_dir / "logs" / "events.jsonl"
        if local_path.is_file():
            # The detached orchestrator records its own events and every
            # structured SSH event in this local JSONL. Query it once per
            # requested display node so VM failures remain visible even when
            # the node-local event file was never created.
            sources.extend((local_path, node) for node in requested)
        remote_nodes = [node for node in requested if node != "local"]
        if remote_nodes:
            try:
                ssh = SSHRunner()
            except BaseException as error:
                for node in remote_nodes:
                    external_issues.append({"node": node, "line": 0, "error": f"connection setup failed: {type(error).__name__}"})
            else:
                for node in remote_nodes:
                    try:
                        ssh_node = "local_vm" if node == "vm" else node
                        content = ssh.read_release_events(ssh_node, args.release_id)
                        local_copy = temporary_root / f"{node}.events.jsonl"
                        local_copy.write_bytes(content)
                        if os.name != "nt":
                            os.chmod(local_copy, 0o600)
                        sources.append((local_copy, node))
                    except BaseException as error:
                        external_issues.append({"node": node, "line": 0, "error": f"event log unavailable: {type(error).__name__}"})
                        if node == "vm":
                            try:
                                failure_event = _vm_gate_failure_event(ssh, args.release_id, deployment_mode)
                            except BaseException as evidence_error:
                                external_issues.append({"node": node, "line": 0, "error": f"failure evidence unavailable: {type(evidence_error).__name__}"})
                            else:
                                if failure_event is not None:
                                    failure_events.append(failure_event)

        events: list[dict[str, Any]] = list(failure_events)
        issue_documents = list(external_issues)
        for path, source_node in sources:
            result = query_events(
                [path],
                LogQuery(node=source_node, stage=args.stage, level=args.level, since=args.since, tail=None),
            )
            events.extend(result.events)
            issue_documents.extend(
                {"node": source_node, "line": issue.line, "error": issue.error}
                for issue in result.issues
            )
        unique_events: dict[tuple[Any, ...], dict[str, Any]] = {}
        for event in events:
            key = (
                event.get("timestamp"), event.get("command_id"), event.get("node"),
                event.get("stage"), event.get("script"), event.get("event"),
                event.get("attempt"), event.get("stream"),
            )
            unique_events[key] = event
        events = list(unique_events.values())
        events.sort(key=lambda item: (item["timestamp"], item["command_id"]))
        if args.tail is not None:
            if args.tail < 0:
                raise ValueError("tail must not be negative")
            events = events[-args.tail:] if args.tail else []
        status = "ok" if not issue_documents else "partial" if events else "unknown"
        return {
            "schema": 1,
            "release_id": args.release_id,
            "log_status": status,
            "filters": {
                "node": args.node,
                "stage": args.stage,
                "level": args.level,
                "tail": args.tail,
                "since_seconds": int(args.since.total_seconds()) if isinstance(args.since, timedelta) else None,
            },
            "events": events,
            "issues": issue_documents,
        }


def print_logs(args: argparse.Namespace) -> None:
    print(canonical_json(logs_view(args)).decode("ascii"))


def wait(args: argparse.Namespace) -> None:
    deadline = time.monotonic() + args.timeout if args.timeout > 0 else None
    while True:
        value = status_view(args.release_id)
        if not value["runner_alive"]:
            print(canonical_json(value).decode("ascii"))
            if value["runner_status"] not in {"verified", "recovered"}:
                raise SystemExit(1)
            return
        if deadline is not None and time.monotonic() >= deadline:
            print(canonical_json({"release_id": args.release_id, "status": "still_running", "runner_alive": True}).decode("ascii"))
            return
        time.sleep(2)


def _final_evidence(production: dict[str, Any]) -> dict[str, Any]:
    history = production.get("history")
    if not isinstance(history, list):
        raise RuntimeError("production history is missing")
    verified = False
    merged: dict[str, Any] = {}
    for event in history:
        if not isinstance(event, dict):
            continue
        evidence = event.get("evidence")
        if isinstance(evidence, dict):
            merged.update(evidence)
        if event.get("stage") in {"production_verified", "production_verified_after_reconciliation"}:
            verified = True
    if not verified:
        raise RuntimeError("production verification stage is missing")
    return merged


def verified_result_view(identifier: str) -> dict[str, Any]:
    run_dir = _run_dir(identifier)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner = _read_json(run_dir / "runner.json", required=True) or {}
    vm = _read_json(run_dir / "state.json", required=True) or {}
    release_state = _read_json(run_dir / "release-state.json", required=True) or {}
    production = _read_json(run_dir / "gate" / "production-result.json", required=True) or {}
    if _runner_alive(runner) or runner.get("status") != "verified" or runner.get("exit_code") != 0:
        raise RuntimeError("release runner is not successfully terminal")
    document = verify_gate(run_dir / "gate", TRUSTED_VM_PUBLIC_KEY, str(manifest.get("profile")), allow_expired=True)
    if document["manifest"] != manifest or manifest.get("release_id") != identifier:
        raise RuntimeError("manifest and signed Gate identity differ")
    if vm.get("stage") != "vm_validate" or vm.get("status") != "verified":
        raise RuntimeError("VM Gate state is not verified")
    if release_state.get("stage") != "production_release" or release_state.get("status") != "verified":
        raise RuntimeError("production orchestration state is not verified")
    if production.get("status") != "verified" or production.get("stage") not in {"production_verified", "production_verified_after_reconciliation"}:
        raise RuntimeError("production result is not verified")
    evidence = _final_evidence(production)
    expected = {
        "direct_health": "pass", "direct_route_health": "pass", "direct_streaming": "not_checked",
        "dmit_route_health": "pass", "dmit_streaming": "not_checked",
        "canary_usage_recorded": "not_checked", "real_client_ip": "not_checked", "final_health": "pass",
        "dmit_final_health": "pass", "gate_consumed": "true", "plaintext_state_removed": "true",
        "backup_units_restored": "true",
    }
    missing = [key for key, value in expected.items() if evidence.get(key) != value]
    candidate = document["evidence"]["candidate_image_id"]
    validate_image_id(candidate)
    running = evidence.get("running_image_id")
    if running != candidate:
        missing.append("running_image_id")
    if missing:
        raise RuntimeError(f"production evidence is incomplete: {','.join(sorted(set(missing)))}")
    return {"release_id": identifier, "status": "verified", "candidate_image_id": candidate, "running_image_id": running, "claim_final_state": "consumed"}


def verify_result(args: argparse.Namespace) -> None:
    print(canonical_json(verified_result_view(args.release_id)).decode("ascii"))


def _inspect_reconciliation(identifier: str) -> dict[str, Any]:
    run_dir = _run_dir(identifier)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner_document = _read_json(run_dir / "runner.json")
    runner_metadata_present = runner_document is not None
    runner = runner_document or {}
    production = _read_json(run_dir / "gate" / "production-result.json", required=True) or {}
    document = verify_gate(run_dir / "gate", TRUSTED_VM_PUBLIC_KEY, str(manifest.get("profile")), allow_expired=True, allow_historical_runner=True)
    candidate = document["evidence"]["candidate_image_id"]
    release_dir = f"/opt/sub2api/releases/{identifier}"
    state_dir = f"/opt/sub2api/backups/release-state/{identifier}"
    script = f"""set -Eeuo pipefail
active=/opt/sub2api/releases/.active-release
claim=absent
if test -L "$active"; then claim=unsafe
elif test -d "$active" && test -f "$active/release_id" && test -f "$active/gate.json" && test -f "$active/CLAIM_SHA256SUMS" && grep -Fxq 'release_id={identifier}' "$active/release_id" && (cd "$active" && sha256sum -c CLAIM_SHA256SUMS >/dev/null 2>&1); then claim=matching
elif test -e "$active"; then claim=other
fi
consumed=false; test -d {release_dir}/.consumed && test ! -L {release_dir}/.consumed && consumed=true
recovered=false; test -d {release_dir}/.recovered && test ! -L {release_dir}/.recovered && recovered=true
state_present=false; test -e {state_dir} && state_present=true
plaintext_cleaned=false; test -f /opt/sub2api/releases/.active-release/plaintext-cleaned && test ! -L /opt/sub2api/releases/.active-release/plaintext-cleaned && plaintext_cleaned=true
route_started=false; if test -e {state_dir}/route-switch-intent || test -e {state_dir}/route-switched; then route_started=true; fi
migration_started=false; if test -e {state_dir}/migration-committed || find {release_dir} {state_dir} -maxdepth 2 -type f -name '*migration*committed*' -print -quit 2>/dev/null | grep -q .; then migration_started=true; fi
candidate_container=sub2api-candidate-{identifier}
candidate_exists=false; docker inspect "$candidate_container" >/dev/null 2>&1 && candidate_exists=true
candidate_health=absent; test "$candidate_exists" = true && candidate_health=$(docker inspect -f '{{{{if .State.Health}}}}{{{{.State.Health.Status}}}}{{{{else}}}}none{{{{end}}}}' "$candidate_container" 2>/dev/null || printf unknown)
slot=/opt/sub2api/active-app
active_container=$(sed -n 's/^container=//p' "$slot" 2>/dev/null || true)
app_health=unknown
running_image_id=unknown
if test -n "$active_container" && docker inspect "$active_container" >/dev/null 2>&1; then
  app_health=$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container" 2>/dev/null || printf unknown)
  running_image_id=$(docker inspect -f '{{{{.Image}}}}' "$active_container" 2>/dev/null || printf unknown)
fi
nginx_active=false; test "$(systemctl is-active nginx 2>/dev/null || true)" = active && nginx_active=true
backup_timer_enabled=false; test "$(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true)" = enabled && backup_timer_enabled=true
printf 'active_claim=%s\nconsumed=%s\nrecovered=%s\nstate_present=%s\nplaintext_cleaned=%s\nroute_started=%s\nmigration_started=%s\ncandidate_exists=%s\ncandidate_health=%s\napp_health=%s\nnginx_active=%s\nbackup_timer_enabled=%s\nrunning_image_id=%s\n' "$claim" "$consumed" "$recovered" "$state_present" "$plaintext_cleaned" "$route_started" "$migration_started" "$candidate_exists" "$candidate_health" "$app_health" "$nginx_active" "$backup_timer_enabled" "$running_image_id"
"""
    remote = SSHRunner().run("racknerd", script, {"active_claim", "consumed", "recovered", "state_present", "plaintext_cleaned", "route_started", "migration_started", "candidate_exists", "candidate_health", "app_health", "nginx_active", "backup_timer_enabled", "running_image_id"}).values
    history = production.get("history") if isinstance(production.get("history"), list) else []
    stages = {item.get("stage") for item in history if isinstance(item, dict)}
    runner_alive = _runner_alive(runner)
    running_image_valid = True
    try:
        validate_image_id(remote["running_image_id"])
    except (TypeError, ValueError):
        running_image_valid = False
    decision = "blocked"
    failure_code = "state_not_proven"
    if runner_alive:
        decision, failure_code = "runner_active", "runner_still_running"
    elif remote["consumed"] == "true":
        decision, failure_code = "already_consumed", "none"
    elif remote["recovered"] == "true":
        decision, failure_code = "already_recovered", "none"
    elif (
        remote["active_claim"] == "matching" and remote["state_present"] == "false"
        and remote["app_health"] == "healthy" and remote["nginx_active"] == "true"
        and remote["backup_timer_enabled"] == "true" and remote["running_image_id"] != candidate
        and running_image_valid
        and "stage_assets_verified" in stages and not stages.intersection(DANGEROUS_STAGES)
        and remote.get("candidate_exists") == "false"
    ):
        decision, failure_code = "claim_only_recover", "caller_interrupted_after_claim"
    elif (
        remote["active_claim"] == "matching" and remote["state_present"] == "true"
        and remote.get("plaintext_cleaned", "false") == "true" and remote.get("route_started", "true") == "false"
        and remote.get("migration_started", "true") == "false" and remote["app_health"] == "healthy"
        and remote["nginx_active"] == "true" and remote["backup_timer_enabled"] == "true"
        and remote["running_image_id"] != candidate and running_image_valid
        and remote.get("candidate_exists") == "true"
    ):
        decision, failure_code = "cleanup_completed_recover", "cleanup_reply_rejected"
    elif remote["state_present"] == "true":
        decision, failure_code = "coordinated_restore_required", "release_state_exists"
    if not runner_metadata_present and decision not in {"already_consumed", "already_recovered", "coordinated_restore_required"}:
        decision, failure_code = "blocked", "runner_metadata_missing"
    return {
        "release_id": identifier, "decision": decision, "failure_code": failure_code,
        "runner_alive": runner_alive, "active_claim": remote["active_claim"],
        "state_present": remote["state_present"], "plaintext_cleaned": remote.get("plaintext_cleaned", "false"),
        "route_started": remote.get("route_started", "true"), "migration_started": remote.get("migration_started", "true"),
        "app_health": remote["app_health"],
        "nginx_active": remote["nginx_active"], "backup_timer_enabled": remote["backup_timer_enabled"],
        "running_image_id": remote["running_image_id"], "candidate_image_id": candidate,
        "candidate_exists": remote.get("candidate_exists", "false"), "candidate_health": remote.get("candidate_health", "absent"),
    }


def reconcile_inspect(args: argparse.Namespace) -> None:
    print(canonical_json(_inspect_reconciliation(args.release_id)).decode("ascii"))


def reconcile(args: argparse.Namespace) -> None:
    inspection = _inspect_reconciliation(args.release_id)
    identifier = args.release_id
    release_dir = f"/opt/sub2api/releases/{identifier}"
    if args.mode == "coordinated-recover":
        if inspection["decision"] == "already_recovered" and not inspection["runner_alive"]:
            verify_script = f"""set -Eeuo pipefail
test -d {release_dir}/.recovered
test ! -L {release_dir}/.recovered
test -f {release_dir}/.recovered/marker
test -f {release_dir}/.recovered/plaintext-cleaned
test ! -e /opt/sub2api/releases/.active-release
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
test -n "$active_container"
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container")" = healthy
test "$(systemctl is-active nginx)" = active
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
test "$(systemctl is-active sub2api-backup.timer)" = active
printf 'backup_units_restored=true\nrelease_claim_reconciled=true\nplaintext_state_removed=true\n'
"""
            values = SSHRunner().run(
                "racknerd",
                verify_script,
                {"backup_units_restored", "release_claim_reconciled", "plaintext_state_removed"},
                timeout=300,
            ).values
            recovery_stage = "recovered_after_coordinated_restore"
        elif inspection["decision"] != "coordinated_restore_required" or inspection["runner_alive"]:
            raise RuntimeError(f"coordinated recovery is not allowed: {inspection['decision']}")
        else:
            runner = SSHRunner()
            remote_temp = runner.create_temp_dir("racknerd", "/opt/sub2api/releases", "coordinated-restore")
            remote_restore = f"{remote_temp}/restore.sh"
            runner.upload_file("racknerd", COORDINATED_RESTORE, remote_restore, 0o500)
            try:
                restore = runner.run(
                    "racknerd",
                    f"RELEASE_DIR={release_dir} {remote_restore}",
                    {"coordinated_restore", "restored_image_id", "application_health"},
                    timeout=2400,
                ).values
                state_dir = f"/opt/sub2api/backups/release-state/{identifier}"
                finish_script = f"""set -Eeuo pipefail
export RELEASE_DIR={release_dir}
export STATE_ROOT=/opt/sub2api/backups/release-state
export STATE_DIR={state_dir}
/opt/sub2api/releases/.active-release/assets/restore-backup-units.sh
/opt/sub2api/releases/.active-release/assets/cleanup-state.sh
/opt/sub2api/releases/.active-release/assets/reconcile.sh
test -f {release_dir}/.recovered/marker
test -f {release_dir}/.recovered/plaintext-cleaned
test ! -e /opt/sub2api/releases/.active-release
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
test -n "$active_container"
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container")" = healthy
test "$(systemctl is-active nginx)" = active
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
test "$(systemctl is-active sub2api-backup.timer)" = active
printf 'backup_units_restored=true\nrelease_claim_reconciled=true\nplaintext_state_removed=true\n'
"""
                finished = runner.run(
                    "racknerd",
                    finish_script,
                    {"backup_units_restored", "release_claim_reconciled", "plaintext_state_removed", "state_cleanup"},
                    timeout=900,
                ).values
            finally:
                runner.run("racknerd", f"rm -rf {remote_temp} && printf 'cleanup=true\\n'", {"cleanup"})
            values = {**restore, **finished}
            recovery_stage = "recovered_after_coordinated_restore"
    else:
        if inspection["decision"] not in {"claim_only_recover", "cleanup_completed_recover"}:
            raise RuntimeError(f"automatic recovery is not allowed: {inspection['decision']}")
        candidate_name = f"sub2api-candidate-{identifier}"
        cleanup_candidate = f"if docker inspect {candidate_name} >/dev/null 2>&1; then docker stop -t 30 {candidate_name} >/dev/null 2>&1 || true; docker rm {candidate_name} >/dev/null 2>&1 || true; fi"
        cleanup_command = f"{cleanup_candidate}\n/opt/sub2api/releases/.active-release/assets/cleanup-state.sh" if inspection["decision"] == "claim_only_recover" else cleanup_candidate
        script = f"""set -Eeuo pipefail
export RELEASE_DIR={release_dir}
{cleanup_command}
/opt/sub2api/releases/.active-release/assets/reconcile.sh
test -f {release_dir}/.recovered/marker
test -f {release_dir}/.recovered/plaintext-cleaned
test ! -e /opt/sub2api/releases/.active-release
active_container=$(sed -n 's/^container=//p' /opt/sub2api/active-app)
test -n "$active_container"
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' "$active_container")" = healthy
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
printf 'release_claim_reconciled=true\nplaintext_state_removed=true\n'
"""
        values = SSHRunner().run("racknerd", script, {"release_claim_reconciled", "plaintext_state_removed"}, timeout=600).values
        recovery_stage = "recovered_after_interruption"
    run_dir = _run_dir(identifier)
    production_path = run_dir / "gate" / "production-result.json"
    production = _read_json(production_path, required=True) or {}
    production["status"] = "recovered"
    production["stage"] = recovery_stage
    history = production.setdefault("history", [])
    if not isinstance(history, list):
        raise RuntimeError("production history is invalid")
    history.append({"stage": recovery_stage, "at": int(time.time()), "evidence": values})
    _write_json(production_path, production)
    state_path = run_dir / "release-state.json"
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    state.transition("production_release", "recovered", values)
    runner = _read_json(run_dir / "runner.json")
    if runner is not None and runner.get("status") != "verified":
        _update_runner(run_dir, status="recovered")
    print(canonical_json({"release_id": identifier, "status": "recovered", "claim_final_state": "recovered"}).decode("ascii"))
