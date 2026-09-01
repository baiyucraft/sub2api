from __future__ import annotations

"""Database-driven migration planning shared by Gate v2 and release checks.

The application runner remains the execution authority.  This module only
discovers the candidate catalog and computes a deterministic, read-only plan
from a ``schema_migrations`` snapshot.
"""

import hashlib
import json
import re
import subprocess
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable, Mapping


CHECKSUM_POLICY_VERSION = "sub2api-migration-checksum-policy-v1"
_FILENAME = re.compile(r"^[^/\\]+\.sql$")
_SHA256 = re.compile(r"^[0-9a-f]{64}$")

# Keep this allowlist byte-for-byte aligned with the Go migration runner.  The
# first checksum is the rule's current file checksum; the remaining values are
# accepted historical database checksums.  A compatibility match is evidence,
# not a rewrite of the database checksum.
_CHECKSUM_RULE_INPUTS: dict[str, tuple[str, ...]] = {
    "054_drop_legacy_cache_columns.sql": ("82de761156e03876653e7a6a4eee883cd927847036f779b0b9f34c42a8af7a7d", "182c193f3359946cf094090cd9e57d5c3fd9abaffbc1e8fc378646b8a6fa12b4"),
    "061_add_usage_log_request_type.sql": ("66207e7aa5dd0429c2e2c0fabdaf79783ff157fa0af2e81adff2ee03790ec65c", "08a248652cbab7cfde147fc6ef8cda464f2477674e20b718312faa252e0481c0", "222b4a09c797c22e5922b6b172327c824f5463aaa8760e4f621bc5c22e2be0f3"),
    "109_auth_identity_compat_backfill.sql": ("0580b4602d85435edf9aca1633db580bb3932f26517f75134106f80275ec2ace", "551e498aa5616d2d91096e9d72cf9fb36e418ee22eacc557f8811cadbc9e20ee"),
    "110_pending_auth_and_provider_default_grants.sql": ("32cf87ee787b1bb36b5c691367c96eee37518fa3eed6f3322cf68795e3745279", "e3d1f433be2b564cfbdc549adf98fce13c5c7b363ebc20fd05b765d0563b0925"),
    "112_add_payment_order_provider_key_snapshot.sql": ("b75f8f56d39455682787696a3d92ad25b055444ca328fb7fca9a460a15d68d99", "ffd3e8a2c9295fa9cbefefd629a78268877e5b51bc970a82d9b3f46ec4ebd15e"),
    "115_auth_identity_legacy_external_backfill.sql": ("022aadd97bb53e755f0cf7a3a957e0cb1a1353b0c39ec4de3234acd2871fd04f", "4cf39e508be9fd1a5aa41610cbbebeb80385c9adda45bf78a706de9db4f1385f"),
    "116_auth_identity_legacy_external_safety_reports.sql": ("07edb09fa8d04ffb172b0621e3c22f4d1757d20a24ae267b3b36b087ab72d488", "f7757bd929ac67ffb08ce69fa4cf20fad39dbff9d5a5085fb2adabb7607e5877"),
    "118_wechat_dual_mode_and_auth_source_defaults.sql": ("b54194d7a3e4fbf710e0a3590d22a2fe7966804c487052a356e0b55f53ef96b0", "e0cdf835d6c688d64100f483d31bc02ac9ebad414bf1837af239a84bf75b8227", "a38243ca0a72c3a01c0a92b7986423054d6133c0399441f853b99802852720fb"),
    "119_enforce_payment_orders_out_trade_no_unique.sql": ("0bbe809ae48a9d811dabda1ba1c74955bd71c4a9cc610f9128816818dfa6c11e", "ebd2c67cce0116393fb4f1b5d5116a67c6aceb73820dfb5133d1ff6f36d72d34"),
    "120_enforce_payment_orders_out_trade_no_unique_notx.sql": ("34aadc0db59a4e390f92a12b73bd74642d9724f33124f73638ae00089ea5e074", "e77921f79d539bc24575cb9c16cbe566d2b23ce816190343d0a7568f6a3fcf61", "707431450603e70a43ce9fbd61e0c12fa67da4875158ccefabacea069587ab22", "04b082b5a239c525154fe9185d324ee2b05ff90da9297e10dba19f9be79aa59a"),
    "123_fix_legacy_auth_source_grant_on_signup_defaults.sql": ("2ce43c2cd89e9f9e1febd34a407ed9e84d177386c5544b6f02c1f58a21129f57", "6cd33422f215dcd1f486ab6f35c0ea5805d9ca69bb25906d94bc649156657145"),
    "159_batch_image_foundation.sql": ("d902b70982025ec519749faf058aab7631e82c3f48167b9a4ae4db718eb72cce", "82da85b5d98e67a0507647b873a40373e84538e4adafdeed6767c0ac8b6570b2"),
    "161_batch_image_pricing_snapshot.sql": ("4012af3e43636cb6af22e0176d59d1fcc70615c0f310194329461ae462c4fbd6", "96d915c9b7a6941ae99039e0ff3f1a61481eb9bddd933d11c6fadb2274554e87"),
    "195_channel_monitor_mode.sql": ("13f3792f3e3e53ee96e26415c884cf8062c77172824b54fcc9a8c0c2b1f185ec", "4c74fe33ef2274cc72e1bb49671e651274532c034b29f5b2982c2a4c88d101a6"),
    "220_clear_non_grok_video_generation_config.sql": ("85e320b9ec64f2d3fcd8cf705b2b4e76a7b49f7a57140c14bff97f32691c818b", "3da48c8fdffe6390325f43d08b8e353e0a365df43d44a78dbbe655d0deb18402"),
    "219_group_search_price_per_1k.sql": ("e86786ebcc3b14206fd2d321380a4e50e80cdadbfcf4962c639255e6a14008db", "df6ffd71b97e30ec2c8fe7b95e15783042dea58c553e32701ee7c42a5619af80"),
    "218_group_audio_voice_pricing.sql": ("40ee9f3a2af0e0a5e99dabc878fd0fe98be1011f26bcfcefcac7197f7081f0e7", "c2a5e5b4ffd6968ad1c10593289fbc11192cdea19fec3ed9bce3a84eff9a8351"),
    # Migration 255 was applied by the reverted multi-proxy release. Keep
    # the historical database checksum accepted while the compatibility
    # migration remains in the catalog; runtime code does not use the table.
    "255_account_proxy_bindings.sql": ("ff3a7486eb897611918fba432d0020d3339cf3ef82ee40843cc0c1636b4d3709", "3f57ba25129e14c781fcbff34ce0cee6980d10ef79907769d57492a522369f06"),
}
CHECKSUM_COMPATIBILITY_RULES = {
    filename: {
        "file_checksum": checksums[0],
        "accepted_checksums": frozenset(checksums),
    }
    for filename, checksums in _CHECKSUM_RULE_INPUTS.items()
}
CHECKSUM_COMPATIBILITY = {
    filename: rule["accepted_checksums"]
    for filename, rule in CHECKSUM_COMPATIBILITY_RULES.items()
}


