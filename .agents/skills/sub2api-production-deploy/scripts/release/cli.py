from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import shutil
import subprocess
import sys
import time
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from pathlib import Path

from .bootstrap import bootstrap_trust
from .atomic import atomic_write, canonical_json
from .doctor import NODES, ReleaseDoctor
from .gate import verify_gate
from .manifest import create_manifest, write_manifest_once
from .paths import RUN_ROOT, SCRIPTS_ROOT, TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .profiles import get_profile
from .production_bootstrap import bootstrap_production
from .state import RunLock, RunState


LOGGING_ROOT = SCRIPTS_ROOT / "logging"
if str(LOGGING_ROOT) not in sys.path:
    sys.path.insert(0, str(LOGGING_ROOT))

from release_logging import EventContext, JSONLEventLogger  # noqa: E402
from release_logging.retention import ReleaseLogRecord, build_retention_plan, verify_retention_plan  # noqa: E402


DEPLOYMENT_MODES = ("blue-green", "downtime")


def _deployment_mode(args: argparse.Namespace | None = None, manifest: dict | None = None) -> str:
    for source in (vars(args) if args is not None else {}, manifest or {}):
        value = source.get("deployment_mode") or source.get("mode")
        if value in {"blue-green", "downtime"}:
            return str(value)
    raise RuntimeError("deployment mode is missing")


def resolve_deployment_mode(args: argparse.Namespace, *, interactive: bool) -> str:
    value = getattr(args, "deployment_mode", None)
    if value in DEPLOYMENT_MODES:
        return str(value)
    if value is not None:
        raise RuntimeError("unsupported deployment mode")
    if interactive and sys.stdin.isatty() and sys.stdout.isatty():
        print("请选择生产部署模式：", file=sys.stderr)
        print("1. 蓝绿无感切换", file=sys.stderr)
        print("2. 简单停机更新", file=sys.stderr)
        selected = input("输入 1 或 2: ").strip()
        if selected == "1":
            return "blue-green"
        if selected == "2":
            return "downtime"
        raise RuntimeError("deployment mode selection was cancelled")
    raise RuntimeError("non-interactive production release requires --mode blue-green|downtime")


def _event_logger(run_dir: Path, identifier: str, mode: str) -> JSONLEventLogger:
    return JSONLEventLogger(run_dir / "logs" / "events.jsonl", EventContext(identifier, mode, "local"))


def _parse_since(value: str) -> timedelta:
    units = {"s": 1, "m": 60, "h": 3600, "d": 86400}
    if len(value) < 2 or value[-1].lower() not in units:
        raise argparse.ArgumentTypeError("since must use s, m, h or d, for example 30m")
    try:
        amount = int(value[:-1])
    except ValueError as exc:
        raise argparse.ArgumentTypeError("since must be an integer duration") from exc
    if amount < 0:
        raise argparse.ArgumentTypeError("since must not be negative")
    return timedelta(seconds=amount * units[value[-1].lower()])


def emit_progress(message: str) -> None:
    try:
        print(message, flush=True)
    except BrokenPipeError:
        pass


def release_id(profile: str, commit: str) -> str:
    return f"{profile}-{commit[:12]}-{int(time.time())}-{secrets.token_hex(4)}"


