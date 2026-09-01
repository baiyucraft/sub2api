from __future__ import annotations

import io
import json
import os
import pathlib
import re
import secrets
import shlex
import posixpath
import socket
import stat
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from typing import Iterable

import paramiko
import socks
import yaml

from .paths import WORKSPACE


LOGGING_ROOT = pathlib.Path(__file__).resolve().parents[1] / "logging"
if str(LOGGING_ROOT) not in sys.path:
    sys.path.insert(0, str(LOGGING_ROOT))

from release_logging import EventContext, JSONLEventLogger  # noqa: E402


ROOT = WORKSPACE
SSH_CONFIG = ROOT / ".ssh.local"
KNOWN_HOSTS = ROOT / ".tmp" / "known_hosts"
RELEASE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$")
MAX_EVENT_LOG_BYTES = 16 * 1024 * 1024
TRANSFER_CHUNK_BYTES = 8 * 1024 * 1024
TRANSFER_MAX_ATTEMPTS = 8
TRANSFER_DOWNLOAD_PREFETCH_REQUESTS = 64
TRANSFER_PARALLEL_PARTS = 16
TRANSFER_DOWNLOAD_PART_PREFETCH_REQUESTS = 8
TRANSFER_PARALLEL_MIN_BYTES = 32 * 1024 * 1024
TRANSFER_SOCKET_TIMEOUT = 90
REMOTE_EVENT_LOGS = {
    "local_vm": "/opt/sub2api-deploy/release-gates/{release_id}/logs/events.jsonl",
    "vm": "/opt/sub2api-deploy/release-gates/{release_id}/logs/events.jsonl",
    "racknerd": "/opt/sub2api/releases/{release_id}/logs/events.jsonl",
    "dmit": "/var/lib/sub2api-release/logs/{release_id}/events.jsonl",
    "backup": "/srv/sub2api-backups/release-logs/{release_id}/events.jsonl",
}
REMOTE_RAW_LOGS = {
    "local_vm": "/opt/sub2api-deploy/release-logs/{release_id}/remote.raw.log",
    "vm": "/opt/sub2api-deploy/release-logs/{release_id}/remote.raw.log",
    "racknerd": "/opt/sub2api/release-logs/{release_id}/remote.raw.log",
    "dmit": "/var/lib/sub2api-release/logs/{release_id}/remote.raw.log",
    "backup": "/srv/sub2api-backups/release-logs/{release_id}/remote.raw.log",
}


@dataclass
class SSHResult:
    values: dict[str, str]


