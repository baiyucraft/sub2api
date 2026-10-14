"""Prepare persistent age backup identities; never rotate signing/daily keys.

Private state stays outside the checkout. Public return values are restricted
to fingerprints and versions; callers keep the storage reference private.
"""
from __future__ import annotations

import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import sys
from dataclasses import dataclass

from .paths import WORKSPACE
from .process import run_hidden


RECIPIENT = re.compile(r"age1[023456789acdefghjklmnpqrstuvwxyz]{58}\Z")
VERSION = re.compile(r"age-x25519-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}\Z")
FIELDS = frozenset({"schema", "version", "created_at", "recipient", "recipient_sha256", "identity_sha256", "tool_sha256"})
AGE_VERSION = "v1.2.1"


class KeyPreparationError(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise KeyPreparationError(message)


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def no_links(path: Path) -> None:
    """Check all existing ancestors, including Windows junctions/reparse points."""
    require(path.is_absolute(), "absolute private storage path required")
    for item in (path, *path.parents):
        if not item.exists() and not item.is_symlink():
            continue
        info = item.lstat()
        require(not stat.S_ISLNK(info.st_mode) and not (getattr(info, "st_file_attributes", 0) & 0x400), "linked private storage path rejected")
        if not item.is_dir():
            require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1, "nonregular or hard-linked private file rejected")


def outside_workspace(path: Path) -> None:
    no_links(path)
    require(not path.resolve().is_relative_to(WORKSPACE.resolve()), "private storage inside workspace rejected")


def _windows_acl(path: Path, *, set_private: bool) -> None:
    # Static script, safely quoted path, hidden process, no ACL/secret output.
    literal = "'" + str(path).replace("'", "''") + "'"
    setup = """
    $acl = if([System.IO.Directory]::Exists($p)) { [System.Security.AccessControl.DirectorySecurity]::new() } else { [System.Security.AccessControl.FileSecurity]::new() }
    $acl.SetOwner($sid)
    $acl.SetAccessRuleProtection($true,$false)
    $inherit = if([System.IO.Directory]::Exists($p)) { [System.Security.AccessControl.InheritanceFlags]'ContainerInherit,ObjectInherit' } else { [System.Security.AccessControl.InheritanceFlags]::None }
    foreach($principal in @($sid,$system)) {
      $rule = [System.Security.AccessControl.FileSystemAccessRule]::new($principal,[System.Security.AccessControl.FileSystemRights]::FullControl,$inherit,[System.Security.AccessControl.PropagationFlags]::None,[System.Security.AccessControl.AccessControlType]::Allow)
      $acl.AddAccessRule($rule)
    }
    if([System.IO.Directory]::Exists($p)) { [System.IO.Directory]::SetAccessControl($p,$acl) } else { [System.IO.File]::SetAccessControl($p,$acl) }
    $acl = if([System.IO.Directory]::Exists($p)) { [System.IO.Directory]::GetAccessControl($p) } else { [System.IO.File]::GetAccessControl($p) }
    """ if set_private else ""
    script = """
    $ErrorActionPreference='Stop'
    $p=PATH_LITERAL
    $sid=[System.Security.Principal.WindowsIdentity]::GetCurrent().User
    $system=[System.Security.Principal.SecurityIdentifier]::new('S-1-5-18')
    $acl = if([System.IO.Directory]::Exists($p)) { [System.IO.Directory]::GetAccessControl($p) } else { [System.IO.File]::GetAccessControl($p) }
    SETUP
    if(!$acl.AreAccessRulesProtected -or $acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value -ne $sid.Value) { throw 'private ACL required' }
    $rules=@($acl.GetAccessRules($true,$true,[System.Security.Principal.SecurityIdentifier]))
    if($rules.Count -ne 2) { throw 'private ACL required' }
    $seen=@{}
    foreach($rule in $rules) {
      $id=$rule.IdentityReference.Value
      if($id -notin @($sid.Value,$system.Value) -or $rule.IsInherited -or $rule.AccessControlType -ne 'Allow' -or $rule.FileSystemRights -ne 'FullControl') { throw 'private ACL required' }
      if($seen.ContainsKey($id)) { throw 'private ACL required' }; $seen[$id]=$true
    }
    """.replace("PATH_LITERAL", literal).replace("SETUP", setup)
    encoded = base64.b64encode(script.encode("utf-16le")).decode("ascii")
    result = run_hidden(["powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", encoded], capture_output=True, timeout=30)
    require(result.returncode == 0, "Windows private ACL verification failed")


def private_permissions(path: Path, *, initialize: bool = False, executable: bool = False) -> None:
    no_links(path)
    require(path.exists(), "private storage unavailable")
    if os.name == "nt":
        _windows_acl(path, set_private=initialize)
    else:
        if initialize:
            os.chmod(path, 0o700 if path.is_dir() or executable else 0o600)
        info = path.stat()
        require(info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == (0o700 if path.is_dir() or executable else 0o600), "private Unix owner/mode required")


