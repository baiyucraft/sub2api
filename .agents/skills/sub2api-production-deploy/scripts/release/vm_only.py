from __future__ import annotations

import argparse
import hashlib
import secrets
import shlex
import subprocess
import time
from pathlib import Path

from .atomic import canonical_json
from .bootstrap import install_vm_validator
from .gate import verify_vm_only_gate
from .manifest import create_vm_only_manifest, sha256_file, validate_commit
from .paths import RELEASE_PACKAGE_ROOT, RUN_ROOT, TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .profiles import CURRENT_RELEASE_PROFILE, get_profile
from .ssh import SSHRunner


VM_ONLY_VALIDATE = RELEASE_PACKAGE_ROOT / "vm-only-validate.sh"
VM_ONLY_SWITCH = RELEASE_PACKAGE_ROOT / "vm-only-switch.sh"


def _git_output(*args: str) -> str:
    return subprocess.check_output(["git", *args], cwd=WORKSPACE, text=True, stderr=subprocess.DEVNULL).strip()


def _source_archive_sha256(commit: str) -> str:
    process = subprocess.Popen(
        ["git", "archive", "--format=tar", commit],
        cwd=WORKSPACE,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
    )
    digest = hashlib.sha256()
    assert process.stdout is not None
    for chunk in iter(lambda: process.stdout.read(1024 * 1024), b""):
        digest.update(chunk)
    if process.wait() != 0:
        raise RuntimeError("unable to create VM-only source archive digest")
    return digest.hexdigest()


def _assert_local_release(commit: str, profile_name: str) -> dict:
    validate_commit(commit)
    if profile_name != CURRENT_RELEASE_PROFILE:
        raise RuntimeError("VM-only release only accepts the current profile")
    if _git_output("status", "--porcelain"):
        raise RuntimeError("tracked workspace changes must be committed before VM-only deployment")
    if _git_output("rev-parse", "HEAD") != commit:
        raise RuntimeError("VM-only deployment commit must be the checked-out HEAD")
    profile = get_profile(profile_name)
    if _git_output("remote", "get-url", "origin") != profile["origin"]:
        raise RuntimeError("local origin does not match the release profile")
    return profile


def _release_id(profile: str, commit: str) -> str:
    return f"{profile}-{commit[:12]}-{int(time.time())}-{secrets.token_hex(4)}"


def vm_only_validate(args: argparse.Namespace, *, runner: SSHRunner | None = None) -> Path:
    profile = _assert_local_release(args.commit, args.profile)
    identifier = _release_id(args.profile, args.commit)
    run_dir = RUN_ROOT / identifier
    gate_dir = run_dir / "vm-only-gate"
    if run_dir.exists() or run_dir.is_symlink():
        raise RuntimeError("VM-only release directory already exists")
    run_dir.mkdir(parents=True, mode=0o700)
    gate_dir.mkdir(mode=0o700)
    manifest = create_vm_only_manifest(
        args.commit,
        profile,
        identifier,
        _source_archive_sha256(args.commit),
        sha256_file(VM_ONLY_VALIDATE),
        sha256_file(VM_ONLY_SWITCH),
    )
    manifest_path = run_dir / "manifest.json"
    manifest_path.write_bytes(canonical_json(manifest) + b"\n")
    manifest_path.chmod(0o400)
    ssh = runner or SSHRunner()
    install_vm_validator(ssh)
    remote_root = ssh.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "vm-only")
    remote_manifest = f"{remote_root}/manifest.json"
    remote_validator = f"{remote_root}/vm-only-validate.sh"
    remote_output = f"/opt/sub2api-deploy/release-gates/{identifier}/output"
    try:
        ssh.upload_file("local_vm", manifest_path, remote_manifest, 0o400)
        ssh.upload_file("local_vm", VM_ONLY_VALIDATE, remote_validator, 0o700)
        ssh.run(
            "local_vm",
            f"{shlex.quote(remote_validator)} {shlex.quote(remote_manifest)} {shlex.quote(remote_output)}",
            {"candidate_image_id", "old_image_id", "existing_app_health", "candidate_health"},
            timeout=7200,
        )
        download_dir = f"{remote_root}/download"
        ssh.run(
            "local_vm",
            f"install -d -m 700 {shlex.quote(download_dir)} && for name in gate.json gate.sig candidate.tar.gz SHA256SUMS; do ln {shlex.quote(remote_output)}/$name {shlex.quote(download_dir)}/$name; done && printf 'download_ready=true\\n'",
            {"download_ready"},
        )
        for name in ("gate.json", "gate.sig", "candidate.tar.gz", "SHA256SUMS"):
            ssh.download_file("local_vm", f"{download_dir}/{name}", gate_dir / name)
    finally:
        ssh.run("local_vm", f"rm -rf -- {shlex.quote(remote_root)} && printf 'input_removed=true\\n'", {"input_removed"})
    verify_vm_only_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, args.profile)
    return gate_dir


def vm_only_switch(args: argparse.Namespace, *, runner: SSHRunner | None = None) -> None:
    gate_dir = Path(args.gate).resolve()
    document = verify_vm_only_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, args.profile)
    manifest = document["manifest"]
    identifier = manifest["release_id"]
    ssh = runner or SSHRunner()
    remote_root = ssh.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "vm-switch")
    remote_gate = f"/opt/sub2api-deploy/release-gates/{identifier}/output"
    remote_archive = f"{remote_root}/candidate.tar.gz"
    remote_switch = f"{remote_root}/vm-only-switch.sh"
    try:
        ssh.upload_file("local_vm", gate_dir / "candidate.tar.gz", remote_archive, 0o400)
        ssh.upload_file("local_vm", VM_ONLY_SWITCH, remote_switch, 0o700)
        result = ssh.run(
            "local_vm",
            f"{shlex.quote(remote_switch)} {shlex.quote(remote_gate)} {shlex.quote(remote_archive)} {shlex.quote(document['evidence']['candidate_image_id'])} {shlex.quote(identifier)}",
            {"switch", "old_image_id", "candidate_image_id", "port", "health", "page"},
            timeout=300,
        ).values
        if result["switch"] != "verified" or result["port"] != "8211" or result["health"] != "200" or result["page"] != "200":
            raise RuntimeError("VM-only switch did not satisfy the 8211 acceptance contract")
    finally:
        ssh.run("local_vm", f"rm -rf -- {shlex.quote(remote_root)} && printf 'input_removed=true\\n'", {"input_removed"})


def main() -> None:
    parser = argparse.ArgumentParser(description="Isolated local VM release path")
    subparsers = parser.add_subparsers(required=True)
    validate = subparsers.add_parser("validate")
    validate.add_argument("--profile", default=CURRENT_RELEASE_PROFILE)
    validate.add_argument("--commit", required=True)
    validate.set_defaults(handler=vm_only_validate)
    switch = subparsers.add_parser("switch")
    switch.add_argument("--profile", default=CURRENT_RELEASE_PROFILE)
    switch.add_argument("--gate", required=True)
    switch.set_defaults(handler=vm_only_switch)
    args = parser.parse_args()
    result = args.handler(args)
    if isinstance(result, Path):
        print(f"gate={result}")


if __name__ == "__main__":
    main()
