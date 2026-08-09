from __future__ import annotations

import base64
import hashlib
import json
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json
from .manifest import release_asset_checksums, runner_checksum, sha256_file, validate_commit, validate_image_id, validate_manifest_profile_contract
from .profiles import get_profile


def gate_payload(manifest: dict[str, Any], evidence: dict[str, Any]) -> bytes:
    value = {"manifest": manifest, "evidence": evidence}
    return canonical_json(value) + b"\n"


def verify_gate(bundle_dir: Path, public_key: Path, expected_profile: str, allow_expired: bool = False) -> dict[str, Any]:
    payload_path = bundle_dir / "gate.json"
    signature_path = bundle_dir / "gate.sig"
    if not payload_path.is_file() or not signature_path.is_file():
        raise RuntimeError("gate bundle is incomplete")
    subprocess.run(
        ["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(public_key), "-rawin", "-in", str(payload_path), "-sigfile", str(signature_path)],
        check=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    document = json.loads(payload_path.read_text(encoding="utf-8"))
    manifest = document["manifest"]
    evidence = document["evidence"]
    validate_commit(manifest["commit_sha"])
    validate_image_id(evidence["candidate_image_id"])
    if manifest["profile"] != expected_profile:
        raise RuntimeError("gate profile does not match")
    profile = get_profile(expected_profile)
    validate_manifest_profile_contract(manifest, profile)
    if expected_profile == "232":
        for field in ("compatibility_version", "compatibility_commit", "compatibility_image_id"):
            if manifest.get(field) != profile[field]:
                raise RuntimeError(f"gate {field} does not match profile 232")
        validate_commit(manifest["compatibility_commit"])
        validate_image_id(manifest["compatibility_image_id"])
    if manifest["runner_sha256"] != runner_checksum():
        raise RuntimeError("gate was created by a different release runner")
    if manifest.get("vm_validator_sha256") != sha256_file(Path(__file__).resolve().parent / "vm-validate.sh"):
        raise RuntimeError("gate was created by a different VM validator")
    if manifest.get("vm_gate_signer_sha256") != sha256_file(Path(__file__).resolve().parent / "sign-gate.sh"):
        raise RuntimeError("gate was created by a different VM gate signer")
    if manifest.get("vm_dr_signer_sha256") != sha256_file(Path(__file__).resolve().parent / "sign-dr-evidence.sh"):
        raise RuntimeError("gate was created by a different VM DR signer")
    if manifest.get("release_asset_sha256") != release_asset_checksums(manifest["commit_sha"]):
        raise RuntimeError("gate release assets do not match the current checkout")
    if not allow_expired and int(manifest["expires_at"]) < int(time.time()):
        raise RuntimeError("gate has expired")
    if evidence.get("vm_restore_verified") is not True or evidence.get("integration_verified") is not True:
        raise RuntimeError("gate lacks VM restore or integration evidence")
    if expected_profile in {"194", "195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232"} and evidence.get("prompt_audit_disabled") is not True:
        raise RuntimeError("gate lacks Prompt Audit disabled-state evidence")
    if expected_profile in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232"}:
        required_migration_evidence = ("migration_195_verified", "fixture_rejected", "restore_completed", "clean_preflight", "verified_replay", "verified_low_watermark_rejected")
        if any(evidence.get(field) is not True for field in required_migration_evidence):
            raise RuntimeError("gate lacks migration 195 semantic evidence")
    if expected_profile in {"198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232"} and evidence.get("managed_monitor_key_names_verified") is not True:
        raise RuntimeError("gate lacks managed monitor key-name evidence")
    if expected_profile in {"199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232"}:
        if evidence.get("reasoning_effort_policy_verified") is not True:
            raise RuntimeError("gate lacks group reasoning-effort policy evidence")
        if evidence.get("vm_old_image_compatibility_verified") is not True:
            raise RuntimeError("gate lacks VM old-image compatibility evidence")
        validate_image_id(evidence.get("vm_old_image_id", ""))
    if expected_profile in {"202", "206", "207", "208", "209", "210", "212", "213", "215", "232"}:
        required_profile_202_evidence = (
            "alipay_mobile_precreate_migration_verified",
            "group_auth_cache_image_generation_verified",
            "composite_model_routes_verified",
        )
        if any(evidence.get(field) is not True for field in required_profile_202_evidence):
            raise RuntimeError("gate lacks profile 202 migration semantic evidence")
    if expected_profile in {"206", "207", "208", "209", "210", "212", "213", "215", "232"}:
        required_profile_206_evidence = (
            "session_id_columns_verified",
            "live_request_type_verified",
            "group_allow_live_verified",
            "email_alias_index_verified",
            "live_runtime_capability_verified",
        )
        if any(evidence.get(field) is not True for field in required_profile_206_evidence):
            raise RuntimeError("gate lacks profile 206 migration semantic evidence")
    if expected_profile in {"208", "209", "210", "212", "213", "215", "232"} and evidence.get("passkey_schema_verified") is not True:
        raise RuntimeError("gate lacks profile 208 passkey schema evidence")
    if expected_profile in {"209", "210", "212", "213", "215", "232"} and evidence.get("user_usage_aggregation_schema_verified") is not True:
        raise RuntimeError("gate lacks profile 209 user usage aggregation schema evidence")
    if expected_profile in {"212", "213", "215", "232"}:
        if evidence.get("migration_211_status") not in {"absent", "verified"} or evidence.get("migration_212_status") not in {"absent", "verified"}:
            raise RuntimeError("gate lacks profile 212 migration status evidence")
        required_profile_212_evidence = (
            "group_profit_control_schema_verified",
            "group_profit_auth_cache_trigger_verified",
        )
        if any(evidence.get(field) is not True for field in required_profile_212_evidence):
            raise RuntimeError("gate lacks profile 212 profit-control migration evidence")
    if expected_profile in {"215", "232"}:
        if evidence.get("migration_214_status") not in {"absent", "verified"} or evidence.get("migration_215_status") not in {"absent", "verified"}:
            raise RuntimeError("gate lacks profile 215 migration status evidence")
        if evidence.get("usage_log_upstream_model_columns_verified") is not True:
            raise RuntimeError("gate lacks usage log upstream-model column evidence")
        if evidence.get("usage_log_upstream_model_mismatch_index_verified") is not True:
            raise RuntimeError("gate lacks usage log upstream-model mismatch index evidence")
    if expected_profile == "232":
        for migration_number in range(216, 233):
            if evidence.get(f"migration_{migration_number}_status") not in {"absent", "verified"}:
                raise RuntimeError(f"gate lacks migration {migration_number} status evidence")
        required_profile_232_evidence = (
            "channel_monitor_v2_schema_verified",
            "channel_monitor_v2_defaults_verified",
            "group_media_pricing_schema_verified",
            "group_media_auth_cache_trigger_verified",
            "migration_232_data_plan_verified",
            "migration_232_postflight_verified",
        )
        if any(evidence.get(field) is not True for field in required_profile_232_evidence):
            raise RuntimeError("gate lacks profile 232 migration semantic evidence")
        if evidence.get("vm_old_image_id") != manifest["compatibility_image_id"]:
            raise RuntimeError("gate profile 232 compatibility image does not match manifest")
    archive_path = bundle_dir / "candidate.tar.gz"
    if not archive_path.is_file():
        raise RuntimeError("gate candidate archive is missing")
    digest = hashlib.sha256()
    with archive_path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != evidence.get("candidate_archive_sha256"):
        raise RuntimeError("gate candidate archive checksum does not match")
    return document
