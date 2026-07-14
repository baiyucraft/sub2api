#!/usr/bin/env python3
"""Run a fixed, sanitized RackNerd status check through the configured SOCKS5 proxy."""

from __future__ import annotations

import argparse
import importlib
import importlib.metadata
import re
import socket
import sys
from dataclasses import dataclass
from pathlib import Path, PureWindowsPath
from typing import Any, Callable


PINNED_DEPENDENCIES = {
    "paramiko": "5.0.0",
    "PySocks": "1.7.1",
    "PyYAML": "6.0.3",
}
CONFIG_FIELDS = {
    "purpose",
    "host",
    "port",
    "user",
    "password",
    "private_key",
    "connection",
    "proxy",
    "proxy_command",
}
REQUIRED_FIELDS = {"host", "port", "user", "proxy", "proxy_command"}
STRING_FIELDS = CONFIG_FIELDS - {"port"}
PROXY_ENDPOINT_RE = re.compile(r"^(127\.0\.0\.1|localhost):([0-9]{1,5})$")
PROXY_COMMAND_RE = re.compile(
    r"^(?:\"(?P<quoted_exe>[A-Za-z]:[\\/][^\r\n\"]*[\\/]connect\.exe)\""
    r"|(?P<plain_exe>[A-Za-z]:[\\/][^\r\n\"]*[\\/]connect\.exe))"
    r"\s+-S\s+(?P<endpoint>(?:127\.0\.0\.1|localhost):[0-9]{1,5})"
    r"\s+%h\s+%p$",
    re.IGNORECASE,
)
ISO_UTC_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
APP_RE = re.compile(
    r"^(sha256:[0-9a-f]{64})\|(created|restarting|running|removing|paused|exited|dead)"
    r"\|(healthy|unhealthy|starting|none)$"
)
HTTP_RE = re.compile(r"^[1-5][0-9]{2}$")
SYSTEMD_ACTIVE_VALUES = {"active", "inactive", "failed", "activating", "deactivating"}
SYSTEMD_ENABLED_VALUES = {
    "enabled",
    "enabled-runtime",
    "disabled",
    "static",
    "indirect",
    "generated",
    "transient",
    "linked",
    "linked-runtime",
    "alias",
    "masked",
    "masked-runtime",
}
SOCKET_TIMEOUT_SECONDS = 15
COMMAND_TIMEOUT_SECONDS = 15
MAX_OUTPUT_BYTES = 512


class StatusCheckError(Exception):
    def __init__(self, category: str, check: str | None = None):
        super().__init__(category)
        self.category = category
        self.check = check


@dataclass(frozen=True)
class RackNerdConfig:
    host: str
    port: int
    user: str
    password: str | None
    private_key: str | None
    proxy_host: str
    proxy_port: int


@dataclass(frozen=True)
class Check:
    name: str
    command: str
    accepted_exit_codes: frozenset[int]
    parser: Callable[[str], dict[str, str]]


def check_dependencies() -> None:
    for distribution, expected_version in PINNED_DEPENDENCIES.items():
        try:
            actual_version = importlib.metadata.version(distribution)
        except importlib.metadata.PackageNotFoundError as exc:
            raise StatusCheckError("dependency_missing") from exc
        if actual_version != expected_version:
            raise StatusCheckError("dependency_version_mismatch")


def load_third_party_modules() -> tuple[Any, Any, Any]:
    check_dependencies()
    try:
        return (
            importlib.import_module("yaml"),
            importlib.import_module("paramiko"),
            importlib.import_module("socks"),
        )
    except ImportError as exc:
        raise StatusCheckError("dependency_missing") from exc


def _required_nonempty_string(mapping: dict[str, Any], field: str) -> str:
    value = mapping.get(field)
    if not isinstance(value, str) or not value or any(char in value for char in "\r\n\0"):
        raise StatusCheckError("config_invalid")
    return value


def _optional_credential(mapping: dict[str, Any], field: str) -> str | None:
    value = mapping.get(field)
    if value is None or value == "":
        return None
    if not isinstance(value, str) or any(char in value for char in "\r\n\0"):
        raise StatusCheckError("config_invalid")
    return value


def parse_proxy_command(proxy_value: str, proxy_command: str) -> tuple[str, int]:
    endpoint_match = PROXY_ENDPOINT_RE.fullmatch(proxy_value)
    command_match = PROXY_COMMAND_RE.fullmatch(proxy_command)
    if endpoint_match is None or command_match is None:
        raise StatusCheckError("proxy_config_unsupported")

    executable = PureWindowsPath(command_match.group("quoted_exe") or command_match.group("plain_exe"))
    if not executable.is_absolute() or executable.name.lower() != "connect.exe":
        raise StatusCheckError("proxy_config_unsupported")
    if command_match.group("endpoint") != proxy_value:
        raise StatusCheckError("proxy_config_unsupported")

    port = int(endpoint_match.group(2))
    if not 1 <= port <= 65535:
        raise StatusCheckError("proxy_config_unsupported")
    return endpoint_match.group(1), port


