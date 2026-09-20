from __future__ import annotations

import base64
import hashlib
import json
import posixpath
import re
import shlex
from dataclasses import dataclass
from typing import Iterable

from .plugin_package import PLUGIN_ID, PackageIdentity
from .ssh import SSHResult, SSHRunner


PLUGIN_RESULT_FIELDS = {
    "operation",
    "installation_id",
    "previous_version",
    "previous_binary_sha256",
    "target_version",
    "previous_state",
    "final_state",
    "binary_sha256",
    "signature_status",
    "config_revision_preserved",
    "managed_scope_digest_preserved",
    "runtime_healthy",
    "instances_expected",
    "instances_verified",
    "restoration_status",
    "write_uncertain",
    "remote_error_status",
    "remote_error_class",
    "remote_error_code",
}


@dataclass(frozen=True)
class PluginApplyResult:
    values: dict[str, str]

    @property
    def operation(self) -> str:
        return self.values["operation"]

    @property
    def write_uncertain(self) -> bool:
        return self.values["write_uncertain"] == "true"


def select_operation(existing: dict | None, target_version: str, target_binary_sha256: str) -> str:
    if existing is None:
        return "install"
    if existing.get("plugin_key") != PLUGIN_ID:
        raise RuntimeError("plugin installation identity mismatch")
    if existing.get("version") == target_version and existing.get("binary_sha256") == target_binary_sha256:
        return "no-op"
    return "upgrade"


