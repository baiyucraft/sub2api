from __future__ import annotations

import argparse
import json
from pathlib import Path
import shlex

from .gate import verify_gate
from .bootstrap import install_vm_validator
from .ssh import SSHRunner


ROOT = Path(__file__).resolve().parents[1]
TRUSTED_KEY = ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
SPACE_CLEANER = ROOT / "release" / "vm-space-clean.sh"
SPACE_FIELDS = {
    "cleanup_mode",
    "space_status",
    "free_bytes",
    "required_bytes",
    "container_candidates",
    "container_reclaimable_bytes",
    "image_candidates",
    "image_reclaimable_bytes",
    "removed_containers",
    "removed_images",
}


def ensure_vm_space(runner: SSHRunner, cleaner: str, commit: str) -> dict[str, str]:
    command = f"{shlex.quote(cleaner)} dry-run {shlex.quote(commit)}"
    report = runner.run("local_vm", command, SPACE_FIELDS).values
    if report["space_status"] == "sufficient":
        return report
    if report["space_status"] != "insufficient":
        raise RuntimeError("VM space cleaner returned an invalid status")
    runner.run(
        "local_vm",
        f"{shlex.quote(cleaner)} apply {shlex.quote(commit)}",
        SPACE_FIELDS,
        timeout=600,
    )
    verified = runner.run("local_vm", command, SPACE_FIELDS).values
    if verified["space_status"] != "sufficient":
        raise RuntimeError("VM disk space remains insufficient after one allowlisted cleanup")
    return verified


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    manifest_path = Path(args.manifest)
    output = Path(args.output)
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    runner = SSHRunner()
    install_vm_validator(runner)
    runner.run(
        "local_vm",
        "install -d -m 700 /opt/sub2api-deploy/release-input && test $(stat -c '%u:%a' /opt/sub2api-deploy/release-input) = $(id -u):700 && printf 'input_root_ready=true\\n'",
        {"input_root_ready"},
    )
    remote_root = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "validation")
    remote_manifest = f"{remote_root}/manifest.json"
    remote_cleaner = f"{remote_root}/vm-space-clean.sh"
    remote_output = f"/opt/sub2api-deploy/release-gates/{manifest['release_id']}/output"
    runner.upload("local_vm", manifest_path.read_bytes(), remote_manifest, 0o400)
    runner.upload_file("local_vm", SPACE_CLEANER, remote_cleaner, 0o700)
    try:
        cleaner_checksum = manifest["release_asset_sha256"]["deploy/release/vm-space-clean.sh"]
        runner.run(
            "local_vm",
            f"test $(sha256sum {shlex.quote(remote_cleaner)} | awk '{{print $1}}') = {shlex.quote(cleaner_checksum)} && printf 'space_cleaner_verified=true\\n'",
            {"space_cleaner_verified"},
        )
        ensure_vm_space(runner, remote_cleaner, manifest["commit_sha"])
        result = runner.run(
            "local_vm",
            f"test $(sha256sum /usr/local/libexec/sub2api-vm-validate | awk '{{print $1}}') = {manifest['vm_validator_sha256']} && /usr/local/libexec/sub2api-vm-validate {remote_manifest} {remote_output}",
            {"gate_status", "candidate_image_id", "candidate_archive_sha256"},
            timeout=7200,
        )
        download_dir = f"{remote_root}/download"
        runner.run(
            "local_vm",
            f"install -d -m 700 {download_dir} && for name in gate.json gate.sig candidate.tar.gz SHA256SUMS; do ln {remote_output}/$name {download_dir}/$name; done && printf 'download_ready=true\\n'",
            {"download_ready"},
        )
        output.mkdir(parents=True, exist_ok=True, mode=0o700)
        for name in ("gate.json", "gate.sig", "candidate.tar.gz", "SHA256SUMS"):
            runner.download_file("local_vm", f"{download_dir}/{name}", output / name)
    finally:
        runner.run("local_vm", f"rm -rf {remote_root} && printf 'input_removed=true\\n'", {"input_removed"})
    verify_gate(output, TRUSTED_KEY, manifest["profile"])
    if result.values["candidate_image_id"] != json.loads((output / "gate.json").read_text())["evidence"]["candidate_image_id"]:
        raise RuntimeError("VM output and signed gate image identities differ")


if __name__ == "__main__":
    main()
