from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable

from .events import validate_event
from .redact import redact_value


@dataclass(frozen=True)
class LogQuery:
    node: str | None = None
    stage: str | None = None
    level: str | None = None
    since: datetime | timedelta | None = None
    tail: int | None = None

    def __post_init__(self) -> None:
        if self.node is not None and self.node not in {"local", "vm", "racknerd", "dmit", "backup"}:
            raise ValueError("invalid node filter")
        if self.level is not None and self.level not in {"debug", "info", "warn", "error"}:
            raise ValueError("invalid level filter")
        if self.tail is not None and self.tail < 0:
            raise ValueError("tail must not be negative")
        if isinstance(self.since, datetime) and self.since.tzinfo is None:
            raise ValueError("since must be timezone-aware")
        if isinstance(self.since, timedelta) and self.since.total_seconds() < 0:
            raise ValueError("since duration must not be negative")


@dataclass(frozen=True)
class LogReadIssue:
    path: str
    line: int
    error: str


@dataclass
class LogQueryResult:
    events: list[dict[str, Any]] = field(default_factory=list)
    issues: list[LogReadIssue] = field(default_factory=list)


def _threshold(since: datetime | timedelta | None, now: datetime) -> datetime | None:
    if since is None:
        return None
    if isinstance(since, timedelta):
        return now - since
    return since.astimezone(timezone.utc)


def query_events(
    paths: Iterable[Path | str],
    query: LogQuery | None = None,
    *,
    now: datetime | None = None,
) -> LogQueryResult:
    """Read, validate, filter and re-redact local structured logs.

    Malformed lines are reported as issues instead of aborting the entire
    query, making partially-written or older logs diagnosable.
    """

    filters = query or LogQuery()
    current = now or datetime.now(timezone.utc)
    if current.tzinfo is None:
        raise ValueError("now must be timezone-aware")
    threshold = _threshold(filters.since, current.astimezone(timezone.utc))
    result = LogQueryResult()
    matches: list[tuple[datetime, int, dict[str, Any]]] = []
    sequence = 0
    for raw_path in paths:
        path = Path(raw_path)
        if path.is_symlink():
            result.issues.append(LogReadIssue(str(path), 0, "symlink log rejected"))
            continue
        try:
            stat = path.stat()
            if not path.is_file():
                raise OSError("not a regular file")
            if stat.st_nlink != 1:
                raise OSError("multiple hard links rejected")
            stream = path.open("r", encoding="utf-8")
        except OSError as exc:
            detail = str(exc) if str(exc) else exc.__class__.__name__
            result.issues.append(LogReadIssue(str(path), 0, f"read failed: {detail}"))
            continue
        with stream:
            for line_number, line in enumerate(stream, start=1):
                try:
                    event = json.loads(line)
                    if not isinstance(event, dict):
                        raise ValueError("event must be an object")
                    validate_event(event)
                    timestamp = datetime.fromisoformat(event["timestamp"].replace("Z", "+00:00")).astimezone(timezone.utc)
                except (json.JSONDecodeError, ValueError) as exc:
                    result.issues.append(LogReadIssue(str(path), line_number, str(exc)))
                    continue
                if filters.node is not None and event["node"] != filters.node:
                    continue
                if filters.stage is not None and event["stage"] != filters.stage:
                    continue
                if filters.level is not None and event["level"] != filters.level:
                    continue
                if threshold is not None and timestamp < threshold:
                    continue
                matches.append((timestamp, sequence, redact_value(event)))
                sequence += 1
    matches.sort(key=lambda item: (item[0], item[1]))
    if filters.tail is not None:
        matches = matches[-filters.tail :] if filters.tail else []
    result.events = [item[2] for item in matches]
    return result
