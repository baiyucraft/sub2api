from __future__ import annotations

import argparse
import ctypes
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .gate import verify_gate
from .manifest import create_manifest, runner_checksum, validate_commit, validate_image_id, write_manifest_once
from .profiles import get_profile
from .ssh import SSHRunner
from .state import RunLock, RunState, TERMINAL_STATES


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
WORKSPACE = Path(__file__).resolve().parents[2]
RUN_ROOT = WORKSPACE / ".tmp" / "releases"
TRUSTED_VM_PUBLIC_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
RELEASE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$")
MAX_JSON_BYTES = 2 * 1024 * 1024
STATUS_FIELDS = (
    "release_id", "profile", "commit", "runner_status", "runner_alive", "runner_exit",
    "vm_stage", "vm_status", "production_stage", "production_status",
    "candidate_image_id", "running_image_id", "image_ids_match", "claim_final_state", "updated_at",
)
DANGEROUS_STAGES = {
    "production_preflight", "pre_switch_streaming_verified", "freeze", "freeze_verified",
    "migration_preflight", "backup", "backup_verified", "migration_and_switch",
    "candidate_internal_verified", "public_route_verification", "split_route_verified",
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


def _update_runner(run_dir: Path, **changes: Any) -> dict[str, Any]:
    path = run_dir / "runner.json"
    value = _read_json(path, required=True)
    assert value is not None
    value.update(changes)
    value["updated_at"] = int(time.time())
    _write_json(path, value)
    return value


def start(args: argparse.Namespace) -> None:
    from .cli import release_id

    commit = validate_commit(args.commit)
    profile = get_profile(args.profile)
    identifier = release_id(args.profile, commit)
    run_dir = _run_dir(identifier)
    run_dir.mkdir(parents=True, mode=0o700)
    manifest = create_manifest(commit, profile, identifier)
    write_manifest_once(run_dir / "manifest.json", manifest)
    RunState.create(run_dir / "state.json", identifier)
    now = int(time.time())
    runner = {
        "schema": 1, "release_id": identifier, "profile": args.profile, "commit": commit,
        "pid": None, "process_token": None, "status": "starting", "exit_code": None,
        "started_at": now, "updated_at": now, "stdout": "runner.stdout.log",
        "stderr": "runner.stderr.log", "runner_sha256": runner_checksum(),
    }
    _write_json(run_dir / "runner.json", runner)
    command = [
        sys.executable, str(DEPLOY_ROOT / "release.py"), "_deploy-worker",
        "--profile", args.profile, "--commit", commit, "--release-id", identifier,
    ]
    flags = 0
    popen_args: dict[str, Any] = {"cwd": DEPLOY_ROOT, "stdin": subprocess.DEVNULL, "close_fds": True}
    if os.name == "nt":
        flags = subprocess.CREATE_NEW_PROCESS_GROUP | subprocess.DETACHED_PROCESS | subprocess.CREATE_NO_WINDOW
        popen_args["creationflags"] = flags
    else:
        popen_args["start_new_session"] = True
    try:
        with (run_dir / "runner.stdout.log").open("ab", buffering=0) as stdout, (run_dir / "runner.stderr.log").open("ab", buffering=0) as stderr:
            process = subprocess.Popen(command, stdout=stdout, stderr=stderr, **popen_args)
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
        current = _read_json(run_dir / "runner.json", required=True)
        assert current is not None
        if current.get("status") in {"waiting_for_lock", "running"}:
            print(f"release_id={identifier} runner=started")
            return
        if current.get("status") in {"failed", "verified", "recovered", "blocked_reconciliation"}:
            raise RuntimeError(f"release worker exited during startup: {current.get('status')}")
        time.sleep(0.1)
    raise RuntimeError(f"release worker did not complete startup handshake; inspect release_id={identifier}")


def worker(args: argparse.Namespace) -> None:
    from .cli import deploy

    run_dir = _run_dir(args.release_id)
    manifest = _read_json(run_dir / "manifest.json", required=True)
    if manifest is None or manifest.get("release_id") != args.release_id or manifest.get("profile") != args.profile or manifest.get("commit_sha") != args.commit:
        raise RuntimeError("worker identity does not match immutable manifest")
    for _ in range(100):
        runner = _read_json(run_dir / "runner.json", required=True)
        if runner and runner.get("pid") == os.getpid() and runner.get("process_token"):
            break
        time.sleep(0.05)
    else:
        raise RuntimeError("worker launcher handshake is incomplete")
    exit_code = 1
    terminal = "failed"
    try:
        _update_runner(run_dir, status="waiting_for_lock")
        with RunLock(RUN_ROOT / ".release.lock"):
            _update_runner(run_dir, status="running")
            deploy(argparse.Namespace(profile=args.profile, commit=args.commit, release_id=args.release_id), acquire_lock=False)
        exit_code = 0
        terminal = "verified"
    except BaseException:
        release_state = _read_json(run_dir / "release-state.json") or {}
        production = _read_json(run_dir / "gate" / "production-result.json") or {}
        candidate = production.get("status") or release_state.get("status")
        if candidate in TERMINAL_STATES:
            terminal = str(candidate)
        raise
    finally:
        _update_runner(run_dir, status=terminal, exit_code=exit_code, finished_at=int(time.time()))


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


def verify_result(args: argparse.Namespace) -> None:
    run_dir = _run_dir(args.release_id)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner = _read_json(run_dir / "runner.json", required=True) or {}
    vm = _read_json(run_dir / "state.json", required=True) or {}
    release_state = _read_json(run_dir / "release-state.json", required=True) or {}
    production = _read_json(run_dir / "gate" / "production-result.json", required=True) or {}
    if _runner_alive(runner) or runner.get("status") != "verified" or runner.get("exit_code") != 0:
        raise RuntimeError("release runner is not successfully terminal")
    document = verify_gate(run_dir / "gate", TRUSTED_VM_PUBLIC_KEY, str(manifest.get("profile")), allow_expired=True)
    if document["manifest"] != manifest or manifest.get("release_id") != args.release_id:
        raise RuntimeError("manifest and signed Gate identity differ")
    if vm.get("stage") != "vm_validate" or vm.get("status") != "verified":
        raise RuntimeError("VM Gate state is not verified")
    if release_state.get("stage") != "production_release" or release_state.get("status") != "verified":
        raise RuntimeError("production orchestration state is not verified")
    if production.get("status") != "verified" or production.get("stage") not in {"production_verified", "production_verified_after_reconciliation"}:
        raise RuntimeError("production result is not verified")
    evidence = _final_evidence(production)
    expected = {
        "direct_health": "pass", "direct_route_health": "pass", "direct_streaming": "pass",
        "dmit_route_health": "pass", "dmit_streaming": "pass",
        "canary_usage_recorded": "true", "real_client_ip": "pass", "final_health": "pass",
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
    print(canonical_json({"release_id": args.release_id, "status": "verified", "candidate_image_id": candidate, "running_image_id": running, "claim_final_state": "consumed"}).decode("ascii"))


def _inspect_reconciliation(identifier: str) -> dict[str, Any]:
    run_dir = _run_dir(identifier)
    manifest = _read_json(run_dir / "manifest.json", required=True) or {}
    runner = _read_json(run_dir / "runner.json", required=True) or {}
    production = _read_json(run_dir / "gate" / "production-result.json", required=True) or {}
    document = verify_gate(run_dir / "gate", TRUSTED_VM_PUBLIC_KEY, str(manifest.get("profile")), allow_expired=True)
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
app_health=$(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api 2>/dev/null || printf unknown)
nginx_active=false; test "$(systemctl is-active nginx 2>/dev/null || true)" = active && nginx_active=true
backup_timer_enabled=false; test "$(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true)" = enabled && backup_timer_enabled=true
running_image_id=$(docker inspect -f '{{{{.Image}}}}' sub2api 2>/dev/null || printf unknown)
printf 'active_claim=%s\nconsumed=%s\nrecovered=%s\nstate_present=%s\napp_health=%s\nnginx_active=%s\nbackup_timer_enabled=%s\nrunning_image_id=%s\n' "$claim" "$consumed" "$recovered" "$state_present" "$app_health" "$nginx_active" "$backup_timer_enabled" "$running_image_id"
"""
    remote = SSHRunner().run("racknerd", script, {"active_claim", "consumed", "recovered", "state_present", "app_health", "nginx_active", "backup_timer_enabled", "running_image_id"}).values
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
    ):
        decision, failure_code = "claim_only_recover", "caller_interrupted_after_claim"
    elif remote["state_present"] == "true":
        decision, failure_code = "coordinated_restore_required", "release_state_exists"
    return {
        "release_id": identifier, "decision": decision, "failure_code": failure_code,
        "runner_alive": runner_alive, "active_claim": remote["active_claim"],
        "state_present": remote["state_present"], "app_health": remote["app_health"],
        "nginx_active": remote["nginx_active"], "backup_timer_enabled": remote["backup_timer_enabled"],
        "running_image_id": remote["running_image_id"], "candidate_image_id": candidate,
    }


def reconcile_inspect(args: argparse.Namespace) -> None:
    print(canonical_json(_inspect_reconciliation(args.release_id)).decode("ascii"))


def reconcile(args: argparse.Namespace) -> None:
    inspection = _inspect_reconciliation(args.release_id)
    if inspection["decision"] != "claim_only_recover":
        raise RuntimeError(f"automatic recovery is not allowed: {inspection['decision']}")
    identifier = args.release_id
    release_dir = f"/opt/sub2api/releases/{identifier}"
    script = f"""set -Eeuo pipefail
export RELEASE_DIR={release_dir}
/opt/sub2api/releases/.active-release/assets/cleanup-state.sh
/opt/sub2api/releases/.active-release/assets/reconcile.sh
test -f {release_dir}/.recovered/marker
test -f {release_dir}/.recovered/plaintext-cleaned
test ! -e /opt/sub2api/releases/.active-release
test "$(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api)" = healthy
test "$(systemctl is-enabled sub2api-backup.timer)" = enabled
printf 'release_claim_reconciled=true\nplaintext_state_removed=true\n'
"""
    values = SSHRunner().run("racknerd", script, {"release_claim_reconciled", "plaintext_state_removed"}, timeout=600).values
    run_dir = _run_dir(identifier)
    production_path = run_dir / "gate" / "production-result.json"
    production = _read_json(production_path, required=True) or {}
    production["status"] = "recovered"
    production["stage"] = "recovered_after_interruption"
    history = production.setdefault("history", [])
    if not isinstance(history, list):
        raise RuntimeError("production history is invalid")
    history.append({"stage": "recovered_after_interruption", "at": int(time.time()), "evidence": values})
    _write_json(production_path, production)
    state_path = run_dir / "release-state.json"
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    state.transition("production_release", "recovered", values)
    runner = _read_json(run_dir / "runner.json", required=True) or {}
    if runner.get("status") != "verified":
        _update_runner(run_dir, status="recovered")
    print(canonical_json({"release_id": identifier, "status": "recovered", "claim_final_state": "recovered"}).decode("ascii"))
