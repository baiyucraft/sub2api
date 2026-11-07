from __future__ import annotations

import hashlib
import ipaddress
import os
import re
import stat
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import yaml


DEPLOYMENT_FIELDS = frozenset({"schema", "public_domain", "health_routes", "backup_upload", "relay_probe", "vm_health"})
ROUTE_FIELDS = frozenset({"address", "observer", "target_node"})


def deployment_role_nodes(deployment: dict[str, Any]) -> dict[str, str]:
    return {
        "racknerd": deployment["health_routes"]["direct"]["target_node"],
        "dmit": deployment["relay_probe"]["node"],
        "backup": deployment["backup_upload"]["node"],
    }


def _mapping(value: Any, fields: frozenset[str], message: str) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != fields:
        raise RuntimeError(message)
    return value


def _port(value: Any) -> bool:
    return type(value) is int and 1 <= value <= 65535


def _address(value: Any) -> bool:
    if not isinstance(value, str) or not value or any(c in value for c in "/\\\x00\r\n"):
        return False
    candidate = value[1:-1] if value.startswith("[") and value.endswith("]") else value
    try:
        ipaddress.ip_address(candidate)
        return True
    except ValueError:
        return bool(re.fullmatch(
            r"(?=.{1,253}$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)(?:\.(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?))*",
            value,
        ))


def _validate(document: Any) -> tuple[dict[str, Any], dict[str, Any]]:
    if not isinstance(document, dict) or not isinstance(document.get("servers"), dict):
        raise RuntimeError("local SSH configuration is invalid")
    servers = document["servers"]
    deployment = _mapping(document.get("deployment"), DEPLOYMENT_FIELDS, "local deployment configuration is invalid")
    if type(deployment["schema"]) is not int or deployment["schema"] != 1:
        raise RuntimeError("unsupported local deployment configuration schema")
    if not _address(deployment["public_domain"]):
        raise RuntimeError("local public domain is invalid")

    routes = deployment["health_routes"]
    if not isinstance(routes, dict) or set(routes) != {"direct", "relay"}:
        raise RuntimeError("local health routes are invalid")
    for route in routes.values():
        route = _mapping(route, ROUTE_FIELDS, "local health route is invalid")
        try:
            ipaddress.ip_address(route["address"])
        except (TypeError, ValueError):
            raise RuntimeError("local health route address is invalid")
        if any(not isinstance(route[key], str) or route[key] not in servers for key in ("observer", "target_node")):
            raise RuntimeError("local health route node reference is invalid")

    for name, config in servers.items():
        if not isinstance(name, str) or not name or not isinstance(config, dict):
            raise RuntimeError("local SSH server configuration is invalid")
        if not _address(config.get("host")) or not _port(config.get("port", 22)):
            raise RuntimeError("local SSH server endpoint is invalid")
        relay = config.get("proxy_via")
        if relay is not None and (not isinstance(relay, str) or relay not in servers or relay == name):
            raise RuntimeError("local SSH relay reference is invalid")
        seen = {name}
        current = relay
        while current is not None:
            if current in seen:
                raise RuntimeError("local SSH proxy route contains a loop")
            seen.add(current)
            current = servers[current].get("proxy_via")
        if relay is not None and (servers[relay].get("proxy_via") or servers[relay].get("proxy")):
            raise RuntimeError("local SSH relay must be direct")

    backup = _mapping(deployment["backup_upload"], frozenset({"node", "user", "key_path", "port"}), "local backup upload configuration is invalid")
    if backup["node"] not in servers:
        raise RuntimeError("local backup upload identity is invalid")
    if (not isinstance(backup["user"], str)
            or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]{0,31}", backup["user"])):
            raise RuntimeError("local backup upload identity is invalid")
    if not isinstance(backup["key_path"], str) or not backup["key_path"].startswith("/") or ".." in Path(backup["key_path"]).parts:
        raise RuntimeError("local backup upload key path is invalid")
    if not _port(backup["port"]):
        raise RuntimeError("local backup upload port is invalid")

    probe = _mapping(
        deployment["relay_probe"],
        frozenset({"allowed_sources", "connect_port", "legacy_port", "listen_address", "listen_port", "node", "service"}),
        "local relay probe configuration is invalid",
    )
    if probe["node"] not in servers or not isinstance(probe["service"], str) or not probe["service"]:
        raise RuntimeError("local relay probe identity is invalid")
    try:
        ipaddress.ip_address(probe["listen_address"])
        sources = probe["allowed_sources"]
        if not isinstance(sources, list) or not sources:
            raise ValueError
        for source in sources:
            ipaddress.ip_network(source, strict=False)
    except (TypeError, ValueError):
        raise RuntimeError("local relay probe address is invalid") from None
    ports = [probe[key] for key in ("connect_port", "legacy_port", "listen_port")]
    if not all(_port(port) for port in ports) or len(set(ports)) != len(ports):
        raise RuntimeError("local relay probe ports are invalid")

    vm = _mapping(deployment["vm_health"], frozenset({"address", "port"}), "local VM health configuration is invalid")
    if not _address(vm["address"]) or not _port(vm["port"]):
        raise RuntimeError("local VM health endpoint is invalid")
    if "local_vm" not in servers:
        raise RuntimeError("local deployment configuration requires the local_vm SSH node")
    aliases = deployment_role_nodes(deployment)
    node_references = [backup["node"], probe["node"]]
    node_references.extend(route[key] for route in routes.values() for key in ("observer", "target_node"))
    node_references.extend(config["proxy_via"] for config in servers.values() if config.get("proxy_via") is not None)
    if any(aliases.get(node, node) != node for node in node_references):
        raise RuntimeError("local deployment node reference conflicts with a reserved role")
    return servers, deployment