def create_vm_gate(profile_name: str, commit: str, deployment_mode: str, identifier: str | None = None, acquire_lock: bool = True) -> Path:
    profile = get_profile(profile_name)
    preallocated = identifier is not None
    identifier = identifier or release_id(profile_name, commit)
    run_dir = RUN_ROOT / identifier
    manifest_path = run_dir / "manifest.json"
    state_path = run_dir / "state.json"
    gate_path = run_dir / "gate"
    if run_dir.exists() or run_dir.is_symlink():
        if not preallocated or not run_dir.is_dir() or run_dir.is_symlink():
            raise RuntimeError("release directory already exists or is unsafe")
        if not manifest_path.is_file() or manifest_path.is_symlink() or not state_path.is_file() or state_path.is_symlink():
            raise RuntimeError("preallocated release state is incomplete or unsafe")
    else:
        run_dir.mkdir(parents=True, mode=0o700)
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        if manifest.get("release_id") != identifier or manifest.get("commit_sha") != commit or manifest.get("profile") != profile_name:
            raise RuntimeError("release manifest identity does not match worker")
    else:
        manifest = create_manifest(commit, profile, identifier, deployment_mode)
        write_manifest_once(manifest_path, manifest)
    if manifest.get("deployment_mode") != deployment_mode:
        raise RuntimeError("immutable manifest deployment mode does not match")
    logger = _event_logger(run_dir, identifier, _deployment_mode(manifest=manifest))
    if gate_path.exists() or gate_path.is_symlink():
        raise RuntimeError("Gate output path already exists or is unsafe")
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    if state.value.get("schema") != 1 or state.value.get("release_id") != identifier:
        raise RuntimeError("release state identity does not match worker")
    emit_progress(f"release_id={identifier} stage=vm_validate status=running")
    logger.emit(stage="vm_validate", script="release.cli", event="stage_started", message="VM validation started")
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        state.transition("vm_validate", "running")
        command = [sys.executable, "-m", "release.vm_validate", "--manifest", str(manifest_path), "--output", str(gate_path)]
        try:
            child_env = os.environ.copy()
            child_env["PYTHONUNBUFFERED"] = "1"
            subprocess.run(command, cwd=SCRIPTS_ROOT, check=True, env=child_env)
            verify_gate(gate_path, TRUSTED_VM_PUBLIC_KEY, profile_name)
        except BaseException as error:
            state.transition("vm_validate", "failed")
            logger.emit(
                stage="vm_validate", script="release.cli", event="stage_failed",
                message="VM validation failed", level="error", exit_code=getattr(error, "returncode", 1),
                details={"error_type": type(error).__name__},
            )
            raise
        state.transition("vm_validate", "verified", {"gate_dir": str(gate_path)})
        logger.emit(stage="vm_validate", script="release.cli", event="stage_finished", message="VM validation verified", exit_code=0)
    finally:
        if lock:
            lock.__exit__(None, None, None)
    return gate_path


def vm_validate(args: argparse.Namespace) -> None:
    gate = create_vm_gate(args.profile, args.commit, resolve_deployment_mode(args, interactive=False))
    print(f"gate={gate}")


def release(args: argparse.Namespace, acquire_lock: bool = True) -> None:
    gate_dir = Path(args.gate).resolve()
    document = verify_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, args.profile)
    identifier = document["manifest"]["release_id"]
    run_dir = RUN_ROOT / identifier
    logger = _event_logger(run_dir, identifier, _deployment_mode(args, document.get("manifest")))
    state_path = run_dir / "release-state.json"
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        state.transition("production_release", "running")
        logger.emit(stage="production_release", script="release.cli", event="stage_started", message="Production release started")
        command = [sys.executable, "-m", "release.production", "--gate", str(gate_dir), "--profile", args.profile]
        try:
            child_env = os.environ.copy()
            child_env["PYTHONUNBUFFERED"] = "1"
            subprocess.run(command, cwd=SCRIPTS_ROOT, check=True, env=child_env)
        except BaseException as error:
            result_path = gate_dir / "production-result.json"
            result = json.loads(result_path.read_text(encoding="utf-8")) if result_path.exists() else {}
            failure_status = result.get("status") if result.get("status") in {"failed", "recovered", "blocked_reconciliation"} else "blocked_reconciliation"
            state.transition("production_release", failure_status)
            logger.emit(
                stage="production_release", script="release.cli", event="stage_failed",
                message="Production release failed", level="error", exit_code=getattr(error, "returncode", 1),
                details={"error_type": type(error).__name__, "failure_status": failure_status},
            )
            raise
        state.transition("production_release", "verified")
        logger.emit(stage="production_release", script="release.cli", event="stage_finished", message="Production release verified", exit_code=0)
    finally:
        if lock:
            lock.__exit__(None, None, None)


