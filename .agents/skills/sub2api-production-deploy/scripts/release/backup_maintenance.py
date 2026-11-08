from __future__ import annotations

import hashlib
import json
import re
import shlex
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json, read_replaceable_text
from .environment import DeploymentEnvironment
from .paths import RELEASE_PACKAGE_ROOT
from .ssh import SSHRunner


UPLOAD_RESERVE_BYTES = 512 * 1024 * 1024
RELEASE_ID = re.compile(r"[1-9][0-9]*-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}")
PLAN_FIELDS = frozenset({
    "cleanup_mode", "cleanup_status", "plan_sha256", "candidate_count", "candidate_bytes",
    "candidate_release_ids", "free_before", "keep_recent", "keep_latest_profile_only",
    "latest_profile", "retention_days",
})
APPLY_FIELDS = frozenset({
    "cleanup_mode", "cleanup_status", "plan_sha256", "deleted_count", "deleted_bytes",
    "free_before", "free_after",
})
SPACE_PROBE = """set -Eeuo pipefail
root=/srv/sub2api-backups
test -d "$root"; test ! -L "$root"; test -w "$root"
observed=$(realpath -e -- "$root")
test "$observed" = "$root"
free_bytes=$(df -PB1 "$root" | awk 'NR==2 {print $4}')
printf 'backup_free_bytes=%s\\n' "$free_bytes"
"""


def _number(values: dict[str, str], field: str) -> int:
    value = values.get(field)
    if not isinstance(value, str) or not re.fullmatch(r"0|[1-9][0-9]*", value):
        raise RuntimeError(f"backup maintenance returned invalid {field}")
    return int(value)


def _plan(values: dict[str, str], release_id: str) -> list[str]:
    if set(values) != PLAN_FIELDS or values["cleanup_mode"] != "dry-run" or values["cleanup_status"] != "ready":
        raise RuntimeError("backup retention plan protocol is invalid")
    if not re.fullmatch(r"[0-9a-f]{64}", values["plan_sha256"]):
        raise RuntimeError("backup retention plan digest is invalid")
    if values["keep_recent"] != "1" or values["keep_latest_profile_only"] != "true":
        raise RuntimeError("backup retention policy changed")
    if _number(values, "retention_days") != 30:
        raise RuntimeError("backup retention policy changed")
    count, size = _number(values, "candidate_count"), _number(values, "candidate_bytes")
    _number(values, "free_before")
    ids = values["candidate_release_ids"].split(",") if values["candidate_release_ids"] else []
    if len(ids) != count or len(set(ids)) != count or count > 1024:
        raise RuntimeError("backup retention candidate count is invalid")
    if not ids:
        if size:
            raise RuntimeError("empty backup retention plan has nonzero bytes")
        raise RuntimeError("backup capacity is insufficient and no eligible release bundle exists")
    latest = _number(values, "latest_profile")
    if not size or not latest or any(not RELEASE_ID.fullmatch(value) for value in ids):
        raise RuntimeError("backup retention candidates are invalid")
    if release_id in ids or any(int(value.split("-", 1)[0]) > latest for value in ids):
        raise RuntimeError("backup retention plan contains a protected or inconsistent release")
    return ids


def _verified_apply(applied: dict[str, str], plan: dict[str, str], ids: list[str]) -> None:
    if (set(applied) != APPLY_FIELDS or applied["cleanup_mode"] != "apply"
            or applied["cleanup_status"] != "completed" or applied["plan_sha256"] != plan["plan_sha256"]
            or _number(applied, "deleted_count") != len(ids)
            or _number(applied, "deleted_bytes") != _number(plan, "candidate_bytes")):
        raise RuntimeError("backup cleanup receipt differs from the approved plan")
    _number(applied, "free_before")
    _number(applied, "free_after")


