from __future__ import annotations

import argparse
import ipaddress
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Iterable

import yaml


DOCUMENTATION_NETWORKS = tuple(
    ipaddress.ip_network(value)
    for value in ("192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32")
)
FICTIONAL_SUFFIXES = (".example", ".example.test", ".test", ".invalid", ".localhost")
ZERO_SHA = "0" * 40


def _run_git(arguments: list[str], *, input: bytes | None = None, cwd: Path | None = None) -> bytes:
    result = subprocess.run(["git", *arguments], input=input, capture_output=True, check=True, cwd=cwd)
    return result.stdout


def _is_fictional_host(value: str) -> bool:
    value = value.strip().lower().rstrip(".")
    if not value or value in {"localhost", "127.0.0.1", "::1"}:
        return True
    try:
        address = ipaddress.ip_address(value.strip("[]"))
    except ValueError:
        try:
            network = ipaddress.ip_network(value, strict=False)
        except ValueError:
            return value.endswith(FICTIONAL_SUFFIXES)
        return any(network.subnet_of(documentation) for documentation in DOCUMENTATION_NETWORKS if network.version == documentation.version)
    return address.is_loopback or any(address in network for network in DOCUMENTATION_NETWORKS)


def deployment_disclosure_values(document: object) -> set[bytes]:
    if not isinstance(document, dict):
        raise RuntimeError("local SSH configuration is invalid")
    values: set[str] = set()
    private_values: set[str] = set()
    deployment = document.get("deployment")
    if isinstance(deployment, dict):
        domain = deployment.get("public_domain")
        if isinstance(domain, str):
            values.add(domain)
            labels = domain.strip('.').split('.')
            if len(labels) > 2:
                values.add('.'.join(labels[-2:]))
        routes = deployment.get("health_routes")
        if isinstance(routes, dict):
            for route in routes.values():
                if isinstance(route, dict) and isinstance(route.get("address"), str):
                    values.add(route["address"])
        probe = deployment.get("relay_probe")
        if isinstance(probe, dict):
            if isinstance(probe.get("listen_address"), str):
                values.add(probe["listen_address"])
            sources = probe.get("allowed_sources")
            if isinstance(sources, list):
                values.update(item for item in sources if isinstance(item, str))
        backup = deployment.get("backup_upload")
        if isinstance(backup, dict):
            for key in ("key_path",):
                value = backup.get(key)
                if isinstance(value, str) and value and not value.startswith('/home/example/'):
                    private_values.add(value)
    servers = document.get("servers")
    if isinstance(servers, dict):
        for server in servers.values():
            if not isinstance(server, dict):
                continue
            host = server.get("host")
            if isinstance(host, str):
                values.add(host)
            proxy = server.get("proxy")
            if isinstance(proxy, str) and ":" in proxy:
                values.add(proxy.rsplit(":", 1)[0].strip("[]"))
            for key in ("private_key", "identity_file", "password", "passphrase"):
                value = server.get(key)
                if isinstance(value, str) and value and not (
                    key in {"private_key", "identity_file"} and value.startswith('/home/example/')
                ):
                    private_values.add(value)
    return {
        value.strip().lower().encode("utf-8")
        for value in values
        if len(value.strip()) >= 7
        and not _is_fictional_host(value)
    } | {value.strip().lower().encode("utf-8") for value in private_values if len(value.strip()) >= 7}


def _private_config_path(root: Path) -> Path:
    configured = os.environ.get("SUB2API_SSH_CONFIG")
    return Path(configured).expanduser().absolute() if configured else root / ".ssh.local"


def load_disclosure_values(root: Path) -> set[bytes]:
    path = _private_config_path(root)
    if path.is_symlink() or not path.is_file():
        raise RuntimeError("local deployment configuration is missing or unsafe")
    try:
        document = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, yaml.YAMLError):
        raise RuntimeError("local deployment configuration cannot be read") from None
    return deployment_disclosure_values(document)


def _scan_text(data: bytes, denied: set[bytes]) -> bool:
    lowered = data.lower()
    return any(value in lowered for value in denied)


def _scan_tree(root: Path, commit: str, denied: set[bytes]) -> list[str]:
    entries = _run_git(["ls-tree", "-rz", commit], cwd=root).split(b"\0")
    blobs: dict[str, list[str]] = {}
    matches: list[str] = []
    for entry in entries:
        if not entry:
            continue
        metadata, _, raw_path = entry.partition(b"\t")
        fields = metadata.split()
        if len(fields) != 3 or fields[1] != b"blob":
            continue
        object_id = fields[2].decode("ascii")
        path = raw_path.decode("utf-8", errors="surrogateescape")
        if path == ".ssh.local" or path.startswith(".ssh.local.") and path != ".ssh.local.example.yaml":
            matches.append(path)
            continue
        blobs.setdefault(object_id, []).append(path)
    if blobs:
        raw = _run_git(["cat-file", "--batch"], input=("\n".join(blobs) + "\n").encode("ascii"), cwd=root)
        offset = 0
        for object_id, paths in blobs.items():
            header_end = raw.find(b"\n", offset)
            if header_end < 0:
                raise RuntimeError("cannot read pushed Git file objects")
            header = raw[offset:header_end].split()
            if len(header) != 3 or header[0].decode("ascii") != object_id or header[1] != b"blob":
                raise RuntimeError("pushed Git object is not a file")
            size = int(header[2])
            object_start = header_end + 1
            object_end = object_start + size
            content = raw[object_start:object_end]
            offset = object_end + 1
            if _scan_text(content, denied):
                matches.extend(paths)
    return matches


