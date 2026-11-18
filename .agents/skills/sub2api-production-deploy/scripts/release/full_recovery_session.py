"""Evidence and owner ordering for an exited, failed offline Full DR session."""
from __future__ import annotations

import base64
import binascii
import hashlib
import json
import os
import re
import stat
import time
from pathlib import Path

from .atomic import atomic_write, canonical_json, read_replaceable_bytes


class FullRecoverySessionBlocked(RuntimeError):
    pass


_DIGEST = r"[0-9a-f]{64}"
_UUID = r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}"
_DRILL = r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}"
_RELEASE = r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}"
_DIAGNOSTIC = r"(?:failure\.json|stage-[a-z_]{1,64}|[a-z_]{1,64}\.(?:started|done))"
_FOREIGN = {"docker", "data_dev", "backup", "production"}
_PLAN = "failed-closure-plan.json"
_ARCHIVE = "failed-closure-diagnostics.json"
_INTENT = "failed-closure-cleanup-intent.json"
_RECEIPT = "failed-closure-receipt.json"
_REMOTE_RECEIPT = "failed-closure-remote-receipt.json"
_MAX_ARCHIVE = 1024 * 1024


def _need(condition, reason):
    if not condition:
        raise FullRecoverySessionBlocked(reason)


def _matches(value, regex):
    return isinstance(value, str) and re.fullmatch(regex, value) is not None


def _fields(value, names):
    return isinstance(value, dict) and value.keys() == names


def _integer(value, minimum=0):
    return type(value) is int and value >= minimum


def _sha(raw):
    return hashlib.sha256(raw).hexdigest()


def _same(first, second):
    return canonical_json(first) == canonical_json(second)


def _directory(path):
    _need(path.is_absolute() and path.resolve(strict=True) == path, "session_directory_unsafe")
    for parent in (path, *path.parents):
        info = parent.lstat()
        _need(stat.S_ISDIR(info.st_mode) and not getattr(info, "st_file_attributes", 0) & 0x400,
              "session_directory_unsafe")


def _read(path, limit=16 * 1024):
    info = path.lstat()
    _need(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and
          not getattr(info, "st_file_attributes", 0) & 0x400, "session_file_unsafe")
    return read_replaceable_bytes(path, max_bytes=limit)