def ensure_backup_capacity(
    runner: SSHRunner,
    environment: DeploymentEnvironment,
    minimum_free_bytes: int,
    run_dir: Path,
    release_id: str,
) -> dict[str, Any]:
    """Perform one bounded, receipt-backed release-bundle cleanup if needed.

    Only release entrypoints call this function, while holding their release
    lock. Doctor remains read-only. A failed or uncertain apply is never retried.
    """
    if not RELEASE_ID.fullmatch(release_id) or type(minimum_free_bytes) is not int or minimum_free_bytes <= 0:
        raise ValueError("backup maintenance context is invalid")
    if run_dir.is_symlink():
        raise RuntimeError("backup maintenance receipt directory is unsafe")
    run_dir.mkdir(parents=True, exist_ok=True)
    receipt_path = run_dir / "backup-maintenance.json"
    required = minimum_free_bytes + UPLOAD_RESERVE_BYTES
    receipt: dict[str, Any] = {"schema": 1, "release_id": release_id, "required_free_bytes": required}
    if receipt_path.exists() or receipt_path.is_symlink():
        prior = json.loads(read_replaceable_text(receipt_path))
        if (not isinstance(prior, dict) or type(prior.get("schema")) is not int or prior.get("schema") != 1
                or prior.get("release_id") != release_id or prior.get("required_free_bytes") != required
                or prior.get("status") not in {"not_needed", "verified", "planned", "applying", "blocked"}):
            raise RuntimeError("existing backup maintenance receipt is invalid")
        if prior["status"] in {"planned", "applying", "blocked"}:
            raise RuntimeError("backup maintenance requires reconciliation; cleanup will not be repeated")
        if prior["status"] == "verified":
            if not isinstance(prior.get("plan"), dict) or not isinstance(prior.get("apply"), dict):
                raise RuntimeError("verified backup maintenance receipt is incomplete")
            _verified_apply(prior["apply"], prior["plan"], _plan(prior["plan"], release_id))
            if _number(prior["apply"], "free_after") < required:
                raise RuntimeError("verified backup maintenance receipt has insufficient capacity")
        receipt = prior

    def save(status: str) -> None:
        receipt["status"] = status
        atomic_write(receipt_path, canonical_json(receipt) + b"\n", mode=0o600)

    def current() -> None:
        environment.assert_current()
        runner.assert_configuration_current()

    node = environment.deployment["backup_upload"]["node"]

    def free_space() -> int:
        current()
        values = runner.run(node, SPACE_PROBE, {"backup_free_bytes"}, timeout=30).values
        if set(values) != {"backup_free_bytes"}:
            raise RuntimeError("backup capacity protocol is invalid")
        return _number(values, "backup_free_bytes")

    try:
        observed_free = free_space()
        if receipt.get("status") == "verified":
            receipt["last_observed_free_bytes"] = observed_free
            if observed_free < required:
                raise RuntimeError("backup capacity drifted after verified cleanup")
            save("verified")
            return receipt
        receipt["free_before"] = observed_free
        if observed_free >= required:
            save("not_needed")
            return receipt
        source = (RELEASE_PACKAGE_ROOT / "backup-release-retention-clean.sh").read_bytes()
        receipt["cleaner_sha256"] = hashlib.sha256(source).hexdigest()
        current()
        plan = runner.run_with_input(node, "bash -s -- dry-run", PLAN_FIELDS, source, timeout=600).values
        ids = _plan(plan, release_id)
        receipt["plan"] = plan
        save("planned")
        if _number(plan, "free_before") + _number(plan, "candidate_bytes") < required:
            raise RuntimeError("eligible backup bundles cannot provide the required upload reserve")
        current()
        # Persist intent before any deletion; never re-plan after a partial or
        # uncertain result. The cleaner revalidates the same plan under locks.
        save("applying")
        command = "bash -s -- apply " + " ".join(shlex.quote(value) for value in [plan["plan_sha256"], *ids])
        applied = runner.run_with_input(node, command, APPLY_FIELDS, source, timeout=600).values
        receipt["apply"] = applied
        _verified_apply(applied, plan, ids)
        if _number(applied, "free_after") < required:
            raise RuntimeError("backup cleanup did not provide the required upload reserve")
        receipt["free_after"] = free_space()
        if receipt["free_after"] < required:
            raise RuntimeError("backup capacity drifted after cleanup")
        save("verified")
        return receipt
    except BaseException as error:
        receipt["error_type"] = type(error).__name__
        save("blocked")
        raise
