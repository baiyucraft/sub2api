"""Bridge a real reviewed isolated restore to the existing baseline promoter.

This module never declares missing observations passed, generates a key, or
edits the legacy DR evidence contract. The existing VM signer and backup-side
atomic promoter remain the final authorization boundaries.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
import re
import shlex
from pathlib import Path
import time
from typing import Any

from .atomic import canonical_json
from .gate import verify_gate
from .manifest import sha256_file
from .paths import TRUSTED_VM_PUBLIC_KEY, WORKSPACE
from .recovery_evidence import read_regular_bytes, strict_json, verify_full_recovery_evidence
from .ssh import SSHRunner


SHA = re.compile(r"[0-9a-f]{64}\Z")
LOAD_FIELDS = {"schema", "type", "drill_id", "release_id", "candidate_archive_sha256", "candidate_image_id", "observed_image_id", "guest_docker_engine_id_sha256", "rootfs_identity_sha256", "load_exit_code", "created_at"}


def need(ok: bool, reason: str) -> None:
    if not ok:
        raise RuntimeError("postrelease baseline rejected: " + reason)


def _read(path: Path) -> dict[str, Any]:
    return strict_json(read_regular_bytes(path, maximum=4 * 1024 * 1024, private=True))


def _iso(epoch: int) -> str:
    return datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def build_legacy_evidence(
    *, gate_dir: Path, profile: str, classification: Path, full_review: Path,
    candidate_bundle_checksum: Path, candidate_load: Path, audit_findings: Path, now: int | None = None,
    workspace: Path = WORKSPACE,
) -> dict[str, Any]:
    """Validate real signed measurements and construct the unchanged schema 1.

    ``full_review`` must review the newly encrypted release recovery point,
    not the earlier pre-Gate source capture. A distinct candidate load receipt
    proves the signed application image was loaded in the same private Guest.
    The old restored application image is deliberately not substituted for it.
    """
    current = int(time.time()) if now is None else now
    gate = verify_gate(gate_dir, TRUSTED_VM_PUBLIC_KEY, profile, accepted_schemas=frozenset({2}))
    manifest, evidence = gate["manifest"], gate["evidence"]
    report = _read(classification)
    reviewed = _read(full_review)
    verified = verify_full_recovery_evidence(reviewed, workspace=workspace, report=report, now=current)
    payload = reviewed["payload"]
    source, measured = payload["source"], payload["measurements"]
    need(verified.target_commit == manifest["commit_sha"] and source["release_id"] == manifest["release_id"], "review must restore this release backup")
    need(source.get("pre_image_id") == evidence["candidate_image_id"], "postrelease restore must use the running candidate image")
    initial_source = manifest.get("recovery_gate", {}).get("full_recovery_evidence", {}).get("payload", {}).get("source", {})
    need(all(source[key] == initial_source.get(key) for key in ("approved_identity_sha256", "approved_recipient_sha256")), "release backup identity differs from signed Gate")
    bundle_raw = read_regular_bytes(candidate_bundle_checksum, maximum=4096, private=True)
    lines = bundle_raw.decode("ascii").splitlines()
    names = ("artifact.tar.age", "candidate.tar.gz", "gate.json", "gate.sig", "manifest", "SHA256SUMS")
    need(len(lines) == len(names), "candidate checksum closure")
    checksums: dict[str, str] = {}
    for line, name in zip(lines, names):
        need(re.fullmatch(r"[0-9a-f]{64}  " + re.escape(name), line) is not None, "candidate checksum name or order")
        checksums[name] = line[:64]
    need(checksums["artifact.tar.age"] == source["artifact_sha256"], "review restored another encrypted artifact")
    need(checksums["candidate.tar.gz"] == evidence["candidate_archive_sha256"] == sha256_file(gate_dir / "candidate.tar.gz"), "candidate archive differs from signed Gate")
    need(checksums["gate.json"] == sha256_file(gate_dir / "gate.json") and checksums["gate.sig"] == sha256_file(gate_dir / "gate.sig"), "candidate Gate bytes differ")
    load = _read(candidate_load)
    findings_raw = read_regular_bytes(audit_findings, maximum=4 * 1024 * 1024, private=True)
    findings = strict_json(findings_raw)
    need(hashlib.sha256(findings_raw).hexdigest() == payload["review"]["findings_sha256"], "independent findings digest differs from signed review")
    need(set(findings) == {"schema", "type", "drill_id", "candidate_load_sha256", "candidate_bundle_sha256"} and findings["schema"] == 1 and findings["type"] == "sub2api-baseline-image-load-review" and findings["drill_id"] == payload["drill_id"], "independent image load review fields")
    need(findings["candidate_load_sha256"] == sha256_file(candidate_load) and findings["candidate_bundle_sha256"] == hashlib.sha256(bundle_raw).hexdigest(), "independent review did not approve actual candidate load and bundle")
    need(set(load) == LOAD_FIELDS and type(load["schema"]) is int and load["schema"] == 1 and load["type"] == "sub2api-baseline-candidate-image-load", "candidate load receipt shape")
    need(load["drill_id"] == payload["drill_id"] and load["release_id"] == manifest["release_id"], "candidate load belongs to another Guest")
    need(load["candidate_archive_sha256"] == evidence["candidate_archive_sha256"] and load["candidate_image_id"] == load["observed_image_id"] == evidence["candidate_image_id"], "actual candidate image load identity")
    need(type(load["load_exit_code"]) is int and load["load_exit_code"] == 0 and type(load["created_at"]) is int and payload["created_at"] <= load["created_at"] <= payload["completed_at"], "candidate image load observation timestamp")
    need(all(load[key] == measured["isolation"][key] for key in ("guest_docker_engine_id_sha256", "rootfs_identity_sha256")), "candidate image loaded outside the measured Guest")
    redis = measured["redis"]
    # The historical promoter requires this equality, which is stronger than
    # the current persistent-floor inequality. Never relax or rewrite its
    # contract: leave the previous verified pointer intact when it cannot hold.
    need(redis["backup_dbsize"] - redis["restored_dbsize"] == redis["backup_expiring"] - redis["restored_expiring"], "legacy Redis TTL equality not observed")
    migration = {"catalog_sha256": manifest["catalog_sha256"], "checksum_policy_sha256": manifest["checksum_policy_sha256"], "pending": evidence["migration_evidence"]["pending"]}
    created, completed = payload["created_at"], payload["completed_at"]
    drill_id = "dr-" + profile + "-" + datetime.fromtimestamp(created, timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    return {
        "schema": 1, "release_id": manifest["release_id"], "drill_id": drill_id,
        "created_at": _iso(created), "completed_at": _iso(completed),
        "artifact_sha256": source["artifact_sha256"],
        "candidate_bundle_sha256": hashlib.sha256(bundle_raw).hexdigest(),
        "candidate_archive_sha256": evidence["candidate_archive_sha256"],
        "candidate_image_id": evidence["candidate_image_id"],
        "migration_checksum": hashlib.sha256(canonical_json(migration)).hexdigest(),
        "image_load_id_check": "pass", "config_manifest_check": "pass",
        "postgres_restore": "pass", "redis_restore": "pass",
        "redis_ttl_reconciliation": "pass", "counts_and_migrations": "pass",
        "temporary_material_destroyed": "pass",
        "redis_backup_dbsize": redis["backup_dbsize"],
        "redis_backup_expiring_keys": redis["backup_expiring"],
        "redis_restored_dbsize": redis["restored_dbsize"],
        "redis_restored_expiring_keys": redis["restored_expiring"],
    }


def write_new(path: Path, document: dict[str, Any]) -> None:
    import os
    need(path.parent.is_dir() and not path.parent.is_symlink(), "output parent unavailable")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o400)
    with os.fdopen(fd, "wb") as stream:
        stream.write(canonical_json(document) + b"\n"); stream.flush(); os.fsync(stream.fileno())


def sign_and_promote(*, release_id: str, drill_id: str, evidence_sha256: str, runner: SSHRunner | None = None) -> dict[str, str]:
    """Invoke existing signer and atomic promoter for already measured evidence.

    Evidence creation is a separate explicit command; a missing or changed
    receipt stops here. The private signing key and backup identity are never
    read or transported by this controller. A signed result already present is
    reverified instead of signing it twice after a transport interruption.
    """
    need(re.fullmatch(r"(?:270|271|272|273|274|275|276|277)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}", release_id) is not None and re.fullmatch(r"dr-(?:270|271|272|273|274|275|276|277)-[0-9]{8}T[0-9]{6}Z", drill_id) is not None and release_id.split("-",1)[0] == drill_id.split("-",2)[1] and SHA.fullmatch(evidence_sha256) is not None, "promotion identity")
    client = runner or SSHRunner()
    evidence = "/opt/sub2api-deploy/dr-evidence/" + release_id + "/" + drill_id + "/evidence.json"
    signature = evidence.removesuffix("evidence.json") + "evidence.sig"
    quote = shlex.quote
    signing = f"""set -Eeuo pipefail