def _object(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            _need(key not in result, "session_json_duplicate_field")
            result[key] = value
        return result

    def reject(_value):
        raise FullRecoverySessionBlocked("session_json_invalid_number")

    value = json.loads(raw, object_pairs_hook=unique, parse_float=reject, parse_constant=reject)
    _need(isinstance(value, dict), "session_json_not_object")
    return value


def _exists(path):
    return path.exists() or path.is_symlink()


def _persist(path, document, limit=16 * 1024):
    raw = canonical_json(document) + b"\n"
    _need(len(raw) <= limit, "session_evidence_size_limit")
    if _exists(path):
        _need(_read(path, limit) == raw, "session_evidence_already_differs")
    else:
        atomic_write(path, raw)
        _need(_read(path, limit) == raw, "session_evidence_readback_mismatch")
    return _sha(raw)


def _validate_remote(remote, boot):
    _need(_fields(remote, {"pid", "starttime", "boot_id", "locks"}) and _integer(remote["pid"], 1) and
          _matches(remote["starttime"], r"[1-9][0-9]*") and remote["boot_id"] == boot and
          _fields(remote["locks"], {"8", "9"}), "holder_remote_identity_invalid")
    for fd, mode in ((8, "READ"), (9, "WRITE")):
        lock = remote["locks"][str(fd)]
        _need(_fields(lock, {"fd", "mode", "device", "inode"}) and _integer(lock["fd"], 1) and
              lock["fd"] == fd and lock["mode"] == mode and _integer(lock["device"]) and
              _integer(lock["inode"], 1), "holder_remote_lock_identity_invalid")
    pair = {(v["device"], v["inode"]) for v in remote["locks"].values()}
    _need(len(pair) == 2, "holder_remote_locks_not_distinct")


def _session_documents(directory, owner_path):
    _directory(directory)
    _directory(owner_path.parent)
    _need(owner_path.name == "owner.json", "session_owner_path_invalid")
    owner_raw = _read(owner_path)
    holder_raw = _read(directory / "locks-status.json")
    intent_raw = _read(directory / "drill-intent.json")
    owner, holder, intent = map(_object, (owner_raw, holder_raw, intent_raw))
    _need(all(_integer(row.get("schema"), 1) and row["schema"] == 1 for row in (owner, holder, intent)),
          "session_schema_invalid")
    return owner, holder, intent, owner_raw, holder_raw, intent_raw


def inspect_session(directory: Path, owner_path: Path):
    """Freeze previous holder/owner input bytes; the adapter must prove live state separately."""
    owner, holder, intent, owner_raw, holder_raw, intent_raw = _session_documents(directory, owner_path)
    source = _sha(_read(directory / "hold-locks.py", 256 * 1024))
    drill, target, release = (intent.get(k) for k in ("drill_id", "target_commit", "source_release_id"))
    bundle, boot = intent.get("bundle_sha256"), owner.get("vm_boot_id")
    pid, token = owner.get("pid"), owner.get("process_token")
    _need(_matches(drill, _DRILL) and _matches(target, r"[0-9a-f]{40}") and _matches(release, _RELEASE) and
          _matches(bundle, _DIGEST) and _matches(boot, _UUID), "session_intent_identity_invalid")
    _need(_matches(owner.get("vm_identity"), _DIGEST) and _matches(owner.get("lease_token"), r"[0-9a-f]{32}") and
          owner.get("lease_status") in {"active", "retained"} and owner.get("vm_power_status") == "running" and
          all(type(owner.get(k)) is bool for k in ("initially_running", "vm_started_by_release")) and
          owner.get("release_id") == release and owner.get("source_commit") == target and
          _integer(pid, 1) and isinstance(token, str) and 1 <= len(token) <= 200 and
          not any(ch.isspace() for ch in token), "session_owner_identity_invalid")
    _need(holder.get("status") in {"failed_held", "failed_retained"} and holder.get("drill_id") == drill and
          holder.get("target_commit") == target and _integer(holder.get("pid"), 1) and holder["pid"] == pid and
          holder.get("process_token") == token and holder.get("holder_sha256") == source and
          holder.get("vm_boot_id") == boot, "session_holder_identity_invalid")
    _validate_remote(holder.get("remote"), boot)
    return {
        "identity": {"drill_id": drill, "release_id": release, "target_commit": target,
                     "bundle_sha256": bundle, "vm_identity": owner["vm_identity"], "boot_id": boot,
                     "pid": pid, "process_token": token, "remote": holder["remote"],
                     "holder_source_sha256": source},
        "lease_token": owner["lease_token"],
        "owner_sha256": _sha(owner_raw), "holder_sha256": _sha(holder_raw), "intent_sha256": _sha(intent_raw),
    }


def _validate_observation(identity, observation, *, deleted=False):
    fields = {"schema", "identity", "local_holder", "remote_holder", "unit", "guest_processes", "mount_references",
              "process_references", "rootfs", "production_before_sha256", "production_now_sha256"}
    _need(_fields(observation, fields) and _integer(observation["schema"], 1) and observation["schema"] == 1 and
          _same(observation["identity"], identity), "closure_observation_identity_changed")
    _need(observation["local_holder"] == observation["remote_holder"] == "exited" and
          observation["unit"] == "inactive" and all(_integer(observation[k]) and observation[k] == 0
          for k in ("guest_processes", "mount_references", "process_references")), "closure_workers_or_references_unproven")
    production = observation["production_before_sha256"]
    _need(_matches(production, _DIGEST) and production == observation["production_now_sha256"],
          "closure_production_changed")
    rootfs = observation["rootfs"]
    if deleted:
        _need(rootfs is None, "closure_rootfs_still_exists")
    else:
        target = "/var/lib/sub2api-full-recovery-drills/" + identity["drill_id"] + "/rootfs"
        _need(_fields(rootfs, {"path", "device", "inode", "uid", "gid", "mode"}) and rootfs["path"] == target and
              _integer(rootfs["device"]) and _integer(rootfs["inode"], 1) and
              all(_integer(rootfs[k]) and rootfs[k] == 0 for k in ("uid", "gid")) and
              _integer(rootfs["mode"]) and rootfs["mode"] == 0o700, "closure_rootfs_identity_unproven")


def _archive(session, diagnostics):
    _need(isinstance(diagnostics, dict) and 1 <= len(diagnostics) <= 64 and "failure.json" in diagnostics,
          "closure_diagnostics_incomplete")
    contents = {}
    for name, raw in diagnostics.items():
        _need(_matches(name, _DIAGNOSTIC) and isinstance(raw, bytes) and 0 < len(raw) <= 65536,
              "closure_diagnostic_not_allowed")
        contents[name] = {"sha256": _sha(raw), "bytes": len(raw), "data": base64.b64encode(raw).decode("ascii")}
    marker = _object(diagnostics["failure.json"])
    _need(_fields(marker, {"schema", "status", "stage", "error_type", "reason_code"}) and
          _integer(marker["schema"], 1) and marker["schema"] == 1 and marker["status"] == "blocked" and
          _matches(marker["stage"], r"[a-z_]{1,64}") and _matches(marker["reason_code"], r"[a-z_]{1,64}") and
          _matches(marker["error_type"], r"[A-Za-z_][A-Za-z0-9_]{0,79}"), "closure_failure_marker_invalid")
    return {"schema": 1, "session": session, "diagnostics": contents}


def _validate_foreign(fingerprints):
    _need(_fields(fingerprints, _FOREIGN) and all(_matches(value, _DIGEST) for value in fingerprints.values()),
          "closure_foreign_fingerprints_unproven")


def _archive_location(directory, archive_dir):
    _directory(archive_dir)
    _need(archive_dir != directory and directory not in archive_dir.parents,
          "closure_archive_directory_not_separate")
    if os.name != "nt":
        info = archive_dir.stat()
        _need(info.st_uid == os.geteuid() and stat.S_IMODE(info.st_mode) == 0o700,
              "closure_archive_permissions_unproven")


def prepare_failure_archive(directory: Path, session, observation, diagnostics, *, archive_dir: Path, foreign):
    """Save bounded diagnostic bytes in an existing caller-owned private directory."""
    _directory(directory)
    _archive_location(directory, archive_dir)
    _validate_observation(session["identity"], observation)
    _validate_foreign(foreign)
    _need(foreign["production"] == observation["production_now_sha256"], "closure_production_fingerprint_mismatch")
    archive_sha = _persist(archive_dir / _ARCHIVE, _archive(session, diagnostics), _MAX_ARCHIVE)
    plan = {"schema": 1, "session": session, "observation": observation,
            "archive_sha256": archive_sha, "foreign": foreign}
    return _persist(directory / _PLAN, plan)


def _read_plan(directory, archive_dir, plan_sha256):
    raw = _read(directory / _PLAN)
    _need(_sha(raw) == plan_sha256, "closure_plan_digest_mismatch")
    plan = _object(raw)
    _need(_fields(plan, {"schema", "session", "observation", "archive_sha256", "foreign"}) and
          _integer(plan["schema"], 1) and plan["schema"] == 1, "closure_plan_invalid")
    archive_raw = _read(archive_dir / _ARCHIVE, _MAX_ARCHIVE)
    _need(_sha(archive_raw) == plan["archive_sha256"], "closure_archive_digest_mismatch")
    archive = _object(archive_raw)
    try:
        diagnostics = {name: base64.b64decode(item["data"], validate=True)
                       for name, item in archive["diagnostics"].items()}
    except (KeyError, TypeError, AttributeError, ValueError, binascii.Error):
        raise FullRecoverySessionBlocked("closure_archive_content_invalid") from None
    _need(_same(_archive(plan["session"], diagnostics), archive), "closure_archive_content_mismatch")
    _validate_observation(plan["session"]["identity"], plan["observation"])
    _validate_foreign(plan["foreign"])
    _need(plan["foreign"]["production"] == plan["observation"]["production_now_sha256"],
          "closure_production_fingerprint_mismatch")
    return plan, diagnostics


def apply_failed_closure(directory: Path, owner_path: Path, plan_sha256: str, adapter, *, archive_dir: Path):
    """Delete only the frozen failed rootfs; read back independent evidence before releasing owner.

    The trusted adapter holds the canonical release, VM identity, VM Gate and unit locks.
    It collects fresh process/boot/lock/resource evidence and rechecks the exact rootfs
    within its cleanup operation. This entry point never signals a process.
    """
    _directory(directory)
    _archive_location(directory, archive_dir)
    with adapter.locks():
        plan, diagnostics = _read_plan(directory, archive_dir, plan_sha256)
        session, before = plan["session"], plan["observation"]
        identity = session["identity"]
        current = adapter.observe(identity)
        intent = {"schema": 1, "plan_sha256": plan_sha256, "rootfs": before["rootfs"]}
        if _exists(directory / _RECEIPT):
            receipt_raw = _read(directory / _RECEIPT)
            receipt = _object(receipt_raw)
            _need(_fields(receipt, {"schema", "plan_sha256", "observation", "cleanup"}) and
                  _integer(receipt["schema"], 1) and receipt["schema"] == 1 and receipt["plan_sha256"] == plan_sha256 and
                  canonical_json(intent) + b"\n" == _read(directory / _INTENT), "closure_receipt_plan_mismatch")
            _validate_observation(identity, current, deleted=True)
            _need(_same(current, {**before, "rootfs": None}) and _same(receipt["observation"], current) and
                  _same(adapter.foreign(), plan["foreign"]),
                  "closure_post_cleanup_state_changed")
            _validate_cleanup(receipt.get("cleanup"), before["rootfs"], directory)
        else:
            _need(_same(inspect_session(directory, owner_path), session), "closure_owner_or_session_drift")
            _validate_observation(identity, current)
            _need(_same(current, before) and _same(adapter.foreign(), plan["foreign"]), "closure_pre_cleanup_state_changed")
            _need(not _exists(directory / _INTENT), "closure_prior_cleanup_state_uncertain")
            _need(getattr(adapter, "last_diagnostics", None) == diagnostics, "closure_diagnostics_changed")
            adapter.expected_diagnostics = {name: {"sha256": _sha(raw), "bytes": len(raw)}
                                            for name, raw in diagnostics.items()}
            _persist(directory / _INTENT, intent)
            cleanup = adapter.cleanup(before["rootfs"])
            _validate_cleanup(cleanup, before["rootfs"], directory)
            current = adapter.observe(identity)
            _validate_observation(identity, current, deleted=True)
            _need(_same(current, {**before, "rootfs": None}) and _same(adapter.foreign(), plan["foreign"]),
                  "closure_post_cleanup_state_changed")
            receipt = {"schema": 1, "plan_sha256": plan_sha256, "observation": current, "cleanup": cleanup}
            receipt_sha = _persist(directory / _RECEIPT, receipt)
            receipt_raw = _read(directory / _RECEIPT)
            _need(_sha(receipt_raw) == receipt_sha, "closure_cleanup_receipt_drift")

        # A failed cleanup never invokes VM shutdown or any success/Gate signer.
        owner, _, _, owner_raw, holder_raw, intent_raw = _session_documents(directory, owner_path)
        _need(_sha(holder_raw) == session["holder_sha256"] and _sha(intent_raw) == session["intent_sha256"] and
              _sha(_read(directory / "hold-locks.py", 256 * 1024)) == identity["holder_source_sha256"],
              "closure_holder_or_intent_drift")
        _validate_cleanup(receipt["cleanup"], before["rootfs"], directory)
        receipt_sha = _sha(receipt_raw)
        if owner.get("lease_status") == "released":
            _need(owner.get("full_dr_failure_closure_sha256") == receipt_sha and
                  owner.get("lease_token") == session["lease_token"] and
                  all(owner.get(k) == identity[k] for k in ("vm_identity", "release_id", "pid", "process_token")) and
                  owner.get("vm_boot_id") == identity["boot_id"] and owner.get("source_commit") == identity["target_commit"],
                  "closure_previous_owner_release_unproven")
        else:
            _need(_sha(owner_raw) == session["owner_sha256"], "closure_owner_changed")
            owner.update(lease_status="released", vm_cleanup_status="failed_dr_guest_removed",
                         full_dr_failure_closure_sha256=receipt_sha, updated_at=int(time.time()))
            _persist_owner(owner_path, owner)
        return {"status": "failed_closed", "plan_sha256": plan_sha256, "receipt_sha256": receipt_sha}


def _validate_cleanup(cleanup, rootfs, directory):
    _need(_fields(cleanup, {"schema", "status", "rootfs", "remote_receipt_sha256"}) and _integer(cleanup["schema"], 1) and
          cleanup["schema"] == 1 and cleanup["status"] == "deleted" and _same(cleanup["rootfs"], rootfs) and
          _matches(cleanup["remote_receipt_sha256"], _DIGEST),
          "closure_cleanup_receipt_invalid")
    try:
        remote_receipt = _read(directory / _REMOTE_RECEIPT)
    except FileNotFoundError:
        raise FullRecoverySessionBlocked("closure_remote_cleanup_receipt_missing") from None
    _need(_sha(remote_receipt) == cleanup["remote_receipt_sha256"], "closure_remote_cleanup_receipt_drift")


def _persist_owner(path, owner):
    raw = canonical_json(owner) + b"\n"
    atomic_write(path, raw)
    _need(_read(path) == raw, "closure_owner_release_readback_mismatch")