class SSHRunner:
    def __init__(self) -> None:
        document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
        self.servers = document["servers"]
        self.temp_dirs: set[tuple[str, str]] = set()
        self.event_log_path = os.environ.get("SUB2API_EVENT_LOG")
        self.release_id = os.environ.get("SUB2API_RELEASE_ID")
        self.deployment_mode = os.environ.get("SUB2API_DEPLOYMENT_MODE")

    def _logger(self, node: str) -> JSONLEventLogger | None:
        path = getattr(self, "event_log_path", None)
        identifier = getattr(self, "release_id", None)
        mode = getattr(self, "deployment_mode", None)
        if not path or not identifier or mode not in {"blue-green", "downtime"} or node not in REMOTE_EVENT_LOGS:
            return None
        event_node = "vm" if node == "local_vm" else node
        return JSONLEventLogger(pathlib.Path(path), EventContext(identifier, mode, event_node))

    def _emit(self, node: str, **event) -> None:
        logger = self._logger(node)
        if logger is not None:
            logger.emit(script="release.ssh", **event)

    def _wrap_remote_raw_logging(self, node: str, script: str, command_id: str) -> str:
        release_id = getattr(self, "release_id", None)
        mode = getattr(self, "deployment_mode", None)
        template = REMOTE_RAW_LOGS.get(node)
        if not template or not release_id or not RELEASE_ID.fullmatch(release_id) or mode not in {"blue-green", "downtime"}:
            return "bash -lc " + shlex.quote(script)
        if not re.fullmatch(r"[0-9a-f]{16}", command_id):
            raise RuntimeError("remote command ID is invalid")
        log_file = template.format(release_id=release_id)
        log_dir = posixpath.dirname(log_file)
        log_root = posixpath.dirname(log_dir)
        wrapped = f'''set -Eeuo pipefail
umask 077
if [[ -e {shlex.quote(log_root)} ]]; then
  [[ -d {shlex.quote(log_root)} && ! -L {shlex.quote(log_root)} ]]
else
  install -d -o 0 -g 0 -m 700 {shlex.quote(log_root)}
fi
[[ $(stat -c '%u:%g:%a' {shlex.quote(log_root)}) == 0:0:700 ]]
if [[ -e {shlex.quote(log_dir)} ]]; then
  [[ -d {shlex.quote(log_dir)} && ! -L {shlex.quote(log_dir)} ]]
else
  install -d -o 0 -g 0 -m 700 {shlex.quote(log_dir)}
fi
[[ $(stat -c '%u:%g:%a' {shlex.quote(log_dir)}) == 0:0:700 ]]
touch {shlex.quote(log_file)}
[[ -f {shlex.quote(log_file)} && ! -L {shlex.quote(log_file)} ]]
chmod 600 {shlex.quote(log_file)}
[[ $(stat -c '%u:%g:%a:%h' {shlex.quote(log_file)}) == 0:0:600:1 ]]
stdout_tmp=$(mktemp {shlex.quote(log_dir + '/.stdout.XXXXXXXX')})
stderr_tmp=$(mktemp {shlex.quote(log_dir + '/.stderr.XXXXXXXX')})
cleanup_remote_raw_log() {{ rm -f -- "$stdout_tmp" "$stderr_tmp"; }}
trap cleanup_remote_raw_log EXIT
set +e
bash -lc {shlex.quote(script)} >"$stdout_tmp" 2>"$stderr_tmp"
code=$?
set -e
{{
  printf '\n[%s] command_id={command_id} stream=stdout\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  cat "$stdout_tmp"
  printf '\n[%s] command_id={command_id} stream=stderr exit=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$code"
  cat "$stderr_tmp"
}} >> {shlex.quote(log_file)}
cat "$stdout_tmp"
cat "$stderr_tmp" >&2
exit "$code"
'''
        return "bash -lc " + shlex.quote(wrapped)

    def _require_temp_path(self, name: str, remote_path: str) -> None:
        normalized = posixpath.normpath(remote_path)
        roots = getattr(self, "temp_dirs", set())
        if not any(host == name and (normalized == root or normalized.startswith(root + "/")) for host, root in roots):
            raise RuntimeError("SFTP path is outside a registered remote temporary directory")

    def connect(self, name: str, command_id: str | None = None) -> paramiko.SSHClient:
        config = self.servers[name]
        last_error: BaseException | None = None
        identifier = command_id or secrets.token_hex(8)
        for attempt in range(1, 4):
            client: paramiko.SSHClient | None = None
            proxy_socket = None
            try:
                self._emit(
                    name, stage="ssh_connect", event="connection_attempt_started",
                    message="SSH connection attempt started", command_id=identifier, attempt=attempt,
                    details={"proxy_enabled": bool(config.get("proxy"))},
                )
                kwargs = {
                    "hostname": config["host"],
                    "port": int(config.get("port", 22)),
                    "username": config["user"],
                    "timeout": 30,
                    "banner_timeout": 30,
                    "auth_timeout": 30,
                    "look_for_keys": False,
                    "allow_agent": False,
                }
                if config.get("private_key"):
                    kwargs["key_filename"] = str(pathlib.Path(config["private_key"]).expanduser())
                else:
                    kwargs["password"] = config["password"]
                if config.get("proxy"):
                    host, port = config["proxy"].rsplit(":", 1)
                    proxy_socket = socks.socksocket()
                    proxy_socket.set_proxy(socks.SOCKS5, host, int(port))
                    proxy_socket.settimeout(30)
                    proxy_socket.connect((config["host"], int(config.get("port", 22))))
                    kwargs["sock"] = proxy_socket
                client = paramiko.SSHClient()
                client.load_host_keys(str(KNOWN_HOSTS))
                port = int(config.get("port", 22))
                host = config["host"]
                if port != 22 and client.get_host_keys().lookup(f"[{host}]:{port}") is None:
                    bare = client.get_host_keys().lookup(host)
                    if bare:
                        for key_type, key in bare.items():
                            client.get_host_keys().add(f"[{host}]:{port}", key_type, key)
                client.set_missing_host_key_policy(paramiko.RejectPolicy())
                client.connect(**kwargs)
                transport = client.get_transport()
                if transport is None:
                    raise RuntimeError(f"{name} SSH transport is unavailable")
                transport.set_keepalive(30)
                self._emit(
                    name, stage="ssh_connect", event="connection_verified",
                    message="SSH connection verified", command_id=identifier, attempt=attempt,
                )
                return client
            except (EOFError, OSError, paramiko.SSHException, socks.ProxyError) as error:
                last_error = error
                if client is not None:
                    client.close()
                if proxy_socket is not None:
                    proxy_socket.close()
                self._emit(
                    name, stage="ssh_connect", event="connection_attempt_failed",
                    message="SSH connection attempt failed", command_id=identifier, attempt=attempt,
                    level="warn" if attempt < 3 else "error", exit_code=1,
                    details={"error_type": type(error).__name__, "will_retry": attempt < 3},
                )
                if attempt < 3:
                    time.sleep(2 ** (attempt - 1))
                    continue
                raise
        assert last_error is not None
        raise last_error

    def run(self, name: str, script: str, allowed: Iterable[str], timeout: int = 120) -> SSHResult:
        return self.run_with_input(name, script, allowed, b"", timeout=timeout)

    def run_with_input(self, name: str, script: str, allowed: Iterable[str], data: bytes, timeout: int = 120) -> SSHResult:
        command_id = secrets.token_hex(8)
        allowlist = set(allowed)
        self._emit(
            name, stage="ssh_command", event="command_started", message="Remote structured command started",
            command_id=command_id, details={"timeout_seconds": timeout, "input_bytes": len(data), "allowed_fields": sorted(allowlist)},
        )
        client = self.connect(name)
        try:
            command = self._wrap_remote_raw_logging(name, script, command_id)
            stdin, stdout, stderr = client.exec_command(command, timeout=timeout, get_pty=False)
            if data:
                stdin.write(data)
                stdin.flush()
            stdin.channel.shutdown_write()
            output = stdout.read().decode("utf-8", "strict")
            error_output = stderr.read().decode("utf-8", "replace")
            exit_code = stdout.channel.recv_exit_status()
            if exit_code:
                self._emit(
                    name, stage="ssh_command", event="command_failed", message="Remote command returned a non-zero exit code",
                    command_id=command_id, level="error", exit_code=exit_code,
                    details={"stdout_bytes": len(output.encode("utf-8")), "stderr_bytes": len(error_output.encode("utf-8"))},
                )
                raise RuntimeError(f"{name} stage failed with exit code {exit_code}; remote stderr withheld")
            values: dict[str, str] = {}
            for line in output.splitlines():
                if not line or "=" not in line:
                    raise RuntimeError(f"{name} returned non-structured output")
                key, value = line.split("=", 1)
                if key not in allowlist:
                    raise RuntimeError(f"{name} returned an undeclared field: {key}")
                values[key] = value
            if error_output.strip():
                self._emit(
                    name, stage="ssh_command", event="command_failed", message="Remote command returned unexpected stderr",
                    command_id=command_id, level="error", exit_code=97,
                    details={"stdout_bytes": len(output.encode("utf-8")), "stderr_bytes": len(error_output.encode("utf-8"))},
                )
                raise RuntimeError(f"{name} returned unexpected stderr")
            missing = allowlist.difference(values)
            if missing:
                raise RuntimeError(f"{name} omitted required fields: {sorted(missing)}")
            self._emit(
                name, stage="ssh_command", event="command_finished", message="Remote structured command finished",
                command_id=command_id, exit_code=0,
                details={"stdout_bytes": len(output.encode("utf-8")), "stderr_bytes": len(error_output.encode("utf-8")), "returned_fields": sorted(values)},
            )
            return SSHResult(values)
        except BaseException as error:
            if not isinstance(error, RuntimeError) or "returned unexpected stderr" not in str(error) and "stage failed with exit code" not in str(error):
                self._emit(
                    name, stage="ssh_command", event="command_failed", message="Remote structured command failed",
                    command_id=command_id, level="error", exit_code=getattr(error, "returncode", 1),
                    details={"error_type": type(error).__name__},
                )
            raise
        finally:
            client.close()

    def read_release_events(self, name: str, release_id: str, max_bytes: int = MAX_EVENT_LOG_BYTES) -> bytes:
        """Read a remote redacted JSONL event log, never a raw stdout/stderr log."""

        if name not in REMOTE_EVENT_LOGS:
            raise ValueError("unsupported release log node")
        if not RELEASE_ID.fullmatch(release_id):
            raise ValueError("invalid release ID")
        if max_bytes <= 0 or max_bytes > MAX_EVENT_LOG_BYTES:
            raise ValueError("invalid maximum event log size")
        remote_path = REMOTE_EVENT_LOGS[name].format(release_id=release_id)
        command_id = secrets.token_hex(8)
        self._emit(
            name, stage="log_query", event="log_read_started", message="Remote structured event log read started",
            command_id=command_id,
        )
        client = self.connect(name)
        try:
            sftp = client.open_sftp()
            try:
                attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                size = int(attributes.st_size)
                mode = getattr(attributes, "st_mode", None)
                links = getattr(attributes, "st_nlink", 1)
                if size < 0 or size > max_bytes:
                    raise RuntimeError("remote event log size is invalid")
                if mode is not None and (not stat.S_ISREG(mode) or mode & 0o077):
                    raise RuntimeError("remote event log permissions are invalid")
                if links != 1:
                    raise RuntimeError("remote event log must have one hard link")
                with sftp.file(remote_path, "rb") as stream:
                    value = stream.read(max_bytes + 1)
                if len(value) > max_bytes:
                    raise RuntimeError("remote event log exceeded its declared size")
            finally:
                sftp.close()
        except BaseException as error:
            self._emit(
                name, stage="log_query", event="log_read_failed", message="Remote structured event log read failed",
                command_id=command_id, level="error", exit_code=1, details={"error_type": type(error).__name__},
            )
            raise
        finally:
            client.close()
        self._emit(
            name, stage="log_query", event="log_read_finished", message="Remote structured event log read finished",
            command_id=command_id, exit_code=0, details={"bytes": len(value)},
        )
        return value

    def upload(self, name: str, data: bytes, remote_path: str, mode: int = 0o600) -> None:
        self._require_temp_path(name, remote_path)
        client = self.connect(name)
        try:
            sftp = client.open_sftp()
            try:
                with sftp.file(remote_path, "wb") as stream:
                    stream.write(data)
                sftp.chmod(remote_path, mode)
            finally:
                sftp.close()
        finally:
            client.close()

    def upload_file(self, name: str, local_path: pathlib.Path, remote_path: str, mode: int = 0o600) -> None:
        self._require_temp_path(name, remote_path)
        if not local_path.is_file() or local_path.is_symlink():
            raise RuntimeError("local transfer input is not a regular file")
        expected_size = local_path.stat().st_size
        if expected_size < TRANSFER_PARALLEL_MIN_BYTES:
            self._upload_file_sequential(name, local_path, remote_path, mode)
            return
        self._upload_file_parallel(name, local_path, remote_path, mode)

    def _upload_file_sequential(self, name: str, local_path: pathlib.Path, remote_path: str, mode: int) -> None:
        """Upload small control assets without creating a pool of SSH sessions."""
        expected_size = local_path.stat().st_size
        for attempt in range(1, TRANSFER_MAX_ATTEMPTS + 1):
            client = None
            sftp = None
            try:
                client = self.connect(name)
                sftp = client.open_sftp()
                self._set_sftp_timeout(sftp)
                remote_exists = False
                try:
                    attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                    remote_exists = True
                    remote_size = int(attributes.st_size)
                    remote_mode = getattr(attributes, "st_mode", None)
                    if remote_mode is not None and not stat.S_ISREG(remote_mode):
                        raise RuntimeError("remote transfer output is not a regular file")
                    if remote_size < 0:
                        raise RuntimeError("remote transfer output size is invalid")
                    if remote_size > expected_size:
                        sftp.remove(remote_path)
                        remote_size = 0
                except (OSError, IOError):
                    remote_size = 0
                if not remote_exists or remote_size != expected_size:
                    with local_path.open("rb") as source, sftp.file(remote_path, "ab") as target:
                        self._set_sftp_file_timeout(target)
                        if hasattr(target, "set_pipelined"):
                            target.set_pipelined(True)
                        source.seek(remote_size)
                        remaining = expected_size - remote_size
                        while remaining:
                            chunk = source.read(min(TRANSFER_CHUNK_BYTES, remaining))
                            if not chunk:
                                raise OSError("local transfer input ended before expected size")
                            target.write(chunk)
                            remaining -= len(chunk)
                        target.flush()
                    if int(sftp.stat(remote_path).st_size) != expected_size:
                        raise OSError("remote transfer output size mismatch")
                sftp.chmod(remote_path, mode)
                return
            except (EOFError, OSError, IOError, socket.timeout, paramiko.SSHException) as error:
                if attempt == TRANSFER_MAX_ATTEMPTS:
                    raise
                self._emit(name, event="transfer_retry", message="SFTP upload retrying from the remote checkpoint", details={"attempt": attempt, "next_attempt": attempt + 1, "error_type": type(error).__name__})
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                if sftp is not None:
                    sftp.close()
                if client is not None:
                    client.close()

    @staticmethod
    def _transfer_ranges(size: int, parts: int) -> list[tuple[int, int]]:
        if size < 0 or parts < 1:
            raise ValueError("invalid transfer range input")
        if size == 0:
            return [(0, 0)]
        count = min(parts, size)
        base, remainder = divmod(size, count)
        ranges: list[tuple[int, int]] = []
        start = 0
        for index in range(count):
            length = base + (1 if index < remainder else 0)
            end = start + length
            ranges.append((start, end))
            start = end
        return ranges

    @staticmethod
    def _set_sftp_timeout(sftp, timeout: int = TRANSFER_SOCKET_TIMEOUT) -> None:
        channel = getattr(sftp, "get_channel", lambda: None)()
        if channel is not None and hasattr(channel, "settimeout"):
            channel.settimeout(timeout)

    @staticmethod
    def _set_sftp_file_timeout(stream, timeout: int = TRANSFER_SOCKET_TIMEOUT) -> None:
        if hasattr(stream, "settimeout"):
            stream.settimeout(timeout)

    def _upload_file_parallel(self, name: str, local_path: pathlib.Path, remote_path: str, mode: int) -> None:
        expected_size = local_path.stat().st_size
        ranges = self._transfer_ranges(expected_size, TRANSFER_PARALLEL_PARTS)
        part_paths = [f"{remote_path}.parallel-part-{index:02d}" for index in range(len(ranges))]
        temporary_path = f"{remote_path}.parallel-assembled"
        for part_path in part_paths:
            self._require_temp_path(name, part_path)
        self._require_temp_path(name, temporary_path)

        def upload_one(index: int) -> None:
            start, end = ranges[index]
            self._upload_range(name, local_path, part_paths[index], start, end)

        try:
            with ThreadPoolExecutor(max_workers=len(ranges), thread_name_prefix="sftp-upload") as executor:
                futures = [executor.submit(upload_one, index) for index in range(len(ranges))]
                for future in futures:
                    future.result()
        except BaseException:
            # Keep completed parts on the remote checkpoint.  The caller may
            # retry the same release path, and each part can resume without
            # retransmitting the other fifteen ranges.
            self._emit(name, event="transfer_parts_preserved", message="Parallel SFTP upload checkpoints preserved for retry", level="warn", details={"parts": len(part_paths)})
            raise

        quoted_parts = " ".join(shlex.quote(path) for path in part_paths)
        script = f'''set -Eeuo pipefail
final={shlex.quote(remote_path)}
assembled={shlex.quote(temporary_path)}
if [[ -L "$final" || -L "$assembled" ]]; then exit 91; fi
parts=({quoted_parts})
for part in "${{parts[@]}}"; do
  [[ -f "$part" && ! -L "$part" ]]
done
rm -f -- "$assembled"
cat -- "${{parts[@]}}" > "$assembled"
chmod {mode:o} "$assembled"
[[ $(stat -c '%s' "$assembled") == {expected_size} ]]
if [[ -e "$final" && ! -f "$final" ]]; then exit 92; fi
mv -f -- "$assembled" "$final"
for part in "${{parts[@]}}"; do rm -f -- "$part"; done
for part in "${{parts[@]}}"; do [[ ! -e "$part" ]]; done
printf 'assembled_size=%s\\n' "$(stat -c '%s' "$final")"
'''
        try:
            assembled = self.run(name, script, {"assembled_size"}, timeout=max(120, expected_size // (1024 * 1024) * 10)).values
        except BaseException:
            self._emit(name, event="transfer_parts_preserved", message="Parallel SFTP upload checkpoints preserved after assembly failure", level="warn", details={"parts": len(part_paths)})
            raise
        if int(assembled["assembled_size"]) != expected_size:
            raise OSError("remote assembled transfer size mismatch")

    def _cleanup_parallel_parts(self, name: str, remote_path: str, part_paths: list[str], temporary_path: str) -> None:
        paths = [*part_paths, temporary_path]
        for path in paths:
            self._require_temp_path(name, path)
        script = "rm -f -- " + " ".join(shlex.quote(path) for path in paths) + " && printf 'transfer_parts_cleaned=true\\n'"
        try:
            self.run(name, script, {"transfer_parts_cleaned"}, timeout=120)
        except BaseException as error:
            self._emit(name, event="transfer_cleanup_failed", message="Parallel SFTP temporary cleanup failed", level="warn", details={"error_type": type(error).__name__})

    def _upload_range(self, name: str, local_path: pathlib.Path, remote_path: str, start: int, end: int) -> None:
        expected_size = end - start
        for attempt in range(1, TRANSFER_MAX_ATTEMPTS + 1):
            client = None
            sftp = None
            try:
                client = self.connect(name)
                sftp = client.open_sftp()
                self._set_sftp_timeout(sftp)
                try:
                    attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                    remote_size = int(attributes.st_size)
                    remote_mode = getattr(attributes, "st_mode", None)
                    if remote_mode is not None and not stat.S_ISREG(remote_mode):
                        raise RuntimeError("remote transfer part is not a regular file")
                    if remote_size < 0:
                        raise RuntimeError("remote transfer part size is invalid")
                    if remote_size > expected_size:
                        sftp.remove(remote_path)
                        remote_size = 0
                except (OSError, IOError):
                    remote_size = 0
                if remote_size == expected_size:
                    return
                with local_path.open("rb") as source, sftp.file(remote_path, "ab") as target:
                    self._set_sftp_file_timeout(target)
                    if hasattr(target, "set_pipelined"):
                        target.set_pipelined(True)
                    source.seek(start + remote_size)
                    remaining = expected_size - remote_size
                    while remaining:
                        chunk = source.read(min(TRANSFER_CHUNK_BYTES, remaining))
                        if not chunk:
                            raise OSError("local transfer input ended before expected part size")
                        target.write(chunk)
                        remaining -= len(chunk)
                    target.flush()
                if int(sftp.stat(remote_path).st_size) != expected_size:
                    raise OSError("remote transfer part size mismatch")
                if attempt > 1:
                    self._emit(name, event="transfer_part_retry_succeeded", message="Parallel SFTP transfer part recovered", details={"attempt": attempt, "bytes": expected_size})
                return
            except (EOFError, OSError, IOError, socket.timeout, paramiko.SSHException) as error:
                if attempt == TRANSFER_MAX_ATTEMPTS:
                    raise
                self._emit(name, event="transfer_part_retry", message="Parallel SFTP transfer part retrying", details={"attempt": attempt, "next_attempt": attempt + 1, "error_type": type(error).__name__})
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                if sftp is not None:
                    sftp.close()
                if client is not None:
                    client.close()

    def download_file(self, name: str, remote_path: str, local_path: pathlib.Path) -> None:
        self._require_temp_path(name, remote_path)
        expected_size = self._remote_file_size(name, remote_path)
        if expected_size >= TRANSFER_PARALLEL_MIN_BYTES:
            self._download_file_parallel(name, remote_path, local_path, expected_size)
            return
        self._download_file_sequential(name, remote_path, local_path)

    def _remote_file_size(self, name: str, remote_path: str) -> int:
        for attempt in range(1, TRANSFER_MAX_ATTEMPTS + 1):
            client = None
            sftp = None
            try:
                client = self.connect(name)
                sftp = client.open_sftp()
                self._set_sftp_timeout(sftp)
                attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                expected_size = int(attributes.st_size)
                mode = getattr(attributes, "st_mode", None)
                if expected_size < 0 or mode is not None and not stat.S_ISREG(mode):
                    raise RuntimeError("remote transfer input is invalid")
                return expected_size
            except (EOFError, OSError, IOError, socket.timeout, paramiko.SSHException):
                if attempt == TRANSFER_MAX_ATTEMPTS:
                    raise
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                if sftp is not None:
                    sftp.close()
                if client is not None:
                    client.close()
        raise RuntimeError("remote transfer input size unavailable")

    def _download_file_sequential(self, name: str, remote_path: str, local_path: pathlib.Path) -> None:
        for attempt in range(1, TRANSFER_MAX_ATTEMPTS + 1):
            client = None
            sftp = None
            try:
                client = self.connect(name)
                sftp = client.open_sftp()
                self._set_sftp_timeout(sftp)
                attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                expected_size = int(attributes.st_size)
                mode = getattr(attributes, "st_mode", None)
                if expected_size < 0 or mode is not None and not stat.S_ISREG(mode):
                    raise RuntimeError("remote transfer input is invalid")
                current_size = local_path.stat().st_size if local_path.exists() else 0
                if local_path.is_symlink():
                    raise RuntimeError("local transfer output must not be a symlink")
                if current_size > expected_size:
                    local_path.write_bytes(b"")
                    current_size = 0
                with sftp.file(remote_path, "rb") as source, local_path.open("ab") as target:
                    self._set_sftp_file_timeout(source)
                    source.seek(current_size)
                    if hasattr(source, "prefetch"):
                        source.prefetch(expected_size, max_concurrent_requests=TRANSFER_DOWNLOAD_PREFETCH_REQUESTS)
                    remaining = expected_size - current_size
                    while remaining:
                        chunk = source.read(min(TRANSFER_CHUNK_BYTES, remaining))
                        if not chunk:
                            raise OSError("remote transfer input ended before expected size")
                        target.write(chunk)
                        target.flush()
                        remaining -= len(chunk)
                if local_path.stat().st_size != expected_size:
                    raise OSError("local transfer output size mismatch")
                return
            except (EOFError, OSError, IOError, socket.timeout, paramiko.SSHException) as error:
                if attempt == TRANSFER_MAX_ATTEMPTS:
                    raise
                self._emit(name, event="transfer_retry", message="SFTP download retrying from the local checkpoint", details={"attempt": attempt, "next_attempt": attempt + 1, "error_type": type(error).__name__})
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                if sftp is not None:
                    sftp.close()
                if client is not None:
                    client.close()

    def _download_file_parallel(self, name: str, remote_path: str, local_path: pathlib.Path, expected_size: int) -> None:
        if local_path.is_symlink():
            raise RuntimeError("local transfer output must not be a symlink")
        if local_path.exists() and not local_path.is_file():
            raise RuntimeError("local transfer output is not a regular file")
        if local_path.exists() and local_path.stat().st_size == expected_size:
            return
        if local_path.exists():
            local_path.unlink()

        ranges = self._transfer_ranges(expected_size, TRANSFER_PARALLEL_PARTS)
        part_paths = [pathlib.Path(f"{local_path}.parallel-part-{index:02d}") for index in range(len(ranges))]
        assembled_path = pathlib.Path(f"{local_path}.parallel-assembled")
        for path in [*part_paths, assembled_path]:
            if path.is_symlink():
                raise RuntimeError("local transfer checkpoint must not be a symlink")
            if path.exists() and not path.is_file():
                raise RuntimeError("local transfer checkpoint is not a regular file")

        def download_one(index: int) -> None:
            start, end = ranges[index]
            self._download_range(name, remote_path, part_paths[index], start, end, expected_size)

        with ThreadPoolExecutor(max_workers=len(ranges), thread_name_prefix="sftp-download") as executor:
            futures = [executor.submit(download_one, index) for index in range(len(ranges))]
            for future in futures:
                future.result()

        if assembled_path.exists():
            assembled_path.unlink()
        with assembled_path.open("wb") as target:
            for path, (start, end) in zip(part_paths, ranges):
                if path.is_symlink() or not path.is_file() or path.stat().st_size != end - start:
                    raise OSError("local transfer checkpoint size mismatch")
                with path.open("rb") as source:
                    while True:
                        chunk = source.read(TRANSFER_CHUNK_BYTES)
                        if not chunk:
                            break
                        target.write(chunk)
            target.flush()
            os.fsync(target.fileno())
        if assembled_path.stat().st_size != expected_size:
            raise OSError("local assembled transfer size mismatch")
        os.replace(assembled_path, local_path)
        for path in part_paths:
            path.unlink(missing_ok=True)

    def _download_range(
        self,
        name: str,
        remote_path: str,
        local_path: pathlib.Path,
        start: int,
        end: int,
        expected_file_size: int,
    ) -> None:
        expected_size = end - start
        for attempt in range(1, TRANSFER_MAX_ATTEMPTS + 1):
            client = None
            sftp = None
            try:
                client = self.connect(name)
                sftp = client.open_sftp()
                self._set_sftp_timeout(sftp)
                attributes = sftp.lstat(remote_path) if hasattr(sftp, "lstat") else sftp.stat(remote_path)
                remote_size = int(attributes.st_size)
                mode = getattr(attributes, "st_mode", None)
                if remote_size != expected_file_size or mode is not None and not stat.S_ISREG(mode):
                    raise RuntimeError("remote transfer input changed during download")
                if local_path.is_symlink():
                    raise RuntimeError("local transfer checkpoint must not be a symlink")
                current_size = local_path.stat().st_size if local_path.exists() else 0
                if current_size > expected_size:
                    local_path.unlink()
                    current_size = 0
                if current_size == expected_size:
                    return
                with sftp.file(remote_path, "rb") as source, local_path.open("ab") as target:
                    self._set_sftp_file_timeout(source)
                    source.seek(start + current_size)
                    if hasattr(source, "prefetch"):
                        # Paramiko treats ``file_size`` as an absolute end
                        # offset.  Limit prefetch to this range so sixteen
                        # workers do not each queue the entire remote file.
                        source.prefetch(end, max_concurrent_requests=TRANSFER_DOWNLOAD_PART_PREFETCH_REQUESTS)
                    remaining = expected_size - current_size
                    while remaining:
                        chunk = source.read(min(TRANSFER_CHUNK_BYTES, remaining))
                        if not chunk:
                            raise OSError("remote transfer input ended before expected part size")
                        target.write(chunk)
                        remaining -= len(chunk)
                    target.flush()
                if local_path.stat().st_size != expected_size:
                    raise OSError("local transfer checkpoint size mismatch")
                if attempt > 1:
                    self._emit(name, event="transfer_part_retry_succeeded", message="Parallel SFTP download part recovered", details={"attempt": attempt, "bytes": expected_size})
                return
            except (EOFError, OSError, IOError, socket.timeout, paramiko.SSHException) as error:
                if attempt == TRANSFER_MAX_ATTEMPTS:
                    raise
                self._emit(name, event="transfer_part_retry", message="Parallel SFTP download part retrying", details={"attempt": attempt, "next_attempt": attempt + 1, "error_type": type(error).__name__})
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                if sftp is not None:
                    sftp.close()
                if client is not None:
                    client.close()

    def copy_file_between(
        self,
        source_name: str,
        source_path: str,
        target_name: str,
        target_path: str,
        *,
        mode: int = 0o600,
        maximum_bytes: int = 256 * 1024 * 1024 * 1024,
        maximum_attempts: int = 3,
    ) -> int:
        """Stream one protected temporary file between two configured hosts.

        The controller never parses or persists the payload. Both endpoints
        must be inside temporary directories created by this runner.
        """
        self._require_temp_path(source_name, source_path)
        self._require_temp_path(target_name, target_path)
        if maximum_attempts < 1:
            raise ValueError("maximum_attempts must be positive")
        last_error: Exception | None = None
        for attempt in range(1, maximum_attempts + 1):
            source_client = target_client = None
            source_sftp = target_sftp = None
            try:
                source_client = self.connect(source_name)
                target_client = self.connect(target_name)
                source_sftp = source_client.open_sftp()
                target_sftp = target_client.open_sftp()
                attributes = source_sftp.lstat(source_path) if hasattr(source_sftp, "lstat") else source_sftp.stat(source_path)
                size = int(attributes.st_size)
                source_mode = getattr(attributes, "st_mode", None)
                if size <= 0 or size > maximum_bytes:
                    raise RuntimeError("remote transfer input size is invalid")
                if source_mode is not None and not stat.S_ISREG(source_mode):
                    raise RuntimeError("remote transfer input is not a regular file")
                with source_sftp.file(source_path, "rb") as source, target_sftp.file(target_path, "wb") as target:
                    remaining = size
                    while remaining:
                        chunk = source.read(min(1024 * 1024, remaining))
                        if not chunk:
                            raise RuntimeError("remote transfer input ended early")
                        target.write(chunk)
                        remaining -= len(chunk)
                    target.flush()
                target_sftp.chmod(target_path, mode)
                target_attributes = target_sftp.lstat(target_path) if hasattr(target_sftp, "lstat") else target_sftp.stat(target_path)
                if int(target_attributes.st_size) != size:
                    raise RuntimeError("remote transfer size differs at destination")
                if attempt > 1:
                    self._emit(source_name, event="transfer_retry_succeeded", message="Protected SFTP transfer recovered", details={"attempt": attempt, "bytes": size})
                return size
            except (paramiko.SSHException, EOFError, OSError, RuntimeError) as error:
                if isinstance(error, RuntimeError) and str(error) in {"remote transfer input size is invalid", "remote transfer input is not a regular file"}:
                    raise
                last_error = error
                if attempt >= maximum_attempts:
                    raise
                self._emit(source_name, event="transfer_retry", message="Protected SFTP transfer retrying", details={"attempt": attempt, "next_attempt": attempt + 1, "error_type": type(error).__name__})
                time.sleep(min(2 ** (attempt - 1), 4))
            finally:
                for handle in (source_sftp, target_sftp, source_client, target_client):
                    if handle is not None:
                        try:
                            handle.close()
                        except Exception:
                            pass
        raise RuntimeError("protected SFTP transfer failed") from last_error

    def create_temp_dir(self, name: str, base: str, prefix: str) -> str:
        if not base.startswith("/") or "/" in prefix or not prefix.replace("-", "").isalnum():
            raise ValueError("invalid remote temporary directory request")
        script = (
            f"test -d {shlex.quote(base)} && test ! -L {shlex.quote(base)} && "
            f"dir=$(mktemp -d {shlex.quote(base + '/' + prefix + '.XXXXXXXX')}) && chmod 700 \"$dir\" && "
            "test $(stat -c '%u:%a' \"$dir\") = $(id -u):700 && printf 'temp_dir=%s\\n' \"$dir\""
        )
        path = self.run(name, script, {"temp_dir"}).values["temp_dir"]
        if posixpath.dirname(path) != base or not posixpath.basename(path).startswith(prefix + "."):
            raise RuntimeError("remote returned an invalid temporary directory")
        self.temp_dirs.add((name, path))
        return path