def checksum_policy_sha256() -> str:
    payload = {
        "version": CHECKSUM_POLICY_VERSION,
        "rules": [
            {
                "filename": filename,
                "file_checksum": rule["file_checksum"],
                "accepted_checksums": sorted(rule["accepted_checksums"]),
            }
            for filename, rule in sorted(CHECKSUM_COMPATIBILITY_RULES.items())
        ],
    }
    return hashlib.sha256(json.dumps(payload, separators=(",", ":")).encode()).hexdigest()


def migration_checksum(content: str | bytes) -> str:
    if isinstance(content, bytes):
        content = content.decode("utf-8")
    return hashlib.sha256(content.strip().encode("utf-8")).hexdigest()


def _validate_filename(filename: str) -> None:
    if not _FILENAME.fullmatch(filename) or filename in {".", ".."}:
        raise ValueError(f"invalid migration filename: {filename!r}")


def discover_migration_catalog(root: Path, commit: str | None = None) -> list[dict[str, str]]:
    """Discover all non-empty candidate migrations in stable filename order."""
    if commit is None:
        paths = sorted((root / "backend" / "migrations").glob("*.sql"), key=lambda p: p.name)
        entries = [(path.name, path.read_bytes()) for path in paths]
    else:
        output = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", commit, "--", "backend/migrations"], cwd=root, text=True)
        names = sorted(path.rsplit("/", 1)[-1] for path in output.splitlines() if path.endswith(".sql"))
        entries = [(name, subprocess.check_output(["git", "show", f"{commit}:backend/migrations/{name}"], cwd=root)) for name in names]
    catalog: list[dict[str, str]] = []
    for filename, content in entries:
        _validate_filename(filename)
        normalized = content.decode("utf-8").strip() if isinstance(content, bytes) else str(content).strip()
        if normalized:
            checksum = migration_checksum(normalized)
            catalog.append({"filename": filename, "checksum": checksum, "non_transactional": filename.endswith("_notx.sql")})
    return catalog


def catalog_sha256(catalog: Iterable[Mapping[str, str]]) -> str:
    ordered = {"entries": [{"filename": item["filename"], "checksum": item["checksum"], "non_transactional": bool(item.get("non_transactional", False))} for item in catalog]}
    return hashlib.sha256(json.dumps(ordered, separators=(",", ":")).encode()).hexdigest()


