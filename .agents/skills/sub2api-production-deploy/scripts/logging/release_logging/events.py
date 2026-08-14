from __future__ import annotations

import json
import os
import re
import secrets
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Literal

from .redact import redact_text, redact_value


DeploymentMode = Literal["blue-green", "downtime", "not_applicable"]
Node = Literal["local", "vm", "racknerd", "dmit", "backup"]
Stream = Literal["stdout", "stderr", "event"]
Level = Literal["debug", "info", "warn", "error"]

SCHEMA_VERSION = 1
REQUIRED_FIELDS = frozenset(
    {
        "schema",
        "timestamp",
        "release_id",
        "deployment_mode",
        "node",
        "stage",
        "script",
        "command_id",
        "attempt",
        "stream",
        "level",
        "event",
        "message",
        "exit_code",
    }
)
_SAFE_COMPONENT = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")


def _utc_timestamp(now: datetime | None = None) -> str:
    value = now or datetime.now(timezone.utc)
    if value.tzinfo is None:
        raise ValueError("timestamp must be timezone-aware")
    return value.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _safe_component(name: str, value: str) -> str:
    if not _SAFE_COMPONENT.fullmatch(value):
        raise ValueError(f"invalid {name}")
    return value


@dataclass(frozen=True)
class EventContext:
    release_id: str
    deployment_mode: DeploymentMode
    node: Node

    def __post_init__(self) -> None:
        _safe_component("release_id", self.release_id)
        if self.deployment_mode not in {"blue-green", "downtime", "not_applicable"}:
            raise ValueError("invalid deployment_mode")
        if self.node not in {"local", "vm", "racknerd", "dmit", "backup"}:
            raise ValueError("invalid node")


def validate_event(event: dict[str, Any]) -> None:
    missing = REQUIRED_FIELDS.difference(event)
    if missing:
        raise ValueError(f"missing event fields: {', '.join(sorted(missing))}")
    if event["schema"] != SCHEMA_VERSION:
        raise ValueError("unsupported event schema")
    EventContext(event["release_id"], event["deployment_mode"], event["node"])
    for field in ("stage", "script", "command_id", "event"):
        _safe_component(field, event[field])
    if not isinstance(event["attempt"], int) or event["attempt"] < 1:
        raise ValueError("attempt must be a positive integer")
    if event["stream"] not in {"stdout", "stderr", "event"}:
        raise ValueError("invalid stream")
    if event["level"] not in {"debug", "info", "warn", "error"}:
        raise ValueError("invalid level")
    if event["exit_code"] is not None and not isinstance(event["exit_code"], int):
        raise ValueError("exit_code must be an integer or null")
    if not isinstance(event["message"], str):
        raise ValueError("message must be a string")
    try:
        parsed = datetime.fromisoformat(event["timestamp"].replace("Z", "+00:00"))
    except (AttributeError, ValueError) as exc:
        raise ValueError("invalid timestamp") from exc
    if parsed.tzinfo is None:
        raise ValueError("timestamp must include timezone")


class JSONLEventLogger:
    """Append redacted release events to a protected JSONL file."""

    def __init__(self, path: Path | str, context: EventContext):
        self.path = Path(path)
        self.context = context
        self._prepare_path()

    def _prepare_path(self) -> None:
        parent = self.path.parent
        parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if parent.is_symlink():
            raise RuntimeError("log directory must not be a symlink")
        if os.name != "nt":
            os.chmod(parent, 0o700)
        if self.path.exists() or self.path.is_symlink():
            stat = self.path.lstat()
            if self.path.is_symlink() or stat.st_nlink != 1:
                raise RuntimeError("log file must be a regular single-link file")
            if not self.path.is_file():
                raise RuntimeError("log path must be a regular file")
            if os.name != "nt":
                os.chmod(self.path, 0o600)

    def emit(
        self,
        *,
        stage: str,
        script: str,
        event: str,
        message: str,
        command_id: str | None = None,
        attempt: int = 1,
        stream: Stream = "event",
        level: Level = "info",
        exit_code: int | None = None,
        details: dict[str, Any] | None = None,
        timestamp: datetime | None = None,
    ) -> dict[str, Any]:
        document: dict[str, Any] = {
            "schema": SCHEMA_VERSION,
            "timestamp": _utc_timestamp(timestamp),
            "release_id": self.context.release_id,
            "deployment_mode": self.context.deployment_mode,
            "node": self.context.node,
            "stage": stage,
            "script": script,
            "command_id": command_id or secrets.token_hex(8),
            "attempt": attempt,
            "stream": stream,
            "level": level,
            "event": event,
            "message": redact_text(message),
            "exit_code": exit_code,
        }
        if details is not None:
            document["details"] = redact_value(details)
        validate_event(document)
        line = json.dumps(document, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8") + b"\n"
        self._append(line)
        return document

    def _append(self, line: bytes) -> None:
        self._prepare_path()
        flags = os.O_APPEND | os.O_CREAT | os.O_WRONLY
        if hasattr(os, "O_BINARY"):
            flags |= os.O_BINARY
        if hasattr(os, "O_NOFOLLOW"):
            flags |= os.O_NOFOLLOW
        descriptor = os.open(self.path, flags, 0o600)
        try:
            stat = os.fstat(descriptor)
            if stat.st_nlink != 1:
                raise RuntimeError("log file must have one hard link")
            if os.name != "nt" and hasattr(os, "fchmod"):
                os.fchmod(descriptor, 0o600)
            written = os.write(descriptor, line)
            if written != len(line):
                raise OSError("short write while appending release log")
            os.fsync(descriptor)
        finally:
            os.close(descriptor)