def default_root() -> Path:
    if os.name == "nt":
        local = os.environ.get("LOCALAPPDATA")
        require(bool(local), "Windows local user storage unavailable")
        return Path(local) / "Sub2API" / "recovery-identities"
    return Path.home() / ".local/share/sub2api/recovery-identities"


def prepare_root(root: Path) -> None:
    outside_workspace(root)
    if not root.exists():
        # The parent carries no secrets. Protect the root before writing keys.
        root.parent.mkdir(parents=True, exist_ok=True)
        no_links(root.parent)
        root.mkdir(mode=0o700)
        private_permissions(root, initialize=True)
    private_permissions(root)


def prepare_tools() -> Path:
    """Install pinned upstream tools with Go's checksum database, if absent."""
    tool_dir = default_root().parent / "age-tools" / AGE_VERSION
    outside_workspace(tool_dir)
    prepare_root(tool_dir)
    suffix = ".exe" if os.name == "nt" else ""
    for name in ("age-keygen", "age"):
        tool = tool_dir / (name + suffix)
        if tool.exists():
            private_permissions(tool, executable=True)
            require(tool.is_file(), "installed age tool is invalid")
            verify_tool_version(tool)
            continue
        compiler = shutil.which("go")
        require(compiler is not None, "Go required to install pinned age tools")
        environment = {**os.environ, "GOBIN": str(tool_dir), "GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GONOSUMDB": "", "GOPRIVATE": "", "GONOPROXY": "", "GOWORK": "off"}
        result = run_hidden([compiler, "install", "filippo.io/age/cmd/" + name + "@" + AGE_VERSION], cwd=tool_dir, env=environment, capture_output=True, timeout=600)
        if result.returncode != 0:
            require(False, "pinned age tool installation failed: " + install_failure_category(result.stderr + result.stdout))
        private_permissions(tool, initialize=True, executable=True)
        verify_tool_version(tool)
    return tool_dir


def exclusive_write(path: Path, data: bytes) -> None:
    no_links(path)
    private_permissions(path.parent)
    require(not path.exists() and not path.is_symlink(), "private file already exists")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    private_permissions(path, initialize=True)


def tool_run(tool: Path, args: list[str], *, timeout: int = 60) -> bytes:
    no_links(tool)
    require(tool.is_file(), "age tool unavailable")
    result = run_hidden([str(tool), *args], capture_output=True, timeout=timeout)
    require(result.returncode == 0, "age operation failed; tool output withheld")
    return result.stdout


def verify_tool_version(tool: Path) -> None:
    version = tool_run(tool, ["--version"]).decode("ascii", "strict").strip()
    require(version in {AGE_VERSION, AGE_VERSION.removeprefix("v")}, "installed age tool version differs from pinned version")


def install_failure_category(output: bytes) -> str:
    """Fixed error codes only; raw build stderr never enters user evidence."""
    value = output.decode("utf-8", "replace").lower()
    for needle, code in (("tls handshake timeout", "tls_timeout"), ("i/o timeout", "network_timeout"), ("no such host", "dns_lookup_failed"), ("connection refused", "connection_refused"), ("proxyconnect", "proxy_connect_failed"), ("checksum mismatch", "checksum_mismatch"), ("certificate", "tls_certificate_failed"), ("toolchain", "toolchain_unavailable"), ("502", "registry_bad_gateway"), ("403", "registry_denied"), ("404", "registry_not_found"), ("unexpected eof", "registry_eof")):
        if needle in value:
            return code
    return "unclassified_install_failure"


@dataclass(frozen=True)
class PreparedIdentity:
    identity_path: Path
    metadata: dict

    def public(self) -> dict:
        return {"schema": 1, "status": "prepared", "version": self.metadata["version"], "created_at": self.metadata["created_at"], "recipient_sha256": self.metadata["recipient_sha256"], "identity_sha256": self.metadata["identity_sha256"], "permissions_verified": True, "encryption_decryption_selftest": True, "full_recovery_status": "not_checked"}


def selftest(directory: Path, identity: Path, recipient: str, age: Path) -> None:
    # Known throwaway bytes, never production plaintext. Owned files only.
    stem = ".selftest-" + secrets.token_hex(8)
    plain, encrypted, restored = (directory / (stem + suffix) for suffix in (".plain", ".age", ".restored"))
    payload = b"sub2api-age-identity-selftest\n" + secrets.token_bytes(64)
    exclusive_write(plain, payload)
    try:
        tool_run(age, ["-r", recipient, "-o", str(encrypted), str(plain)])
        private_permissions(encrypted, initialize=True)
        tool_run(age, ["--decrypt", "-i", str(identity), "-o", str(restored), str(encrypted)])
        private_permissions(restored, initialize=True)
        require(restored.read_bytes() == payload, "age selftest plaintext differs")
    finally:
        for path in (plain, encrypted, restored):
            if path.exists():
                private_permissions(path)
                path.unlink()