def deploy(args: argparse.Namespace, acquire_lock: bool = True) -> None:
    deployment_mode = resolve_deployment_mode(args, interactive=getattr(args, "release_id", None) is None)
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        identifier = getattr(args, "release_id", None)
        logger = _event_logger(RUN_ROOT / identifier, identifier, _deployment_mode(args)) if identifier else None
        emit_progress(f"release_profile={args.profile} release_commit={args.commit} stage=doctor status=running")
        if logger:
            logger.emit(stage="doctor", script="release.cli", event="stage_started", message="Release doctor started")
        doctor = ReleaseDoctor(args.profile, args.commit)
        doctor.run(("local", "vm", "dmit", "backup"))
        if logger:
            logger.emit(stage="doctor", script="release.cli", event="nodes_verified", message="Local, VM, DMIT and backup checks verified")
        bootstrap_production(args.profile, doctor.runner)
        if logger:
            logger.emit(stage="bootstrap_production", script="release.cli", event="stage_finished", message="Production bootstrap verified", exit_code=0)
        doctor.run(("racknerd",))
        if logger:
            logger.emit(stage="doctor", script="release.cli", event="stage_finished", message="Production doctor verified", exit_code=0)
        gate = create_vm_gate(args.profile, args.commit, deployment_mode, identifier=identifier, acquire_lock=False)
        release(argparse.Namespace(gate=str(gate), profile=args.profile, deployment_mode=deployment_mode), acquire_lock=False)
    except BaseException as error:
        if "logger" in locals() and logger:
            logger.emit(
                stage="orchestration", script="release.cli", event="deploy_failed", message="Release orchestration failed",
                level="error", exit_code=getattr(error, "returncode", 1), details={"error_type": type(error).__name__},
            )
        raise
    finally:
        if lock:
            lock.__exit__(None, None, None)
    emit_progress(f"release=verified gate={gate}")


def status(args: argparse.Namespace) -> None:
    from .supervisor import print_status

    print_status(args.release_id)


def logs(args: argparse.Namespace) -> None:
    from .supervisor import print_logs

    print_logs(args)


def bootstrap(args: argparse.Namespace) -> None:
    bootstrap_trust()
    print("trust_bootstrap=verified")


def doctor(args: argparse.Namespace) -> None:
    nodes = NODES if args.node == "all" else (args.node,)
    evidence = ReleaseDoctor(args.profile, args.commit).run(nodes)
    print(" ".join(f"{key}={value}" for key, value in evidence.items()))


def production_bootstrap(args: argparse.Namespace) -> None:
    evidence = bootstrap_production(args.profile)
    print(" ".join(f"{key}={value}" for key, value in evidence.items()))


def production_cleanup(args: argparse.Namespace) -> None:
    from .production_cleanup import cleanup_production

    evidence = cleanup_production(args.release_id, args.mode, args.plan_sha256)
    print(" ".join(f"{key}={value}" for key, value in sorted(evidence.items())))


_RETENTION_RELEASE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$")


def _retention_read_json(path: Path) -> dict:
    try:
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 2 * 1024 * 1024:
            return {}
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        return {}
    return value if isinstance(value, dict) else {}


def _local_retention_records() -> list[ReleaseLogRecord]:
    records: list[ReleaseLogRecord] = []
    if not RUN_ROOT.exists() or RUN_ROOT.is_symlink():
        return records
    for run_dir in sorted(RUN_ROOT.iterdir(), key=lambda item: item.name):
        if not run_dir.is_dir() or run_dir.is_symlink() or not _RETENTION_RELEASE_ID.fullmatch(run_dir.name):
            continue
        logs_dir = run_dir / "logs"
        if logs_dir.is_symlink():
            # An unsafe log path is retained by the unknown status branch.
            logs_path = logs_dir
        else:
            logs_path = logs_dir
        manifest = _retention_read_json(run_dir / "manifest.json")
        runner = _retention_read_json(run_dir / "runner.json")
        state = _retention_read_json(run_dir / "release-state.json")
        production = _retention_read_json(run_dir / "gate" / "production-result.json")
        created_raw = manifest.get("created_at")
        try:
            created = datetime.fromtimestamp(int(created_raw), tz=timezone.utc)
        except (TypeError, ValueError, OSError, OverflowError):
            created = datetime.fromtimestamp(int(run_dir.stat().st_mtime), tz=timezone.utc)
        runner_status = str(runner.get("status") or "unknown").lower()
        status = str(production.get("status") or state.get("status") or runner_status).lower()
        active = runner_status in {"starting", "waiting_for_lock", "running"}
        recovered_marker = run_dir / ".recovered"
        reconciliation = status in {"blocked_reconciliation", "reconciliation"}
        if production.get("stage") in {"recovered", "recovered_after_interruption", "recovered_after_coordinated_restore"}:
            reconciliation = True
        if logs_dir.is_symlink() or (logs_dir.exists() and not logs_dir.is_dir()):
            status = "unknown"
        records.append(
            ReleaseLogRecord(
                release_id=run_dir.name,
                path=logs_path,
                created_at=created,
                status=status,
                active=active,
                has_recovery_point=recovered_marker.exists() or status in {"recovered", "recovery"},
                has_reconciliation_evidence=reconciliation,
            )
        )
    verified = [record for record in records if record.status in {"verified", "success", "completed", "production_verified"}]
    if verified:
        current = max(verified, key=lambda record: (record.created_at, record.release_id))
        records = [replace(record, current_baseline=True) if record.release_id == current.release_id else record for record in records]
    return records


