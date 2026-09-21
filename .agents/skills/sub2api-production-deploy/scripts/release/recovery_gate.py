from __future__ import annotations

import hashlib
import json
import re
import subprocess
from pathlib import Path
from typing import Any, Iterable

from .process import check_output_hidden, run_hidden


REPORT_SCHEMA = 1
MODES = frozenset({"fast", "specialized", "full"})
ESTIMATED_EXTRA_SECONDS = {
    "fast": 0,
    "specialized": 600,
    "full": 2400,
}
FULL_SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")

_RECOVERY_PREFIX = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/"
_RELEASE_STATE_MACHINE_FILES = frozenset(
    {
        ".agents/skills/sub2api-production-deploy/scripts/release/cli.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/doctor.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/gate.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/manifest.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/production.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/production_snapshot.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/state.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/supervisor.py",
        ".agents/skills/sub2api-production-deploy/scripts/release/vm_validate.py",
    }
)
_SPECIALIZED_PREFIXES = ("backend/migrations/",)


def _normalize_paths(paths: Iterable[str]) -> list[str]:
    normalized: set[str] = set()
    for path in paths:
        if not path:
            continue
        value = path.replace("\\", "/")
        if value.startswith("./"):
            value = value[2:]
        normalized.add(value)
    return sorted(normalized)


