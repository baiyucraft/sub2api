from __future__ import annotations

import base64
import hashlib
import json
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .manifest import release_asset_checksums, runner_checksum, sha256_file, validate_commit, validate_image_id
from .profiles import get_profile


def gate_payload(manifest: dict[str, Any], evidence: dict[str, Any]) -> bytes:
    value = {"manifest": manifest, "evidence": evidence}
    return canonical_json(value) + b"\n"


def verify_gate(bundle_dir: Path, public_key: Path, expected_profile: str) -> dict[str, Any]:
    payload_path = bundle_dir / "gate.json"
    signature_path = bundle_dir / "gate.sig"
    if not payload_path.is_file() or not signature_path.is_file():
        raise RuntimeError("gate bundle is incomplete")
    subprocess.run(
        ["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(public_key), "-rawin", "-in", str(payload_path), "-sigfile", str(signature_path)],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    document = json.loads(payload_path.read_text(encoding="utf-8"))
    manifest = document["manifest"]
    evidence = document["evidence"]
    validate_commit(manifest["commit_sha"])
    validate_image_id(evidence["candidate_image_id"])
    if manifest["profile"] != expected_profile:
        raise RuntimeError("gate profile does not match")
    profile = get_profile(expected_profile)
    if manifest.get("origin") != profile["origin"] or manifest.get("vm_identity") != profile["vm_identity"]:
        raise RuntimeError("gate origin or VM identity does not match")
    if manifest["runner_sha256"] != runner_checksum():
        raise RuntimeError("gate was created by a different release runner")
    if manifest.get("vm_validator_sha256") != sha256_file(Path(__file__).resolve().parent / "vm-validate.sh"):
        raise RuntimeError("gate was created by a different VM validator")
    if manifest.get("release_asset_sha256") != release_asset_checksums():
        raise RuntimeError("gate release assets do not match the current checkout")
    if int(manifest["expires_at"]) < int(time.time()):
        raise RuntimeError("gate has expired")
    if evidence.get("vm_restore_verified") is not True or evidence.get("integration_verified") is not True:
        raise RuntimeError("gate lacks VM restore or integration evidence")
    archive_path = bundle_dir / "candidate.tar.gz"
    if not archive_path.is_file():
        raise RuntimeError("gate candidate archive is missing")
    digest = hashlib.sha256()
    with archive_path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != evidence.get("candidate_archive_sha256"):
        raise RuntimeError("gate candidate archive checksum does not match")
    return document
