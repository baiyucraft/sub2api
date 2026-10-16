"""Verify existing trusted full recovery reviews; never issue or sign evidence."""
from __future__ import annotations

import base64
from copy import deepcopy
from dataclasses import dataclass
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import time
from typing import Any

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
from cryptography.exceptions import InvalidSignature

from .atomic import canonical_json
from .paths import TRUSTED_VM_PUBLIC_KEY, LAYOUT_SKILL_V1
from .process import check_output_hidden
from .recovery_bundle import recovery_bundle_document, recovery_bundle_sha256


SHA = re.compile(r"[0-9a-f]{64}")
COMMIT = re.compile(r"[0-9a-f]{40}")
DRILL = re.compile(r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}")
ORIGINAL_FIELDS = {"schema", "mode", "base_commit", "target_commit", "reason_codes", "changed_paths_sha256", "estimated_extra_seconds"}
SCOPE_FIELDS = {"base_commit", "target_commit", "changed_paths_sha256", "inventory", "inventory_sha256", "recovery_helper_bundle_sha256", "classification_sha256", "trusted_public_key_sha256"}
PHASES = ("compose_restored", "postgres_restored", "redis_restored", "app_healthy", "nginx_restored", "backup_units_restored", "state_cleanup", "claim_reconciled")
KEY_PATH = ".agents/skills/sub2api-production-deploy/scripts/release/trust/vm-gate-ed25519.pub"
PAYLOAD_FIELDS = SCOPE_FIELDS | {"drill_id", "created_at", "completed_at", "source", "review", "measurements"}
MAX_EVIDENCE_BYTES = 4 * 1024 * 1024


def require(condition: bool, reason: str) -> None:
    if not condition:
        raise RuntimeError("full recovery evidence rejected: " + reason)


def shape(value: Any, fields: set[str], label: str) -> dict[str, Any]:
    require(isinstance(value, dict) and set(value) == fields, label + " fields")
    return value


def sha(value: Any) -> bool:
    return isinstance(value, str) and SHA.fullmatch(value) is not None


def integer(value: Any, minimum: int = 0) -> bool:
    return type(value) is int and value >= minimum


def checksum(value: Any) -> str:
    return hashlib.sha256(canonical_json(value)).hexdigest()


def read_regular_bytes(path: Path, *, maximum: int, private: bool = False) -> bytes:
    """Open a bounded regular file without following a replaced symlink."""
    try:
        initial = path.lstat()
        require(stat.S_ISREG(initial.st_mode) and initial.st_nlink == 1, "regular single-link evidence file required")
        require(path.resolve() == path.absolute(), "noncanonical evidence path")
        descriptor = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(descriptor, "rb") as stream:
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and (info.st_dev, info.st_ino) == (initial.st_dev, initial.st_ino), "evidence file changed during open")
            if private and os.name != "nt":
                require(info.st_uid == 0 and info.st_gid == 0 and stat.S_IMODE(info.st_mode) == 0o600, "root-only evidence receipt required")
            require(info.st_size <= maximum, "evidence file size limit")
            raw = stream.read(maximum + 1)
            require(len(raw) <= maximum, "evidence file size limit")
            return raw
    except OSError as error:
        raise RuntimeError("full recovery evidence rejected: evidence file unavailable") from error


def strict_json(raw: bytes) -> dict[str, Any]:
    def unique_object(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "duplicate JSON field")
            result[key] = value
        return result

    def invalid_constant(value):
        raise RuntimeError("full recovery evidence rejected: non-finite JSON value")

    def finite_float(value):
        number = float(value)
        require(math.isfinite(number), "non-finite JSON value")
        return number

    try:
        document = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object, parse_constant=invalid_constant, parse_float=finite_float)
    except (ValueError, UnicodeDecodeError, RecursionError) as error:
        raise RuntimeError("full recovery evidence rejected: invalid evidence JSON") from error
    require(isinstance(document, dict), "evidence JSON object required")
    return document


def load_signed_evidence(path: Path) -> dict[str, Any]:
    """Read existing evidence only; reject ambiguous JSON before verification."""
    return strict_json(read_regular_bytes(path, maximum=MAX_EVIDENCE_BYTES))