_REMOTE_HELPER = r'''
import hashlib, json, os, re, socket, sys, urllib.error, urllib.request, uuid

cfg = json.load(sys.stdin)
plugin_key = cfg["plugin_id"]
target_version = cfg["target_version"]
target_binary = cfg["target_binary_sha256"]
target_package = cfg["target_package"]
restore_packages = cfg.get("restore_packages", {})
restore_after = bool(cfg.get("restore_after"))
base_urls = cfg["base_urls"]
primary = base_urls[0].rstrip("/")
headers = {
    "Accept": "application/json",
    "User-Agent": "sub2api-plugin-release/1",
    "x-api-key": cfg["admin_api_key"],
}

class HTTPFailure(Exception):
    def __init__(self, status, payload):
        super().__init__("remote API request failed")
        self.status = status
        self.payload = payload

def request(base, path, method="GET", payload=None, raw=None, content_type="application/json", timeout=45):
    request_headers = dict(headers)
    data = raw
    if payload is not None:
        data = json.dumps(payload, separators=(",", ":")).encode()
    if data is not None:
        request_headers["Content-Type"] = content_type
    req = urllib.request.Request(base.rstrip("/") + path, data=data, headers=request_headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            body = response.read()
            parsed = json.loads(body) if body else {}
            return response.status, parsed.get("data", parsed)
    except urllib.error.HTTPError as error:
        body = error.read()
        try:
            parsed = json.loads(body) if body else {}
        except Exception:
            parsed = {}
        raise HTTPFailure(error.code, parsed)

def select_primary():
    global primary
    last_error = None
    for candidate in base_urls:
        try:
            primary = candidate.rstrip("/")
            plugins(primary)
            return
        except (socket.timeout, TimeoutError, urllib.error.URLError) as error:
            last_error = error
            continue
    raise RuntimeError("admin_endpoint_unavailable") from last_error

def plugins(base=None):
    selected = (base or primary).rstrip("/")
    _, data = request(selected, "/admin/plugins")
    if not isinstance(data, list):
        raise RuntimeError("plugin_list_invalid")
    return data

def current(base=None):
    found = [item for item in plugins(base) if item.get("plugin_key") == plugin_key]
    if len(found) > 1:
        raise RuntimeError("duplicate_plugin_installations")
    return found[0] if found else None

def digest_scope(item):
    raw = json.dumps(item.get("managed_scope") or [], sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(raw).hexdigest()

def multipart(path):
    boundary = "----sub2api-" + uuid.uuid4().hex
    content = open(path, "rb").read()
    filename = os.path.basename(path)
    body = (
        ("--" + boundary + "\r\nContent-Disposition: form-data; name=\"plugin\"; filename=\"" + filename + "\"\r\n"
         "Content-Type: application/octet-stream\r\n\r\n").encode() + content + ("\r\n--" + boundary + "--\r\n").encode()
    )
    return body, "multipart/form-data; boundary=" + boundary

def write_package(operation, installation_id, path):
    body, content_type = multipart(path)
    endpoint = "/admin/plugins/upload" if operation == "install" else "/admin/plugins/%s/upgrade" % installation_id
    return request(primary, endpoint, "POST", raw=body, content_type=content_type, timeout=120)[1]

def error_class(error):
    payload_text = json.dumps(error.payload, sort_keys=True, separators=(",", ":")).lower()
    for marker, category in (
        ("signature", "signature_validation"),
        ("签名", "signature_validation"),
        ("manifest", "manifest_validation"),
        ("清单", "manifest_validation"),
        ("compatib", "compatibility_validation"),
        ("不兼容", "compatibility_validation"),
        ("协议", "compatibility_validation"),
        ("duplicate", "duplicate_installation"),
        ("重复", "duplicate_installation"),
        ("trust", "publisher_trust"),
        ("信任", "publisher_trust"),
        ("超过", "payload_too_large"),
        ("too large", "payload_too_large"),
        ("zip", "archive_validation"),
        ("archive", "archive_validation"),
        ("插件包", "package_validation"),
        (".s2plugin", "multipart_upload"),
    ):
        if marker in payload_text:
            return category
    if not error.payload:
        return "empty_response"
    if error.status in (401, 403):
        return "authorization"
    if error.status in (413,):
        return "payload_too_large"
    if 400 <= error.status < 500:
        return "client_request"
    if 500 <= error.status < 600:
        return "server_error"
    return "http_error"

def error_code(error):
    candidates = []
    if isinstance(error.payload, dict):
        candidates.append(error.payload.get("code"))
        nested = error.payload.get("error")
        if isinstance(nested, dict):
            candidates.append(nested.get("code"))
    for candidate in candidates:
        if isinstance(candidate, str) and re.fullmatch(r"[A-Z][A-Z0-9_.-]{1,95}", candidate):
            return candidate
    return "none"

def delete_plugin(installation_id):
    request(primary, "/admin/plugins/%s" % installation_id, "DELETE")

select_primary()
before = current()
before_version = before.get("version") if before else "not_installed"
before_binary = before.get("binary_sha256") if before else "not_installed"
before_state = before.get("state") if before else "not_installed"
before_revision = str(before.get("config_revision", 0)) if before else "0"
before_scope = digest_scope(before) if before else hashlib.sha256(b"[]").hexdigest()
if before is None:
    operation = "install"
elif before.get("version") == target_version and before.get("binary_sha256") == target_binary:
    operation = "no-op"
else:
    operation = "upgrade"

restore_key = before_version + ":" + before_binary
if restore_after and operation == "upgrade" and not restore_packages.get(restore_key):
    raise RuntimeError("trusted_previous_package_required")

write_uncertain = "false"
remote_error_status = "none"
remote_error_class = "none"
remote_error_code = "none"
if operation != "no-op":
    try:
        write_package(operation, before.get("id") if before else 0, target_package)
    except HTTPFailure as error:
        remote_error_status = str(error.status)
        remote_error_class = error_class(error)
        remote_error_code = error_code(error)
    except (socket.timeout, TimeoutError, urllib.error.URLError):
        reconciled = current()
        if reconciled and reconciled.get("version") == target_version and reconciled.get("binary_sha256") == target_binary:
            write_uncertain = "reconciled"
        else:
            write_uncertain = "true"

after = current()
if write_uncertain == "true" or after is None or after.get("version") != target_version or after.get("binary_sha256") != target_binary:
    print("operation=" + operation)
    print("installation_id=" + str(after.get("id", 0) if after else 0))
    print("previous_version=" + before_version)
    print("previous_binary_sha256=" + before_binary)
    print("target_version=" + target_version)
    print("previous_state=" + before_state)
    print("final_state=" + (after.get("state", "unknown") if after else "unknown"))
    print("binary_sha256=" + (after.get("binary_sha256", "unknown") if after else "unknown"))
    print("signature_status=" + (after.get("signature_status", "unknown") if after else "unknown"))
    print("config_revision_preserved=false")
    print("managed_scope_digest_preserved=false")
    print("runtime_healthy=false")
    print("instances_expected=0")
    print("instances_verified=0")
    print("restoration_status=not_started")
    print("write_uncertain=" + write_uncertain)
    print("remote_error_status=" + remote_error_status)
    print("remote_error_class=" + remote_error_class)
    print("remote_error_code=" + remote_error_code)
    raise SystemExit(0)

if operation == "install" and after.get("state") != "disabled":
    raise RuntimeError("first_install_not_disabled")
revision_ok = operation == "install" or str(after.get("config_revision", 0)) == before_revision
scope_ok = operation == "install" or digest_scope(after) == before_scope
if operation == "upgrade" and after.get("state") != before_state:
    raise RuntimeError("upgrade_state_not_preserved")

expected = 0
verified = 0
runtime_healthy = True
for base in base_urls:
    try:
        item = current(base.rstrip("/"))
    except Exception:
        continue
    expected += 1
    if item and item.get("version") == target_version and item.get("binary_sha256") == target_binary:
        if item.get("state") != "enabled" or item.get("runtime_healthy") is True:
            verified += 1
        runtime_healthy = runtime_healthy and (item.get("state") != "enabled" or item.get("runtime_healthy") is True)

restoration = "not_required"
if restore_after:
    if before is None:
        delete_plugin(after["id"])
        restoration = "removed"
        if current() is not None:
            raise RuntimeError("vm_restore_remove_failed")
    elif operation == "upgrade":
        old_path = restore_packages.get(restore_key)
        if not old_path:
            raise RuntimeError("trusted_previous_package_required")
        write_package("upgrade", after["id"], old_path)
        restored = current()
        if not restored or restored.get("version") != before_version or restored.get("binary_sha256") != before.get("binary_sha256"):
            raise RuntimeError("vm_restore_upgrade_failed")
        restoration = "restored"
    else:
        restoration = "unchanged"

print("operation=" + operation)
print("installation_id=" + str(after["id"]))
print("previous_version=" + before_version)
print("previous_binary_sha256=" + before_binary)
print("target_version=" + target_version)
print("previous_state=" + before_state)
print("final_state=" + str(after.get("state", "unknown")))
print("binary_sha256=" + str(after.get("binary_sha256", "unknown")))
print("signature_status=" + str(after.get("signature_status", "unknown")))
print("config_revision_preserved=" + str(bool(revision_ok)).lower())
print("managed_scope_digest_preserved=" + str(bool(scope_ok)).lower())
print("runtime_healthy=" + str(bool(runtime_healthy)).lower())
print("instances_expected=" + str(expected))
print("instances_verified=" + str(verified))
print("restoration_status=" + restoration)
print("write_uncertain=" + write_uncertain)
print("remote_error_status=" + remote_error_status)
print("remote_error_class=" + remote_error_class)
print("remote_error_code=" + remote_error_code)
'''


