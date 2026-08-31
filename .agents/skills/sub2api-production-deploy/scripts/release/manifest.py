from __future__ import annotations

import hashlib
import json
import re
import subprocess
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .process import check_output_hidden
from .migration_planner import (
    catalog_sha256,
    checksum_policy_sha256,
    discover_migration_catalog,
)
from .paths import (
    LAYOUT_DEPLOY_V1,
    LAYOUT_SKILL_V1,
    LEGACY_UNIT_ROOT,
    LOGGING_PACKAGE_ROOT,
    MAINTENANCE_ROOT,
    RELEASE_PACKAGE_ROOT,
    SCRIPTS_ROOT,
    UNIT_ROOT,
    WINDOWS_ROOT,
    WORKSPACE,
    deploy_asset_path,
    skill_asset_path,
)
from .profiles import CURRENT_RELEASE_PROFILE


FULL_SHA = re.compile(r"^[0-9a-f]{40}$")
IMAGE_ID = re.compile(r"^sha256:[0-9a-f]{64}$")
RELEASE_ID = re.compile(r"^([0-9]+)-([0-9a-f]{12})-([0-9]+)-([0-9a-f]{8})$")
_GIT_PROCESS_INIT_FAILURES = frozenset({0xC0000142, -1073741502})
_GIT_READ_RETRY_DELAYS = (0, 1, 2)
_LEGACY_RELEASE_FILE = re.compile(r"^deploy/release/[^/]+$")
_LEGACY_RELEASE_NESTED_FILE = re.compile(r"^deploy/release/(?:trust|drverify)/[^/]+$")
_LEGACY_MAINTENANCE_FILE = re.compile(r"^deploy/maintenance/release/[^/]+$")
_SKILL_RELEASE_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/release/[^/]+$"
)
_SKILL_RELEASE_NESTED_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/release/(?:trust|drverify)/[^/]+$"
)
_SKILL_MAINTENANCE_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/maintenance/release/[^/]+$"
)
_SKILL_UNIT_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/maintenance/181/[^/]+$"
)
_SKILL_LOGGING_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/logging/release_logging/[^/]+$"
)
_SKILL_WINDOWS_FILE = re.compile(
    r"^\.agents/skills/sub2api-production-deploy/scripts/windows/[^/]+$"
)
_UNIT_ASSET_NAMES = ("mask-backup-units.sh", "restore-backup-units.sh")


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
    return WORKSPACE


def deploy_root() -> Path:
    return SCRIPTS_ROOT


def manifest_release_asset_layout(manifest: dict[str, Any]) -> str:
    """Resolve the immutable release asset layout.

    Historical manifests did not carry an explicit layout field and are the
    only accepted representation of deploy-v1.  New manifests must explicitly
    bind skill-v1, preventing callers from minting new deploy-v1 candidates.
    """
    value = manifest.get("release_asset_layout")
    if value is None:
        return LAYOUT_DEPLOY_V1
    if value != LAYOUT_SKILL_V1:
        raise RuntimeError("manifest release asset layout is invalid")
    return LAYOUT_SKILL_V1


def release_unit_relative_paths(layout: str) -> dict[str, str]:
    if layout == LAYOUT_DEPLOY_V1:
        prefix = "deploy/release"
    elif layout == LAYOUT_SKILL_V1:
        prefix = skill_asset_path("release")
    else:
        raise ValueError("unknown release asset layout")
    return {
        "validator": f"{prefix}/vm-validate.sh",
        "gate_signer": f"{prefix}/sign-gate.sh",
        "dr_signer": f"{prefix}/sign-dr-evidence.sh",
        "space_cleaner": f"{prefix}/vm-space-clean.sh",
    }


def release_control_relative_paths(layout: str) -> tuple[str, ...]:
    if layout == LAYOUT_DEPLOY_V1:
        prefix = "deploy/maintenance/181"
    elif layout == LAYOUT_SKILL_V1:
        prefix = skill_asset_path("maintenance/181")
    else:
        raise ValueError("unknown release asset layout")
    return tuple(f"{prefix}/{name}" for name in _UNIT_ASSET_NAMES)


