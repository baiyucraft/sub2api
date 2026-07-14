from __future__ import annotations

import argparse
import json
import os
import secrets
import subprocess
import sys
import time
from pathlib import Path

from .atomic import atomic_write, canonical_json
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


def release_id(profile: str, commit: str) -> str:
    return f"{profile}-{commit[:12]}-{int(time.time())}-{secrets.token_hex(4)}"


def create_vm_gate(profile_name: str, commit: str) -> Path:
    profile = get_profile(profile_name)
    identifier = release_id(profile_name, commit)
    run_dir = RUN_ROOT / identifier
    run_dir.mkdir(parents=True, mode=0o700)
    manifest = create_manifest(commit, profile, identifier)
    write_manifest_once(run_dir / "manifest.json", manifest)
    state = RunState.create(run_dir / "state.json", identifier)
    with RunLock(RUN_ROOT / ".release.lock"):
        state.transition("vm_validate", "running")
        command = [sys.executable, "-m", "release.vm_validate", "--manifest", str(run_dir / "manifest.json"), "--output", str(run_dir / "gate")]
        try:
            subprocess.run(command, cwd=DEPLOY_ROOT, check=True)
            verify_gate(run_dir / "gate", TRUSTED_VM_PUBLIC_KEY, profile_name)
        except BaseException:
            state.transition("vm_validate", "failed")
            raise
        state.transition("vm_validate", "verified", {"gate_dir": str(run_dir / "gate")})
    return run_dir / "gate"


def vm_validate(args: argparse.Namespace) -> None:
    gate = create_vm_gate(args.profile, args.commit)
    print(f"gate={gate}")


def release(args: argparse.Namespace) -> None:
    gate_dir = Path(args.gate).resolve()
    document = verify_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, args.profile)
    identifier = document["manifest"]["release_id"]
    run_dir = RUN_ROOT / identifier
    state_path = run_dir / "release-state.json"
    state = RunState.load(state_path) if state_path.exists() else RunState.create(state_path, identifier)
    with RunLock(RUN_ROOT / ".release.lock"):
        state.transition("production_release", "running")
        command = [sys.executable, "-m", "release.production", "--gate", str(gate_dir), "--profile", args.profile]
        try:
            subprocess.run(command, cwd=DEPLOY_ROOT, check=True)
        except BaseException:
            result_path = gate_dir / "production-result.json"
            result = json.loads(result_path.read_text(encoding="utf-8")) if result_path.exists() else {}
            failure_status = result.get("status") if result.get("status") in {"failed", "recovered", "blocked_reconciliation"} else "blocked_reconciliation"
            state.transition("production_release", failure_status)
            raise
        state.transition("production_release", "verified")


def deploy(args: argparse.Namespace) -> None:
    doctor = ReleaseDoctor(args.profile, args.commit)
    doctor.run(("local", "vm", "dmit", "backup"))
    bootstrap_production(args.profile, doctor.runner)
    doctor.run(("racknerd",))
    gate = create_vm_gate(args.profile, args.commit)
    release(argparse.Namespace(gate=str(gate), profile=args.profile))
    print(f"release=verified gate={gate}")


def status(args: argparse.Namespace) -> None:
    path = RUN_ROOT / args.release_id
    for name in ("state.json", "release-state.json"):
        candidate = path / name
        if candidate.exists():
            print(candidate.read_text(encoding="utf-8").strip())


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
    args = parser.parse_args()
    args.handler(args)
