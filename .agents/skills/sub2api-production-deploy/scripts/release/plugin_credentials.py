from __future__ import annotations

import json
from pathlib import Path

import yaml

from .plugin_package import SSH_CONFIG


_TARGETS = {"vm", "production"}
_MAX_CONFIG_BYTES = 1024 * 1024


def load_plugin_admin_credentials(target: str, *, path: Path = SSH_CONFIG) -> bytearray:
    if target not in _TARGETS:
        raise ValueError("unsupported plugin administrator credential target")
    if not path.is_file() or path.is_symlink() or path.stat().st_size > _MAX_CONFIG_BYTES:
        raise RuntimeError(".ssh.local plugin administrator configuration is unavailable")
    document = yaml.safe_load(path.read_text(encoding="utf-8"))
    plugin_admin = document.get("plugin_admin") if isinstance(document, dict) else None
    configured = plugin_admin.get(target) if isinstance(plugin_admin, dict) else None
    if not isinstance(configured, dict):
        raise RuntimeError(f"plugin_admin.{target} is missing from .ssh.local")
    api_key = configured.get("api_key")
    if not isinstance(api_key, str) or not api_key.strip():
        raise RuntimeError(f"plugin_admin.{target}.api_key is required")
    return bytearray(
        json.dumps(
            {"admin_api_key": api_key.strip()},
            separators=(",", ":"),
        ).encode("utf-8")
    )