def checkpoint_identity_bytes(payload: dict[str, Any]) -> bytes:
    source = payload["source"]
    return ("schema=1\nrelease_id=" + source["release_id"] + "\nrecovery_point_sha256=" + source["recovery_point_sha256"] + "\npre_image_id=" + source["pre_image_id"] + "\nrecovery_helper_bundle_sha256=" + payload["recovery_helper_bundle_sha256"] + "\n").encode("ascii")


def checkpoint_marker_bytes(payload: dict[str, Any], phase: str) -> bytes:
    return checkpoint_identity_bytes(payload) + ("phase=" + phase + "\n").encode("ascii")


def verify_checkpoint_bytes(value: Any, expected: bytes, digest: Any) -> None:
    require(isinstance(value, str) and len(value) <= 2048 and sha(digest), "checkpoint raw bytes or digest")
    try:
        raw = base64.b64decode(value, validate=True)
    except ValueError as error:
        raise RuntimeError("full recovery evidence rejected: checkpoint encoding") from error
    require(base64.b64encode(raw).decode("ascii") == value and raw == expected and hashlib.sha256(raw).hexdigest() == digest, "checkpoint identity or raw digest differs")


def original_report(report: dict[str, Any]) -> dict[str, Any]:
    require(isinstance(report, dict), "classification type")
    extras = {"full_recovery_evidence", "evidence_sha256"} if report.get("schema") == 2 else set()
    require(set(report) == ORIGINAL_FIELDS | extras, "classification shape")
    require(type(report["schema"]) is int and report["schema"] in {1, 2}, "classification schema")
    result = {key: deepcopy(report[key]) for key in ORIGINAL_FIELDS}
    result["schema"] = 1
    require(result["mode"] == "full", "full classification required")
    for key in ("base_commit", "target_commit"):
        require(isinstance(result[key], str) and COMMIT.fullmatch(result[key]) is not None, "immutable commit required")
    reasons = result["reason_codes"]
    require(isinstance(reasons, list) and all(isinstance(item, str) and item for item in reasons) and reasons == sorted(set(reasons)), "classification reasons")
    require("recovery_change_requires_review" in reasons and "production_commit_unproven" not in reasons, "reviewable proven classification required")
    require(sha(result["changed_paths_sha256"]) and result["estimated_extra_seconds"] == 2400, "classification checksum or estimate")
    return result


def git(workspace: Path, *args: str) -> bytes:
    return check_output_hidden(["git", *args], cwd=workspace)


def tree(workspace: Path, commit: str, paths: tuple[str, ...] = ()) -> dict[str, tuple[str, str]]:
    output = git(workspace, "ls-tree", "-r", "-z", commit, "--", *paths)
    result = {}
    for record in output.split(b"\0"):
        if not record:
            continue
        metadata, raw_path = record.split(b"\t", 1)
        mode, kind, blob = metadata.decode("ascii").split()
        require(kind == "blob" and mode in {"100644", "100755", "120000"}, "non-blob Git inventory unsupported")
        path = raw_path.decode("utf-8")
        require("\\" not in path and not path.startswith("/") and all(part not in {"", ".", ".."} for part in path.split("/")), "noncanonical Git path")
        result[path] = (mode, blob)
    return result


