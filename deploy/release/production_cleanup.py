from __future__ import annotations

import json
import re
import shlex
import subprocess
from dataclasses import dataclass
from pathlib import Path

from .manifest import sha256_file, validate_image_id
from .ssh import SSHRunner
from .state import RunLock


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
WORKSPACE = Path(__file__).resolve().parents[2]
RUN_ROOT = WORKSPACE / ".tmp" / "releases"
TRUSTED_VM_PUBLIC_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
PRODUCTION_SPACE_CLEANER = DEPLOY_ROOT / "release" / "production-space-clean.sh"
RELEASE_ID = re.compile(r"^(182|187|191|192|194|195|197|198|199)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$")
FINAL_STAGES = {"production_verified", "production_verified_after_reconciliation"}
PRODUCTION_CLEAN_FIELDS = {
    "cleanup_mode",
    "cleanup_status",
    "plan_sha256",
    "release_id",
    "current_image_id",
    "pre_switch_image_id",
    "root_free_before_bytes",
    "root_free_after_bytes",
    "root_free_delta_bytes",
    "containerd_free_before_bytes",
    "containerd_free_after_bytes",
    "containerd_free_delta_bytes",
    "migration_evidence_containers",
    "image_candidates",
    "image_candidates_after",
    "image_candidate_logical_bytes",
    "removed_images",
    "build_cache_records_before",
    "build_cache_records_after",
    "build_cache_policy",
    "build_cache_gc_attempted",
}


@dataclass(frozen=True)
class CleanupIdentity:
    release_id: str
    current_image_id: str
    pre_switch_image_id: str


def validate_cleanup_evidence(
    values: dict[str, str], identity: CleanupIdentity, mode: str, expected_plan_sha256: str | None
) -> None:
    if (
        values["release_id"] != identity.release_id
        or values["current_image_id"] != identity.current_image_id
        or values["pre_switch_image_id"] != identity.pre_switch_image_id
    ):
        raise RuntimeError("production cleanup returned a different protected identity")
    expected_status = "ready" if mode == "dry-run" else "completed"
    if values["cleanup_mode"] != mode or values["cleanup_status"] != expected_status:
        raise RuntimeError("production cleanup returned an invalid status")
    if values["build_cache_policy"] != "all_lru_maxused_2gb_reserved_2gb":
        raise RuntimeError("production cleanup returned an invalid BuildKit policy")
    if not re.fullmatch(r"[0-9a-f]{64}", values["plan_sha256"]):
        raise RuntimeError("production cleanup returned an invalid plan checksum")
    if expected_plan_sha256 is not None and values["plan_sha256"] != expected_plan_sha256:
        raise RuntimeError("production cleanup returned a different plan checksum")
    unsigned_fields = {
        "root_free_before_bytes",
        "root_free_after_bytes",
        "containerd_free_before_bytes",
        "containerd_free_after_bytes",
        "migration_evidence_containers",
        "image_candidates",
        "image_candidates_after",
        "image_candidate_logical_bytes",
        "removed_images",
        "build_cache_records_before",
        "build_cache_records_after",
    }
    signed_fields = {"root_free_delta_bytes", "containerd_free_delta_bytes"}
    if any(not re.fullmatch(r"[0-9]+", values[field]) for field in unsigned_fields):
        raise RuntimeError("production cleanup returned an invalid unsigned measurement")
    if any(not re.fullmatch(r"-?[0-9]+", values[field]) for field in signed_fields):
        raise RuntimeError("production cleanup returned an invalid signed measurement")
    numbers = {field: int(values[field]) for field in unsigned_fields | signed_fields}
    if numbers["root_free_after_bytes"] - numbers["root_free_before_bytes"] != numbers["root_free_delta_bytes"]:
        raise RuntimeError("production cleanup returned an inconsistent root filesystem delta")
    if (
        numbers["containerd_free_after_bytes"] - numbers["containerd_free_before_bytes"]
        != numbers["containerd_free_delta_bytes"]
    ):
        raise RuntimeError("production cleanup returned an inconsistent containerd filesystem delta")
    expected_gc = numbers["build_cache_records_before"] > 0 and mode == "apply"
    if values["build_cache_gc_attempted"] != str(expected_gc).lower():
        raise RuntimeError("production cleanup returned an inconsistent BuildKit GC result")
    if mode == "dry-run":
        if numbers["removed_images"] != 0 or numbers["image_candidates_after"] != numbers["image_candidates"]:
            raise RuntimeError("production cleanup dry-run changed or lost image candidates")
    elif numbers["removed_images"] > numbers["image_candidates"] or numbers["image_candidates_after"] != 0:
        raise RuntimeError("production cleanup did not converge to an empty image candidate set")


