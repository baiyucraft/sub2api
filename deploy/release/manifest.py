from __future__ import annotations

import hashlib
import json
import re
import subprocess
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json


FULL_SHA = re.compile(r"^[0-9a-f]{40}$")
IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate_commit(commit: str) -> str:
    if not FULL_SHA.fullmatch(commit):
        raise ValueError("commit must be a complete 40-character lowercase SHA")
    return commit


def validate_image_id(image_id: str) -> str:
    if not IMAGE_ID.fullmatch(image_id):
        raise ValueError("candidate image ID is invalid")
    return image_id


def workspace_root() -> Path:
    return Path(__file__).resolve().parents[2]


def deploy_root() -> Path:
    return Path(__file__).resolve().parents[1]


def runner_checksum() -> str:
    files = release_asset_paths()
    digest = hashlib.sha256()
    for path in files:
        digest.update(path.relative_to(workspace_root()).as_posix().encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def release_asset_paths() -> list[Path]:
    root = workspace_root()
    candidates = [root / "deploy" / "release.py"]
    candidates.extend(path for path in (root / "deploy" / "release").rglob("*") if path.is_file() and "__pycache__" not in path.parts)
    candidates.extend(path for path in (root / "deploy" / "maintenance" / "release").rglob("*") if path.is_file())
    candidates.extend(
        root / "deploy" / "maintenance" / "181" / name
        for name in ("mask-backup-units.sh", "restore-backup-units.sh")
    )
    return sorted(candidates, key=lambda path: path.relative_to(root).as_posix())


def release_asset_checksums() -> dict[str, str]:
    root = workspace_root()
    return {path.relative_to(root).as_posix(): sha256_file(path) for path in release_asset_paths()}


def migration_checksums(profile: dict[str, Any]) -> dict[str, str]:
    root = workspace_root()
    return {
        name: hashlib.sha256((root / "backend" / "migrations" / name).read_text(encoding="utf-8").strip().encode()).hexdigest()
        for name in profile["migrations"]
    }


def create_manifest(commit: str, profile: dict[str, Any], release_id: str) -> dict[str, Any]:
    commit = validate_commit(commit)
    root = workspace_root()
    origin = subprocess.check_output(["git", "remote", "get-url", "origin"], cwd=root, text=True).strip()
    if origin != profile["origin"]:
        raise RuntimeError("local origin does not match the release profile")
    resolved = subprocess.check_output(["git", "rev-parse", commit], cwd=root, text=True).strip()
    if resolved != commit:
        raise RuntimeError("commit is not available in the local repository")
    return {
        "schema": 1,
        "release_id": release_id,
        "created_at": int(time.time()),
        "expires_at": int(time.time()) + int(profile["gate_ttl_seconds"]),
        "commit_sha": commit,
        "origin": origin,
        "profile": profile["name"],
        "version": profile["version"],
        "runner_sha256": runner_checksum(),
        "vm_validator_sha256": sha256_file(deploy_root() / "release" / "vm-validate.sh"),
        "release_asset_sha256": release_asset_checksums(),
        "migration_sha256": migration_checksums(profile),
        "migrations": list(profile["migrations"]),
        "vm_identity": profile["vm_identity"],
    }


def write_manifest_once(path: Path, manifest: dict[str, Any]) -> None:
    if path.exists():
        existing = json.loads(path.read_text(encoding="utf-8"))
        if canonical_json(existing) != canonical_json(manifest):
            raise RuntimeError("immutable manifest already exists with different content")
        return
    atomic_write(path, canonical_json(manifest) + b"\n", 0o400)
