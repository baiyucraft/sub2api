"""验证完整灾备演练使用的加密生产基线来源。

此模块只处理来源资产：它不会创建生产备份、上传文件、修改生产状态，
也不会签发 Gate。调用方应在隔离 Guest 内调用 :func:`decrypt_source`，
再把返回的脱敏摘要绑定到独立审阅证据。明文 tar 只能放在受控的临时目录。
"""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import tarfile
from typing import Any, Mapping

if __package__:
    from .process import run_hidden
else:
    from process import run_hidden


SHA = re.compile(r"[0-9a-f]{64}\Z")
IMAGE = re.compile(r"sha256:[0-9a-f]{64}\Z")
RELEASE = re.compile(r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}\Z")
_ARCHIVE_TOP = {"database", "redis", "config", "metadata", "SHA256SUMS"}
_REQUIRED_FILES = {
    "database/sub2api.dump",
    "database/globals.sql",
    "redis/dump.rdb",
    "metadata/manifest.txt",
    "SHA256SUMS",
}
_EXPECTED_FIELDS = {"artifact_sha256", "artifact_bytes", "approved_identity_sha256", "approved_recipient_sha256", "recipient_type", "release_id", "pre_image_id"}
NAMESPACES = ("mnt", "pid", "net", "user", "cgroup", "uts", "ipc")


def _checksum(value: Any) -> str:
    raw = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()
    return hashlib.sha256(raw).hexdigest()


def _fail(message: str) -> None:
    raise RuntimeError("full recovery source rejected: " + message)


def _require(condition: bool, message: str) -> None:
    if not condition:
        _fail(message)


def _sha_file(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        before = _regular_private(path, maximum=8 * 1024 * 1024 * 1024)
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(fd, "rb") as stream:
            after = os.fstat(stream.fileno())
            _require((before.st_dev, before.st_ino) == (after.st_dev, after.st_ino) and after.st_nlink == 1, "source changed during hashing")
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
            _require(stream.tell() == before.st_size and os.fstat(stream.fileno()).st_mtime_ns == before.st_mtime_ns, "source changed during hashing")
    except OSError as error:
        raise RuntimeError("full recovery source rejected: source unavailable") from error
    return digest.hexdigest()


def _regular_private(path: Path, *, maximum: int = 64 * 1024 * 1024) -> os.stat_result:
    """Require an unchanged regular single-link file (never follow symlinks)."""
    try:
        before = path.lstat()
        _require(path.resolve() == path.absolute(), "noncanonical source path")
        _require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1, "regular single-link file required")
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(descriptor, "rb") as stream:
            after = os.fstat(stream.fileno())
            _require((before.st_dev, before.st_ino) == (after.st_dev, after.st_ino), "source changed during open")
            _require(after.st_size <= maximum, "source file exceeds bounded size")
        if os.name != "nt":
            _require(stat.S_IMODE(after.st_mode) in {0o400, 0o600}, "private source mode required")
        return after
    except OSError as error:
        raise RuntimeError("full recovery source rejected: source unavailable") from error


def _private_output(path: Path) -> None:
    parent = path.parent
    _require(parent.is_dir() and not parent.is_symlink(), "private output directory required")
    _require(parent.resolve() == parent.absolute(), "noncanonical output directory")
    if os.name != "nt":
        info = parent.stat()
        _require(stat.S_IMODE(info.st_mode) == 0o700 and info.st_uid == os.getuid(), "private output directory mode required")
    _require(not path.exists() and not path.is_symlink(), "output already exists")


def _safe_member(name: str) -> str:
    _require(isinstance(name, str) and name and "\\" not in name and ":" not in name and all(ord(c) >= 32 and ord(c) != 127 for c in name), "invalid tar member name")
    if name.startswith("./"):
        name = name[2:]
    value = PurePosixPath(name)
    _require(not value.is_absolute() and all(part not in {"", ".", ".."} for part in value.parts), "tar path escapes extraction root")
    normalized = "/".join(value.parts)
    _require(normalized == name.rstrip("/") and not name.endswith("//"), "non-canonical tar member name")
    _require(len(normalized) <= 512, "tar member name too long")
    _require(value.parts and value.parts[0] in _ARCHIVE_TOP, "unknown archive root")
    return normalized