def parse_config_document(document: Any) -> RackNerdConfig:
    if not isinstance(document, dict) or set(document) != {"servers"}:
        raise StatusCheckError("config_invalid")
    servers = document["servers"]
    if not isinstance(servers, dict) or not isinstance(servers.get("racknerd"), dict):
        raise StatusCheckError("config_invalid")

    racknerd = servers["racknerd"]
    if not REQUIRED_FIELDS.issubset(racknerd) or set(racknerd) - CONFIG_FIELDS:
        raise StatusCheckError("config_invalid")
    for field in STRING_FIELDS & set(racknerd):
        if racknerd[field] is not None and not isinstance(racknerd[field], str):
            raise StatusCheckError("config_invalid")

    port = racknerd["port"]
    if isinstance(port, bool) or not isinstance(port, int) or not 1 <= port <= 65535:
        raise StatusCheckError("config_invalid")

    host = _required_nonempty_string(racknerd, "host")
    user = _required_nonempty_string(racknerd, "user")
    proxy_value = _required_nonempty_string(racknerd, "proxy")
    proxy_command = _required_nonempty_string(racknerd, "proxy_command")
    password = _optional_credential(racknerd, "password")
    private_key = _optional_credential(racknerd, "private_key")
    if (password is None) == (private_key is None):
        raise StatusCheckError("config_invalid")

    proxy_host, proxy_port = parse_proxy_command(proxy_value, proxy_command)
    return RackNerdConfig(
        host=host,
        port=port,
        user=user,
        password=password,
        private_key=private_key,
        proxy_host=proxy_host,
        proxy_port=proxy_port,
    )


def load_config(config_path: Path, yaml_module: Any) -> RackNerdConfig:
    try:
        raw_text = config_path.read_text(encoding="utf-8")
        document = yaml_module.safe_load(raw_text)
    except (OSError, UnicodeError, yaml_module.YAMLError) as exc:
        raise StatusCheckError("config_invalid") from exc
    return parse_config_document(document)


def parse_checked_at(value: str) -> dict[str, str]:
    if ISO_UTC_RE.fullmatch(value) is None:
        raise StatusCheckError("remote_output_invalid", "checked_at")
    return {"checked_at": value}


def parse_app(value: str) -> dict[str, str]:
    match = APP_RE.fullmatch(value)
    if match is None:
        raise StatusCheckError("remote_output_invalid", "app")
    if match.group(2) != "running" or match.group(3) != "healthy":
        raise StatusCheckError("remote_status_unhealthy", "app")
    return {
        "app_image_id": match.group(1),
        "app_state": match.group(2),
        "app_health": match.group(3),
    }


def parse_http(value: str) -> dict[str, str]:
    if HTTP_RE.fullmatch(value) is None:
        raise StatusCheckError("remote_output_invalid", "internal_health_http")
    if value != "200":
        raise StatusCheckError("remote_status_unhealthy", "internal_health_http")
    return {"internal_health_http": value}


def parse_systemd_active(field: str, expected: str) -> Callable[[str], dict[str, str]]:
    def parser(value: str) -> dict[str, str]:
        if value not in SYSTEMD_ACTIVE_VALUES:
            raise StatusCheckError("remote_output_invalid", field)
        if value != expected:
            raise StatusCheckError("remote_status_unhealthy", field)
        return {field: value}

    return parser


def parse_systemd_enabled(value: str) -> dict[str, str]:
    if value not in SYSTEMD_ENABLED_VALUES:
        raise StatusCheckError("remote_output_invalid", "backup_timer_enabled")
    if value != "enabled":
        raise StatusCheckError("remote_status_unhealthy", "backup_timer_enabled")
    return {"backup_timer_enabled": value}


CHECKS = (
    Check("checked_at", "date -u +%Y-%m-%dT%H:%M:%SZ", frozenset({0}), parse_checked_at),
    Check(
        "app",
        "docker inspect -f '{{.Image}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api",
        frozenset({0}),
        parse_app,
    ),
    Check(
        "internal_health_http",
        "curl -fsS -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/health",
        frozenset({0}),
        parse_http,
    ),
    Check(
        "nginx_active",
        "systemctl is-active nginx",
        frozenset({0, 3}),
        parse_systemd_active("nginx_active", "active"),
    ),
    Check(
        "backup_timer_active",
        "systemctl is-active sub2api-backup.timer",
        frozenset({0, 3}),
        parse_systemd_active("backup_timer_active", "active"),
    ),
    Check(
        "backup_timer_enabled",
        "systemctl is-enabled sub2api-backup.timer",
        frozenset({0, 1}),
        parse_systemd_enabled,
    ),
    Check(
        "backup_service_active",
        "systemctl is-active sub2api-backup.service",
        frozenset({0, 3}),
        parse_systemd_active("backup_service_active", "inactive"),
    ),
)