def derive_expected_scope(workspace: Path, report: dict[str, Any]) -> dict[str, Any]:
    """Read immutable Git objects and the complete target release-asset closure."""
    from .manifest import is_release_asset_relative_path, release_asset_pathspecs
    from .recovery_gate import changed_paths_sha256
    original = original_report(report)
    base, target = original["base_commit"], original["target_commit"]
    for commit in (base, target):
        require(git(workspace, "rev-parse", "--verify", commit + "^{commit}").decode().strip() == commit, "Git commit identity")
    paths = sorted(part.decode("utf-8") for part in git(workspace, "diff", "--no-renames", "--name-only", "-z", base, target, "--").split(b"\0") if part)
    require(changed_paths_sha256(paths) == original["changed_paths_sha256"], "changed path inventory differs from classification")
    before, after = tree(workspace, base), tree(workspace, target)

    def state(entries, path):
        if path not in entries:
            return None
        mode, blob = entries[path]
        return {"mode": mode, "blob_sha1": blob, "raw_sha256": hashlib.sha256(git(workspace, "cat-file", "blob", blob)).hexdigest()}

    inventory = [{"path": path, "before": state(before, path), "after": state(after, path)} for path in paths]
    asset_tree = tree(workspace, target, release_asset_pathspecs(LAYOUT_SKILL_V1))
    assets = {}
    for path, (mode, blob) in asset_tree.items():
        require(is_release_asset_relative_path(path, LAYOUT_SKILL_V1) and mode in {"100644", "100755"}, "release asset closure or mode")
        assets[path] = hashlib.sha256(git(workspace, "cat-file", "blob", blob)).hexdigest()
    bundle = recovery_bundle_document(target, assets)
    require(KEY_PATH in before, "existing trusted root absent at base commit")
    trusted_checksum = hashlib.sha256(git(workspace, "cat-file", "blob", before[KEY_PATH][1])).hexdigest()
    return {"base_commit": base, "target_commit": target, "changed_paths_sha256": original["changed_paths_sha256"], "inventory": inventory, "inventory_sha256": checksum(inventory), "recovery_helper_bundle_sha256": recovery_bundle_sha256(bundle), "classification_sha256": checksum(original), "trusted_public_key_sha256": trusted_checksum}


def verify_signature(envelope: dict[str, Any], expected_scope: dict[str, Any]) -> None:
    signature = shape(envelope["signature"], {"algorithm", "key_sha256", "value"}, "signature")
    require(signature["algorithm"] == "Ed25519" and sha(signature["key_sha256"]), "signature algorithm or fingerprint")
    key_path = TRUSTED_VM_PUBLIC_KEY
    require(key_path.is_file() and not key_path.is_symlink() and key_path.stat().st_nlink == 1, "existing trust root unavailable")
    key_bytes = key_path.read_bytes()
    fingerprint = hashlib.sha256(key_bytes).hexdigest()
    require(fingerprint == signature["key_sha256"] == expected_scope["trusted_public_key_sha256"], "existing base trust root mismatch")
    require(isinstance(signature["value"], str), "signature encoding")
    try:
        raw_signature = base64.b64decode(signature["value"], validate=True)
        require(len(raw_signature) == 64 and base64.b64encode(raw_signature).decode() == signature["value"], "signature length or canonical encoding")
        key = serialization.load_pem_public_key(key_bytes)
        require(isinstance(key, Ed25519PublicKey), "existing trust root is not Ed25519")
        # Domain separation prevents an existing Gate or schema-1 DR signature
        # from being reused even if some payload values happen to coincide.
        signed = {"schema": 2, "type": "sub2api-full-recovery-review", "payload": envelope["payload"]}
        key.verify(raw_signature, canonical_json(signed))
    except (ValueError, TypeError, InvalidSignature) as error:
        raise RuntimeError("full recovery evidence rejected: trusted signature verification") from error


REDIS_LEGACY_FIELDS = frozenset({"restore_verified", "rdb_sha256", "backup_dbsize", "backup_expiring", "restored_dbsize", "restored_expiring", "restored_keyspace_keys", "persistent_before", "persistent_after", "health_wait_ms", "health_budget_ms", "ttl_reconciled", "lazy_expiry_observed"})


