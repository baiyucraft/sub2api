from __future__ import annotations

import re
from collections.abc import Mapping, Sequence
from typing import Any


REDACTED = "[REDACTED]"

_SENSITIVE_KEY = re.compile(
    r"(?:^|[_-])(?:"
    r"authorization|proxy[_-]?authorization|cookie|set[_-]?cookie|"
    r"password|passwd|pwd|secret|client[_-]?secret|"
    r"access[_-]?token|refresh[_-]?token|id[_-]?token|token|"
    r"api[_-]?key|private[_-]?key|credentials?|"
    r"database[_-]?url|dsn|request[_-]?body"
    r")(?:$|[_-])",
    re.IGNORECASE,
)

_TEXT_PATTERNS: tuple[tuple[re.Pattern[str], str], ...] = (
    (
        re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----", re.IGNORECASE | re.DOTALL),
        REDACTED,
    ),
    (re.compile(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+"), r"\1 " + REDACTED),
    (
        re.compile(
            r"(?i)(\b(?:password|passwd|pwd|secret|client_secret|access_token|refresh_token|id_token|token|api[_-]?key|authorization|database_url|dsn)\b\s*[:=]\s*)(?:\"[^\"]*\"|'[^']*'|[^\s,;]+)"
        ),
        r"\1" + REDACTED,
    ),
    (
        re.compile(r"(?i)([?&](?:access_token|refresh_token|api[_-]?key|token|secret|password)=)[^&#\s]+"),
        r"\1" + REDACTED,
    ),
    (re.compile(r"(?i)\b(?:sk|rk|pk)-[A-Za-z0-9_-]{16,}\b"), REDACTED),
    (
        re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b"),
        REDACTED,
    ),
    (
        re.compile(r"(?i)(\b[a-z][a-z0-9+.-]*://)([^/@\s:]+):([^/@\s]+)@"),
        r"\1" + REDACTED + "@",
    ),
)


def is_sensitive_key(key: object) -> bool:
    """Return whether a mapping key represents secret material.

    Identifiers such as ``upstream_key_id`` and ``command_id`` are explicitly
    preserved: they are correlation metadata, not credentials.
    """

    normalized = str(key).strip().lower().replace("-", "_")
    if normalized.endswith("_id") or normalized in {"key_id", "upstream_key_id"}:
        return False
    return bool(_SENSITIVE_KEY.search(normalized)) or normalized in {
        "key",
        "headers",
        "body",
        "env",
        "environment",
    }


def redact_text(value: str) -> str:
    """Redact common inline credential forms from human-readable text."""

    result = value
    for pattern, replacement in _TEXT_PATTERNS:
        result = pattern.sub(replacement, result)
    return result


def redact_value(value: Any) -> Any:
    """Recursively redact structured values without changing their shape."""

    if isinstance(value, str):
        return redact_text(value)
    if isinstance(value, Mapping):
        return {
            str(key): REDACTED if is_sensitive_key(key) else redact_value(item)
            for key, item in value.items()
        }
    if isinstance(value, tuple):
        return tuple(redact_value(item) for item in value)
    if isinstance(value, Sequence) and not isinstance(value, (bytes, bytearray)):
        return [redact_value(item) for item in value]
    return value