def run_remote_check(client: Any, check: Check) -> dict[str, str]:
    try:
        _stdin, stdout, stderr = client.exec_command(check.command, timeout=COMMAND_TIMEOUT_SECONDS)
        stdout.channel.settimeout(COMMAND_TIMEOUT_SECONDS)
        raw_stdout = stdout.read(MAX_OUTPUT_BYTES + 1)
        stderr.read(MAX_OUTPUT_BYTES + 1)
        exit_code = stdout.channel.recv_exit_status()
    except (OSError, socket.timeout) as exc:
        raise StatusCheckError("remote_command_timeout", check.name) from exc

    if exit_code not in check.accepted_exit_codes or len(raw_stdout) > MAX_OUTPUT_BYTES:
        raise StatusCheckError("remote_check_failed", check.name)
    try:
        value = raw_stdout.decode("ascii").strip()
    except UnicodeDecodeError as exc:
        raise StatusCheckError("remote_output_invalid", check.name) from exc
    return check.parser(value)


def connect_and_check(config: RackNerdConfig, paramiko_module: Any, socks_module: Any) -> dict[str, str]:
    proxy_socket = socks_module.socksocket()
    client = paramiko_module.SSHClient()
    try:
        proxy_socket.set_proxy(socks_module.SOCKS5, config.proxy_host, config.proxy_port, rdns=True)
        proxy_socket.settimeout(SOCKET_TIMEOUT_SECONDS)
        known_hosts = Path.home() / ".ssh" / "known_hosts"
        client.load_host_keys(str(known_hosts))
        client.set_missing_host_key_policy(paramiko_module.RejectPolicy())

        connect_kwargs: dict[str, Any] = {
            "hostname": config.host,
            "port": config.port,
            "username": config.user,
            "sock": proxy_socket,
            "timeout": SOCKET_TIMEOUT_SECONDS,
            "banner_timeout": SOCKET_TIMEOUT_SECONDS,
            "auth_timeout": SOCKET_TIMEOUT_SECONDS,
            "channel_timeout": COMMAND_TIMEOUT_SECONDS,
            "allow_agent": False,
            "look_for_keys": False,
        }
        if config.password is not None:
            connect_kwargs["password"] = config.password
        else:
            connect_kwargs["key_filename"] = config.private_key

        proxy_socket.connect((config.host, config.port))
        client.connect(**connect_kwargs)
        result: dict[str, str] = {}
        for check in CHECKS:
            result.update(run_remote_check(client, check))
        return result
    except paramiko_module.BadHostKeyException as exc:
        raise StatusCheckError("host_key_mismatch") from exc
    except paramiko_module.hostkeys.InvalidHostKey as exc:
        raise StatusCheckError("known_hosts_invalid") from exc
    except paramiko_module.AuthenticationException as exc:
        raise StatusCheckError("authentication_failed") from exc
    except paramiko_module.SSHException as exc:
        raise StatusCheckError("host_key_or_ssh_rejected") from exc
    except FileNotFoundError as exc:
        raise StatusCheckError("known_hosts_or_key_unavailable") from exc
    except (OSError, socket.timeout) as exc:
        raise StatusCheckError("proxy_or_connection_failed") from exc
    finally:
        client.close()
        proxy_socket.close()


def print_success(result: dict[str, str]) -> None:
    for field in (
        "checked_at",
        "app_image_id",
        "app_state",
        "app_health",
        "internal_health_http",
        "nginx_active",
        "backup_timer_active",
        "backup_timer_enabled",
        "backup_service_active",
    ):
        print(f"{field}={result[field]}")
    print("status=pass")


def print_failure(error: StatusCheckError) -> None:
    print("status=failed")
    print(f"error_category={error.category}")
    if error.check is not None:
        print(f"failed_check={error.check}")


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", type=Path, default=Path(".ssh.local"))
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(sys.argv[1:] if argv is None else argv)
    try:
        yaml_module, paramiko_module, socks_module = load_third_party_modules()
        config = load_config(args.config, yaml_module)
        result = connect_and_check(config, paramiko_module, socks_module)
        print_success(result)
        return 0
    except StatusCheckError as error:
        print_failure(error)
        return 1
    except Exception:
        print_failure(StatusCheckError("unexpected_failure"))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
