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
RELEASE_ID = re.compile(r"^([0-9]+)-([0-9a-f]{12})-([0-9]+)-([0-9a-f]{8})$")


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


def git_blob_sha256(commit: str, relative_path: str) -> str:
    root = workspace_root()
    blob = subprocess.check_output(
        ["git", "show", f"{validate_commit(commit)}:{relative_path}"],
        cwd=root,
    )
    return hashlib.sha256(blob).hexdigest()


def release_asset_checksums(commit: str) -> dict[str, str]:
    root = workspace_root()
    relative_paths = [path.relative_to(root).as_posix() for path in release_asset_paths()]
    return {relative_path: git_blob_sha256(commit, relative_path) for relative_path in relative_paths}


def migration_checksums(profile: dict[str, Any], commit: str | None = None) -> dict[str, str]:
    root = workspace_root()
    if commit is not None:
        commit = validate_commit(commit)
        return {
            name: hashlib.sha256(
                subprocess.check_output(
                    ["git", "show", f"{commit}:backend/migrations/{name}"],
                    cwd=root,
                ).decode("utf-8").strip().encode()
            ).hexdigest()
            for name in profile["migrations"]
        }
    return {
        name: hashlib.sha256((root / "backend" / "migrations" / name).read_text(encoding="utf-8").strip().encode()).hexdigest()
        for name in profile["migrations"]
    }


def validate_manifest_profile_contract(manifest: dict[str, Any], profile: dict[str, Any]) -> None:
    if manifest.get("schema") != 1:
        raise RuntimeError("manifest schema does not match")
    commit = validate_commit(str(manifest.get("commit_sha", "")))
    match = RELEASE_ID.fullmatch(str(manifest.get("release_id", "")))
    if match is None or match.group(1) != profile["name"] or match.group(2) != commit[:12]:
        raise RuntimeError("manifest release ID does not match profile and commit")
    if manifest.get("profile") != profile["name"]:
        raise RuntimeError("manifest profile does not match")
    if manifest.get("version") != profile["version"]:
        raise RuntimeError("manifest version does not match")
    if manifest.get("origin") != profile["origin"] or manifest.get("vm_identity") != profile["vm_identity"]:
        raise RuntimeError("manifest origin or VM identity does not match")
    if manifest.get("migrations") != list(profile["migrations"]):
        raise RuntimeError("manifest ordered migrations do not match")
    if manifest.get("migration_sha256") != migration_checksums(profile, commit):
        raise RuntimeError("manifest migration checksums do not match commit")
    compatibility_fields = ("compatibility_version", "compatibility_commit", "compatibility_image_id")
    expected_compatibility = any(field in profile for field in compatibility_fields)
    present_compatibility = any(field in manifest for field in compatibility_fields)
    if expected_compatibility != present_compatibility:
        raise RuntimeError("manifest compatibility identity presence does not match profile")
    if expected_compatibility:
        if not all(manifest.get(field) == profile.get(field) for field in compatibility_fields):
            raise RuntimeError("manifest compatibility identity does not match profile")
        validate_commit(str(manifest["compatibility_commit"]))
        validate_image_id(str(manifest["compatibility_image_id"]))


def create_manifest(commit: str, profile: dict[str, Any], release_id: str) -> dict[str, Any]:
    commit = validate_commit(commit)
    root = workspace_root()
    origin = subprocess.check_output(["git", "remote", "get-url", "origin"], cwd=root, text=True).strip()
    if origin != profile["origin"]:
        raise RuntimeError("local origin does not match the release profile")
    resolved = subprocess.check_output(["git", "rev-parse", commit], cwd=root, text=True).strip()
    if resolved != commit:
        raise RuntimeError("commit is not available in the local repository")
    manifest = {
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
        "vm_gate_signer_sha256": sha256_file(deploy_root() / "release" / "sign-gate.sh"),
        "vm_dr_signer_sha256": sha256_file(deploy_root() / "release" / "sign-dr-evidence.sh"),
        "release_asset_sha256": release_asset_checksums(commit),
        "migration_sha256": migration_checksums(profile, commit),
        "migrations": list(profile["migrations"]),
        "vm_identity": profile["vm_identity"],
    }
    for key in ("compatibility_version", "compatibility_commit", "compatibility_image_id"):
        if key in profile:
            manifest[key] = profile[key]
    validate_manifest_profile_contract(manifest, profile)
    return manifest


def write_manifest_once(path: Path, manifest: dict[str, Any]) -> None:
    if path.exists():
        existing = json.loads(path.read_text(encoding="utf-8"))
        if canonical_json(existing) != canonical_json(manifest):
            raise RuntimeError("immutable manifest already exists with different content")
        return
    atomic_write(path, canonical_json(manifest) + b"\n", 0o400)
