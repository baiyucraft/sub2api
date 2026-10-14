"""Controlled signer for an independently measured full recovery review.

This module is deliberately separate from the VM Gate signer.  It never
creates a measurement, chooses a reviewer, or generates a key.  A root-only
receipt from the isolated Guest and a separately prepared approval document
are required before the existing VM trust key may sign the schema-2 review.
The resulting signature authorizes review evidence only; it is not a Gate.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import sys
import time
from typing import Any

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from .atomic import canonical_json
from .paths import TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .recovery_evidence import (
    DRILL,
    MAX_EVIDENCE_BYTES,
    PAYLOAD_FIELDS,
    checksum,
    derive_expected_scope,
    validate_measurements,
    strict_json,
    verify_full_recovery_evidence,
)


APPROVAL_FIELDS = {
    "schema", "type", "drill_id", "collector_id", "collector_sha256",
    "reviewer", "audit_id", "independent", "approved_at", "expires_at",
    "findings_sha256", "user_authorization_sha256",
    "receipt_sha256", "classification_sha256", "approved_identity_sha256",
    "approved_recipient_sha256",
}
RECEIPT_FIELDS = {
    "schema", "type", "drill_id", "created_at", "completed_at", "source",
    "measurements", "recovery_helper_bundle_sha256",
}
SHA = re.compile(r"[0-9a-f]{64}")
AUDIT = re.compile(r"audit-[0-9a-f]{32}")
COLLECTOR = re.compile(r"linux-root:[0-9a-f]{64}")


def reject(reason: str) -> None:
    raise RuntimeError("full recovery authorization rejected: " + reason)


def _regular(path: Path, *, maximum: int = MAX_EVIDENCE_BYTES, private: bool = True) -> bytes:
    try:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or path.resolve() != path.absolute():
            reject("canonical regular file required")
        if private and os.name != "nt" and (info.st_uid != 0 or info.st_gid != 0 or stat.S_IMODE(info.st_mode) not in {0o400, 0o600}):
            reject("root-only input required")
        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
        with os.fdopen(fd, "rb") as stream:
            observed = os.fstat(stream.fileno())
            if (observed.st_dev, observed.st_ino) != (info.st_dev, info.st_ino) or observed.st_nlink != 1:
                reject("input changed while opening")
            if observed.st_size > maximum:
                reject("input size limit")
            value = stream.read(maximum + 1)
        if len(value) > maximum:
            reject("input size limit")
        return value
    except OSError as error:
        raise RuntimeError("full recovery authorization rejected: input unavailable") from error


def _json(path: Path) -> dict[str, Any]:
    return strict_json(_regular(path))


def _sha_file(path: Path) -> str:
    return hashlib.sha256(_regular(path, maximum=16 * 1024 * 1024)).hexdigest()


def _approval(value: dict[str, Any], *, now: int) -> dict[str, Any]:
    if set(value) != APPROVAL_FIELDS:
        reject("approval fields")
    if type(value["schema"]) is not int or value["schema"] != 1 or value["type"] != "sub2api-full-recovery-approval":
        reject("approval domain")
    if not isinstance(value["drill_id"], str) or not DRILL.fullmatch(value["drill_id"]):
        reject("approval drill identity")
    if not isinstance(value["collector_id"], str) or not COLLECTOR.fullmatch(value["collector_id"]):
        reject("collector identity")
    if not isinstance(value["collector_sha256"], str) or not SHA.fullmatch(value["collector_sha256"]):
        reject("collector fingerprint")
    if not isinstance(value["reviewer"], str) or not re.fullmatch(r"[A-Za-z0-9_.@-]{3,128}", value["reviewer"]):
        reject("reviewer identity")
    if not isinstance(value["audit_id"], str) or not AUDIT.fullmatch(value["audit_id"]):
        reject("audit identity")
    if value["independent"] is not True or not all(type(value[k]) is int and value[k] > 0 for k in ("approved_at", "expires_at")):
        reject("approval timestamps")
    if value["approved_at"] > now + 300 or value["expires_at"] < now or value["expires_at"] > value["approved_at"] + 86400:
        reject("approval expired or lifetime too long")
    for key in ("findings_sha256", "user_authorization_sha256", "receipt_sha256", "classification_sha256", "approved_identity_sha256", "approved_recipient_sha256"):
        if not isinstance(value[key], str) or not SHA.fullmatch(value[key]):
            reject("approval digest")
    return value


def _receipt(value: dict[str, Any], *, now: int) -> dict[str, Any]:
    if set(value) != RECEIPT_FIELDS or type(value["schema"]) is not int or value["schema"] != 1 or value["type"] != "sub2api-full-recovery-measurements":
        reject("measurement receipt fields or domain")
    if not isinstance(value["drill_id"], str) or not DRILL.fullmatch(value["drill_id"]):
        reject("measurement drill identity")
    if not all(type(value[k]) is int and value[k] > 0 for k in ("created_at", "completed_at")):
        reject("measurement timestamps")
    if value["created_at"] > value["completed_at"] or value["completed_at"] > now + 300 or value["completed_at"] < now - 86400:
        reject("measurement receipt expired or future")
    if not isinstance(value["source"], dict) or not isinstance(value["measurements"], dict) or not SHA.fullmatch(value["recovery_helper_bundle_sha256"]):
        reject("measurement body")
    return value


def _payload(receipt: dict[str, Any], approval: dict[str, Any], scope: dict[str, Any]) -> dict[str, Any]:
    if receipt["drill_id"] != approval["drill_id"]:
        reject("approval and receipt drill mismatch")
    # The receipt is the sole source of measured values.  Only the reviewer
    # block is added by a human approval; no status or measurement is inferred.
    payload = {**scope, "drill_id": receipt["drill_id"], "created_at": receipt["created_at"], "completed_at": receipt["completed_at"], "source": receipt["source"], "review": {k: approval[k] for k in ("reviewer", "audit_id", "independent", "approved_at", "findings_sha256")}, "measurements": receipt["measurements"]}
    if set(payload) != PAYLOAD_FIELDS:
        reject("schema-2 payload fields")
    if payload["recovery_helper_bundle_sha256"] != receipt["recovery_helper_bundle_sha256"]:
        reject("helper bundle differs")
    validate_measurements(payload["measurements"], payload)
    return payload


def build_request(*, classification: Path, receipt: Path, approval: Path, workspace: Path = WORKSPACE, now: int | None = None) -> dict[str, Any]:
    current = int(time.time()) if now is None else now
    report = _json(classification)
    measured = _receipt(_json(receipt), now=current)
    consent = _approval(_json(approval), now=current)
    scope = derive_expected_scope(workspace, report)
    if consent["classification_sha256"] != checksum(report) or consent["receipt_sha256"] != _sha_file(receipt):
        reject("independent approval does not bind actual receipt and classification")
    source = measured["source"]
    if source.get("approved_identity_sha256") != consent["approved_identity_sha256"] or source.get("approved_recipient_sha256") != consent["approved_recipient_sha256"]:
        reject("independently approved decrypt identity differs")
    if consent["approved_at"] < measured["completed_at"]:
        reject("review predates measurement completion")
    collector_path = ".agents/skills/sub2api-production-deploy/scripts/release/full_recovery_drill.py"
    from .recovery_evidence import git
    collector_sha = hashlib.sha256(git(workspace, "show", scope["target_commit"] + ":" + collector_path)).hexdigest()
    if consent["collector_sha256"] != collector_sha or consent["collector_id"] != "linux-root:" + collector_sha:
        reject("collector differs from exact reviewed Git target")
    payload = _payload(measured, consent, scope)
    # This request is intentionally unsigned.  It is an auditable handoff to
    # the fixed signer and cannot itself authorize a release or a Gate.
    return {"schema": 1, "type": "sub2api-full-recovery-sign-request", "payload": payload, "receipt_sha256": _sha_file(receipt), "approval_sha256": _sha_file(approval), "collector_id": consent["collector_id"], "collector_sha256": consent["collector_sha256"], "expires_at": consent["expires_at"]}


def sign_request(*, request: Path, classification: Path, receipt: Path, approval: Path, private_key: Path, workspace: Path = WORKSPACE, output: Path, now: int | None = None) -> dict[str, Any]:
    current = int(time.time()) if now is None else now
    document = _json(request)
    if document != build_request(classification=classification, receipt=receipt, approval=approval, workspace=workspace, now=current):
        reject("request differs from independently approved measured inputs")
    if set(document) != {"schema", "type", "payload", "receipt_sha256", "approval_sha256", "collector_id", "collector_sha256", "expires_at"} or document["schema"] != 1 or document["type"] != "sub2api-full-recovery-sign-request":
        reject("sign request fields or domain")
    consent = _approval(_json(approval), now=current)
    if document["approval_sha256"] != _sha_file(approval) or document["collector_id"] != consent["collector_id"] or document["collector_sha256"] != consent["collector_sha256"] or document["expires_at"] != consent["expires_at"]:
        reject("approval changed after request")
    payload = document["payload"]
    if not isinstance(payload, dict) or payload.get("drill_id") != consent["drill_id"]:
        reject("request payload identity")
    if current > consent["expires_at"]:
        reject("approval expired")
    report = _json(classification)
    if set(payload) != PAYLOAD_FIELDS:
        reject("request payload fields")
    validate_measurements(payload["measurements"], payload)
    try:
        key_bytes = _regular(private_key, maximum=64 * 1024)
        key = serialization.load_pem_private_key(key_bytes, password=None)
    except Exception as error:
        raise RuntimeError("full recovery authorization rejected: existing Ed25519 signer unavailable") from error
    if not isinstance(key, Ed25519PrivateKey):
        reject("existing signer is not Ed25519")
    trust = _regular(TRUSTED_VM_PUBLIC_KEY, maximum=64 * 1024, private=False)
    derived = key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo)
    if hashlib.sha256(derived).digest() != hashlib.sha256(trust).digest():
        reject("signer does not match existing trust root")
    signed = {"schema": 2, "type": "sub2api-full-recovery-review", "payload": payload}
    envelope = {**signed, "signature": {"algorithm": "Ed25519", "key_sha256": hashlib.sha256(trust).hexdigest(), "value": base64.b64encode(key.sign(canonical_json(signed))).decode("ascii")}}
    verify_full_recovery_evidence(envelope, workspace=workspace, report=report, now=current)
    if output.exists() or output.is_symlink():
        reject("output already exists")
    _write_new(output, canonical_json(envelope))
    return envelope


def _write_new(path: Path, raw: bytes) -> None:
    if not path.parent.is_dir() or path.parent.resolve() != path.parent.absolute():
        reject("output parent must already exist and be canonical")
    if os.name != "nt":
        info = path.parent.stat()
        if (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) != (0, 0, 0o700):
            reject("output directory must be root-only")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o400)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw)
        stream.flush()
        os.fsync(stream.fileno())


def issue_drill_authorization(*, payload_path: Path, approval_path: Path, output: Path, workspace: Path = WORKSPACE, private_key: Path = Path("/opt/sub2api-release-signer/vm-gate-ed25519.pem"), now: int | None = None) -> dict[str, Any]:
    """Issue a planned offline-execution authorization, never a Gate result.

    An independently prepared approval binds the complete request bytes. The
    signer recomputes helper/code fingerprints from the immutable target.
    """
    from .full_recovery_drill_authorization import validate, sign_authorization
    from .recovery_evidence import git, tree
    from .recovery_bundle import recovery_bundle_document, recovery_bundle_sha256
    from .manifest import is_release_asset_relative_path, release_asset_pathspecs
    from .paths import LAYOUT_SKILL_V1
    current = int(time.time()) if now is None else now
    payload = _json(payload_path)
    consent = _json(approval_path)
    expected = {"schema", "type", "request_sha256", "reviewer", "audit_id", "independent", "approved_at", "user_authorization_sha256"}
    if set(consent) != expected or type(consent["schema"]) is not int or consent["schema"] != 1 or consent["type"] != "sub2api-isolated-recovery-approval" or consent["independent"] is not True:
        reject("independent offline approval required")
    if consent["request_sha256"] != _sha_file(payload_path) or any(consent[k] != payload[k] for k in ("reviewer", "audit_id", "user_authorization_sha256")):
        reject("offline request differs from independently approved bytes")
    if type(consent["approved_at"]) is not int or not 0 < consent["approved_at"] <= current + 300 or consent["approved_at"] < payload["created_at"]:
        reject("offline approval time")
    validate(payload, now=current)
    target = payload["target_commit"]
    if git(workspace, "rev-parse", "--verify", target + "^{commit}").decode().strip() != target:
        reject("immutable offline target")
    assets = {p: hashlib.sha256(git(workspace, "cat-file", "blob", b)).hexdigest() for p, (m, b) in tree(workspace, target, release_asset_pathspecs(LAYOUT_SKILL_V1)).items() if is_release_asset_relative_path(p, LAYOUT_SKILL_V1) and m in {"100644", "100755"}}
    bundle = recovery_bundle_document(target, assets)
    if recovery_bundle_sha256(bundle) != payload["recovery_helper_bundle_sha256"]:
        reject("offline complete helper closure")
    prefix = ".agents/skills/sub2api-production-deploy/scripts/"
    for field, relative in (("collector_sha256", "release/full_recovery_drill.py"), ("verifier_sha256", "release/full_recovery_drill_authorization.py"), ("bundle_verifier_sha256", "release/recovery_bundle.py"), ("isolation_guard_sha256", "maintenance/release/recovery-isolation.sh"), ("trusted_public_key_sha256", "release/trust/vm-gate-ed25519.pub")):
        if payload[field] != assets.get(prefix + relative):
            reject("offline reviewed code fingerprint differs")
    envelope = sign_authorization(payload, private_key=private_key, trust=TRUSTED_VM_PUBLIC_KEY, now=current)
    _write_new(output, canonical_json(envelope))
    return envelope


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="controlled full recovery review authorization")
    sub = parser.add_subparsers(dest="command", required=True)
    request = sub.add_parser("request")
    request.add_argument("--classification", type=Path, required=True)
    request.add_argument("--receipt", type=Path, required=True)
    request.add_argument("--approval", type=Path, required=True)
    request.add_argument("--output", type=Path, required=True)
    sign = sub.add_parser("sign")
    sign.add_argument("--request", type=Path, required=True)
    sign.add_argument("--approval", type=Path, required=True)
    sign.add_argument("--classification", type=Path, required=True)
    sign.add_argument("--receipt", type=Path, required=True)
    sign.add_argument("--output", type=Path, required=True)
    authorize = sub.add_parser("authorize-drill")
    authorize.add_argument("--payload", type=Path, required=True)
    authorize.add_argument("--approval", type=Path, required=True)
    authorize.add_argument("--output", type=Path, required=True)
    args = parser.parse_args(argv)
    if args.command == "request":
        _write_new(args.output, canonical_json(build_request(classification=args.classification, receipt=args.receipt, approval=args.approval)))
    else:
        if not sys.platform.startswith("linux") or os.geteuid() != 0:
            reject("existing VM signer requires Linux root")
        identity_path = args.payload if args.command == "authorize-drill" else args.receipt
        identity_document = _json(identity_path)
        drill_id = identity_document.get("drill_id", "")
        if not isinstance(drill_id, str) or not DRILL.fullmatch(drill_id):
            reject("fixed issuer drill identity")
        output_name = "drill-authorization.json" if args.command == "authorize-drill" else "full-recovery-evidence.json"
        issued_directory = Path("/var/lib/sub2api-full-recovery-drills") / drill_id
        if args.output.resolve() != (issued_directory / output_name).resolve():
            reject("one immutable output per drill and signing domain required")
        if args.command == "sign" and args.receipt.resolve() != (issued_directory / "measurements.json").resolve():
            reject("fixed isolated collector receipt path required")
        import fcntl
        lock_path = Path("/opt/sub2api-release-signer/.full-recovery.lock")
        _write_new(lock_path, b"") if not lock_path.exists() else None
        _regular(lock_path, maximum=0)
        with lock_path.open("rb") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            if args.command == "authorize-drill":
                issue_drill_authorization(payload_path=args.payload, approval_path=args.approval, output=args.output)
            else:
                sign_request(request=args.request, classification=args.classification, receipt=args.receipt, approval=args.approval, private_key=Path("/opt/sub2api-release-signer/vm-gate-ed25519.pem"), output=args.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