def is_release_asset_relative_path(relative_path: str, layout: str) -> bool:
    if not relative_path or "\\" in relative_path or relative_path.startswith("/") or ".." in relative_path.split("/"):
        return False
    unit_assets = set(release_control_relative_paths(layout))
    if relative_path in unit_assets:
        return True
    if layout == LAYOUT_DEPLOY_V1:
        return (
            relative_path == "deploy/release.py"
            or _LEGACY_RELEASE_FILE.fullmatch(relative_path) is not None
            or _LEGACY_RELEASE_NESTED_FILE.fullmatch(relative_path) is not None
            or _LEGACY_MAINTENANCE_FILE.fullmatch(relative_path) is not None
        )
    if layout == LAYOUT_SKILL_V1:
        return (
            relative_path == skill_asset_path("release.py")
            or _SKILL_RELEASE_FILE.fullmatch(relative_path) is not None
            or _SKILL_RELEASE_NESTED_FILE.fullmatch(relative_path) is not None
            or _SKILL_MAINTENANCE_FILE.fullmatch(relative_path) is not None
            or _SKILL_UNIT_FILE.fullmatch(relative_path) is not None
            or _SKILL_LOGGING_FILE.fullmatch(relative_path) is not None
            or _SKILL_WINDOWS_FILE.fullmatch(relative_path) is not None
        )
    return False


def release_asset_pathspecs(layout: str) -> tuple[str, ...]:
    unit_assets = release_control_relative_paths(layout)
    if layout == LAYOUT_DEPLOY_V1:
        return (
            "deploy/release.py",
            "deploy/release",
            "deploy/maintenance/release",
            *unit_assets,
        )
    if layout == LAYOUT_SKILL_V1:
        return (
            skill_asset_path("release.py"),
            skill_asset_path("release"),
            skill_asset_path("maintenance/release"),
            skill_asset_path("logging/release_logging"),
            skill_asset_path("windows"),
            *unit_assets,
        )
    raise ValueError("unknown release asset layout")


def release_asset_relative_paths_from_commit(commit: str, layout: str) -> list[str]:
    commit = validate_commit(commit)
    output = _git_output(
        ["git", "ls-tree", "-r", "-z", "--name-only", commit, "--", *release_asset_pathspecs(layout)],
        cwd=workspace_root(),
    )
    assert isinstance(output, bytes)
    relative_paths = sorted(path.decode("utf-8") for path in output.split(b"\0") if path)
    invalid = [path for path in relative_paths if not is_release_asset_relative_path(path, layout)]
    if invalid:
        raise RuntimeError(f"release asset path is outside the {layout} allowlist: {invalid[0]}")
    required = {release_asset_pathspecs(layout)[0], *release_unit_relative_paths(layout).values()}
    missing = sorted(required.difference(relative_paths))
    if missing:
        raise RuntimeError(f"release asset is missing from commit: {missing[0]}")
    return relative_paths


