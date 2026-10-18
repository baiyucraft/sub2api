"""Offline OCI Distribution fixture for the real VM build integration.

This file runs on the VM inside an isolated network namespace. It never uses
credentials, pulls an image, or changes the host daemon. Request traces contain
only protocol method, resource class, status, digest and byte count.
"""
from __future__ import annotations

import gzip
import hashlib
import io
import json
import os
import re
import socket
import ssl
import tarfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

INDEX = "application/vnd.oci.image.index.v1+json"
MANIFEST = "application/vnd.oci.image.manifest.v1+json"
CONFIG = "application/vnd.oci.image.config.v1+json"
LAYER = "application/vnd.oci.image.layer.v1.tar+gzip"
ROLES = ("node", "golang", "alpine", "postgres")


def sha256(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def json_bytes(value: Any) -> bytes:
    return json.dumps(value, separators=(",", ":"), sort_keys=True).encode()


def descriptor(data: bytes, media_type: str) -> dict[str, Any]:
    return {"mediaType": media_type, "digest": sha256(data), "size": len(data)}


def read_saved_image(path: Path) -> tuple[bytes, list[bytes]]:
    """Read a Docker save archive without extracting paths to the filesystem."""
    with tarfile.open(path) as archive:
        manifest_file = archive.extractfile("manifest.json")
        if manifest_file is None:
            raise ValueError("save_manifest_missing")
        manifests = json.load(manifest_file)
        if not isinstance(manifests, list) or len(manifests) != 1:
            raise ValueError("save_manifest_ambiguous")
        manifest = manifests[0]
        config_file = archive.extractfile(manifest["Config"])
        if config_file is None:
            raise ValueError("save_config_missing")
        config = config_file.read()
        layers = []
        for name in manifest["Layers"]:
            layer_file = archive.extractfile(name)
            if layer_file is None:
                raise ValueError("save_layer_missing")
            saved_layer = layer_file.read()
            # Docker's containerd-backed save can keep gzip blobs behind the
            # legacy manifest.json layer names. Diff IDs bind uncompressed tar.
            layers.append(gzip.decompress(saved_layer) if saved_layer.startswith(b"\x1f\x8b") else saved_layer)
    document = json.loads(config)
    if document.get("os") != "linux" or document.get("architecture") != "amd64":
        raise ValueError("frontend_platform")
    if document.get("rootfs", {}).get("diff_ids") != [sha256(layer) for layer in layers]:
        raise ValueError("save_layer_digest")
    return config, layers


class FixtureData:
    def __init__(self, frontend_archive: Path):
        self.blobs: dict[str, bytes] = {}
        self.manifests: dict[str, tuple[bytes, str]] = {}
        self.tags: dict[str, str] = {}
        self.role_data: dict[str, dict[str, Any]] = {}
        self.alternate_tags: dict[str, str] = {}
        for role in ROLES:
            for version in ("A", "B"):
                by_platform = []
                for architecture in ("amd64", "arm64"):
                    marker = ("fixture=" + role + ";version=" + version + ";arch=" + architecture + "\n").encode()
                    layer_tar = io.BytesIO()
                    with tarfile.open(fileobj=layer_tar, mode="w", format=tarfile.USTAR_FORMAT) as archive:
                        entry = tarfile.TarInfo("etc/os-release")
                        entry.size = len(marker)
                        entry.mode = 0o644
                        entry.mtime = 0
                        archive.addfile(entry, io.BytesIO(marker))
                    raw_layer = layer_tar.getvalue()
                    config = json_bytes({"architecture": architecture, "os": "linux", "config": {},
                                         "rootfs": {"type": "layers", "diff_ids": [sha256(raw_layer)]}})
                    manifest = self.add_image(config, [raw_layer])
                    desc = descriptor(manifest, MANIFEST)
                    desc["platform"] = {"os": "linux", "architecture": architecture}
                    by_platform.append(desc)
                    if architecture == "amd64" and version == "A":
                        document = json.loads(manifest)
                        self.role_data[role] = {"marker": marker, "manifest": sha256(manifest),
                                                "config": document["config"]["digest"],
                                                "layer": document["layers"][0]["digest"]}
                index = json_bytes({"schemaVersion": 2, "mediaType": INDEX, "manifests": by_platform})
                self.manifests[sha256(index)] = (index, INDEX)
                if version == "A":
                    self.tags[role] = sha256(index)
                else:
                    self.alternate_tags[role] = sha256(index)
        config, layers = read_saved_image(frontend_archive)
        frontend = self.add_image(config, layers)
        frontend_desc = descriptor(frontend, MANIFEST)
        frontend_desc["platform"] = {"os": "linux", "architecture": "amd64"}
        index = json_bytes({"schemaVersion": 2, "mediaType": INDEX, "manifests": [frontend_desc]})
        self.manifests[sha256(index)] = (index, INDEX)
        self.tags["frontend"] = sha256(index)
        frontend_doc = json.loads(frontend)
        self.role_data["frontend"] = {"manifest": sha256(frontend), "config": frontend_doc["config"]["digest"],
                                      "layers": [layer["digest"] for layer in frontend_doc["layers"]]}

    def add_image(self, config: bytes, raw_layers: list[bytes]) -> bytes:
        self.blobs[sha256(config)] = config
        layers = []
        for raw in raw_layers:
            compressed = gzip.compress(raw, mtime=0)
            self.blobs[sha256(compressed)] = compressed
            layers.append(descriptor(compressed, LAYER))
        manifest = json_bytes({"schemaVersion": 2, "mediaType": MANIFEST,
                               "config": descriptor(config, CONFIG), "layers": layers})
        self.manifests[sha256(manifest)] = (manifest, MANIFEST)
        return manifest


class Registry:
    def __init__(self, data: FixtureData, scenario: str, certificate: Path | None = None, key: Path | None = None):
        self.data = data
        self.scenario = scenario
        self.frozen = False
        self.trace: list[dict[str, Any]] = []
        self.lock = threading.Lock()
        registry = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, format: str, *args: object) -> None:
                pass

            def do_GET(self) -> None:
                self.respond(False)

            def do_HEAD(self) -> None:
                self.respond(True)

            def do_POST(self) -> None:
                self.send_resource("unsupported", "", b"", "application/json", 405, False)

            do_PUT = do_POST
            do_DELETE = do_POST
            do_PATCH = do_POST

            def send_resource(self, resource: str, digest: str, payload: bytes, media: str,
                              status: int, head: bool, truncated: bool = False) -> None:
                self.send_response(status)
                self.send_header("Docker-Distribution-Api-Version", "registry/2.0")
                self.send_header("Content-Type", media)
                self.send_header("Content-Length", str(len(payload)))
                if digest:
                    self.send_header("Docker-Content-Digest", digest)
                self.end_headers()
                sent = 0
                if not head:
                    body = payload[:max(1, len(payload) // 2)] if truncated else payload
                    try:
                        self.wfile.write(body)
                        self.wfile.flush()
                        sent = len(body)
                    except (BrokenPipeError, ConnectionResetError):
                        pass
                with registry.lock:
                    registry.trace.append({"method": self.command, "resource": resource, "status": status,
                                           "digest": digest, "byte_count": sent})
                if truncated:
                    self.close_connection = True

            def respond(self, head: bool) -> None:
                if self.path in ("/v2/", "/v2"):
                    self.send_resource("version", "", b"{}", "application/json", 200, head)
                    return
                match = re.fullmatch(r"/v2/fixture/(node|golang|alpine|postgres|frontend)/(manifests|blobs)/([^/?]+)", self.path)
                if not match:
                    self.send_resource("unsupported", "", b"{}", "application/json", 404, head)
                    return
                role, kind, reference = match.groups()
                is_digest = bool(re.fullmatch(r"sha256:[0-9a-f]{64}", reference))
                resource = "blob" if kind == "blobs" else "manifest_digest" if is_digest else "manifest_tag"
                if registry.scenario == "auth_error" and role == "node":
                    self.send_resource(resource, "", b'{"errors":[{"code":"UNAUTHORIZED"}]}', "application/json", 401, head)
                    return
                if kind == "manifests":
                    digest = reference if is_digest else registry.data.tags[role] if reference == "v1" else ""
                    if registry.frozen and registry.scenario == "tag_drift" and not is_digest and role in ROLES:
                        digest = registry.data.alternate_tags[role]
                    if registry.scenario == "tag_head_502" and not is_digest and head and role == "golang":
                        self.send_resource(resource, "", b'{"errors":[{"code":"UNKNOWN"}]}', "application/json", 502, head)
                        return
                    missing = registry.scenario == "frontend_missing" and role == "frontend"
                    missing = missing or (registry.scenario == "manifest_missing" and role == "golang" and is_digest and digest == registry.data.role_data[role]["manifest"])
                    if missing or digest not in registry.data.manifests:
                        self.send_resource(resource, "", b'{"errors":[{"code":"MANIFEST_UNKNOWN"}]}', "application/json", 404, head)
                        return
                    payload, media = registry.data.manifests[digest]
                    if registry.scenario == "wrong_digest" and role == "golang" and is_digest and digest == registry.data.role_data[role]["manifest"]:
                        payload = payload.replace(b'"schemaVersion":2', b'"schemaVersion":3')
                    self.send_resource(resource, digest, payload, media, 200, head)
                else:
                    if reference not in registry.data.blobs or (registry.scenario == "config_missing" and role == "golang" and reference == registry.data.role_data[role]["config"]):
                        self.send_resource(resource, reference if is_digest else "", b'{"errors":[{"code":"BLOB_UNKNOWN"}]}', "application/json", 404, head)
                        return
                    truncated = registry.scenario == "layer_truncated" and role == "golang" and reference == registry.data.role_data[role]["layer"] and not head
                    self.send_resource(resource, reference, registry.data.blobs[reference], "application/octet-stream", 200, head, truncated)

        class DualProtocolServer(ThreadingHTTPServer):
            def get_request(self):
                connection, address = super().get_request()
                connection.settimeout(10)
                # Imagetools defaults to HTTP for loopback; the Docker driver's
                # frontend resolver selects HTTPS. Both use the same real OCI
                # resources and digest identities on this isolated address.
                if connection.recv(1, socket.MSG_PEEK) == b"\x16":
                    connection = self.tls_context.wrap_socket(connection, server_side=True)
                return connection, address

        self.server = DualProtocolServer(("127.0.0.1", 0), Handler)
        if certificate is not None and key is not None:
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(str(certificate), str(key))
            self.server.tls_context = context
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def address(self) -> str:
        return "127.0.0.1:" + str(self.server.server_address[1])

    def start(self) -> None:
        self.thread.start()

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def write_trace(self, path: Path) -> None:
        path.write_bytes(b"\n".join(json_bytes(row) for row in self.trace) + b"\n")
        os.chmod(path, 0o600)
