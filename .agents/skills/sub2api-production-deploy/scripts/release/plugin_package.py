from __future__ import annotations

import base64
import hashlib
import json
import re
import shutil
import tempfile
import zipfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml
from cryptography.hazmat.primitives import serialization

from .paths import PLUGIN_RUN_ROOT, WORKSPACE
from .process import run_hidden


PLUGIN_ID = "baiyu.codex-state"
PLUGIN_DIR_NAME = "codex-state"
PLUGIN_SIGNING_KEY_ID = "baiyu-codex-state-v1"
PLUGIN_SIGNING_PUBLIC_KEY_BASE64 = "C8XBc7JNYAxANwijdCjy0A54r5n2WfTSeQGSJIgD18M="
SSH_CONFIG = WORKSPACE / ".ssh.local"
SUPPORTED_ARCHES = ("amd64", "arm64")
FULL_SHA = re.compile(r"^[0-9a-f]{40}$")
HEX_SHA256 = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class PackageIdentity:
    path: Path
    arch: str
    version: str
    package_sha256: str
    binary_sha256: str
    key_id: str
    signature_status: str


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def validate_source_commit(value: str) -> str:
    if not FULL_SHA.fullmatch(value):
        raise ValueError("plugin source commit must be a full lowercase 40-character SHA")
    result = run_hidden(
        ["git", "rev-parse", "--verify", f"{value}^{{commit}}"],
        cwd=WORKSPACE,
        capture_output=True,
        text=True,
        check=True,
    ).stdout.strip().lower()
    if result != value:
        raise RuntimeError("plugin source commit does not resolve exactly")
    return value


def resolve_signing_config() -> tuple[Path, str]:
    document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
    signing = document.get("plugin_signing") if isinstance(document, dict) else None
    if not isinstance(signing, dict):
        raise RuntimeError("plugin_signing is missing from .ssh.local")
    raw_path = signing.get("private_key")
    key_id = str(signing.get("key_id") or "").strip()
    if not isinstance(raw_path, str) or not raw_path.strip() or not key_id:
        raise RuntimeError("plugin signing key path and key_id are required")
    key_path = Path(raw_path).expanduser().resolve(strict=True)
    workspace = WORKSPACE.resolve()
    run_root = PLUGIN_RUN_ROOT.resolve(strict=False)
    if key_path == workspace or workspace in key_path.parents or key_path == run_root or run_root in key_path.parents:
        raise RuntimeError("plugin signing key must be outside the workspace and release output")
    if not key_path.is_file() or key_path.is_symlink():
        raise RuntimeError("plugin signing key is not a regular file")
    return key_path, key_id


def _read_archive(path: Path, expected_arch: str, public_key) -> PackageIdentity:
    if not path.is_file() or path.is_symlink():
        raise RuntimeError("plugin package is not a regular file")
    with zipfile.ZipFile(path) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)) or any(name.startswith("/") or ".." in Path(name).parts or "\\" in name for name in names):
            raise RuntimeError("plugin package contains unsafe members")
        manifest_raw = archive.read("manifest.json")
        signature_raw = archive.read("signature.json")
        manifest = json.loads(manifest_raw)
        signature = json.loads(signature_raw)
        if manifest.get("id") != PLUGIN_ID:
            raise RuntimeError("unexpected plugin ID")
        runtime_path = ((manifest.get("runtimes") or {}).get(f"linux-{expected_arch}") or {}).get("path")
        if not isinstance(runtime_path, str) or runtime_path not in names:
            raise RuntimeError("plugin runtime architecture is missing")
        files = manifest.get("files")
        if not isinstance(files, dict):
            raise RuntimeError("plugin file hash map is missing")
        for name, expected in files.items():
            if not isinstance(name, str) or not isinstance(expected, str) or not HEX_SHA256.fullmatch(expected):
                raise RuntimeError("plugin file hash entry is invalid")
            if hashlib.sha256(archive.read(name)).hexdigest() != expected:
                raise RuntimeError("plugin file checksum mismatch")
        if signature.get("algorithm") != "ed25519" or not signature.get("key_id"):
            raise RuntimeError("plugin signature metadata is invalid")
        try:
            public_key.verify(base64.b64decode(signature["signature"], validate=True), manifest_raw)
        except Exception as error:
            raise RuntimeError("plugin package signature verification failed") from error
        binary_sha = hashlib.sha256(archive.read(runtime_path)).hexdigest()
        return PackageIdentity(
            path=path,
            arch=expected_arch,
            version=str(manifest.get("version") or ""),
            package_sha256=sha256_file(path),
            binary_sha256=binary_sha,
            key_id=str(signature["key_id"]),
            signature_status="trusted",
        )


