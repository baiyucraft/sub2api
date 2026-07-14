from __future__ import annotations

import secrets
from pathlib import Path

from .manifest import sha256_file
from .ssh import SSHRunner


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
TRUSTED_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
VALIDATOR = DEPLOY_ROOT / "release" / "vm-validate.sh"
BOOTSTRAP = DEPLOY_ROOT / "release" / "bootstrap_vm_signer.sh"


def bootstrap_trust() -> None:
    runner = SSHRunner()
    runner.run(
        "local_vm",
        "install -d -m 700 /opt/sub2api-deploy/release-input && test $(stat -c '%u:%a' /opt/sub2api-deploy/release-input) = $(id -u):700 && printf 'input_root_ready=true\\n'",
        {"input_root_ready"},
    )
    remote_dir = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "bootstrap")
    remote_validator = f"{remote_dir}/validator"
    remote_bootstrap = f"{remote_dir}/bootstrap"
    runner.upload_file("local_vm", VALIDATOR, remote_validator, 0o700)
    runner.upload_file("local_vm", BOOTSTRAP, remote_bootstrap, 0o700)
    try:
        values = runner.run(
            "local_vm",
            f"VALIDATOR_SOURCE={remote_validator} {remote_bootstrap}",
            {"signer_status", "public_key_sha256", "validator_sha256"},
        ).values
        if values["validator_sha256"] != sha256_file(VALIDATOR):
            raise RuntimeError("installed VM validator checksum differs")
        review_dir = DEPLOY_ROOT.parent / ".tmp" / "release-trust"
        review_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        downloaded = review_dir / "vm-gate-ed25519.pub"
        if downloaded.exists():
            downloaded.unlink()
        try:
            signer_copy = f"{remote_dir}/vm-gate-ed25519.pub"
            runner.run("local_vm", f"install -m 400 /opt/sub2api-release-signer/vm-gate-ed25519.pub {signer_copy} && printf 'public_key_staged=true\\n'", {"public_key_staged"})
            runner.download_file("local_vm", signer_copy, downloaded)
            if sha256_file(downloaded) != values["public_key_sha256"]:
                raise RuntimeError("downloaded VM public key checksum differs")
            if not TRUSTED_KEY.is_file():
                raise RuntimeError(f"review VM public key at {downloaded} and add it to {TRUSTED_KEY} before installing RackNerd trust")
            if TRUSTED_KEY.read_bytes() != downloaded.read_bytes():
                raise RuntimeError("repository trust key differs from VM signer public key")
            runner.run(
                "racknerd",
                "install -d -m 700 /opt/sub2api/releases && test $(stat -c '%u:%a' /opt/sub2api/releases) = $(id -u):700 && printf 'release_root_ready=true\\n'",
                {"release_root_ready"},
            )
            rack_temp = runner.create_temp_dir("racknerd", "/opt/sub2api/releases", "trust-bootstrap")
            remote_key = f"{rack_temp}/vm-gate-ed25519.pub"
            runner.upload_file("racknerd", TRUSTED_KEY, remote_key, 0o400)
            try:
                installed = runner.run(
                    "racknerd",
                    f"install -d -m 755 /opt/sub2api-release-trust && if test -e /opt/sub2api-release-trust/vm-gate-ed25519.pub; then test $(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}') = {values['public_key_sha256']}; else install -o root -g root -m 644 {remote_key} /opt/sub2api-release-trust/vm-gate-ed25519.pub; fi && printf 'rack_trust_sha256=%s\\n' $(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}')",
                    {"rack_trust_sha256"},
                ).values
            finally:
                runner.run("racknerd", f"rm -rf {rack_temp} && printf 'cleanup=true\\n'", {"cleanup"})
            if installed["rack_trust_sha256"] != values["public_key_sha256"]:
                raise RuntimeError("RackNerd trust key checksum differs")
            downloaded.unlink()
        except BaseException:
            raise
    finally:
        runner.run("local_vm", f"rm -rf {remote_dir} && printf 'cleanup=true\\n'", {"cleanup"})