def _validate_existing(directory: Path, age: Path, keygen: Path) -> PreparedIdentity:
    private_permissions(directory)
    require(VERSION.fullmatch(directory.name) is not None, "identity version invalid")
    identity = directory / "identity.agekey"
    metadata_path = directory / "registry.json"
    private_permissions(identity)
    private_permissions(metadata_path)
    require(identity.stat().st_size <= 4096 and metadata_path.stat().st_size <= 4096, "identity registry size invalid")
    def unique(pairs):
        value = {}
        for k, v in pairs:
            require(k not in value, "duplicate identity registry field")
            value[k] = v
        return value
    document = json.loads(metadata_path.read_text(encoding="utf-8"), object_pairs_hook=unique)
    require(isinstance(document, dict) and set(document) == FIELDS and type(document["schema"]) is int and document["schema"] == 1 and document["version"] == directory.name, "identity registry contract invalid")
    require(isinstance(document["created_at"], str) and isinstance(document["recipient"], str) and RECIPIENT.fullmatch(document["recipient"]) is not None, "identity registry public metadata invalid")
    require(document["identity_sha256"] == sha(identity.read_bytes()), "identity fingerprint differs from registry")
    require(document["tool_sha256"] == {"age": sha(age.read_bytes()), "age-keygen": sha(keygen.read_bytes())}, "age tool bytes differ from registered version")
    recipient = tool_run(keygen, ["-y", str(identity)]).decode("ascii").strip()
    require(RECIPIENT.fullmatch(recipient) is not None and recipient == document["recipient"] and sha(recipient.encode()) == document["recipient_sha256"], "recipient differs from registered identity")
    selftest(directory, identity, recipient, age)
    return PreparedIdentity(identity, document)


def prepare_identity(*, root: Path | None = None, tool_dir: Path | None = None, expected_recipient_sha256: str | None = None) -> PreparedIdentity:
    root = default_root() if root is None else root
    prepare_root(root)
    suffix = ".exe" if os.name == "nt" else ""
    if tool_dir is None:
        tool_dir = prepare_tools()
    else:
        # Inspect the original path before resolution can hide a junction.
        no_links(tool_dir.absolute())
        tool_dir = tool_dir.absolute()
    age, keygen = tool_dir / ("age" + suffix), tool_dir / ("age-keygen" + suffix)
    for tool in (age, keygen):
        no_links(tool)
        require(tool.is_file(), "pinned age tools unavailable")
        verify_tool_version(tool)
    if expected_recipient_sha256 is not None:
        require(re.fullmatch(r"[0-9a-f]{64}", expected_recipient_sha256) is not None, "expected recipient fingerprint invalid")
    # Atomic lock is outside Git and contains no private material.
    lock = root / ".preparation.lock"
    exclusive_write(lock, b"age-identity-preparation\n")
    try:
        for directory in sorted(root.iterdir(), reverse=True):
            if not VERSION.fullmatch(directory.name):
                continue
            prepared = _validate_existing(directory, age, keygen)
            if expected_recipient_sha256 is None or prepared.metadata["recipient_sha256"] == expected_recipient_sha256:
                return prepared
        if expected_recipient_sha256 is not None:
            raise KeyPreparationError("matching historical identity unavailable; create a new backup with a new identity")
        now = datetime.now(timezone.utc)
        version = "age-x25519-" + now.strftime("%Y%m%dT%H%M%SZ") + "-" + secrets.token_hex(8)
        directory = root / version
        directory.mkdir(mode=0o700)
        private_permissions(directory, initialize=True)
        identity = directory / "identity.agekey"
        # age-keygen uses exclusive creation; output never reaches logs.
        tool_run(keygen, ["-o", str(identity)])
        private_permissions(identity, initialize=True)
        recipient = tool_run(keygen, ["-y", str(identity)]).decode("ascii").strip()
        require(RECIPIENT.fullmatch(recipient) is not None, "generated recipient invalid")
        selftest(directory, identity, recipient, age)
        document = {"schema": 1, "version": version, "created_at": now.isoformat(timespec="seconds"), "recipient": recipient, "recipient_sha256": sha(recipient.encode()), "identity_sha256": sha(identity.read_bytes()), "tool_sha256": {"age": sha(age.read_bytes()), "age-keygen": sha(keygen.read_bytes())}}
        exclusive_write(directory / "registry.json", (json.dumps(document, sort_keys=True, separators=(",", ":")) + "\n").encode())
        return PreparedIdentity(identity, document)
    finally:
        private_permissions(lock)
        lock.unlink()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tool-dir", type=Path)
    parser.add_argument("--expected-recipient-sha256")
    args = parser.parse_args()
    try:
        result = prepare_identity(tool_dir=args.tool_dir, expected_recipient_sha256=args.expected_recipient_sha256)
        print(json.dumps(result.public(), sort_keys=True))
    except Exception as exc:
        # Do not emit paths, tool stderr or key material from exceptions.
        print(json.dumps({"status": "failed", "error_type": type(exc).__name__}))
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
