from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Iterable


_TERMINAL_SUCCESS = frozenset({"verified", "success", "completed", "production_verified"})
_PERMANENT_STATUS = frozenset(
    {
        "failed",
        "recovered",
        "recovery",
        "blocked_reconciliation",
        "reconciliation",
        "unknown",
    }
)


def _canonical_json(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("utf-8")


@dataclass(frozen=True)
class ReleaseLogRecord:
    release_id: str
    path: Path
    created_at: datetime
    status: str
    current_baseline: bool = False
    active: bool = False
    has_recovery_point: bool = False
    has_reconciliation_evidence: bool = False

    def __post_init__(self) -> None:
        if not self.release_id or "/" in self.release_id or "\\" in self.release_id:
            raise ValueError("invalid release_id")
        if self.created_at.tzinfo is None:
            raise ValueError("created_at must be timezone-aware")


@dataclass(frozen=True)
class RetentionPlan:
    schema: int
    generated_at: str
    success_retention_days: int
    keep_recent: int
    delete: tuple[dict[str, str], ...]
    retain: tuple[dict[str, str], ...]
    plan_sha256: str

    def document(self, *, include_checksum: bool = True) -> dict[str, Any]:
        value: dict[str, Any] = {
            "schema": self.schema,
            "generated_at": self.generated_at,
            "success_retention_days": self.success_retention_days,
            "keep_recent": self.keep_recent,
            "delete": list(self.delete),
            "retain": list(self.retain),
        }
        if include_checksum:
            value["plan_sha256"] = self.plan_sha256
        return value


def _iso(value: datetime) -> str:
    return value.astimezone(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def _retention_reason(record: ReleaseLogRecord, recent_ids: set[str], cutoff: datetime) -> tuple[bool, str]:
    if record.active:
        return True, "active_release"
    if record.current_baseline:
        return True, "current_baseline"
    if record.has_recovery_point:
        return True, "recovery_point"
    if record.has_reconciliation_evidence:
        return True, "reconciliation_evidence"
    if record.status.lower() in _PERMANENT_STATUS:
        return True, "failure_or_recovery_evidence"
    if record.release_id in recent_ids:
        return True, "recent_release"
    if record.status.lower() not in _TERMINAL_SUCCESS:
        return True, "nonterminal_or_unrecognized_status"
    if record.created_at.astimezone(timezone.utc) >= cutoff:
        return True, "within_success_retention"
    return False, "expired_success"


def build_retention_plan(
    records: Iterable[ReleaseLogRecord],
    *,
    now: datetime | None = None,
    success_retention_days: int = 90,
    keep_recent: int = 10,
) -> RetentionPlan:
    if success_retention_days < 1:
        raise ValueError("success_retention_days must be positive")
    if keep_recent < 0:
        raise ValueError("keep_recent must not be negative")
    source_now = now or datetime.now(timezone.utc)
    if source_now.tzinfo is None:
        raise ValueError("now must be timezone-aware")
    generated = source_now.astimezone(timezone.utc)
    normalized = sorted(records, key=lambda item: (item.created_at.astimezone(timezone.utc), item.release_id), reverse=True)
    if len({item.release_id for item in normalized}) != len(normalized):
        raise ValueError("duplicate release_id")
    recent_ids = {item.release_id for item in normalized[:keep_recent]}
    cutoff = generated - timedelta(days=success_retention_days)
    delete: list[dict[str, str]] = []
    retain: list[dict[str, str]] = []
    for record in normalized:
        protected, reason = _retention_reason(record, recent_ids, cutoff)
        entry = {
            "release_id": record.release_id,
            "path": str(record.path),
            "created_at": _iso(record.created_at),
            "status": record.status,
            "reason": reason,
        }
        (retain if protected else delete).append(entry)
    unsigned = {
        "schema": 1,
        "generated_at": _iso(generated),
        "success_retention_days": success_retention_days,
        "keep_recent": keep_recent,
        "delete": delete,
        "retain": retain,
    }
    checksum = hashlib.sha256(_canonical_json(unsigned)).hexdigest()
    return RetentionPlan(
        schema=1,
        generated_at=unsigned["generated_at"],
        success_retention_days=success_retention_days,
        keep_recent=keep_recent,
        delete=tuple(delete),
        retain=tuple(retain),
        plan_sha256=checksum,
    )


def verify_retention_plan(plan: RetentionPlan | dict[str, Any], expected_sha256: str) -> bool:
    document = plan.document(include_checksum=False) if isinstance(plan, RetentionPlan) else dict(plan)
    embedded = document.pop("plan_sha256", None)
    actual = hashlib.sha256(_canonical_json(document)).hexdigest()
    if embedded is not None and embedded != actual:
        return False
    return actual == expected_sha256