def _manifest(data: bytes) -> dict[str, str]:
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError as error:
        raise RuntimeError("full recovery source rejected: manifest encoding") from error
    values: dict[str, str] = {}
    for line in text.splitlines():
        if not line:
            continue
        _require("=" in line, "malformed manifest")
        key, value = line.split("=", 1)
        _require(re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,63}", key) is not None and key not in values, "manifest field")
        _require(value and "\x00" not in value and "\n" not in value, "manifest value")
        values[key] = value
    _require(set(values) >= {"release_id", "pre_switch_image_id"}, "manifest identity missing")
    return values


def inspect_archive(archive: Path, *, release_id: str, pre_image_id: str) -> dict[str, Any]:
    """Inspect and hash a decrypted release tar without extracting it.

    Tar links, devices, FIFOs, duplicate members, path traversal and oversized
    archives are rejected. The returned fields contain only SHA-256 digests and
    counts; no credentials or archive content is returned.
    """
    _require(RELEASE.fullmatch(release_id) is not None, "invalid release identity")
    _require(IMAGE.fullmatch(pre_image_id) is not None, "invalid pre-image identity")
    _regular_private(archive, maximum=8 * 1024 * 1024 * 1024)
    archive_digest = _sha_file(archive)
    names: set[str] = set()
    files: dict[str, tuple[int, str]] = {}
    total_size = 0
    manifest: dict[str, str] | None = None
    checksum_bytes: bytes | None = None
    try:
        with tarfile.open(archive, mode="r:") as bundle:
            for member in bundle:
                if member.name in {".", "./"}:
                    _require(member.isdir() and "." not in names and member.uid == 0 and member.gid == 0 and member.mode & 0o7022 == 0, "unsafe archive root")
                    names.add(".")
                    continue
                name = _safe_member(member.name)
                _require(name not in names, "duplicate tar member")
                names.add(name)
                _require(len(names) <= 100000, "tar member count limit")
                _require(member.isreg() or member.isdir(), "tar links, devices, and special files are forbidden")
                # The formal backup preserves application-owned data files.
                # Extraction never applies archive ownership, so ownership is
                # recorded as metadata while permissions remain fail-closed.
                _require(type(member.uid) is int and type(member.gid) is int and 0 <= member.uid <= 65535 and 0 <= member.gid <= 65535, "tar ownership metadata invalid")
                _require((member.uid == 0 and member.gid == 0) or name == "redis/dump.rdb" or name == "config/app/data" or name.startswith("config/app/data/"), "root ownership required for configuration metadata")
                _require(member.sparse is None and not any(key.startswith("GNU.sparse") for key in member.pax_headers), "sparse tar member forbidden")
                _require((member.mode & 0o7022) == 0, "tar member permissions are unsafe")
                if member.isdir():
                    _require(member.mode & 0o700 == 0o700, "tar directory owner access required")
                    continue
                _require((member.mode & 0o111 == 0 or name.startswith("config/app/data/")) and member.mode & 0o400 == 0o400, "tar regular file permissions are unsafe")
                _require(type(member.size) is int and 0 <= member.size <= 8 * 1024 * 1024 * 1024, "tar member size limit")
                total_size += member.size
                _require(total_size <= 16 * 1024 * 1024 * 1024, "tar expands beyond size limit")
                stream = bundle.extractfile(member)
                _require(stream is not None, "tar regular member unavailable")
                data_digest = hashlib.sha256()
                remaining = member.size
                while remaining:
                    block = stream.read(min(1024 * 1024, remaining))
                    _require(block, "short tar member")
                    remaining -= len(block)
                    data_digest.update(block)
                files[name] = (member.size, data_digest.hexdigest())
                if name == "metadata/manifest.txt":
                    stream = bundle.extractfile(member)
                    _require(stream is not None and member.size <= 64 * 1024, "manifest unavailable")
                    manifest = _manifest(stream.read())
                if name == "SHA256SUMS":
                    _require(member.size <= 32 * 1024 * 1024, "checksum inventory size limit")
                    stream = bundle.extractfile(member)
                    _require(stream is not None, "checksum inventory unavailable")
                    checksum_bytes = stream.read()
    except (tarfile.TarError, OSError) as error:
        raise RuntimeError("full recovery source rejected: invalid decrypted tar") from error
    _require(manifest is not None, "manifest missing")
    _require(manifest.get("release_id") == release_id, "archive release identity differs")
    _require(manifest.get("pre_switch_image_id") == pre_image_id, "archive old image differs")
    _require(_REQUIRED_FILES <= set(files), "required recovery files missing")
    _require(files["database/sub2api.dump"][0] > 0 and files["redis/dump.rdb"][0] > 0, "empty database or Redis source")
    # A regular file may never also be used as a parent directory, independent
    # of member order in the tar. Manual extraction does not reinterpret links.
    for name in names - {"."}:
        parent = PurePosixPath(name).parent
        while str(parent) != ".":
            _require(str(parent) not in files, "tar file used as parent directory")
            parent = parent.parent
    _require(checksum_bytes is not None, "checksum inventory missing")
    try:
        checksum_text = checksum_bytes.decode("utf-8")
    except UnicodeDecodeError as error:
        raise RuntimeError("full recovery source rejected: checksum inventory encoding") from error
    checksums: dict[str, str] = {}
    for line in checksum_text.splitlines():
        _require(len(line) > 66 and SHA.fullmatch(line[:64]) is not None and line[64:66] == "  ", "malformed checksum inventory")
        name = _safe_member(line[66:])
        _require(name != "SHA256SUMS" and name not in checksums, "duplicate or recursive checksum inventory")
        checksums[name] = line[:64]
    _require(set(checksums) == set(files) - {"SHA256SUMS"} and all(checksums[name] == files[name][1] for name in checksums), "archive checksum closure differs")
    # A path under config is required for the formal restore helper. Empty
    # marker-only configuration archives are not accepted as a production base.
    _require(any(name.startswith("config/app/") for name in files), "application compose snapshot missing")
    _require(any(name.startswith("config/nginx/") for name in files), "nginx snapshot missing")
    _require(_sha_file(archive) == archive_digest, "archive changed during inventory")
    return {
        "recovery_point_sha256": archive_digest,
        "postgres_dump_sha256": files["database/sub2api.dump"][1],
        "redis_rdb_sha256": files["redis/dump.rdb"][1],
        "manifest_sha256": files["metadata/manifest.txt"][1],
        "member_count": len(names),
        "expanded_bytes": total_size,
    }