def build_packages(source_commit: str, output_dir: Path) -> dict[str, PackageIdentity]:
    validate_source_commit(source_commit)
    key_path, key_id = resolve_signing_config()
    output_dir.mkdir(parents=True, exist_ok=False, mode=0o700)
    with key_path.open("rb") as source:
        private_key = serialization.load_pem_private_key(source.read(), password=None)
    public_key = private_key.public_key()
    public_raw = public_key.public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
    if key_id != PLUGIN_SIGNING_KEY_ID or base64.b64encode(public_raw).decode("ascii") != PLUGIN_SIGNING_PUBLIC_KEY_BASE64:
        raise RuntimeError("plugin signing identity does not match the built-in trusted publisher")
    with tempfile.TemporaryDirectory(prefix="sub2api-plugin-worktree-") as temporary:
        checkout = Path(temporary) / "source"
        run_hidden(["git", "worktree", "add", "--detach", str(checkout), source_commit], cwd=WORKSPACE, check=True)
        try:
            plugin_root = checkout / "plugins" / PLUGIN_DIR_NAME
            if not plugin_root.is_dir():
                raise RuntimeError("plugin source directory is missing at the selected commit")
            ui_root = plugin_root / "ui"
            run_hidden(["corepack", "pnpm", "install", "--frozen-lockfile"], cwd=ui_root, check=True)
            run_hidden(["corepack", "pnpm", "run", "build"], cwd=ui_root, check=True)
            run_hidden(["go", "test", "-p", "2", "./..."], cwd=plugin_root, check=True)
            run_hidden(
                [
                    "go", "run", "./tools/package", "--out", str(output_dir),
                    "--signing-key", str(key_path), "--key-id", key_id,
                    "--arches", ",".join(SUPPORTED_ARCHES),
                ],
                cwd=plugin_root,
                check=True,
            )
        finally:
            run_hidden(["git", "worktree", "remove", "--force", str(checkout)], cwd=WORKSPACE, check=False)
    identities: dict[str, PackageIdentity] = {}
    for arch in SUPPORTED_ARCHES:
        matches = list(output_dir.glob(f"{PLUGIN_ID}-*-linux-{arch}.s2plugin"))
        if len(matches) != 1:
            raise RuntimeError(f"expected exactly one linux-{arch} plugin package")
        identity = _read_archive(matches[0], arch, public_key)
        if identity.key_id != key_id:
            raise RuntimeError("plugin signature key ID mismatch")
        identities[arch] = identity
    if len({item.version for item in identities.values()}) != 1:
        raise RuntimeError("plugin package versions differ across architectures")
    sums = output_dir / "SHA256SUMS"
    declared: dict[str, str] = {}
    for line in sums.read_text(encoding="ascii").splitlines():
        digest, name = line.split("  ", 1)
        declared[name] = digest
    for identity in identities.values():
        if declared.get(identity.path.name) != identity.package_sha256:
            raise RuntimeError("SHA256SUMS does not match plugin package")
    return identities