evidence={quote(evidence)}
signature={quote(signature)}
test -f "$evidence"
test ! -L "$evidence"
required_value=$(stat -c '%U:%G:%a:%h' "$evidence")
test "$required_value" = root:root:400:1
required_value=$(sha256sum "$evidence" | awk '{{print $1}}')
test "$required_value" = {evidence_sha256}
if test ! -e "$signature" && test ! -L "$signature"; then
 /usr/local/libexec/sub2api-sign-dr-evidence "$evidence" "$signature" >/dev/null
fi
test -f "$signature"
test ! -L "$signature"
required_value=$(stat -c '%U:%G:%a:%h' "$signature")
test "$required_value" = root:root:400:1
openssl pkeyutl -verify -pubin -inkey /opt/sub2api-release-signer/vm-gate-ed25519.pub -rawin -in "$evidence" -sigfile "$signature" >/dev/null 2>&1
observed_evidence_sha256=$(sha256sum "$evidence" | awk '{{print $1}}')
observed_signature_sha256=$(sha256sum "$signature" | awk '{{print $1}}')
printf 'evidence_sha256=%s\\nsignature_sha256=%s\\n' "$observed_evidence_sha256" "$observed_signature_sha256"
"""
    signed = client.run("local_vm", signing, {"evidence_sha256", "signature_sha256"}, timeout=120).values
    need(signed["evidence_sha256"] == evidence_sha256 and SHA.fullmatch(signed["signature_sha256"]) is not None, "signed evidence identity differs")
    source_temp = client.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "baseline-evidence")
    target_temp = client.create_temp_dir("backup", f"/srv/sub2api-backups/releases/{release_id.split('-',1)[0]}/promotion-input", release_id + "--" + drill_id)
    # Preserve both stage directories on failure for reconciliation. They hold
    # safe signed evidence only. Successful promotion may remove our source
    # staging copy; backup's input remains an auditable immutable submission.
    client.run("local_vm", f"set -Eeuo pipefail; install -m 400 {quote(evidence)} {quote(source_temp + '/evidence.json')}; install -m 400 {quote(signature)} {quote(source_temp + '/evidence.sig')}; printf 'staged=true\\n'", {"staged"}, timeout=120)
    for name in ("evidence.json", "evidence.sig"):
        client.copy_file_between("local_vm", source_temp + "/" + name, "backup", target_temp + "/" + name, mode=0o400, maximum_bytes=4 * 1024 * 1024)
    promoter_local = Path(__file__).parent / "promote-dr-baseline.sh"
    promoter_temp = client.create_temp_dir("backup", "/srv/sub2api-backups", "baseline-promote")
    promoter_remote = promoter_temp + "/promote-dr-baseline.sh"
    client.upload_file("backup", promoter_local, promoter_remote, 0o700)
    script = f"""set -Eeuo pipefail