def changed_paths_sha256(paths: Iterable[str]) -> str:
    normalized = _normalize_paths(paths)
    payload = json.dumps(normalized, ensure_ascii=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _is_specialized_path(path: str) -> tuple[bool, str | None]:
    lowered = path.lower()
    if path.startswith(_SPECIALIZED_PREFIXES):
        return True, "migration_changed"
    name = Path(lowered).name
    if "docker-compose" in name or name.startswith("compose.") or name in {"compose.yml", "compose.yaml"}:
        return True, "compose_changed"
    if "redis" in lowered:
        return True, "redis_changed"
    if "postgres" in lowered or "postgresql" in lowered:
        return True, "postgres_changed"
    return False, None


def _changed_paths(workspace: Path, base_commit: str, target_commit: str) -> list[str]:
    output = check_output_hidden(
        ["git", "diff", "--name-only", "-z", base_commit, target_commit, "--"],
        cwd=workspace,
    )
    assert isinstance(output, bytes)
    return _normalize_paths(item.decode("utf-8") for item in output.split(b"\0") if item)


def _is_ancestor(workspace: Path, base_commit: str, target_commit: str) -> bool:
    result = run_hidden(
        ["git", "merge-base", "--is-ancestor", base_commit, target_commit],
        cwd=workspace,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def _commit_exists(workspace: Path, commit: str) -> bool:
    result = run_hidden(
        ["git", "cat-file", "-e", f"{commit}^{{commit}}"],
        cwd=workspace,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
    )
    return result.returncode == 0


def _unproven_report(target_commit: str, base_commit: str | None = None) -> dict[str, Any]:
    return validate_report(
        {
            "schema": REPORT_SCHEMA,
            "mode": "specialized",
            "base_commit": base_commit,
            "target_commit": target_commit,
            "reason_codes": ["production_commit_unproven"],
            "changed_paths_sha256": changed_paths_sha256([]),
            "estimated_extra_seconds": ESTIMATED_EXTRA_SECONDS["specialized"],
        },
        target_commit=target_commit,
    )


def validate_report(report: dict[str, Any], *, target_commit: str | None = None) -> dict[str, Any]:
    expected_fields = {
        "schema",
        "mode",
        "base_commit",
        "target_commit",
        "reason_codes",
        "changed_paths_sha256",
        "estimated_extra_seconds",
    }
    if not isinstance(report, dict) or set(report) != expected_fields:
        raise RuntimeError("recovery Gate report shape is invalid")
    if report.get("schema") != REPORT_SCHEMA or report.get("mode") not in MODES:
        raise RuntimeError("recovery Gate report identity is invalid")
    base_commit = report.get("base_commit")
    if base_commit is not None and not FULL_SHA.fullmatch(str(base_commit)):
        raise RuntimeError("recovery Gate base commit is invalid")
    report_target = str(report.get("target_commit", ""))
    if not FULL_SHA.fullmatch(report_target) or (target_commit is not None and report_target != target_commit):
        raise RuntimeError("recovery Gate target commit is invalid")
    reasons = report.get("reason_codes")
    if (
        not isinstance(reasons, list)
        or any(not isinstance(reason, str) or not reason for reason in reasons)
        or reasons != sorted(set(reasons))
    ):
        raise RuntimeError("recovery Gate reason codes are invalid")
    if not SHA256.fullmatch(str(report.get("changed_paths_sha256", ""))):
        raise RuntimeError("recovery Gate changed-path checksum is invalid")
    if report.get("estimated_extra_seconds") != ESTIMATED_EXTRA_SECONDS[report["mode"]]:
        raise RuntimeError("recovery Gate time estimate is invalid")
    return dict(report)


def classify(workspace: Path, base_commit: str | None, target_commit: str) -> dict[str, Any]:
    if not FULL_SHA.fullmatch(target_commit):
        raise ValueError("target commit must be a complete 40-character lowercase SHA")
    if base_commit is None or not FULL_SHA.fullmatch(base_commit):
        return _unproven_report(target_commit)
    if not _commit_exists(workspace, target_commit):
        raise ValueError("target commit is not available in the local repository")
    if not _commit_exists(workspace, base_commit):
        return _unproven_report(target_commit, base_commit)

    paths = _changed_paths(workspace, base_commit, target_commit)
    reasons: set[str] = set()
    mode = "fast"
    if not _is_ancestor(workspace, base_commit, target_commit):
        mode = "specialized"
        reasons.add("base_commit_not_ancestor")
    for path in paths:
        if path.startswith(_RECOVERY_PREFIX) or path in _RELEASE_STATE_MACHINE_FILES:
            mode = "specialized"
            reasons.add("release_state_machine_changed")
            if path.startswith(_RECOVERY_PREFIX):
                reasons.add("recovery_logic_changed")
            continue
        specialized, reason = _is_specialized_path(path)
        if specialized and mode != "full":
            mode = "specialized"
        if reason:
            reasons.add(reason)
    if not reasons:
        reasons.add("ordinary_change")
    return validate_report(
        {
            "schema": REPORT_SCHEMA,
            "mode": mode,
            "base_commit": base_commit,
            "target_commit": target_commit,
            "reason_codes": sorted(reasons),
            "changed_paths_sha256": changed_paths_sha256(paths),
            "estimated_extra_seconds": ESTIMATED_EXTRA_SECONDS[mode],
        },
        target_commit=target_commit,
    )


def require_specialized(report: dict[str, Any], reason: str) -> dict[str, Any]:
    value = validate_report(report, target_commit=str(report.get("target_commit", "")))
    if not reason:
        raise ValueError("recovery Gate escalation reason is required")
    if value["mode"] == "fast":
        value["mode"] = "specialized"
        value["estimated_extra_seconds"] = ESTIMATED_EXTRA_SECONDS["specialized"]
    value["reason_codes"] = sorted(set(value["reason_codes"]) | {reason})
    return validate_report(value, target_commit=value["target_commit"])


def require_full(report: dict[str, Any], reason: str = "manual_full_drill") -> dict[str, Any]:
    value = validate_report(report, target_commit=str(report.get("target_commit", "")))
    if not reason:
        raise ValueError("full recovery Gate reason is required")
    value["mode"] = "full"
    value["estimated_extra_seconds"] = ESTIMATED_EXTRA_SECONDS["full"]
    value["reason_codes"] = sorted(set(value["reason_codes"]) | {reason})
    return validate_report(value, target_commit=value["target_commit"])


def unproven_report(target_commit: str) -> dict[str, Any]:
    return classify(Path.cwd(), None, target_commit)