def _compatible(filename: str, db_checksum: str, file_checksum: str) -> bool:
    accepted = CHECKSUM_COMPATIBILITY.get(filename)
    return accepted is not None and db_checksum in accepted and file_checksum in accepted


def plan_migrations(catalog: Iterable[Mapping[str, str]], existing_rows: Iterable[Mapping[str, Any]] | Mapping[str, str]) -> dict[str, Any]:
    catalog_list = [{"filename": str(item["filename"]), "checksum": str(item["checksum"]), "non_transactional": bool(item.get("non_transactional", False))} for item in catalog]
    catalog_list.sort(key=lambda item: item["filename"])
    by_name = {item["filename"]: item for item in catalog_list}
    if len(by_name) != len(catalog_list):
        raise ValueError("candidate migration catalog contains duplicate filenames")
    for item in catalog_list:
        _validate_filename(item["filename"])
        if not _SHA256.fullmatch(item["checksum"]):
            raise ValueError("candidate migration catalog contains an invalid checksum")
    if isinstance(existing_rows, Mapping):
        rows = [{"filename": str(name), "checksum": str(checksum)} for name, checksum in existing_rows.items()]
    else:
        rows = [{"filename": str(row["filename"]), "checksum": str(row["checksum"])} for row in existing_rows]
    row_checksums: dict[str, str] = {}
    for row in rows:
        _validate_filename(row["filename"])
        if not _SHA256.fullmatch(row["checksum"]):
            raise ValueError("schema_migrations contains an invalid checksum")
        previous = row_checksums.setdefault(row["filename"], row["checksum"])
        if previous != row["checksum"]:
            raise ValueError("schema_migrations contains duplicate conflicting records")
    rows = [{"filename": filename, "checksum": checksum} for filename, checksum in row_checksums.items()]
    conflicts: list[dict[str, str]] = []
    unknown: list[dict[str, str]] = []
    existing: list[dict[str, str]] = []
    pending: list[dict[str, str]] = []
    for row in sorted(rows, key=lambda item: item["filename"]):
        item = by_name.get(row["filename"])
        if item is None:
            unknown.append(dict(row))
            continue
        if row["checksum"] == item["checksum"]:
            existing.append({**item, "status": "verified"})
        elif _compatible(item["filename"], row["checksum"], item["checksum"]):
            existing.append({**item, "status": "verified-compatible", "database_checksum": row["checksum"]})
        else:
            conflicts.append({"filename": item["filename"], "database_checksum": row["checksum"], "candidate_checksum": item["checksum"], "reason": "checksum mismatch"})
    applied = {item["filename"] for item in existing}
    for item in catalog_list:
        if item["filename"] not in applied:
            pending.append({**item, "preflight": False, "postflight": False})
    high_watermark = max((row["filename"] for row in rows), default=None)
    return {
        "database_high_watermark": high_watermark,
        "catalog_sha256": catalog_sha256(catalog_list),
        "checksum_policy_sha256": checksum_policy_sha256(),
        "existing": existing,
        "pending": pending,
        "conflicts": conflicts,
        "unknown": unknown,
        "existing_checksums_verified": not conflicts and not unknown,
    }


HOOK_REGISTRY: dict[str, dict[str, Any]] = {
    "242_user_platform_quotas_add_cn_providers.sql": {"script": "migration-242-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
    "195_upstream_scheduling_monitor_rates.sql": {"script": "migration-195-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": ("bind",), "postflight": ("postflight_db", "postflight_runtime")},
    "232_clear_non_grok_video_generation_config.sql": {"script": "migration-232-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": ("bind",), "postflight": ("postflight",)},
    "233_upstream_management.sql": {"script": "migration-233-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
    "239_reconcile_non_grok_video_pricing.sql": {"script": "migration-239-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": ("bind",), "postflight": ("postflight",)},
    "243_backfill_codex_fingerprint_seed.sql": {"script": "migration-243-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
    "244_channel_model_time_pricing.sql": {"script": "migration-244-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
    "245_channel_monitor_quota_mode.sql": {"script": "migration-245-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
    "254_enable_balance_notifications_for_existing_users.sql": {"script": "migration-254-assert.sh", "rollback_policy": "coordinated_restore", "preflight": ("preflight",), "bind": (), "postflight": ("postflight",)},
}


def pending_hooks(pending: Iterable[Mapping[str, Any]]) -> list[dict[str, Any]]:
    result = []
    for item in pending:
        hook = HOOK_REGISTRY.get(str(item["filename"]))
        if hook:
            result.append({"filename": str(item["filename"]), **hook})
    return result