class PluginAPIClient:
    def __init__(self, runner: SSHRunner | None = None) -> None:
        self.runner = runner or SSHRunner()

    def _base_urls(self, node: str) -> list[str]:
        if node in {"local_vm", "vm"}:
            ssh_node = "local_vm" if node == "vm" else node
            config = self.runner.servers.get(ssh_node)
            host = config.get("host") if isinstance(config, dict) else None
            if not isinstance(host, str) or not host.strip():
                raise RuntimeError("VM plugin API host is missing from .ssh.local")
            return [f"http://{host.strip()}:8211/api/v1"]
        if node == "racknerd":
            result = self.runner.run(
                node,
                """set -Eeuo pipefail
slot=/opt/sub2api/active-app
upstream=/etc/nginx/conf.d/sub2api-release-upstream.conf
test -f "$slot" && test ! -L "$slot"
test -f "$upstream" && test ! -L "$upstream"
test "$(grep -c '^port=' "$slot")" = 1
active_port=$(sed -n 's/^port=//p' "$slot")
case "$active_port" in 18080|18081) ;; *) exit 1 ;; esac
test "$(sed -nE 's/^[[:space:]]*server[[:space:]]+127[.]0[.]0[.]1:(18080|18081);[[:space:]]*$/\1/p' "$upstream" | wc -l)" = 1
upstream_port=$(sed -nE 's/^[[:space:]]*server[[:space:]]+127[.]0[.]0[.]1:(18080|18081);[[:space:]]*$/\1/p' "$upstream")
test "$upstream_port" = "$active_port"
printf 'active_port=%s\n' "$active_port"
""",
                {"active_port"},
            )
            active_port = result.values["active_port"]
            standby_port = "18081" if active_port == "18080" else "18080"
            return [
                f"http://127.0.0.1:{active_port}/api/v1",
                f"http://127.0.0.1:{standby_port}/api/v1",
            ]
        raise ValueError("unsupported plugin deployment node")

    def apply(
        self,
        *,
        node: str,
        identity: PackageIdentity,
        credentials: bytearray,
        restore_after: bool,
        previous_packages: Iterable[PackageIdentity] = (),
    ) -> PluginApplyResult:
        try:
            base_urls = self._base_urls(node)
            base = "/tmp" if node in {"local_vm", "vm"} else "/opt/sub2api"
            remote_dir = self.runner.create_temp_dir(node, base, "plugin-release")
            remote_target = posixpath.join(remote_dir, identity.path.name)
            self.runner.upload_file(node, identity.path, remote_target, 0o400)
            restore_map: dict[str, str] = {}
            for previous in previous_packages:
                if previous.arch != identity.arch or previous.signature_status != "trusted":
                    raise RuntimeError("previous plugin package identity is not trusted for this architecture")
                remote_previous = posixpath.join(remote_dir, previous.path.name)
                self.runner.upload_file(node, previous.path, remote_previous, 0o400)
                restore_map[f"{previous.version}:{previous.binary_sha256}"] = remote_previous

            auth = json.loads(bytes(credentials).decode("utf-8"))
            payload = {
                **auth,
                "plugin_id": PLUGIN_ID,
                "target_version": identity.version,
                "target_binary_sha256": identity.binary_sha256,
                "target_package": remote_target,
                "restore_packages": restore_map,
                "restore_after": restore_after,
                "base_urls": base_urls,
            }
            sensitive = bytearray(json.dumps(payload, separators=(",", ":")).encode("utf-8"))
            for key in list(auth):
                auth[key] = None
            encoded = base64.b64encode(_REMOTE_HELPER.encode("utf-8")).decode("ascii")
            script = (
                "set -Eeuo pipefail; umask 077; "
                f"trap 'rm -rf -- {shlex.quote(remote_dir)}' EXIT; "
                f"python3 -c \"import base64;exec(base64.b64decode('{encoded}'))\""
            )
            result = self.runner.run_with_sensitive_input(
                node,
                script,
                PLUGIN_RESULT_FIELDS,
                sensitive,
                timeout=240,
            )
        finally:
            for index in range(len(credentials)):
                credentials[index] = 0
        return PluginApplyResult(result.values)


def encode_credentials(admin_api_key: str) -> bytearray:
    return bytearray(
        json.dumps(
            {"admin_api_key": admin_api_key},
            separators=(",", ":"),
        ).encode("utf-8")
    )