def runner_checksum() -> str:
    files = release_asset_paths(LAYOUT_SKILL_V1)
    digest = hashlib.sha256()
    for path in files:
        digest.update(path.relative_to(workspace_root()).as_posix().encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def release_asset_paths(layout: str = LAYOUT_SKILL_V1) -> list[Path]:
    root = workspace_root()
    if layout != LAYOUT_SKILL_V1:
        raise ValueError("current runner assets must use skill-v1")
    candidates = [SCRIPTS_ROOT / "release.py"]
    candidates.extend(path for path in RELEASE_PACKAGE_ROOT.rglob("*") if path.is_file() and "__pycache__" not in path.parts)
    candidates.extend(path for path in MAINTENANCE_ROOT.rglob("*") if path.is_file())
    candidates.extend(path for path in LOGGING_PACKAGE_ROOT.rglob("*") if path.is_file() and "__pycache__" not in path.parts)
    candidates.extend(path for path in WINDOWS_ROOT.rglob("*") if path.is_file())
    candidates.extend(
        UNIT_ROOT / name
        for name in _UNIT_ASSET_NAMES
    )
    selected = sorted(candidates, key=lambda path: path.relative_to(root).as_posix())
    invalid = [path for path in selected if not is_release_asset_relative_path(path.relative_to(root).as_posix(), layout)]
    if invalid:
        raise RuntimeError(f"current release asset is outside the skill-v1 allowlist: {invalid[0].name}")
    return selected


def _git_output(args: list[str], *, cwd: Path, text: bool = False) -> bytes | str:
    """Read committed bytes while tolerating a transient Windows Git startup failure.

    ``0xC0000142`` is emitted by Windows before Git has executed any command
    logic (DLL/process initialization failure).  It is safe to retry this
    narrow class a few times; real Git errors such as a missing commit or path
    must still fail immediately and remain visible to the release gate.
    """
    for attempt, delay in enumerate(_GIT_READ_RETRY_DELAYS):
        if delay:
            time.sleep(delay)
        try:
            if text:
                return check_output_hidden(args, cwd=cwd, text=True)
            return check_output_hidden(args, cwd=cwd)
        except subprocess.CalledProcessError as error:
            if error.returncode not in _GIT_PROCESS_INIT_FAILURES or attempt == len(_GIT_READ_RETRY_DELAYS) - 1:
                raise
    raise AssertionError("unreachable git read retry state")


def git_blob_sha256(commit: str, relative_path: str) -> str:
    root = workspace_root()
    blob = _git_output(
        ["git", "show", f"{validate_commit(commit)}:{relative_path}"],
        cwd=root,
    )
    return hashlib.sha256(blob).hexdigest()


def release_asset_checksums(commit: str, layout: str = LAYOUT_SKILL_V1) -> dict[str, str]:
    relative_paths = release_asset_relative_paths_from_commit(commit, layout)
    return {relative_path: git_blob_sha256(commit, relative_path) for relative_path in relative_paths}


def migration_checksums(profile: dict[str, Any], commit: str | None = None) -> dict[str, str]:
    root = workspace_root()
    if commit is not None:
        commit = validate_commit(commit)
        return {
            name: hashlib.sha256(
                _git_output(
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
    schema = manifest.get("schema")
    if schema not in {1, 2}:
        raise RuntimeError("manifest schema does not match")
    layout = manifest_release_asset_layout(manifest)
    deployment_mode = manifest.get("deployment_mode")
    if layout == LAYOUT_SKILL_V1 and deployment_mode not in {"blue-green", "downtime"}:
        raise RuntimeError("manifest deployment mode is invalid")
    if layout == LAYOUT_DEPLOY_V1 and deployment_mode is not None and deployment_mode not in {"blue-green", "downtime"}:
        raise RuntimeError("historical manifest deployment mode is invalid")
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
    if schema == 2:
        if profile.get("gate_schema") != 2:
            raise RuntimeError("Gate v2 manifest requires the current release profile")
        catalog = discover_migration_catalog(workspace_root(), commit)
        if manifest.get("migration_catalog") != catalog:
            raise RuntimeError("manifest migration catalog does not match commit")
        if manifest.get("catalog_sha256") != catalog_sha256(catalog):
            raise RuntimeError("manifest migration catalog checksum does not match")
        if manifest.get("checksum_policy_sha256") != checksum_policy_sha256():
            raise RuntimeError("manifest checksum policy does not match runner")
        bound_fields = ("production_current_image_id", "production_snapshot_sha256")
        if any(manifest.get(field) is not None for field in bound_fields) and not all(manifest.get(field) is not None for field in bound_fields):
            raise RuntimeError("manifest production snapshot binding is incomplete")
        if manifest.get("production_current_image_id") is not None:
            validate_image_id(str(manifest["production_current_image_id"]))
            if not re.fullmatch(r"[0-9a-f]{64}", str(manifest["production_snapshot_sha256"])):
                raise RuntimeError("manifest production snapshot checksum is invalid")
        if manifest.get("parent_profile") != profile.get("parent"):
            raise RuntimeError("manifest parent profile does not match")
        if manifest.get("new_migrations") != profile.get("new_migrations"):
            raise RuntimeError("manifest new migrations do not match profile")
        if manifest.get("release_policy") != profile.get("release_policy"):
            raise RuntimeError("manifest release policy does not match profile")
        if any(field in manifest for field in ("migrations", "migration_sha256", "compatibility_version", "compatibility_commit", "compatibility_image_id")):
            raise RuntimeError("Gate v2 manifest contains a legacy migration contract")
        return
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


def create_manifest(commit: str, profile: dict[str, Any], release_id: str, deployment_mode: str, production_current_image_id: str | None = None, production_snapshot_sha256: str | None = None) -> dict[str, Any]:
    commit = validate_commit(commit)
    if deployment_mode not in {"blue-green", "downtime"}:
        raise ValueError("deployment mode must be blue-green or downtime")
    root = workspace_root()
    origin = check_output_hidden(["git", "remote", "get-url", "origin"], cwd=root, text=True).strip()
    if origin != profile["origin"]:
        raise RuntimeError("local origin does not match the release profile")
    resolved = check_output_hidden(["git", "rev-parse", commit], cwd=root, text=True).strip()
    if resolved != commit:
        raise RuntimeError("commit is not available in the local repository")
    layout = LAYOUT_SKILL_V1
    asset_checksums = release_asset_checksums(commit, layout)
    release_units = release_unit_relative_paths(layout)
    schema = int(profile.get("gate_schema", 1))
    manifest = {
        "schema": schema,
        "release_asset_layout": layout,
        "deployment_mode": deployment_mode,
        "release_id": release_id,
        "created_at": int(time.time()),
        "expires_at": int(time.time()) + int(profile["gate_ttl_seconds"]),
        "commit_sha": commit,
        "origin": origin,
        "profile": profile["name"],
        "version": profile["version"],
        "runner_sha256": runner_checksum(),
        "vm_validator_sha256": asset_checksums[release_units["validator"]],
        "vm_gate_signer_sha256": asset_checksums[release_units["gate_signer"]],
        "vm_dr_signer_sha256": asset_checksums[release_units["dr_signer"]],
        "release_asset_sha256": asset_checksums,
        "vm_identity": profile["vm_identity"],
    }
    if schema == 2:
        if production_current_image_id is not None:
            validate_image_id(production_current_image_id)
        catalog = discover_migration_catalog(root, commit)
        manifest.update({
            "parent_profile": profile.get("parent"),
            "new_migrations": list(profile.get("new_migrations", [])),
            "release_policy": dict(profile.get("release_policy", {})),
            "migration_catalog": catalog,
            "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(),
        })
        if production_current_image_id is not None:
            manifest["production_current_image_id"] = production_current_image_id
        if production_snapshot_sha256 is not None:
            if not re.fullmatch(r"[0-9a-f]{64}", production_snapshot_sha256):
                raise ValueError("production snapshot checksum is invalid")
            manifest["production_snapshot_sha256"] = production_snapshot_sha256
    else:
        manifest.update({
            "migration_sha256": migration_checksums(profile, commit),
            "migrations": list(profile["migrations"]),
        })
        for key in ("compatibility_version", "compatibility_commit", "compatibility_image_id"):
            if key in profile:
                manifest[key] = profile[key]
    validate_manifest_profile_contract(manifest, profile)
    return manifest


def bind_production_snapshot(manifest: dict[str, Any], image_id: str, snapshot_sha256: str) -> dict[str, Any]:
    """Bind the point-in-time production baseline before VM Gate starts."""
    if manifest.get("schema") != 2 or manifest.get("profile") != CURRENT_RELEASE_PROFILE:
        raise RuntimeError("production snapshot binding requires Gate v2")
    validate_image_id(image_id)
    if not re.fullmatch(r"[0-9a-f]{64}", snapshot_sha256):
        raise ValueError("production snapshot checksum is invalid")
    value = dict(manifest)
    for field, incoming in (("production_current_image_id", image_id), ("production_snapshot_sha256", snapshot_sha256)):
        current = value.get(field)
        if current is not None and current != incoming:
            raise RuntimeError(f"manifest {field} already bound to a different value")
        value[field] = incoming
    return value


def write_manifest_once(path: Path, manifest: dict[str, Any]) -> None:
    if path.exists():
        existing = json.loads(path.read_text(encoding="utf-8"))
        if canonical_json(existing) != canonical_json(manifest):
            raise RuntimeError("immutable manifest already exists with different content")
        return
    atomic_write(path, canonical_json(manifest) + b"\n", 0o400)