def _retention_plan_from_document(document: dict) -> tuple[dict, datetime]:
    generated_at = document.get("generated_at")
    if not isinstance(generated_at, str):
        raise RuntimeError("retention plan generated_at is missing")
    try:
        generated = datetime.fromisoformat(generated_at.replace("Z", "+00:00"))
    except ValueError as error:
        raise RuntimeError("retention plan generated_at is invalid") from error
    if generated.tzinfo is None:
        raise RuntimeError("retention plan generated_at has no timezone")
    return document, generated.astimezone(timezone.utc)


def retention(args: argparse.Namespace) -> None:
    plan_path = RUN_ROOT / ".retention-plan.json"
    if args.mode == "dry-run":
        plan = build_retention_plan(
            _local_retention_records(),
            success_retention_days=args.success_retention_days,
            keep_recent=args.keep_recent,
        )
        atomic_write(plan_path, canonical_json(plan.document()) + b"\n", 0o600)
        print(canonical_json(plan.document()).decode("ascii"))
        return
    if args.plan_sha256 is None:
        raise RuntimeError("retention apply requires --plan-sha256 from an unchanged dry-run")
    saved = _retention_read_json(plan_path)
    if not saved or not verify_retention_plan(saved, args.plan_sha256):
        raise RuntimeError("retention plan checksum does not match the saved dry-run plan")
    saved, generated = _retention_plan_from_document(saved)
    try:
        success_days = int(saved["success_retention_days"])
        keep_recent = int(saved["keep_recent"])
    except (KeyError, TypeError, ValueError) as error:
        raise RuntimeError("retention plan parameters are invalid") from error
    current = build_retention_plan(
        _local_retention_records(), now=generated,
        success_retention_days=success_days, keep_recent=keep_recent,
    )
    if current.plan_sha256 != args.plan_sha256:
        raise RuntimeError("retention plan drift detected; run a new dry-run")
    deleted: list[str] = []
    for entry in current.delete:
        release_id = entry["release_id"]
        if not _RETENTION_RELEASE_ID.fullmatch(release_id):
            raise RuntimeError("retention plan contains an invalid release ID")
        candidate_run_dir = RUN_ROOT / release_id
        if candidate_run_dir.is_symlink():
            raise RuntimeError("retention target changed; run a new dry-run")
        run_dir = candidate_run_dir.resolve()
        root = RUN_ROOT.resolve()
        target_path = candidate_run_dir / "logs"
        if target_path.is_symlink():
            raise RuntimeError("retention target changed; run a new dry-run")
        target = target_path.resolve(strict=False)
        planned = Path(entry["path"]).resolve(strict=False)
        if run_dir.parent != root or target.parent != run_dir or target != planned or not target_path.is_dir():
            raise RuntimeError("retention target changed; run a new dry-run")
        shutil.rmtree(target_path)
        deleted.append(release_id)
    print(canonical_json({"schema": 1, "retention_status": "completed", "plan_sha256": args.plan_sha256, "deleted_release_ids": deleted}).decode("ascii"))