def _read_configuration(path: Path) -> tuple[bytes, dict[str, Any]]:
    try:
        before = path.lstat()
        if not stat.S_ISREG(before.st_mode):
            raise RuntimeError("local SSH configuration path is unsafe")
        with path.open("rb") as stream:
            opened = os.fstat(stream.fileno())
            if not stat.S_ISREG(opened.st_mode) or not os.path.samefile(path, stream.name):
                raise RuntimeError("local SSH configuration changed while opening")
            raw = stream.read(1024 * 1024 + 1)
        after = path.lstat()
        if len(raw) > 1024 * 1024 or before.st_ino != after.st_ino or not os.path.samefile(path, stream.name):
            raise RuntimeError("local SSH configuration changed while reading")
        document = yaml.safe_load(raw)
    except (OSError, UnicodeError, yaml.YAMLError) as error:
        raise RuntimeError("local SSH configuration cannot be read") from error
    if not isinstance(document, dict):
        raise RuntimeError("local SSH configuration is invalid")
    return raw, document


@dataclass(frozen=True)
class DeploymentEnvironment:
    config_path: Path
    sha256: str
    servers: dict[str, Any]
    deployment: dict[str, Any]

    @classmethod
    def from_file(
        cls,
        path: Path,
        *,
        required: bool = True,
        expected_sha256: str | None = None,
    ) -> tuple[dict[str, Any], DeploymentEnvironment | None]:
        raw, document = _read_configuration(path)
        digest = hashlib.sha256(raw).hexdigest()
        expected = expected_sha256 or os.environ.get("SUB2API_DEPLOYMENT_CONFIG_SHA256")
        if expected is not None and digest != expected:
            raise RuntimeError("local deployment configuration drifted during the release")
        if "deployment" not in document:
            if required:
                raise RuntimeError("local deployment configuration is missing")
            return document, None
        servers, deployment = _validate(document)
        return document, cls(path.absolute(), digest, servers, deployment)

    @property
    def public_domain(self) -> str:
        return self.deployment["public_domain"]

    @property
    def direct_route(self) -> dict[str, str]:
        return self.deployment["health_routes"]["direct"]

    @property
    def relay_route(self) -> dict[str, str]:
        return self.deployment["health_routes"]["relay"]

    def profile_values(self) -> dict[str, str]:
        return {
            "public_domain": self.public_domain,
            "direct_public_ip": self.direct_route["address"],
            "relay_public_ip": self.relay_route["address"],
        }

    def node_for_role(self, role: str) -> str:
        return deployment_role_nodes(self.deployment).get(role, role)

    def assert_current(self) -> None:
        raw, _ = _read_configuration(self.config_path)
        if hashlib.sha256(raw).hexdigest() != self.sha256:
            raise RuntimeError("local deployment configuration changed during the release")


def load_deployment_environment(path: Path, *, expected_sha256: str | None = None) -> DeploymentEnvironment:
    _, environment = DeploymentEnvironment.from_file(path, required=True, expected_sha256=expected_sha256)
    assert environment is not None
    return environment


def pin_deployment_environment(path: Path) -> DeploymentEnvironment:
    environment = load_deployment_environment(path)
    expected = os.environ.get("SUB2API_DEPLOYMENT_CONFIG_SHA256")
    if expected is not None and expected != environment.sha256:
        raise RuntimeError("local deployment configuration drifted during the release")
    os.environ["SUB2API_DEPLOYMENT_CONFIG_SHA256"] = environment.sha256
    environment.assert_current()
    return environment
