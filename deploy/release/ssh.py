from __future__ import annotations

import io
import json
import pathlib
import shlex
import posixpath
from dataclasses import dataclass
from typing import Iterable

import paramiko
import socks
import yaml


ROOT = pathlib.Path(__file__).resolve().parents[2]
SSH_CONFIG = ROOT / ".ssh.local"
KNOWN_HOSTS = ROOT / ".tmp" / "known_hosts"


@dataclass
class SSHResult:
    values: dict[str, str]


class SSHRunner:
    def __init__(self) -> None:
        document = yaml.safe_load(SSH_CONFIG.read_text(encoding="utf-8"))
        self.servers = document["servers"]
        self.temp_dirs: set[tuple[str, str]] = set()

    def _require_temp_path(self, name: str, remote_path: str) -> None:
        normalized = posixpath.normpath(remote_path)
        roots = getattr(self, "temp_dirs", set())
        if not any(host == name and (normalized == root or normalized.startswith(root + "/")) for host, root in roots):
            raise RuntimeError("SFTP path is outside a registered remote temporary directory")

    def connect(self, name: str) -> paramiko.SSHClient:
        config = self.servers[name]
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
            client.close()
            raise RuntimeError(f"{name} SSH transport is unavailable")
        transport.set_keepalive(30)
        return client

    def run(self, name: str, script: str, allowed: Iterable[str], timeout: int = 120) -> SSHResult:
        client = self.connect(name)
        try:
            command = "bash -lc " + shlex.quote(script)
            _, stdout, stderr = client.exec_command(command, timeout=timeout, get_pty=False)
            output = stdout.read().decode("utf-8", "strict")
            error_output = stderr.read().decode("utf-8", "replace")
            exit_code = stdout.channel.recv_exit_status()
            if exit_code:
                raise RuntimeError(f"{name} stage failed with exit code {exit_code}; remote stderr withheld")
            values: dict[str, str] = {}
            allowlist = set(allowed)
            for line in output.splitlines():
                if not line or "=" not in line:
                    raise RuntimeError(f"{name} returned non-structured output")
                key, value = line.split("=", 1)
                if key not in allowlist:
                    raise RuntimeError(f"{name} returned an undeclared field: {key}")
                values[key] = value
            if error_output.strip():
                raise RuntimeError(f"{name} returned unexpected stderr")
            missing = allowlist.difference(values)
            if missing:
                raise RuntimeError(f"{name} omitted required fields: {sorted(missing)}")
            return SSHResult(values)
        finally:
            client.close()

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
        client = self.connect(name)
        try:
            sftp = client.open_sftp()
            try:
                sftp.put(str(local_path), remote_path)
                sftp.chmod(remote_path, mode)
            finally:
                sftp.close()
        finally:
            client.close()

    def download_file(self, name: str, remote_path: str, local_path: pathlib.Path) -> None:
        self._require_temp_path(name, remote_path)
        client = self.connect(name)
        try:
            sftp = client.open_sftp()
            try:
                sftp.get(remote_path, str(local_path))
            finally:
                sftp.close()
        finally:
            client.close()

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