def package_manifest(identities: dict[str, PackageIdentity]) -> dict[str, Any]:
    first = identities[SUPPORTED_ARCHES[0]]
    return {
        "plugin_id": PLUGIN_ID,
        "version": first.version,
        "signature_key_id": first.key_id,
        "arches": {
            arch: {
                "file": item.path.name,
                "package_sha256": item.package_sha256,
                "binary_sha256": item.binary_sha256,
                "signature_status": item.signature_status,
            }
            for arch, item in sorted(identities.items())
        },
    }


def find_previous_package(version: str, arch: str, *, exclude_release_id: str | None = None) -> Path | None:
    if arch not in SUPPORTED_ARCHES:
        raise ValueError("unsupported plugin architecture")
    if not PLUGIN_RUN_ROOT.exists():
        return None
    candidates: list[Path] = []
    pattern = f"{PLUGIN_ID}-{version}-linux-{arch}.s2plugin"
    for run_dir in PLUGIN_RUN_ROOT.iterdir():
        if exclude_release_id and run_dir.name == exclude_release_id:
            continue
        candidate = run_dir / "artifacts" / pattern
        if candidate.is_file() and not candidate.is_symlink():
            candidates.append(candidate)
    return max(candidates, key=lambda item: item.stat().st_mtime, default=None)


def find_previous_identity(
    version: str,
    arch: str,
    *,
    binary_sha256: str | None = None,
    exclude_release_id: str | None = None,
) -> PackageIdentity | None:
    if binary_sha256 is not None and not HEX_SHA256.fullmatch(binary_sha256):
        raise ValueError("invalid archived plugin binary SHA-256")
    candidates = find_previous_identities(arch, exclude_release_id=exclude_release_id)
    return next(
        (
            identity
            for identity in candidates
            if identity.version == version
            and (binary_sha256 is None or identity.binary_sha256 == binary_sha256)
        ),
        None,
    )


def find_previous_identities(
    arch: str,
    *,
    exclude_release_id: str | None = None,
    limit: int = 10,
) -> list[PackageIdentity]:
    if arch not in SUPPORTED_ARCHES:
        raise ValueError("unsupported plugin architecture")
    if limit < 1 or limit > 100:
        raise ValueError("invalid archived plugin package limit")
    if not PLUGIN_RUN_ROOT.exists():
        return []
    key_path, configured_key_id = resolve_signing_config()
    with key_path.open("rb") as source:
        public_key = serialization.load_pem_private_key(source.read(), password=None).public_key()
    candidates: list[tuple[float, PackageIdentity]] = []
    for run_dir in PLUGIN_RUN_ROOT.iterdir():
        if exclude_release_id and run_dir.name == exclude_release_id:
            continue
        package_json = run_dir / "package.json"
        if not package_json.is_file() or package_json.is_symlink():
            continue
        try:
            package = json.loads(package_json.read_text(encoding="utf-8"))
            if package.get("plugin_id") != PLUGIN_ID:
                continue
            details = package["arches"][arch]
            path = run_dir / "artifacts" / details["file"]
            identity = _read_archive(path, arch, public_key)
            if (
                identity.version != package.get("version")
                or identity.package_sha256 != details.get("package_sha256")
                or identity.binary_sha256 != details.get("binary_sha256")
                or identity.key_id != package.get("signature_key_id")
                or identity.key_id != configured_key_id
                or identity.signature_status != "trusted"
                or details.get("signature_status") != "trusted"
            ):
                continue
            candidates.append((path.stat().st_mtime, identity))
        except (KeyError, TypeError, ValueError, json.JSONDecodeError, OSError):
            continue
        except Exception:
            continue
    candidates.sort(key=lambda item: item[0], reverse=True)
    unique: list[PackageIdentity] = []
    seen: set[tuple[str, str]] = set()
    for _, identity in candidates:
        key = (identity.version, identity.binary_sha256)
        if key in seen:
            continue
        seen.add(key)
        unique.append(identity)
        if len(unique) >= limit:
            break
    return unique
