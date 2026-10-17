"""Measure a signed candidate archive load inside a private offline Guest."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import time
from typing import Any

if __package__:
    from .full_recovery_source import assert_isolated_guest, _regular_private, _sha_file, _private_output
    from .process import run_hidden
else:
    from full_recovery_source import assert_isolated_guest, _regular_private, _sha_file, _private_output
    from process import run_hidden


def need(ok: bool, message: str) -> None:
    if not ok:
        raise RuntimeError("baseline candidate load rejected: " + message)


def collect(*, archive: Path, gate: Path, signature: Path, trust: Path, expected_trust_sha256: str,
            proof: dict[str, Any], drill_id: str, release_id: str, output: Path) -> dict[str, Any]:
    """Load exactly the Gate-bound archive; no application container is run.

    A postrelease Guest task invokes this before its isolated runtime is
    destroyed. The controller must independently approve the actual receipt
    checksum in the full recovery review's findings. This helper signs nothing.
    """
    need(re.fullmatch(r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", drill_id) is not None, "drill identity")
    need(re.fullmatch(r"(?:270|271|272|273)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}", release_id) is not None, "release identity")
    need(isinstance(proof, dict) and set(proof) >= {"host_namespaces", "rootfs_identity_sha256", "host_docker_engine_id"}, "host proof missing")
    assert_isolated_guest(proof["host_namespaces"])
    need(re.fullmatch(r"[0-9a-f]{64}", str(proof["rootfs_identity_sha256"])) is not None, "rootfs identity missing")
    need(isinstance(proof["host_docker_engine_id"], str) and bool(proof["host_docker_engine_id"].strip()), "host Engine missing")
    _private_output(output)
    for path, maximum in ((archive, 8 * 1024 ** 3), (gate, 4 * 1024 ** 2), (signature, 1024), (trust, 4096)):
        info = _regular_private(path, maximum=maximum)
        need(os.name == "nt" or info.st_uid == 0, "root-owned candidate input required")
    need(re.fullmatch(r"[0-9a-f]{64}", expected_trust_sha256) is not None and _sha_file(trust) == expected_trust_sha256, "existing trust root differs")
    verified = run_hidden(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(trust), "-rawin", "-in", str(gate), "-sigfile", str(signature)], capture_output=True, timeout=30)
    need(verified.returncode == 0, "Gate signature verification")
    def unique(pairs):
        value = {}
        for key, item in pairs:
            need(key not in value, "duplicate Gate JSON field"); value[key] = item
        return value
    document = json.loads(gate.read_bytes(), object_pairs_hook=unique)
    need(isinstance(document, dict) and isinstance(document.get("manifest"), dict) and isinstance(document.get("evidence"), dict), "Gate fields")
    manifest, evidence = document["manifest"], document["evidence"]
    need(type(manifest.get("schema")) is int and manifest["schema"] == 2 and manifest.get("release_id") == release_id and manifest.get("profile") == release_id.split("-",1)[0], "Gate release identity")
    expected_image = evidence.get("candidate_image_id")
    need(isinstance(expected_image, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", expected_image) is not None, "candidate image identity")
    archive_digest = _sha_file(archive)
    need(archive_digest == evidence.get("candidate_archive_sha256"), "candidate archive differs from signed Gate")
    engine = run_hidden(["docker", "info", "--format", "{{.ID}}"], capture_output=True, timeout=30)
    need(engine.returncode == 0 and engine.stdout.strip() and engine.stdout.decode().strip() != proof["host_docker_engine_id"], "private Docker Engine observation")
    root = run_hidden(["docker", "info", "--format", "{{.DockerRootDir}}"], capture_output=True, timeout=30)
    need(root.returncode == 0 and root.stdout.strip() == b"/var/lib/docker", "Guest Docker root")
    # Docker auto-detects gzip for --input. Suppress its raw image/tag output.
    loaded = run_hidden(["docker", "load", "--input", str(archive)], capture_output=True, timeout=1800)
    need(loaded.returncode == 0, "actual candidate archive load failed")
    observed = run_hidden(["docker", "image", "inspect", "--format", "{{.Id}}", expected_image], capture_output=True, timeout=30)
    need(observed.returncode == 0 and observed.stdout.decode().strip() == expected_image, "loaded candidate image differs")
    need(_sha_file(archive) == archive_digest, "candidate archive changed during loading")
    receipt = {"schema": 1, "type": "sub2api-baseline-candidate-image-load", "drill_id": drill_id, "release_id": release_id,
               "candidate_archive_sha256": archive_digest, "candidate_image_id": expected_image, "observed_image_id": expected_image,
               "guest_docker_engine_id_sha256": hashlib.sha256(engine.stdout.strip()).hexdigest(), "rootfs_identity_sha256": proof["rootfs_identity_sha256"],
               "load_exit_code": loaded.returncode, "created_at": int(time.time())}
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o400)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(json.dumps(receipt, sort_keys=True, separators=(",", ":")).encode()); stream.flush(); os.fsync(stream.fileno())
    return receipt


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("archive", "gate", "signature", "trust", "proof", "output"):
        parser.add_argument("--" + name, required=True, type=Path)
    for name in ("expected-trust-sha256", "drill-id", "release-id"):
        parser.add_argument("--" + name, required=True)
    args = vars(parser.parse_args()); args["proof"] = json.loads(args["proof"].read_bytes())
    receipt = collect(**args)
    print(json.dumps({"status": "measured", "candidate_archive_sha256": receipt["candidate_archive_sha256"], "candidate_image_id": receipt["candidate_image_id"]}, sort_keys=True))


if __name__ == "__main__":
    main()