def _scan_messages(root: Path, revisions: list[str], denied: set[bytes]) -> list[str]:
    if not revisions:
        return []
    commits = _run_git(["rev-list", *revisions], cwd=root).decode("ascii").splitlines()
    if not commits:
        return []
    raw = _run_git(["cat-file", "--batch"], input=("\n".join(commits) + "\n").encode("ascii"), cwd=root)
    matches: list[str] = []
    offset = 0
    for commit in commits:
        header_end = raw.find(b"\n", offset)
        if header_end < 0:
            raise RuntimeError("cannot read pushed Git commit objects")
        header = raw[offset:header_end].split()
        if len(header) != 3 or header[1] != b"commit":
            raise RuntimeError("pushed Git object is not a commit")
        size = int(header[2])
        object_start = header_end + 1
        object_end = object_start + size
        commit_object = raw[object_start:object_end]
        offset = object_end + 1
        _, separator, message = commit_object.partition(b"\n\n")
        if not separator:
            raise RuntimeError("pushed Git commit message is invalid")
        if _scan_text(message, denied):
            matches.append("commit " + commit)
    return matches


def _scan_history_blobs(root: Path, revisions: list[str], denied: set[bytes]) -> list[str]:
    if not revisions:
        return []
    entries = _run_git(["rev-list", "--objects", *revisions], cwd=root).splitlines()
    paths_by_object: dict[str, set[str]] = {}
    private_paths: set[str] = set()
    for entry in entries:
        object_id, separator, raw_path = entry.partition(b" ")
        if not separator:
            continue
        path = raw_path.decode("utf-8", errors="surrogateescape")
        if path == ".ssh.local" or path.startswith(".ssh.local.") and path != ".ssh.local.example.yaml":
            private_paths.add(path)
            continue
        paths_by_object.setdefault(object_id.decode("ascii"), set()).add(path)
    if not paths_by_object:
        return []
    object_ids = list(paths_by_object)
    type_output = _run_git(
        ["cat-file", "--batch-check=%(objectname) %(objecttype)"],
        input=("\n".join(object_ids) + "\n").encode("ascii"), cwd=root,
    )
    blob_ids = [
        line.split(b" ", 1)[0].decode("ascii")
        for line in type_output.splitlines()
        if line.endswith(b" blob")
    ]
    matches: set[str] = set(private_paths)
    for start in range(0, len(blob_ids), 256):
        batch = blob_ids[start:start + 256]
        raw = _run_git(
            ["cat-file", "--batch"], input=("\n".join(batch) + "\n").encode("ascii"), cwd=root,
        )
        offset = 0
        for object_id in batch:
            header_end = raw.find(b"\n", offset)
            if header_end < 0:
                raise RuntimeError("cannot read pushed Git history blobs")
            header = raw[offset:header_end].split()
            if len(header) != 3 or header[0].decode("ascii") != object_id or header[1] != b"blob":
                raise RuntimeError("pushed Git history object is not a blob")
            size = int(header[2])
            object_start = header_end + 1
            object_end = object_start + size
            content = raw[object_start:object_end]
            offset = object_end + 1
            if _scan_text(content, denied):
                matches.update(paths_by_object[object_id])
    return sorted(matches)


def _incoming_ranges(stdin: Iterable[str]) -> tuple[list[tuple[str, str]], list[str]]:
    trees: list[tuple[str, str]] = []
    ranges: list[str] = []
    for line in stdin:
        fields = line.split()
        if len(fields) != 4:
            continue
        _, local_sha, _, remote_sha = fields
        if local_sha == ZERO_SHA:
            continue
        trees.append((local_sha, remote_sha))
        ranges.append(local_sha if remote_sha == ZERO_SHA else f"{remote_sha}..{local_sha}")
    return trees, ranges


def check_push(root: Path, stdin: Iterable[str]) -> list[str]:
    denied = load_disclosure_values(root)
    trees, ranges = _incoming_ranges(stdin)
    failures: set[str] = set()
    for commit, _ in trees:
        failures.update(_scan_tree(root, commit, denied))
    valid_ranges: list[str] = []
    for revision_range in ranges:
        try:
            _run_git(["rev-list", "--count", revision_range], cwd=root)
            valid_ranges.append(revision_range)
        except subprocess.CalledProcessError:
            valid_ranges.append(revision_range.split("..")[-1])
    valid_ranges = sorted(set(valid_ranges))
    failures.update(_scan_history_blobs(root, valid_ranges, denied))
    failures.update(_scan_messages(root, valid_ranges, denied))
    return sorted(failures)


def main() -> int:
    parser = argparse.ArgumentParser(description="Check pushed Git content against local deployment endpoints")
    parser.add_argument("--pre-push", action="store_true")
    args = parser.parse_args()
    root = Path(_run_git(["rev-parse", "--show-toplevel"]).decode().strip())
    if not args.pre_push:
        parser.error("--pre-push is required")
    try:
        failures = check_push(root, sys.stdin)
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        return 2
    if failures:
        print("blocked local deployment values in:", file=sys.stderr)
        for path in failures:
            print(path, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