def main() -> None:
    parser = argparse.ArgumentParser(description="VM-gated Sub2API release runner")
    subparsers = parser.add_subparsers(required=True)
    bootstrap_parser = subparsers.add_parser("bootstrap-trust")
    bootstrap_parser.set_defaults(handler=bootstrap)
    production_bootstrap_parser = subparsers.add_parser("bootstrap-production")
    production_bootstrap_parser.add_argument("--profile", default="182")
    production_bootstrap_parser.set_defaults(handler=production_bootstrap)
    doctor_parser = subparsers.add_parser("doctor")
    doctor_parser.add_argument("--profile", default="182")
    doctor_parser.add_argument("--commit")
    doctor_parser.add_argument("--node", choices=("all", *NODES), default="all")
    doctor_parser.set_defaults(handler=doctor)
    validate_parser = subparsers.add_parser("vm-validate")
    validate_parser.add_argument("--profile", default="182")
    validate_parser.add_argument("--commit", required=True)
    validate_parser.add_argument("--mode", dest="deployment_mode", choices=DEPLOYMENT_MODES, required=True)
    validate_parser.set_defaults(handler=vm_validate)
    deploy_parser = subparsers.add_parser("deploy")
    deploy_parser.add_argument("--profile", default="182")
    deploy_parser.add_argument("--commit", required=True)
    deploy_parser.add_argument("--mode", dest="deployment_mode", choices=DEPLOYMENT_MODES)
    deploy_parser.set_defaults(handler=deploy)
    release_parser = subparsers.add_parser("release")
    release_parser.add_argument("--profile", default="182")
    release_parser.add_argument("--gate", required=True)
    release_parser.set_defaults(handler=release)
    status_parser = subparsers.add_parser("status")
    status_parser.add_argument("release_id")
    status_parser.set_defaults(handler=status)
    logs_parser = subparsers.add_parser("logs")
    logs_parser.add_argument("release_id")
    logs_parser.add_argument("--node", choices=("local", "vm", "racknerd", "dmit", "backup", "all"), default="local")
    logs_parser.add_argument("--stage")
    logs_parser.add_argument("--level", choices=("debug", "info", "warn", "error"))
    logs_parser.add_argument("--tail", type=int, default=100)
    logs_parser.add_argument("--since", type=_parse_since)
    logs_parser.set_defaults(handler=logs)
    start_parser = subparsers.add_parser("deploy-start")
    start_parser.add_argument("--profile", default="182")
    start_parser.add_argument("--commit", required=True)
    start_parser.add_argument("--mode", dest="deployment_mode", choices=DEPLOYMENT_MODES)
    start_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["start"]).start(args))
    wait_parser = subparsers.add_parser("wait")
    wait_parser.add_argument("release_id")
    wait_parser.add_argument("--timeout", type=int, default=0)
    wait_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["wait"]).wait(args))
    verify_parser = subparsers.add_parser("verify-result")
    verify_parser.add_argument("release_id")
    verify_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["verify_result"]).verify_result(args))
    cleanup_parser = subparsers.add_parser("cleanup-production")
    cleanup_parser.add_argument("release_id")
    cleanup_parser.add_argument("--mode", choices=("dry-run", "apply"), default="dry-run")
    cleanup_parser.add_argument("--plan-sha256")
    cleanup_parser.set_defaults(handler=production_cleanup)
    retention_parser = subparsers.add_parser("retention")
    retention_parser.add_argument("--mode", choices=("dry-run", "apply"), default="dry-run")
    retention_parser.add_argument("--plan-sha256")
    retention_parser.add_argument("--success-retention-days", type=int, default=90)
    retention_parser.add_argument("--keep-recent", type=int, default=10)
    retention_parser.set_defaults(handler=retention)
    inspect_parser = subparsers.add_parser("reconcile-inspect")
    inspect_parser.add_argument("release_id")
    inspect_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["reconcile_inspect"]).reconcile_inspect(args))
    reconcile_parser = subparsers.add_parser("reconcile")
    reconcile_parser.add_argument("release_id")
    reconcile_parser.add_argument("--mode", choices=("recover", "coordinated-recover"), required=True)
    reconcile_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["reconcile"]).reconcile(args))
    worker_parser = subparsers.add_parser("_deploy-worker", help=argparse.SUPPRESS)
    worker_parser.add_argument("--profile", required=True)
    worker_parser.add_argument("--commit", required=True)
    worker_parser.add_argument("--release-id", required=True)
    worker_parser.add_argument("--mode", dest="deployment_mode", choices=DEPLOYMENT_MODES, required=True)
    worker_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["worker"]).worker(args))
    args = parser.parse_args()
    args.handler(args)
