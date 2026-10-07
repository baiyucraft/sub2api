from __future__ import annotations

import re
import shlex

from .manifest import sha256_file
from .paths import RELEASE_PACKAGE_ROOT, RUN_ROOT
from .ssh import SSHRunner
from .state import RunLock


PRODUCTION_RECOVERY_RETENTION_CLEANER = RELEASE_PACKAGE_ROOT / "production-recovery-retention-clean.sh"
PLAN_SHA = re.compile(r"^[0-9a-f]{64}$")
UINT_FIELDS = {
    "retention_days",
    "minimum_keep",
    "cutoff_epoch",
    "valid_count",
    "protected_count",
    "candidate_count",
    "candidate_bytes",
    "deleted_count",
    "deleted_bytes",
    "candidate_count_after",
    "root_free_before_bytes",
    "root_free_after_bytes",
}
SIGNED_FIELDS = {"root_free_delta_bytes"}
FIELDS = {
    "cleanup_mode",
    "cleanup_status",
    "plan_sha256",
    "retention_days",
    "minimum_keep",
    "cutoff_epoch",
    "valid_count",
    "protected_count",
    "candidate_count",
    "candidate_bytes",
    "candidate_ids",
    "deleted_count",
    "deleted_bytes",
    "candidate_count_after",
    "root_free_before_bytes",
    "root_free_after_bytes",
    "root_free_delta_bytes",
}


def _validate(values: dict[str, str], mode: str, expected_plan_sha256: str | None) -> None:
    if values["cleanup_mode"] != mode or values["cleanup_status"] != ("ready" if mode == "dry-run" else "completed"):
        raise RuntimeError("production recovery retention returned an invalid status")
    if not PLAN_SHA.fullmatch(values["plan_sha256"]):
        raise RuntimeError("production recovery retention returned an invalid plan checksum")
    if expected_plan_sha256 is not None and values["plan_sha256"] != expected_plan_sha256:
        raise RuntimeError("production recovery retention returned a different plan checksum")
    for field in UINT_FIELDS:
        if not re.fullmatch(r"[0-9]+", values[field]):
            raise RuntimeError("production recovery retention returned an invalid measurement")
    for field in SIGNED_FIELDS:
        if not re.fullmatch(r"-?[0-9]+", values[field]):
            raise RuntimeError("production recovery retention returned an invalid signed measurement")
    numbers = {field: int(values[field]) for field in UINT_FIELDS | SIGNED_FIELDS}
    if numbers["retention_days"] != 3 or numbers["minimum_keep"] != 3:
        raise RuntimeError("production recovery retention returned an invalid policy")
    if numbers["candidate_count_after"] != (numbers["candidate_count"] if mode == "dry-run" else 0):
        raise RuntimeError("production recovery retention returned an inconsistent candidate count")
    if mode == "dry-run" and (numbers["deleted_count"] != 0 or numbers["deleted_bytes"] != 0):
        raise RuntimeError("production recovery retention dry-run changed state")
    if mode == "apply" and (
        numbers["deleted_count"] != numbers["candidate_count"]
        or numbers["deleted_bytes"] != numbers["candidate_bytes"]
    ):
        raise RuntimeError("production recovery retention did not converge candidates")
    if numbers["root_free_after_bytes"] - numbers["root_free_before_bytes"] != numbers["root_free_delta_bytes"]:
        raise RuntimeError("production recovery retention returned an inconsistent filesystem delta")
    candidate_ids = values["candidate_ids"]
    if candidate_ids:
        for release_id in candidate_ids.split(","):
            if not re.fullmatch(r"(?:182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}", release_id):
                raise RuntimeError("production recovery retention returned an invalid candidate ID")


def cleanup_production_recovery_points(
    mode: str,
    plan_sha256: str | None = None,
    cutoff_epoch: int | None = None,
    runner: SSHRunner | None = None,
) -> dict[str, str]:
    if mode not in {"dry-run", "apply"}:
        raise ValueError("cleanup mode is invalid")
    if mode == "apply" and (plan_sha256 is None or not PLAN_SHA.fullmatch(plan_sha256)):
        raise ValueError("apply requires the exact dry-run plan SHA-256")
    if mode == "apply" and (cutoff_epoch is None or cutoff_epoch < 0):
        raise ValueError("apply requires the exact dry-run cutoff epoch")
    if mode == "dry-run" and plan_sha256 is not None:
        raise ValueError("dry-run does not accept a plan SHA-256")
    if mode == "dry-run" and cutoff_epoch is not None:
        raise ValueError("dry-run does not accept a cutoff epoch")
    runner = runner or SSHRunner()
    with RunLock(RUN_ROOT / ".release.lock"):
        remote_root = runner.create_temp_dir("racknerd", "/tmp", "production-recovery-retention")
        remote_cleaner = f"{remote_root}/production-recovery-retention-clean.sh"
        runner.upload_file("racknerd", PRODUCTION_RECOVERY_RETENTION_CLEANER, remote_cleaner, 0o700)
        try:
            expected_checksum = sha256_file(PRODUCTION_RECOVERY_RETENTION_CLEANER)
            runner.run(
                "racknerd",
                f"test $(sha256sum {shlex.quote(remote_cleaner)} | awk '{{print $1}}') = {shlex.quote(expected_checksum)} && printf 'cleaner_verified=true\\n'",
                {"cleaner_verified"},
            )
            command = " ".join(
                shlex.quote(value)
                for value in (remote_cleaner, mode, plan_sha256 or "-")
                + ((str(cutoff_epoch),) if mode == "apply" else ("-",))
            )
            values = runner.run("racknerd", command, FIELDS, timeout=1200).values
            _validate(values, mode, plan_sha256)
            if mode == "apply" and values["cutoff_epoch"] != str(cutoff_epoch):
                raise RuntimeError("production recovery retention used a different cutoff epoch")
            return values
        finally:
            runner.run(
                "racknerd",
                f"rm -rf -- {shlex.quote(remote_root)} && printf 'cleanup=true\\n'",
                {"cleanup"},
            )