def extract_archive(archive: Path, destination: Path, *, release_id: str, pre_image_id: str) -> dict[str, Any]:
    """Extract validated regular members into a new private owned directory.

    No tarfile extraction API is used: ownership, links and permission bits
    from the archive cannot redirect writes. The tar identity is reverified
    after extraction before the caller may use the result.
    """
    result = inspect_archive(archive, release_id=release_id, pre_image_id=pre_image_id)
    _private_output(destination)
    destination.mkdir(mode=0o700)
    with tarfile.open(archive, mode="r:") as bundle:
        directories: list[tuple[Path, int]] = []
        for member in bundle:
            if member.name in {".", "./"}:
                continue
            name = _safe_member(member.name)
            _require(member.isdir() or member.isreg(), "archive changed before extraction")
            target = destination.joinpath(*PurePosixPath(name).parts)
            _require(target.resolve().is_relative_to(destination.resolve()), "extraction root differs")
            for parent in reversed(target.parents):
                if parent == destination or parent.is_relative_to(destination):
                    _require(not parent.is_symlink(), "extraction parent link")
                    parent.mkdir(mode=0o700, exist_ok=True)
            if member.isdir():
                target.mkdir(mode=0o700, exist_ok=True)
                directories.append((target, member.mode))
                continue
            descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
            with os.fdopen(descriptor, "wb") as output:
                stream = bundle.extractfile(member)
                _require(stream is not None, "extraction member unavailable")
                remaining = member.size
                while remaining:
                    block = stream.read(min(1024 * 1024, remaining))
                    _require(block, "short extracted member")
                    output.write(block)
                    remaining -= len(block)
                output.flush()
                os.fsync(output.fileno())
            os.chmod(target, member.mode)
        for directory, mode in sorted(directories, key=lambda item: len(item[0].parts), reverse=True):
            os.chmod(directory, mode)
    _require(_sha_file(archive) == result["recovery_point_sha256"], "archive changed during extraction")
    return result