def validate_redis_measurement(redis: dict[str, Any], *, isolation: dict[str, Any],
                               rdb_sha256: str, created_at: int, completed_at: int,
                               health_budget_max: int = 600000) -> None:
    """Keep historic signed semantics and validate the explicit new format.

    Source counts describe only restored production data. A separately owned
    canary proves lazy expiry deterministically without relying on the age of
    the source's TTL keys. Unknown, partial and mixed formats fail closed.
    """
    require(isinstance(redis, dict), "Redis measurement object")
    fields = set(redis)
    require(fields in (REDIS_LEGACY_FIELDS, REDIS_LEGACY_FIELDS | {"source_observation", "boundary_probe"}), "Redis measurement fields")
    require(redis["restore_verified"] is True and redis["ttl_reconciled"] is True and sha(redis["rdb_sha256"]) and redis["rdb_sha256"] == rdb_sha256, "Redis measured restoration")
    counts = ("backup_dbsize", "backup_expiring", "restored_dbsize", "restored_expiring", "restored_keyspace_keys", "persistent_before", "persistent_after", "health_wait_ms", "health_budget_ms")
    require(all(integer(redis[key]) for key in counts), "Redis measured integer values")
    require(60000 < redis["health_wait_ms"] <= redis["health_budget_ms"] <= health_budget_max, "real delayed Redis startup budget")
    require(redis["persistent_before"] == redis["backup_dbsize"] - redis["backup_expiring"] and redis["persistent_after"] == redis["restored_dbsize"] - redis["restored_expiring"], "Redis persistent count formulas")
    if fields == REDIS_LEGACY_FIELDS:
        require(redis["lazy_expiry_observed"] is True, "Redis measured restoration")
        require(redis["backup_dbsize"] >= redis["restored_dbsize"] > redis["restored_keyspace_keys"] >= 0 and redis["restored_dbsize"] >= redis["restored_expiring"] and redis["restored_keyspace_keys"] >= redis["restored_dbsize"] - redis["restored_expiring"], "Redis lazy expiration count observation")
        require(redis["backup_dbsize"] >= redis["backup_expiring"] > redis["restored_expiring"] and redis["persistent_after"] >= redis["persistent_before"], "Redis TTL and persistent floor")
        return
    binding = {"drill_id", "release_id", "guest_docker_engine_id_sha256", "rootfs_identity_sha256"}
    source = shape(redis["source_observation"], {"schema", "type", "image_id", "before_dbsize", "before_expiring", "after_dbsize", "after_expiring", "observed_at"} | binding, "Redis source observation")
    require(type(source["schema"]) is int and source["schema"] == 1 and source["type"] == "redis-restored-source-atomic-scan", "Redis source observation schema")
    require(all(source[key] == isolation[key] for key in binding) and all(sha(source[key]) for key in ("guest_docker_engine_id_sha256", "rootfs_identity_sha256")), "Redis source Guest binding")
    require(isinstance(source["image_id"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", source["image_id"]) is not None, "Redis source image identity")
    require(integer(source["observed_at"], 1) and created_at <= source["observed_at"] <= completed_at, "Redis source observation timestamp")
    require(all(integer(source[key]) for key in ("before_dbsize", "before_expiring", "after_dbsize", "after_expiring")), "Redis source integer counts")
    before, exp_before, after, exp_after = (source[key] for key in ("before_dbsize", "before_expiring", "after_dbsize", "after_expiring"))
    require(before >= after >= exp_after >= 0 and before >= exp_before >= exp_after and before - after == exp_before - exp_after, "Redis source atomic TTL reconciliation")
    require(type(redis["lazy_expiry_observed"]) is bool and redis["lazy_expiry_observed"] == (before > after), "Redis source natural expiry observation")
    require(redis["restored_dbsize"] == redis["restored_keyspace_keys"] == after and redis["restored_expiring"] == exp_after, "Redis source final counts differ")
    require(redis["backup_dbsize"] >= before and redis["backup_dbsize"] >= redis["backup_expiring"] >= exp_before and redis["persistent_before"] == redis["persistent_after"] == before - exp_before, "Redis source persistent keys or TTL counts differ")
    probe = shape(redis["boundary_probe"], {"schema", "type", "production_source", "image_id", "container_id_sha256", "network_mode", "tmpfs_data", "read_only_rootfs", "published_ports_absent", "volumes_absent", "debug_config", "active_expiry_disable_reply", "ttl_ms", "initial_pttl_ms", "wait_elapsed_ms", "final_pttl_ms", "before_dbsize", "before_expiring", "after_dbsize", "after_expiring", "persistent_before_sha256", "persistent_after_sha256", "created_at", "completed_at", "owned_resources_cleaned"} | binding, "Redis boundary probe")
    require(type(probe["schema"]) is int and probe["schema"] == 1 and probe["type"] == "redis-owned-ttl-boundary" and probe["production_source"] is False, "Redis boundary probe schema or provenance")
    require(all(probe[key] == source[key] for key in binding | {"image_id"}) and sha(probe["container_id_sha256"]), "Redis boundary source image or Guest binding")
    require(probe["network_mode"] == "none" and all(probe[key] is True for key in ("tmpfs_data", "read_only_rootfs", "published_ports_absent", "volumes_absent", "owned_resources_cleaned")) and probe["debug_config"] == "yes" and probe["active_expiry_disable_reply"] == "OK", "Redis boundary isolation or cleanup")
    require(all(integer(probe[key]) for key in ("before_dbsize", "before_expiring", "after_dbsize", "after_expiring")) and (probe["before_dbsize"], probe["before_expiring"], probe["after_dbsize"], probe["after_expiring"]) == (2, 1, 1, 0), "Redis boundary measured lazy deletion")
    require(type(probe["ttl_ms"]) is int and probe["ttl_ms"] == 2000 and integer(probe["initial_pttl_ms"], 1) and probe["initial_pttl_ms"] <= probe["ttl_ms"] and integer(probe["wait_elapsed_ms"], 2200) and probe["wait_elapsed_ms"] <= 30000 and type(probe["final_pttl_ms"]) is int and probe["final_pttl_ms"] == -2, "Redis boundary real TTL wait")
    require(sha(probe["persistent_before_sha256"]) and probe["persistent_before_sha256"] == probe["persistent_after_sha256"], "Redis boundary persistent sentinel changed")
    require(integer(probe["created_at"], 1) and integer(probe["completed_at"], 1) and source["observed_at"] <= probe["created_at"] <= probe["completed_at"] <= completed_at and probe["wait_elapsed_ms"] <= (probe["completed_at"] - probe["created_at"]) * 1000 + 2000, "Redis boundary timestamp binding")


def validate_measurements(measurements: dict[str, Any], payload: dict[str, Any]) -> None:
    shape(measurements, {"compose", "postgres", "redis", "faults", "checkpoints", "rto", "cleanup", "isolation"}, "measurement")
    isolation = shape(measurements["isolation"], {"backend", "drill_id", "release_id", "recovery_helper_bundle_sha256", "namespaces", "host_identity_sha256", "guest_identity_sha256", "rootfs_identity_sha256", "host_docker_engine_id_sha256", "guest_docker_engine_id_sha256", "docker_root_dir", "docker_socket", "default_routes_absent", "host_facing_interfaces_absent", "uid0_host", "uid0_range", "unified_cgroup2"}, "isolation")
    require(isolation["drill_id"] == payload["drill_id"] and isolation["release_id"] == payload["source"]["release_id"] and isolation["recovery_helper_bundle_sha256"] == payload["recovery_helper_bundle_sha256"], "isolation proof belongs to a different drill or helper bundle")
    require(isolation["backend"] == "nspawn-offline-private-users" and all(sha(isolation[key]) for key in ("host_identity_sha256", "guest_identity_sha256", "rootfs_identity_sha256", "host_docker_engine_id_sha256", "guest_docker_engine_id_sha256")), "actual isolated Guest and Engine proof required")
    require(isolation["host_identity_sha256"] != isolation["guest_identity_sha256"] and isolation["host_docker_engine_id_sha256"] != isolation["guest_docker_engine_id_sha256"] and isolation["docker_root_dir"] == "/var/lib/docker" and isolation["docker_socket"] == "/run/docker.sock", "private Guest Docker identity required")
    require(isolation["default_routes_absent"] is True and isolation["host_facing_interfaces_absent"] is True and isolation["unified_cgroup2"] is True and integer(isolation["uid0_host"], 65536) and integer(isolation["uid0_range"], 65536), "actual offline user and cgroup isolation required")
    namespaces = shape(isolation["namespaces"], {"mnt", "pid", "net", "user", "cgroup", "uts", "ipc"}, "isolation namespace")
    for name, item in namespaces.items():
        namespace = shape(item, {"host", "guest", "guest_pid1"}, "namespace " + name)
        require(all(isinstance(value, str) and re.fullmatch(name + r":\[[1-9][0-9]*\]", value) is not None for value in namespace.values()) and namespace["host"] != namespace["guest"] == namespace["guest_pid1"], "actual private namespace proof differs")
    compose = shape(measurements["compose"], {"closure_restored", "render_before_sha256", "render_after_sha256", "old_image_id", "restored_image_id"}, "Compose")
    require(compose["closure_restored"] is True and sha(compose["render_before_sha256"]) and compose["render_before_sha256"] == compose["render_after_sha256"], "complete Compose closure and render")
    require(isinstance(compose["old_image_id"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", compose["old_image_id"]) is not None and compose["old_image_id"] == compose["restored_image_id"], "restored old image identity")
    pg = shape(measurements["postgres"], {"restore_verified", "dump_sha256", "migrations_before_sha256", "migrations_after_sha256", "rowcounts_before_sha256", "rowcounts_after_sha256"}, "PostgreSQL")
    require(pg["restore_verified"] is True and all(sha(value) for key, value in pg.items() if key != "restore_verified"), "PostgreSQL measured restore")
    require(pg["migrations_before_sha256"] == pg["migrations_after_sha256"] and pg["rowcounts_before_sha256"] == pg["rowcounts_after_sha256"], "PostgreSQL restoration identity")
    require(payload["source"]["archive_inventory"]["postgres_dump_sha256"] == pg["dump_sha256"], "actual archive PG bytes differ from measured restore")
    validate_redis_measurement(measurements["redis"], isolation=isolation, rdb_sha256=payload["source"]["archive_inventory"]["redis_rdb_sha256"], created_at=payload["created_at"], completed_at=payload["completed_at"])
    faults = shape(measurements["faults"], {"app_healthy_interrupt", "cleanup_before_interrupt", "stale_ingress"}, "fault")
    for name in ("app_healthy_interrupt", "cleanup_before_interrupt"):
        fault = shape(faults[name], {"exit_code", "resume_verified", "postgres_oid_before", "postgres_oid_after", "redis_run_before_sha256", "redis_run_after_sha256"}, name)
        require(type(fault["exit_code"]) is int and fault["exit_code"] == 79 and fault["resume_verified"] is True, "actual checkpoint interruption and resume")
        require(integer(fault["postgres_oid_before"], 1) and integer(fault["postgres_oid_after"], 1) and fault["postgres_oid_before"] == fault["postgres_oid_after"] and sha(fault["redis_run_before_sha256"]) and fault["redis_run_before_sha256"] == fault["redis_run_after_sha256"], "data restore was repeated during resume")
    stale = shape(faults["stale_ingress"], {"recovery_restored_and_applied_observed", "live_health_reverified", "applied_removed"}, "stale ingress")
    require(all(value is True for value in stale.values()), "actual stale ingress reconciliation")
    checkpoints = shape(measurements["checkpoints"], set(PHASES), "checkpoint")
    identity = hashlib.sha256(checkpoint_identity_bytes(payload)).hexdigest()
    for phase in PHASES:
        checkpoint = shape(checkpoints[phase], {"completed", "live_reverified", "apply_count", "resume_apply_count", "binding_sha256", "started_base64", "started_sha256", "done_base64", "done_sha256"}, phase)
        require(checkpoint["completed"] is True and checkpoint["live_reverified"] is True and type(checkpoint["apply_count"]) is int and checkpoint["apply_count"] == 1 and type(checkpoint["resume_apply_count"]) is int and checkpoint["resume_apply_count"] == 0 and checkpoint["binding_sha256"] == identity, "checkpoint binding or idempotent replay")
        for state in ("started", "done"):
            verify_checkpoint_bytes(checkpoint[state + "_base64"], checkpoint_marker_bytes(payload, phase), checkpoint[state + "_sha256"])
    rto = shape(measurements["rto"], {"started_at", "completed_at", "elapsed_ms", "budget_ms"}, "RTO")
    require(all(integer(value, 1) for value in rto.values()), "RTO measured integer values")
    require(payload["created_at"] == rto["started_at"] <= rto["completed_at"] == payload["completed_at"] and 0 < rto["elapsed_ms"] <= rto["budget_ms"] <= 7200000, "RTO measured budget")
    require(abs(rto["elapsed_ms"] - 1000 * (rto["completed_at"] - rto["started_at"])) <= 2000, "RTO clock consistency")
    cleanup = shape(measurements["cleanup"], {"plaintext_destroyed", "owned_guest_destroyed", "active_claim_cleared", "stale_ingress_removed", "foreign_resources_unchanged", "foreign_before_sha256", "foreign_after_sha256"}, "cleanup")
    require(all(cleanup[key] is True for key in ("plaintext_destroyed", "owned_guest_destroyed", "active_claim_cleared", "stale_ingress_removed", "foreign_resources_unchanged")) and sha(cleanup["foreign_before_sha256"]) and cleanup["foreign_before_sha256"] == cleanup["foreign_after_sha256"], "actual owned cleanup and foreign resource preservation")


@dataclass(frozen=True)
class VerifiedEvidence:
    drill_id: str
    evidence_sha256: str
    target_commit: str
    completed_at: int


def verify_full_recovery_evidence(envelope: dict[str, Any], *, workspace: Path, report: dict[str, Any], now: int | None = None, allow_expired: bool = False) -> VerifiedEvidence:
    shape(envelope, {"schema", "type", "payload", "signature"}, "signed envelope")
    require(len(canonical_json(envelope)) <= MAX_EVIDENCE_BYTES, "signed envelope size limit")
    require(type(envelope["schema"]) is int and envelope["schema"] == 2 and envelope["type"] == "sub2api-full-recovery-review", "independent full review schema required")
    payload = shape(envelope["payload"], PAYLOAD_FIELDS, "signed payload")
    scope = derive_expected_scope(workspace, report)
    verify_signature(envelope, scope)
    require(all(payload[key] == scope[key] for key in SCOPE_FIELDS), "actual Git inventory, helper bundle or classification differs")
    require(isinstance(payload["drill_id"], str) and DRILL.fullmatch(payload["drill_id"]) is not None, "independent unique full drill identity")
    current = int(time.time()) if now is None else now
    require(type(allow_expired) is bool, "explicit historical evidence mode required")
    require(integer(current, 1) and integer(payload["created_at"], 1) and integer(payload["completed_at"], 1) and payload["created_at"] <= payload["completed_at"] <= current + 300 and (allow_expired or payload["completed_at"] >= current - 86400), "full drill evidence expired or future")
    source = shape(payload["source"], {"kind", "artifact_sha256", "observed_artifact_sha256", "artifact_bytes", "identity_available", "decryption_verified", "decrypted_sha256", "release_id", "recovery_point_sha256", "pre_image_id", "recipient_type", "approved_recipient_sha256", "derived_recipient_sha256", "approved_identity_sha256", "observed_identity_sha256", "archive_inventory", "archive_inventory_sha256"}, "encrypted source")
    require(source["kind"] == "encrypted-production-baseline" and sha(source["artifact_sha256"]) and source["artifact_sha256"] == source["observed_artifact_sha256"] and integer(source["artifact_bytes"], 1) and source["identity_available"] is True and source["decryption_verified"] is True and sha(source["decrypted_sha256"]), "real encrypted asset and approved decryption identity required")
    require(isinstance(source["release_id"], str) and re.fullmatch(r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}", source["release_id"]) is not None and sha(source["recovery_point_sha256"]) and isinstance(source["pre_image_id"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", source["pre_image_id"]) is not None, "actual recovery source identity required")
    # Existing assets may use an age X25519 or SSH-Ed25519 recipient. The
    # reviewer hashes the normalized public recipient (SSH's first two fields,
    # separated by one space, or the exact age1 string; no trailing newline).
    # Full recipients and identity bytes never enter the evidence envelope.
    require(source["recipient_type"] in {"age1", "ssh-ed25519"} and sha(source["approved_recipient_sha256"]) and source["approved_recipient_sha256"] == source["derived_recipient_sha256"] and sha(source["approved_identity_sha256"]) and source["approved_identity_sha256"] == source["observed_identity_sha256"], "approved decrypt recipient or identity fingerprint differs")
    inventory = shape(source["archive_inventory"], {"artifact_sha256", "decrypted_sha256", "recovery_point_sha256", "postgres_dump_sha256", "redis_rdb_sha256"}, "actual archive inventory")
    require(all(sha(value) for value in inventory.values()) and all(inventory[key] == source[key] for key in ("artifact_sha256", "decrypted_sha256", "recovery_point_sha256")) and source["archive_inventory_sha256"] == checksum(inventory), "actual encrypted and decrypted archive inventory binding differs")
    require(isinstance(payload["measurements"], dict) and isinstance(payload["measurements"].get("compose"), dict) and source["pre_image_id"] == payload["measurements"]["compose"].get("old_image_id"), "source old image and Compose recovery differ")
    review = shape(payload["review"], {"reviewer", "audit_id", "independent", "approved_at", "findings_sha256"}, "independent review")
    require(isinstance(review["reviewer"], str) and re.fullmatch(r"[A-Za-z0-9_.@-]{3,128}", review["reviewer"]) is not None and isinstance(review["audit_id"], str) and re.fullmatch(r"audit-[0-9a-f]{32}", review["audit_id"]) is not None and review["independent"] is True and integer(review["approved_at"], 1) and payload["completed_at"] <= review["approved_at"] <= current + 300 and sha(review["findings_sha256"]), "independent review identity or decision")
    validate_measurements(payload["measurements"], payload)
    return VerifiedEvidence(payload["drill_id"], checksum(envelope), payload["target_commit"], payload["completed_at"])


def apply_review(report: dict[str, Any], envelope: dict[str, Any], *, workspace: Path, now: int | None = None) -> dict[str, Any]:
    verified = verify_full_recovery_evidence(envelope, workspace=workspace, report=report, now=now)
    original = original_report(report)
    # Preserve the original classification and blocking reason. A production
    # entrance must reverify this complete envelope, never trust a bare flag.
    return {**original, "schema": 2, "full_recovery_evidence": deepcopy(envelope), "evidence_sha256": verified.evidence_sha256}


def verify_reviewed_report(report: dict[str, Any], *, workspace: Path, now: int | None = None, allow_expired: bool = False) -> VerifiedEvidence:
    require(isinstance(report, dict) and report.get("schema") == 2 and sha(report.get("evidence_sha256")), "signed full reviewed report required")
    verified = verify_full_recovery_evidence(report.get("full_recovery_evidence"), workspace=workspace, report=report, now=now, allow_expired=allow_expired)
    require(verified.evidence_sha256 == report["evidence_sha256"], "signed envelope digest differs")
    return verified


class EvidenceLedger:
    """Consume only on first CLI acceptance; verification is always read-only."""
    def __init__(self, directory: Path):
        self.directory = directory
        self.assert_directory()

    def assert_directory(self) -> None:
        directory = self.directory
        require(directory.is_dir() and not directory.is_symlink() and directory.resolve() == directory.absolute(), "ledger directory unavailable or unsafe")
        if os.name != "nt":
            info = directory.stat()
            require(info.st_uid == 0 and info.st_gid == 0 and stat.S_IMODE(info.st_mode) == 0o700, "ledger must be root-only")

    @staticmethod
    def receipt(verified: VerifiedEvidence) -> dict[str, Any]:
        require(isinstance(verified, VerifiedEvidence) and isinstance(verified.drill_id, str) and DRILL.fullmatch(verified.drill_id) is not None and sha(verified.evidence_sha256) and isinstance(verified.target_commit, str) and COMMIT.fullmatch(verified.target_commit) is not None and integer(verified.completed_at, 1), "verified ledger identity required")
        return {"schema": 1, "drill_id": verified.drill_id, "evidence_sha256": verified.evidence_sha256, "target_commit": verified.target_commit, "completed_at": verified.completed_at}

    def read_registered(self, verified: VerifiedEvidence) -> dict[str, Any]:
        """Reverify the exact first-accept receipt without creating or updating it."""
        self.assert_directory()
        expected = self.receipt(verified)
        raw = read_regular_bytes(self.directory / (verified.drill_id + ".json"), maximum=4096, private=True)
        document = strict_json(raw)
        require(set(document) == set(expected) and document == expected and raw == canonical_json(expected), "registered receipt differs from verified evidence or canonical bytes")
        return document

    def register(self, verified: VerifiedEvidence) -> None:
        self.assert_directory()
        receipt = self.receipt(verified)
        target = self.directory / (verified.drill_id + ".json")
        try:
            descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError as error:
            raise RuntimeError("full recovery evidence rejected: drill identity already registered") from error
        with os.fdopen(descriptor, "wb") as stream:
            if os.name != "nt":
                os.fchown(stream.fileno(), 0, 0)
                os.fchmod(stream.fileno(), 0o600)
            stream.write(canonical_json(receipt))
            stream.flush()
            os.fsync(stream.fileno())
        if os.name != "nt":
            descriptor = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY | getattr(os, "O_NOFOLLOW", 0))
            try:
                os.fsync(descriptor)
            finally:
                os.close(descriptor)