observed_evidence_sha256=$(sha256sum {quote(target_temp + '/evidence.json')} | awk '{{print $1}}')
test "$observed_evidence_sha256" = {evidence_sha256}
observed_signature_sha256=$(sha256sum {quote(target_temp + '/evidence.sig')} | awk '{{print $1}}')
test "$observed_signature_sha256" = {signed['signature_sha256']}
{quote(promoter_remote)} {quote(release_id)} {quote(drill_id)} {quote(target_temp)}
"""
    result = client.run("backup", script, {"promotion_status", "verified_target", "evidence_sha256", "signature_sha256", "verified_bundle_sha256"}, timeout=1800).values
    need(result.get("promotion_status") == "verified" and result.get("evidence_sha256") == evidence_sha256 and result.get("signature_sha256") == signed["signature_sha256"], "atomic baseline promotion not verified")
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    build = commands.add_parser("build-evidence")
    build.add_argument("--gate-dir", required=True, type=Path)
    build.add_argument("--profile", required=True)
    build.add_argument("--classification", required=True, type=Path)
    build.add_argument("--full-review", required=True, type=Path)
    build.add_argument("--candidate-bundle-checksum", required=True, type=Path)
    build.add_argument("--candidate-load", required=True, type=Path)
    build.add_argument("--audit-findings", required=True, type=Path)
    build.add_argument("--output", required=True, type=Path)
    promote = commands.add_parser("sign-promote")
    promote.add_argument("--release-id", required=True)
    promote.add_argument("--drill-id", required=True)
    promote.add_argument("--evidence-sha256", required=True)
    args = parser.parse_args()
    if args.command == "sign-promote":
        print(json.dumps(sign_and_promote(release_id=args.release_id, drill_id=args.drill_id, evidence_sha256=args.evidence_sha256), sort_keys=True))
        return
    output = args.output
    values = vars(args); del values["output"]; del values["command"]
    document = build_legacy_evidence(**values)
    expected = Path("/opt/sub2api-deploy/dr-evidence") / document["release_id"] / document["drill_id"] / "evidence.json"
    need(output == expected, "fixed legacy signer evidence path required")
    write_new(output, document)
    print(json.dumps({"status": "ready_for_existing_signer", "release_id": document["release_id"], "drill_id": document["drill_id"], "evidence_sha256": sha256_file(output)}, sort_keys=True))


if __name__ == "__main__":
    main()
