from __future__ import annotations

import argparse
import json
import os
import secrets
import subprocess
import sys
import time
from pathlib import Path

from .bootstrap import bootstrap_trust
from .doctor import NODES, ReleaseDoctor
from .gate import verify_gate
from .manifest import create_manifest, write_manifest_once
from .profiles import get_profile
from .production_bootstrap import bootstrap_production
from .state import RunLock, RunState


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
WORKSPACE = Path(__file__).resolve().parents[2]
RUN_ROOT = WORKSPACE / ".tmp" / "releases"
TRUSTED_VM_PUBLIC_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"


def emit_progress(message: str) -> None:
    try:
        print(message, flush=True)
    except BrokenPipeError:
        pass


def release_id(profile: str, commit: str) -> str:
    return f"{profile}-{commit[:12]}-{int(time.time())}-{secrets.token_hex(4)}"


def create_vm_gate(profile_name: str, commit: str, identifier: str | None = None, acquire_lock: bool = True) -> Path:
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
        manifest = create_manifest(commit, profile, identifier)
        write_manifest_once(manifest_path, manifest)
    if gate_path.exists() or gate_path.is_symlink():
        raise RuntimeError("Gate output path already exists or is unsafe")
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    if state.value.get("schema") != 1 or state.value.get("release_id") != identifier:
        raise RuntimeError("release state identity does not match worker")
    emit_progress(f"release_id={identifier} stage=vm_validate status=running")
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        state.transition("vm_validate", "running")
        command = [sys.executable, "-m", "release.vm_validate", "--manifest", str(manifest_path), "--output", str(gate_path)]
        try:
            child_env = os.environ.copy()
            child_env["PYTHONUNBUFFERED"] = "1"
            subprocess.run(command, cwd=DEPLOY_ROOT, check=True, env=child_env)
            verify_gate(gate_path, TRUSTED_VM_PUBLIC_KEY, profile_name)
        except BaseException:
            state.transition("vm_validate", "failed")
            raise
        state.transition("vm_validate", "verified", {"gate_dir": str(gate_path)})
    finally:
        if lock:
            lock.__exit__(None, None, None)
    return gate_path


def vm_validate(args: argparse.Namespace) -> None:
    gate = create_vm_gate(args.profile, args.commit)
    print(f"gate={gate}")


def release(args: argparse.Namespace, acquire_lock: bool = True) -> None:
    gate_dir = Path(args.gate).resolve()
    document = verify_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, args.profile)
    identifier = document["manifest"]["release_id"]
    run_dir = RUN_ROOT / identifier
    state_path = run_dir / "release-state.json"
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        state.transition("production_release", "running")
        command = [sys.executable, "-m", "release.production", "--gate", str(gate_dir), "--profile", args.profile]
        try:
            child_env = os.environ.copy()
            child_env["PYTHONUNBUFFERED"] = "1"
            subprocess.run(command, cwd=DEPLOY_ROOT, check=True, env=child_env)
        except BaseException:
            result_path = gate_dir / "production-result.json"
            result = json.loads(result_path.read_text(encoding="utf-8")) if result_path.exists() else {}
            failure_status = result.get("status") if result.get("status") in {"failed", "recovered", "blocked_reconciliation"} else "blocked_reconciliation"
            state.transition("production_release", failure_status)
            raise
        state.transition("production_release", "verified")
    finally:
        if lock:
            lock.__exit__(None, None, None)


def deploy(args: argparse.Namespace, acquire_lock: bool = True) -> None:
    lock = RunLock(RUN_ROOT / ".release.lock") if acquire_lock else None
    if lock:
        lock.__enter__()
    try:
        emit_progress(f"release_profile={args.profile} release_commit={args.commit} stage=doctor status=running")
        doctor = ReleaseDoctor(args.profile, args.commit)
        doctor.run(("local", "vm", "dmit", "backup"))
        bootstrap_production(args.profile, doctor.runner)
        doctor.run(("racknerd",))
        identifier = getattr(args, "release_id", None)
        gate = create_vm_gate(args.profile, args.commit, identifier=identifier, acquire_lock=False)
        release(argparse.Namespace(gate=str(gate), profile=args.profile), acquire_lock=False)
    finally:
        if lock:
            lock.__exit__(None, None, None)
    emit_progress(f"release=verified gate={gate}")


def status(args: argparse.Namespace) -> None:
    from .supervisor import print_status

    print_status(args.release_id)


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
    validate_parser.set_defaults(handler=vm_validate)
    deploy_parser = subparsers.add_parser("deploy")
    deploy_parser.add_argument("--profile", default="182")
    deploy_parser.add_argument("--commit", required=True)
    deploy_parser.set_defaults(handler=deploy)
    release_parser = subparsers.add_parser("release")
    release_parser.add_argument("--profile", default="182")
    release_parser.add_argument("--gate", required=True)
    release_parser.set_defaults(handler=release)
    status_parser = subparsers.add_parser("status")
    status_parser.add_argument("release_id")
    status_parser.set_defaults(handler=status)
    start_parser = subparsers.add_parser("deploy-start")
    start_parser.add_argument("--profile", default="182")
    start_parser.add_argument("--commit", required=True)
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
    inspect_parser = subparsers.add_parser("reconcile-inspect")
    inspect_parser.add_argument("release_id")
    inspect_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["reconcile_inspect"]).reconcile_inspect(args))
    reconcile_parser = subparsers.add_parser("reconcile")
    reconcile_parser.add_argument("release_id")
    reconcile_parser.add_argument("--mode", choices=("recover",), required=True)
    reconcile_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["reconcile"]).reconcile(args))
    worker_parser = subparsers.add_parser("_deploy-worker", help=argparse.SUPPRESS)
    worker_parser.add_argument("--profile", required=True)
    worker_parser.add_argument("--commit", required=True)
    worker_parser.add_argument("--release-id", required=True)
    worker_parser.set_defaults(handler=lambda args: __import__("release.supervisor", fromlist=["worker"]).worker(args))
    args = parser.parse_args()
    args.handler(args)
