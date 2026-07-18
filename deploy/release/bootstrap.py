from __future__ import annotations

import shlex
import secrets
from pathlib import Path

from .manifest import sha256_file
from .ssh import SSHRunner


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
TRUSTED_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
VALIDATOR = DEPLOY_ROOT / "release" / "vm-validate.sh"
GATE_SIGNER = DEPLOY_ROOT / "release" / "sign-gate.sh"
DR_SIGNER = DEPLOY_ROOT / "release" / "sign-dr-evidence.sh"
BOOTSTRAP = DEPLOY_ROOT / "release" / "bootstrap_vm_signer.sh"


SIGNER_FIELDS = {"signer_status", "public_key_sha256", "validator_sha256", "gate_signer_sha256", "dr_signer_sha256"}


def validate_installed_unit(values: dict[str, str]) -> None:
    expected = {
        "validator_sha256": sha256_file(VALIDATOR),
        "gate_signer_sha256": sha256_file(GATE_SIGNER),
        "dr_signer_sha256": sha256_file(DR_SIGNER),
    }
    if any(values[field] != checksum for field, checksum in expected.items()):
        raise RuntimeError("installed VM release unit checksum differs")
    if values["public_key_sha256"] != sha256_file(TRUSTED_KEY):
        raise RuntimeError("VM signer public key differs from repository trust key")


def bootstrap_command(remote_validator: str, remote_gate_signer: str, remote_dr_signer: str, remote_bootstrap: str, require_existing: bool) -> str:
    prefix = "REQUIRE_EXISTING_SIGNER_KEYS=true " if require_existing else ""
    return (
        f"{prefix}VALIDATOR_SOURCE={shlex.quote(remote_validator)} GATE_SIGNER_SOURCE={shlex.quote(remote_gate_signer)} DR_SIGNER_SOURCE={shlex.quote(remote_dr_signer)} "
        f"VALIDATOR_SHA256={sha256_file(VALIDATOR)} GATE_SIGNER_SHA256={sha256_file(GATE_SIGNER)} DR_SIGNER_SHA256={sha256_file(DR_SIGNER)} {shlex.quote(remote_bootstrap)}"
    )


def prepare_vm_host(runner: SSHRunner) -> None:
    runner.run(
        "local_vm",
        "if command -v jq >/dev/null 2>&1; then status=present; else command -v apt-get >/dev/null 2>&1 && command -v timeout >/dev/null 2>&1 && export DEBIAN_FRONTEND=noninteractive && timeout 300 apt-get update -qq >/dev/null 2>&1 && timeout 300 apt-get install -y --no-install-recommends --no-upgrade jq >/dev/null 2>&1 && status=installed; fi && command -v jq >/dev/null 2>&1 && printf 'jq_status=%s\\n' \"$status\" && printf 'jq_version=%s\\n' \"$(jq --version)\"",
        {"jq_status", "jq_version"},
        timeout=660,
    )


def install_vm_validator(runner: SSHRunner) -> None:
    prepare_vm_host(runner)
    runner.run(
        "local_vm",
        "install -d -m 700 /opt/sub2api-deploy/release-input && test $(stat -c '%u:%a' /opt/sub2api-deploy/release-input) = $(id -u):700 && printf 'input_root_ready=true\\n'",
        {"input_root_ready"},
    )
    remote_dir = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "validator")
    remote_validator = f"{remote_dir}/validator"
    remote_gate_signer = f"{remote_dir}/gate-signer"
    remote_dr_signer = f"{remote_dir}/dr-signer"
    remote_bootstrap = f"{remote_dir}/bootstrap"
    runner.upload_file("local_vm", VALIDATOR, remote_validator, 0o700)
    runner.upload_file("local_vm", GATE_SIGNER, remote_gate_signer, 0o700)
    runner.upload_file("local_vm", DR_SIGNER, remote_dr_signer, 0o700)
    runner.upload_file("local_vm", BOOTSTRAP, remote_bootstrap, 0o700)
    try:
        values = runner.run(
            "local_vm",
            bootstrap_command(remote_validator, remote_gate_signer, remote_dr_signer, remote_bootstrap, True),
            SIGNER_FIELDS,
        ).values
        validate_installed_unit(values)
    finally:
        runner.run("local_vm", f"rm -rf -- {shlex.quote(remote_dir)} && printf 'cleanup=true\\n'", {"cleanup"})


def bootstrap_trust() -> None:
    runner = SSHRunner()
    prepare_vm_host(runner)
    runner.run(
        "local_vm",
        "install -d -m 700 /opt/sub2api-deploy/release-input && test $(stat -c '%u:%a' /opt/sub2api-deploy/release-input) = $(id -u):700 && printf 'input_root_ready=true\\n'",
        {"input_root_ready"},
    )
    remote_dir = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "bootstrap")
    remote_validator = f"{remote_dir}/validator"
    remote_gate_signer = f"{remote_dir}/gate-signer"
    remote_dr_signer = f"{remote_dir}/dr-signer"
    remote_bootstrap = f"{remote_dir}/bootstrap"
    runner.upload_file("local_vm", VALIDATOR, remote_validator, 0o700)
    runner.upload_file("local_vm", GATE_SIGNER, remote_gate_signer, 0o700)
    runner.upload_file("local_vm", DR_SIGNER, remote_dr_signer, 0o700)
    runner.upload_file("local_vm", BOOTSTRAP, remote_bootstrap, 0o700)
    try:
        values = runner.run(
            "local_vm",
            bootstrap_command(remote_validator, remote_gate_signer, remote_dr_signer, remote_bootstrap, False),
            SIGNER_FIELDS,
        ).values
        expected_unit = {
            "validator_sha256": sha256_file(VALIDATOR),
            "gate_signer_sha256": sha256_file(GATE_SIGNER),
            "dr_signer_sha256": sha256_file(DR_SIGNER),
        }
        if any(values[field] != checksum for field, checksum in expected_unit.items()):
            raise RuntimeError("installed VM release unit checksum differs")
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
        runner.run("local_vm", f"rm -rf -- {shlex.quote(remote_dir)} && printf 'cleanup=true\\n'", {"cleanup"})
