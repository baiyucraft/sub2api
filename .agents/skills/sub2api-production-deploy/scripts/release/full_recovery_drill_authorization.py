"""A separate, time-bounded authorization domain for an offline DR Guest.

No Gate schema or pass assertion is emitted.  Production recovery continues
to require its signed Gate.  The caller MUST perform the formal isolation
guard before invoking this verifier inside an owned Guest.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import sys
import time
from typing import Any

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey, Ed25519PrivateKey

DOMAIN = "sub2api-isolated-recovery-authorization"
FIELDS = {"schema", "type", "intended_use", "drill_id", "release_id", "target_commit", "recovery_helper_bundle_sha256", "trusted_public_key_sha256", "collector_sha256", "collector_id", "verifier_sha256", "bundle_verifier_sha256", "isolation_guard_sha256", "artifact_sha256", "artifact_bytes", "recipient_type", "pre_image_id", "approved_identity_sha256", "approved_recipient_sha256", "created_at", "expires_at", "reviewer", "audit_id", "user_authorization_sha256", "drill_context", "guest_manifest_sha256"}
CONTEXT_FIELDS = {"schema", "type", "release_id", "source_snapshot_sha256", "commit_sha", "profile", "version", "deployment_mode", "candidate_image_id", "archive_kind", "archive_sha256"}
SHA = re.compile(r"[0-9a-f]{64}")
DRILL = re.compile(r"full-dr-[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}")


def require(value: bool, reason: str) -> None:
    if not value:
        raise RuntimeError("isolated recovery authorization rejected: " + reason)


def canonical(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode()


def read(path: Path, *, private: bool = True) -> bytes:
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and path.resolve() == path.absolute(), "regular canonical input")
    if private and os.name != "nt":
        require((info.st_uid, info.st_gid) == (0, 0) and stat.S_IMODE(info.st_mode) in {0o400, 0o600}, "root-only input")
    fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0))
    with os.fdopen(fd, "rb") as stream:
        observed = os.fstat(stream.fileno())
        require((info.st_dev, info.st_ino) == (observed.st_dev, observed.st_ino) and observed.st_nlink == 1 and observed.st_size <= 4 * 1024 * 1024, "bounded unchanged input")
        return stream.read(4 * 1024 * 1024 + 1)


def load(path: Path) -> dict[str, Any]:
    def unique(pairs):
        output = {}
        for key, value in pairs:
            require(key not in output, "duplicate JSON field")
            output[key] = value
        return output
    value = json.loads(read(path), object_pairs_hook=unique, parse_constant=lambda x: require(False, "non-finite JSON"))
    require(isinstance(value, dict), "JSON object")
    return value


def validate(payload: dict[str, Any], *, now: int) -> None:
    require(isinstance(payload, dict) and set(payload) == FIELDS, "authorization fields")
    require(type(payload["schema"]) is int and payload["schema"] == 1 and payload["type"] == DOMAIN and payload["intended_use"] == "offline-full-dr-only", "separate offline domain")
    require(isinstance(payload["drill_id"], str) and DRILL.fullmatch(payload["drill_id"]) is not None, "drill UUID")
    require(isinstance(payload["release_id"], str) and re.fullmatch(r"[0-9]{3}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}", payload["release_id"]) is not None, "release identity")
    require(isinstance(payload["target_commit"], str) and re.fullmatch(r"[0-9a-f]{40}", payload["target_commit"]) is not None, "immutable target")
    for key in FIELDS:
        if key.endswith("sha256"):
            require(isinstance(payload[key], str) and SHA.fullmatch(payload[key]) is not None, "fingerprint " + key)
    require(payload["collector_id"] == "linux-root:" + payload["collector_sha256"], "exact collector identity")
    require(type(payload["artifact_bytes"]) is int and payload["artifact_bytes"] > 0 and payload["recipient_type"] == "age1", "bounded existing age1 source")
    require(isinstance(payload["pre_image_id"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", payload["pre_image_id"]) is not None, "old image")
    context = payload["drill_context"]
    require(isinstance(context, dict) and set(context) == CONTEXT_FIELDS, "offline context fields")
    require(type(context["schema"]) is int and context["schema"] == 1 and context["type"] == "sub2api-offline-recovery-context", "offline context domain")
    require(context["release_id"] == payload["release_id"] and context["candidate_image_id"] == payload["pre_image_id"], "offline context old source identity")
    require(context["deployment_mode"] == "downtime" and context["archive_kind"] == "encrypted-recovery-source" and context["archive_sha256"] == payload["artifact_sha256"], "offline context is not a candidate Gate")
    require(isinstance(context["source_snapshot_sha256"], str) and SHA.fullmatch(context["source_snapshot_sha256"]) is not None, "independently authorized source snapshot")
    require(isinstance(context["commit_sha"], str) and re.fullmatch(r"[0-9a-f]{40}", context["commit_sha"]) is not None, "offline source commit")
    # release_id is the isolated capture/execution namespace. It need not
    # identify the running production revision represented by this snapshot.
    require(type(context["profile"]) is int and 100 <= context["profile"] <= 999, "offline source profile")
    require(isinstance(context["version"], str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,79}", context["version"]) is not None, "offline source version")
    require(isinstance(payload["reviewer"], str) and re.fullmatch(r"[A-Za-z0-9_.@-]{3,128}", payload["reviewer"]) is not None and isinstance(payload["audit_id"], str) and re.fullmatch(r"audit-[0-9a-f]{32}", payload["audit_id"]) is not None, "independent authorization review")
    require(all(type(payload[k]) is int and payload[k] > 0 for k in ("created_at", "expires_at")), "integer timestamps")
    require(payload["created_at"] <= now + 300 and now <= payload["expires_at"] <= payload["created_at"] + 7200, "authorization expired or excessive lifetime")


def verify_authorization(path: Path, *, trust: Path, bundle: Path, drill_id: str, release_id: str, expected_bundle: str, now: int | None = None) -> dict[str, Any]:
    envelope = load(path)
    require(set(envelope) == {"payload", "signature"}, "signed offline envelope")
    payload, signature = envelope["payload"], envelope["signature"]
    validate(payload, now=int(time.time()) if now is None else now)
    require(isinstance(signature, dict) and set(signature) == {"algorithm", "key_sha256", "value"} and signature["algorithm"] == "Ed25519", "signature shape")
    public = read(trust, private=False)
    require(hashlib.sha256(public).hexdigest() == signature["key_sha256"] == payload["trusted_public_key_sha256"], "existing trust root")
    key = serialization.load_pem_public_key(public)
    require(isinstance(key, Ed25519PublicKey), "existing Ed25519 public root")
    raw = base64.b64decode(signature["value"], validate=True)
    require(len(raw) == 64 and base64.b64encode(raw).decode() == signature["value"], "signature encoding")
    key.verify(raw, canonical(payload))
    require(payload["drill_id"] == drill_id and payload["release_id"] == release_id and payload["recovery_helper_bundle_sha256"] == expected_bundle, "exact owned Guest identity")
    # Verify the validator's own bytes and the isolation guard before loading
    # any executable verifier from the bundle.
    require(hashlib.sha256(read(Path(__file__).resolve(), private=False)).hexdigest() == payload["verifier_sha256"], "authorization verifier bytes")
    for name, field in (("recovery_bundle.py", "bundle_verifier_sha256"), ("recovery-isolation.sh", "isolation_guard_sha256")):
        require(hashlib.sha256(read(bundle / name, private=False)).hexdigest() == payload[field], "signed helper verifier bytes")
    spec = importlib.util.spec_from_file_location("controlled_dr_bundle", bundle / "recovery_bundle.py")
    require(spec is not None and spec.loader is not None, "bundle verifier load")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    document = load(bundle / "recovery-helper-bundle.json")
    require(module.FULL_RECOVERY_REQUIRED.issubset(module.execution_paths(document["files"])), "full isolated helper closure")
    require(document["commit_sha"] == payload["target_commit"] and module.recovery_bundle_sha256(document) == expected_bundle, "complete helper closure binding")
    module.verify_recovery_bundle(bundle, document)
    return payload


def sign_authorization(payload: dict[str, Any], *, private_key: Path, trust: Path, now: int | None = None) -> dict[str, Any]:
    validate(payload, now=int(time.time()) if now is None else now)
    key = serialization.load_pem_private_key(read(private_key), password=None)
    require(isinstance(key, Ed25519PrivateKey), "existing private root")
    public = read(trust, private=False)
    require(key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo) == public and hashlib.sha256(public).hexdigest() == payload["trusted_public_key_sha256"], "existing signer/trust match")
    return {"payload": payload, "signature": {"algorithm": "Ed25519", "key_sha256": payload["trusted_public_key_sha256"], "value": base64.b64encode(key.sign(canonical(payload))).decode()}}


def verify_context(path: Path, payload: dict[str, Any]) -> None:
    require(load(path) == payload["drill_context"], "claim context differs from signed offline source")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--authorization", type=Path, required=True)
    parser.add_argument("--trust", type=Path, required=True)
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--drill-id", required=True)
    parser.add_argument("--release-id", required=True)
    parser.add_argument("--expected-bundle", required=True)
    parser.add_argument("--context", type=Path, required=True)
    args = parser.parse_args()
    # Namespace isolation is verified independently by the formal shell guard
    # immediately before this program; direct production use also fails here.
    require(sys.platform.startswith("linux") and os.geteuid() == 0 and os.environ.get("RECOVERY_DRILL_MODE") == "1", "offline drill process required")
    require(Path("/run/sub2api-recovery-drill-isolated").is_file() and Path("/run/sub2api-recovery-drill-host.json").is_file(), "formal isolation proofs required")
    payload = verify_authorization(args.authorization, trust=args.trust, bundle=args.bundle, drill_id=args.drill_id, release_id=args.release_id, expected_bundle=args.expected_bundle)
    verify_context(args.context, payload)


if __name__ == "__main__":
    main()