def _identity_recipient(identity: Path, *, age_keygen: str = "age-keygen") -> str:
    _regular_private(identity, maximum=16 * 1024)
    try:
        result = run_hidden([age_keygen, "-y", str(identity)], capture_output=True, timeout=15)
        _require(result.returncode == 0, "identity recipient derivation failed")
        raw = result.stdout
    except Exception as error:
        raise RuntimeError("full recovery source rejected: identity recipient derivation failed") from error
    recipient = raw.decode("ascii").strip() if isinstance(raw, bytes) else str(raw).strip()
    _require(re.fullmatch(r"age1[0-9a-z]{10,120}", recipient) is not None, "unsupported age recipient")
    return recipient


def assert_isolated_guest(host_namespaces: Mapping[str, str]) -> None:
    """Measure isolation here before permitting any production decryption."""
    _require(os.name == "posix" and Path("/proc/self/ns").is_dir() and os.geteuid() == 0, "root Linux Guest required")
    _require(isinstance(host_namespaces, dict) and set(host_namespaces) == set(NAMESPACES), "host namespace proof required")
    for name in NAMESPACES:
        expected = host_namespaces[name]
        _require(isinstance(expected, str) and re.fullmatch(name + r":\[[0-9]+\]", expected) is not None, "host namespace proof malformed")
        _require(os.readlink("/proc/self/ns/" + name) != expected, "decryption attempted in host namespace")
    mapping = Path("/proc/self/uid_map").read_text().split()
    _require(len(mapping) == 3 and all(part.isdigit() for part in mapping) and int(mapping[0]) == 0 and int(mapping[1]) >= 65536 and int(mapping[2]) >= 65536, "private UID mapping required")
    for command in (["ip", "-j", "route", "show", "table", "all"], ["ip", "-j", "-6", "route", "show", "table", "all"]):
        result = run_hidden(command, capture_output=True, timeout=15)
        _require(result.returncode == 0, "Guest route inspection failed")
        try:
            routes = json.loads(result.stdout)
        except (ValueError, UnicodeDecodeError) as error:
            raise RuntimeError("full recovery source rejected: Guest route proof malformed") from error
        _require(isinstance(routes, list) and all(isinstance(route, dict) and route.get("dst") not in {"default", "0.0.0.0/0", "::/0"} for route in routes), "Guest external default route exists")
    result = run_hidden(["ip", "-j", "link", "show"], capture_output=True, timeout=15)
    _require(result.returncode == 0, "Guest interface inspection failed")
    try:
        links = json.loads(result.stdout)
    except (ValueError, UnicodeDecodeError) as error:
        raise RuntimeError("full recovery source rejected: Guest interface proof malformed") from error
    _require(isinstance(links, list) and all(isinstance(link, dict) and re.fullmatch(r"(?:lo|docker[0-9]+|br-[0-9a-f]+|veth[A-Za-z0-9]+)", str(link.get("ifname", ""))) is not None for link in links), "host-facing Guest interface exists")