def load_cleanup_identity(release_id: str, run_root: Path = RUN_ROOT) -> CleanupIdentity:
    if not RELEASE_ID.fullmatch(release_id):
        raise ValueError("release ID is invalid")
    run_dir = run_root / release_id
    gate_dir = run_dir / "gate"
    if not run_dir.is_dir() or run_dir.is_symlink() or not gate_dir.is_dir() or gate_dir.is_symlink():
        raise RuntimeError("cleanup release evidence directory is incomplete or unsafe")
    gate_path = gate_dir / "gate.json"
    signature_path = gate_dir / "gate.sig"
    result_path = gate_dir / "production-result.json"
    for path in (gate_path, signature_path, result_path):
        if not path.is_file() or path.is_symlink():
            raise RuntimeError("cleanup release evidence is incomplete or unsafe")
    subprocess.run(
        [
            "openssl",
            "pkeyutl",
            "-verify",
            "-pubin",
            "-inkey",
            str(TRUSTED_VM_PUBLIC_KEY),
            "-rawin",
            "-in",
            str(gate_path),
            "-sigfile",
            str(signature_path),
        ],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    gate = json.loads(gate_path.read_text(encoding="utf-8"))
    result = json.loads(result_path.read_text(encoding="utf-8"))
    if gate.get("manifest", {}).get("release_id") != release_id:
        raise RuntimeError("signed Gate release identity does not match")
    current_image_id = validate_image_id(gate.get("evidence", {}).get("candidate_image_id", ""))
    if result.get("release_id") != release_id or result.get("status") != "verified" or result.get("stage") not in FINAL_STAGES:
        raise RuntimeError("production result is not terminal verified evidence")
    history = result.get("history")
    if not isinstance(history, list):
        raise RuntimeError("production result history is invalid")
    pre_switch_values: list[str] = []
    final_consumed = False
    for event in history:
        if not isinstance(event, dict):
            raise RuntimeError("production result event is invalid")
        evidence = event.get("evidence")
        if not isinstance(evidence, dict):
            continue
        if "pre_switch_image_id" in evidence:
            pre_switch_values.append(validate_image_id(evidence["pre_switch_image_id"]))
        if event.get("stage") in FINAL_STAGES and evidence.get("gate_consumed") == "true":
            final_consumed = True
    if not pre_switch_values or len(set(pre_switch_values)) != 1:
        raise RuntimeError("production result lacks one unambiguous pre-switch image")
    if not final_consumed:
        raise RuntimeError("production result does not prove the Gate was consumed")
    pre_switch_image_id = pre_switch_values[-1]
    if pre_switch_image_id == current_image_id:
        raise RuntimeError("current and pre-switch image identities must differ")
    return CleanupIdentity(release_id, current_image_id, pre_switch_image_id)


def cleanup_production(
    release_id: str,
    mode: str,
    plan_sha256: str | None = None,
    runner: SSHRunner | None = None,
) -> dict[str, str]:
    if mode not in {"dry-run", "apply"}:
        raise ValueError("cleanup mode is invalid")
    if mode == "apply" and (plan_sha256 is None or not re.fullmatch(r"[0-9a-f]{64}", plan_sha256)):
        raise ValueError("apply requires the exact dry-run plan SHA-256")
    if mode == "dry-run" and plan_sha256 is not None:
        raise ValueError("dry-run does not accept a plan SHA-256")
    identity = load_cleanup_identity(release_id)
    runner = runner or SSHRunner()
    with RunLock(RUN_ROOT / ".release.lock"):
        remote_root = runner.create_temp_dir("racknerd", "/tmp", "production-clean")
        remote_cleaner = f"{remote_root}/production-space-clean.sh"
        runner.upload_file("racknerd", PRODUCTION_SPACE_CLEANER, remote_cleaner, 0o700)
        try:
            expected_checksum = sha256_file(PRODUCTION_SPACE_CLEANER)
            runner.run(
                "racknerd",
                f"test $(sha256sum {shlex.quote(remote_cleaner)} | awk '{{print $1}}') = {shlex.quote(expected_checksum)} && printf 'cleaner_verified=true\\n'",
                {"cleaner_verified"},
            )
            command = " ".join(
                shlex.quote(value)
                for value in (
                    remote_cleaner,
                    mode,
                    identity.release_id,
                    identity.current_image_id,
                    identity.pre_switch_image_id,
                    plan_sha256 or "-",
                )
            )
            values = runner.run("racknerd", command, PRODUCTION_CLEAN_FIELDS, timeout=1200).values
            validate_cleanup_evidence(values, identity, mode, plan_sha256)
            return values
        finally:
            runner.run(
                "racknerd",
                f"rm -rf -- {shlex.quote(remote_root)} && printf 'cleanup=true\\n'",
                {"cleanup"},
            )