def decrypt_source(
    artifact: Path,
    identity: Path,
    output_tar: Path,
    *,
    expected: dict[str, Any],
    host_namespaces: dict[str, str],
    age_command: str = "age",
    keygen_command: str = "age-keygen",
) -> dict[str, Any]:
    """Decrypt, verify, and inventory one encrypted recovery source.

    Identity bytes are never returned. Fingerprints are SHA-256 only; callers
    should place the identity in an isolated Guest and destroy it after use.
    """
    _require(isinstance(expected, dict) and set(expected) == _EXPECTED_FIELDS, "source approval fields")
    _require(all(isinstance(expected[key], str) and SHA.fullmatch(expected[key]) is not None for key in ("artifact_sha256", "approved_identity_sha256", "approved_recipient_sha256")), "source approval digests")
    _require(type(expected["artifact_bytes"]) is int and 0 < expected["artifact_bytes"] <= 8 * 1024 * 1024 * 1024, "source approval size")
    _require(isinstance(expected["release_id"], str) and RELEASE.fullmatch(expected["release_id"]) is not None and isinstance(expected["pre_image_id"], str) and IMAGE.fullmatch(expected["pre_image_id"]) is not None, "source approval identity")
    _require(expected["recipient_type"] == "age1", "only age X25519 identity is accepted")
    assert_isolated_guest(host_namespaces)
    artifact_info = _regular_private(artifact, maximum=8 * 1024 * 1024 * 1024)
    identity_info = _regular_private(identity, maximum=16 * 1024)
    _require(artifact_info.st_uid == 0 and identity_info.st_uid == 0, "root-only Guest source ownership required")
    observed_artifact = _sha_file(artifact)
    observed_identity = _sha_file(identity)
    _require(observed_artifact == expected["artifact_sha256"] and artifact_info.st_size == expected["artifact_bytes"], "encrypted source differs from approval")
    _require(observed_identity == expected["approved_identity_sha256"], "identity differs from approval")
    _private_output(output_tar)
    recipient = _identity_recipient(identity, age_keygen=keygen_command)
    recipient_digest = hashlib.sha256(recipient.encode("ascii")).hexdigest()
    _require(recipient_digest == expected["approved_recipient_sha256"], "recipient differs from approval")
    try:
        result = run_hidden([age_command, "-d", "-i", str(identity), "-o", str(output_tar), str(artifact)], capture_output=True, timeout=900)
        _require(result.returncode == 0, "age decryption failed")
        _regular_private(output_tar, maximum=8 * 1024 * 1024 * 1024)
        archive = inspect_archive(output_tar, release_id=expected["release_id"], pre_image_id=expected["pre_image_id"])
        _require(_sha_file(artifact) == observed_artifact and _sha_file(identity) == observed_identity, "encrypted source or identity changed during decryption")
    except Exception:
        # This path did not exist before our age invocation. Remove only this
        # owned partial plaintext; never recursively remove a caller directory.
        if output_tar.is_file() and not output_tar.is_symlink():
            output_tar.unlink()
        raise
    inventory = {
        "artifact_sha256": observed_artifact,
        "decrypted_sha256": archive["recovery_point_sha256"],
        "recovery_point_sha256": archive["recovery_point_sha256"],
        "postgres_dump_sha256": archive["postgres_dump_sha256"],
        "redis_rdb_sha256": archive["redis_rdb_sha256"],
    }
    return {
        "kind": "encrypted-production-baseline",
        "artifact_sha256": expected["artifact_sha256"],
        "observed_artifact_sha256": observed_artifact,
        "artifact_bytes": artifact_info.st_size,
        "identity_available": True,
        "decryption_verified": True,
        "decrypted_sha256": archive["recovery_point_sha256"],
        "recovery_point_sha256": archive["recovery_point_sha256"],
        "release_id": expected["release_id"],
        "pre_image_id": expected["pre_image_id"],
        "recipient_type": expected["recipient_type"],
        "approved_recipient_sha256": expected["approved_recipient_sha256"],
        "derived_recipient_sha256": recipient_digest,
        "approved_identity_sha256": expected["approved_identity_sha256"],
        "observed_identity_sha256": observed_identity,
        "archive_inventory": inventory,
        "archive_inventory_sha256": _checksum(inventory),
    }


__all__ = ["decrypt_source", "inspect_archive", "extract_archive", "assert_isolated_guest"]
