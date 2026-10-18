from __future__ import annotations

import copy
import hashlib
import json
import os
import shlex
import shutil
import sys
from pathlib import Path

import pytest

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import canonical_json
from release import vm_build
from release.process import run_hidden


DOCKERFILE_BYTES = b"# syntax=docker/dockerfile:1.7\nFROM scratch\n"


def proof_case():
    manifest = {
        "release_id": "274-aaaaaaaaaaaa-1792330000-aaaaaaaa",
        "commit_sha": "a" * 40,
        "release_asset_sha256": {
            vm_build.BUILD_HELPER_ASSET: hashlib.sha256(vm_build.BUILD_HELPER.read_bytes()).hexdigest(),
        },
    }
    roles = {}
    for role, ref in vm_build.BUILD_DEPENDENCY_REFS.items():
        roles[role] = {
            "declared_ref": ref,
            "index_digest": "sha256:" + "1" * 64,
            "manifest_digest": "sha256:" + "2" * 64,
            "pinned_ref": ref.rsplit(":", 1)[0] + "@sha256:" + "2" * 64,
            "config": {"digest": "sha256:" + "3" * 64, "size": 32,
                       "media_type": "application/vnd.oci.image.config.v1+json"},
            "layers": [{"digest": "sha256:" + "4" * 64, "size": 64,
                        "media_type": "application/vnd.oci.image.layer.v1.tar+gzip"}],
        }
    proof = {
        "schema": 1, "release_id": manifest["release_id"], "commit_sha": manifest["commit_sha"],
        "platform": "linux/amd64", "dockerfile_sha256": hashlib.sha256(DOCKERFILE_BYTES).hexdigest(),
        "helper_sha256": manifest["release_asset_sha256"][vm_build.BUILD_HELPER_ASSET],
        "builder": {"name": "default", "driver": "docker", "identity_sha256": "5" * 64},
        "roles": roles, "materialized_sha256": {role: "6" * 64 for role in roles if role != "frontend"},
        "dependency_solve_count": 1,
    }
    return manifest, proof


def verified_case(proof):
    data = canonical_json(proof) + b"\n"
    return vm_build.VerifiedBuildDependencies({
        "build_dependency_status": "verified", "build_failure_code": "none",
        "build_dependency_count": "5", "build_dependency_verified_count": "5",
        "build_dependency_proof_sha256": hashlib.sha256(data).hexdigest(),
        "build_dependency_proof_size": str(len(data)),
        "build_failure_phase": "none", "build_failure_role": "none", "build_failure_method": "none", "build_retry_count": "0",
    }, proof)


def mock_committed_files(monkeypatch):
    def checksum(commit, path):
        assert commit == "a" * 40
        return hashlib.sha256(DOCKERFILE_BYTES if path == "Dockerfile" else vm_build.BUILD_HELPER.read_bytes()).hexdigest()
    monkeypatch.setattr(vm_build, "git_blob_sha256", checksum)
    monkeypatch.setattr(vm_build, "check_output_hidden", lambda *args, **kwargs: DOCKERFILE_BYTES)


@pytest.fixture(autouse=True)
def committed_files(monkeypatch):
    mock_committed_files(monkeypatch)


def test_valid_proof_binds_once_and_is_idempotent():
    manifest, proof = proof_case()
    bound = vm_build.bind_build_dependencies(manifest, verified_case(proof))
    assert "build_dependencies" not in manifest
    assert bound["build_dependencies"] == proof
    assert vm_build.bind_build_dependencies(bound, verified_case(proof)) == bound
    vm_build.validate_build_dependency_binding(bound, required=True)


@pytest.mark.parametrize("path,value", [
    (("schema",), True), (("schema",), 2), (("secret",), "secret-token-value"),
    (("dependency_solve_count",), True), (("dependency_solve_count",), 2),
    (("release_id",), "274-aaaaaaaaaaaa-1792330001-bbbbbbbb"),
    (("commit_sha",), "b" * 40), (("dockerfile_sha256",), "b" * 64),
    (("helper_sha256",), "b" * 64), (("platform",), "linux/arm64"),
    (("builder", "name"), "other"), (("builder", "driver"), "docker-container"),
    (("builder", "identity_sha256"), "invalid"), (("builder", "endpoint"), "secret-token-value"),
    (("roles", "node", "declared_ref"), "docker.io/library/node:24-alpine"),
    (("roles", "node", "pinned_ref"), "docker.m.daocloud.io/library/node@sha256:" + "7" * 64),
    (("roles", "frontend", "index_digest"), "sha256:invalid"),
    (("roles", "golang", "config", "size"), True),
    (("roles", "golang", "config", "size"), 0),
    (("roles", "golang", "config", "size"), 10737418241),
    (("roles", "node", "config", "media_type"), "text/plain"),
    (("roles", "node", "layers"), []), (("roles", "node", "layers"), [{}] * 129),
    (("materialized_sha256", "frontend"), "6" * 64),
    (("materialized_sha256", "node"), "invalid"),
])
def test_malformed_or_cross_identity_proof_is_rejected(path, value):
    manifest, proof = proof_case()
    target = proof
    for segment in path[:-1]:
        target = target[segment]
    target[path[-1]] = value
    with pytest.raises(RuntimeError, match="proof contract is invalid") as error:
        vm_build.validate_build_dependency_proof(proof, manifest)
    assert "secret-token-value" not in str(error.value)


@pytest.mark.parametrize("path", [("roles", "frontend"), ("roles", "node"), ("materialized_sha256", "postgres")])
def test_missing_roles_or_materialized_files_are_rejected(path):
    manifest, proof = proof_case()
    del proof[path[0]][path[1]]
    with pytest.raises(RuntimeError):
        vm_build.validate_build_dependency_proof(proof, manifest)


def test_old_manifest_is_readable_but_new_entrypoint_requires_proof():
    manifest, _ = proof_case()
    vm_build.validate_build_dependency_binding(manifest)
    with pytest.raises(RuntimeError):
        vm_build.validate_build_dependency_binding(manifest, required=True)


@pytest.mark.parametrize("field", ["build_dependencies", "build_dependencies_sha256"])
def test_partial_binding_is_rejected(field):
    manifest, proof = proof_case()
    bound = vm_build.bind_build_dependencies(manifest, verified_case(proof))
    del bound[field]
    with pytest.raises(RuntimeError):
        vm_build.validate_build_dependency_binding(bound)


def test_modified_hash_body_or_second_proof_is_rejected():
    manifest, proof = proof_case()
    bound = vm_build.bind_build_dependencies(manifest, verified_case(proof))
    changed = copy.deepcopy(bound)
    changed["build_dependencies"]["materialized_sha256"]["node"] = "7" * 64
    with pytest.raises(RuntimeError):
        vm_build.validate_build_dependency_binding(changed)
    with pytest.raises(RuntimeError):
        vm_build.bind_build_dependencies(bound, verified_case(changed["build_dependencies"]))


def test_vm_only_checksum_comes_from_committed_helper():
    manifest, proof = proof_case()
    manifest["scope"] = "vm-only"
    del manifest["release_asset_sha256"]
    assert vm_build.bind_build_dependencies(manifest, verified_case(proof))["build_dependencies"] == proof


@pytest.mark.parametrize("override", [
    {"unexpected": "secret-token-value"}, {"build_dependency_verified_count": "4"},
    {"build_dependency_proof_size": "0"}, {"build_dependency_proof_size": "262145"},
    {"build_dependency_proof_size": "099"}, {"build_dependency_proof_sha256": "none"},
    {"build_dependency_status": "failed"}, {"build_failure_code": "build_unknown"},
    {"build_failure_phase": "metadata"}, {"build_failure_role": "node"},
    {"build_failure_method": "GET"}, {"build_retry_count": "1"},
])
def test_binding_requires_valid_complete_success_report(override):
    manifest, proof = proof_case()
    verified = verified_case(proof)
    verified.report.update(override)
    with pytest.raises(RuntimeError):
        vm_build.bind_build_dependencies(manifest, verified)


def test_report_cannot_claim_a_different_proof_size():
    manifest, proof = proof_case()
    verified = verified_case(proof)
    verified.report["build_dependency_proof_size"] = str(int(verified.report["build_dependency_proof_size"]) + 1)
    with pytest.raises(RuntimeError):
        vm_build.bind_build_dependencies(manifest, verified)


@pytest.fixture(scope="module")
def signed_gate_keys(tmp_path_factory):
    directory = tmp_path_factory.mktemp("build-proof-signing")
    private = directory / "private.pem"
    public = directory / "public.pem"
    run_hidden(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private)], check=True, capture_output=True)
    run_hidden(["openssl", "pkey", "-in", str(private), "-pubout", "-out", str(public)], check=True, capture_output=True)
    return private, public


@pytest.mark.parametrize("kind", ["v1", "v2", "vm-only"])
@pytest.mark.parametrize("damage", ["missing_proof", "partial", "forged_marker"])
def test_signed_new_gate_cannot_drop_proof_or_forge_contract(kind, damage, signed_gate_keys, tmp_path):
    from release import gate
    manifest, proof = proof_case()
    manifest["schema"] = 1 if kind == "v1" else 2
    manifest["build_dependency_contract"] = vm_build.BUILD_DEPENDENCY_CONTRACT
    manifest = vm_build.bind_build_dependencies(manifest, verified_case(proof))
    if damage == "missing_proof":
        del manifest["build_dependencies"]
        del manifest["build_dependencies_sha256"]
    elif damage == "partial":
        del manifest["build_dependencies_sha256"]
    else:
        manifest["build_dependency_contract"] = "unknown-contract"
    document = {"manifest": manifest, "evidence": {}}
    if kind != "v1":
        document.update(gate_version=2, profile_id=274)
    (tmp_path / "gate.json").write_bytes(canonical_json(document) + b"\n")
    private, public = signed_gate_keys
    run_hidden(["openssl", "pkeyutl", "-sign", "-inkey", str(private), "-rawin", "-in", str(tmp_path / "gate.json"), "-out", str(tmp_path / "gate.sig")], check=True, capture_output=True)
    verifier = gate.verify_vm_only_gate if kind == "vm-only" else gate.verify_gate
    with pytest.raises(RuntimeError, match="proof contract is invalid"):
        verifier(tmp_path, public, "274")


def test_signed_new_v2_gate_with_bound_proof_round_trips(monkeypatch):
    from test_gate_v2 import GateV2Test
    case = GateV2Test()
    case.setUp()
    try:
        _, proof = proof_case()
        assets = case._assets()
        assets[vm_build.BUILD_HELPER_ASSET] = proof["helper_sha256"]
        monkeypatch.setattr(case, "_assets", lambda: assets)
        document = case._document()
        proof["release_id"] = document["manifest"]["release_id"]
        document["manifest"]["build_dependency_contract"] = vm_build.BUILD_DEPENDENCY_CONTRACT
        document["manifest"] = vm_build.bind_build_dependencies(document["manifest"], verified_case(proof))
        case._sign(document)
        assert case._verify(validate_profile_contract=True) == document
    finally:
        case.tearDown()


def test_signed_vm_only_gate_with_bound_proof_round_trips(monkeypatch):
    from release import gate
    from test_vm_only import VMOnlyGateTest
    case = VMOnlyGateTest()
    case.setUp()
    try:
        directory = case._bundle()
        path = directory / "gate.json"
        document = json.loads(path.read_bytes())
        _, proof = proof_case()
        proof["release_id"] = document["manifest"]["release_id"]
        document["manifest"]["build_dependency_contract"] = vm_build.BUILD_DEPENDENCY_CONTRACT
        document["manifest"] = vm_build.bind_build_dependencies(document["manifest"], verified_case(proof))
        path.write_bytes(canonical_json(document) + b"\n")
        run_hidden(["openssl", "pkeyutl", "-sign", "-inkey", str(case.private_key), "-rawin", "-in", str(path), "-out", str(directory / "gate.sig")], check=True, capture_output=True)
        expected_profile = document["manifest"]["profile"]
        profile = {"gate_schema": 2, "version": document["manifest"]["version"], "origin": document["manifest"]["origin"]}
        monkeypatch.setattr(gate, "get_profile", lambda value: profile)
        assert gate.verify_vm_only_gate(directory, case.public_key, expected_profile) == document
    finally:
        case.tearDown()


@pytest.mark.parametrize("override,expected", [
    ({}, ("layer", "golang", "GET")),
    ({"gate_build_failure_phase": "secret-token-value", "gate_build_failure_role": [], "gate_build_failure_method": {}}, ("candidate", "none", "unknown")),
])
def test_candidate_failure_diagnostics_are_fixed_and_never_report_retry(override, expected, tmp_path):
    from release.vm_validate import _persist_vm_build_failure
    manifest, _ = proof_case()
    worker = {"worker_exit_code": "1", "gate_stage": "candidate_build", "gate_build_failure_code": "build_registry_502",
              "gate_build_failure_phase": "layer", "gate_build_failure_role": "golang", "gate_build_failure_method": "GET",
              "gate_build_retry_count": "secret-token-value", **override}
    _persist_vm_build_failure(worker, manifest, tmp_path)
    report = json.loads((tmp_path / "vm-build-failure.json").read_bytes())
    assert tuple(report[key] for key in ("build_failure_phase", "build_failure_role", "build_failure_method")) == expected
    assert report["build_retry_count"] == "0"
    assert "secret-token-value" not in json.dumps(report)


@pytest.mark.parametrize("code", sorted(vm_build.BUILD_FAILURE_CODES - {"none"}))
def test_failure_report_allows_fixed_reason_without_proof(code):
    report = vm_build.VMCandidateBuildError({"build_failure_code": code}).report
    assert vm_build.validate_build_dependency_report(report)
    report["build_dependency_proof_sha256"] = "7" * 64
    assert not vm_build.validate_build_dependency_report(report)


def test_direct_validator_binds_locally_and_reuses_existing_proof(monkeypatch, tmp_path):
    from release import vm_validate
    manifest, proof = proof_case()
    path = tmp_path / "manifest.json"
    path.write_bytes(canonical_json(manifest) + b"\n")
    calls = []
    monkeypatch.setattr(vm_validate, "ensure_vm_build_dependencies", lambda runner, value: calls.append(value) or verified_case(proof))
    bound = vm_validate._ensure_build_dependencies(object(), manifest, path)
    assert len(calls) == 1
    assert json.loads(path.read_bytes()) == bound
    assert bound["build_dependencies"] == proof
    assert json.loads(path.with_name("build-dependencies.json").read_bytes())["build_dependency_proof_sha256"] == bound["build_dependencies_sha256"]
    assert vm_validate._ensure_build_dependencies(object(), bound, path) == bound
    assert len(calls) == 1


def test_direct_validator_rejects_partial_proof_without_new_solve(monkeypatch, tmp_path):
    from release import vm_validate
    manifest, proof = proof_case()
    manifest["build_dependencies"] = proof
    monkeypatch.setattr(vm_validate, "ensure_vm_build_dependencies", lambda *args: pytest.fail("must not solve malformed existing proof"))
    with pytest.raises(RuntimeError):
        vm_validate._ensure_build_dependencies(object(), manifest, tmp_path / "manifest.json")


def test_snapshot_is_bound_before_dependency_failure_and_is_immutable(monkeypatch, tmp_path):
    from release import cli
    manifest, _ = proof_case()
    manifest["migration_catalog"] = []
    snapshot = {"current_image_id": "sha256:" + "b" * 64, "production_current_commit_sha": "c" * 40, "schema_migrations": []}
    def bind(value, image, checksum, recovery):
        assert image == snapshot["current_image_id"]
        return {**value, "production_current_image_id": image, "production_snapshot_sha256": checksum, "recovery_gate": recovery}
    monkeypatch.setattr(cli, "bind_production_snapshot", bind)
    bound, stored = cli._bind_preflight_snapshot(manifest, snapshot, snapshot["current_image_id"], {"mode": "specialized"}, tmp_path)
    assert json.loads((tmp_path / "manifest.json").read_bytes()) == bound
    assert json.loads((tmp_path / "production-snapshot.json").read_bytes()) == stored
    assert bound["production_snapshot_sha256"] == cli.snapshot_sha256(stored)
    assert cli._bind_preflight_snapshot(bound, stored, snapshot["current_image_id"], {"mode": "specialized"}, tmp_path) == (bound, stored)
    with pytest.raises(RuntimeError, match="immutable production snapshot"):
        cli._bind_preflight_snapshot(bound, {**stored, "production_current_commit_sha": "d" * 40}, snapshot["current_image_id"], {"mode": "specialized"}, tmp_path)


@pytest.mark.parametrize("filename", ["vm-validate.sh", "vm-only-validate.sh"])
@pytest.mark.parametrize("scenario", ["success", "missing", "partial", "null", "bad_sha", "reject"])
def test_shell_requires_same_local_proof_without_solving_or_mutating_manifest(filename, scenario):
    from test_vm_build_registry_proxy import bash_path
    jq = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")
    if not jq:
        pytest.skip("real jq is unavailable; set SUB2API_TEST_JQ")
    manifest, proof = proof_case()
    bound = vm_build.bind_build_dependencies(manifest, verified_case(proof))
    if scenario == "missing":
        bound = manifest
    elif scenario == "partial":
        del bound["build_dependencies_sha256"]
    elif scenario == "null":
        bound["build_dependencies"] = None
    elif scenario == "bad_sha":
        bound["build_dependencies_sha256"] = "7" * 64
    source = (vm_build.RELEASE_PACKAGE_ROOT / filename).read_text(encoding="utf-8")
    function = "prepare_vm_build_proof() {" + source.split("prepare_vm_build_proof() {", 1)[1].split("\n}\n", 1)[0] + "\n}\n"
    script = "set -Eeuo pipefail\n" + function + f'''
jq() {{ {shlex.quote(Path(jq).as_posix())} -b "$@"; }}
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
manifest="$scratch/manifest.json"
source_dir="$scratch"
release_id={shlex.quote(manifest['release_id'])}
commit={shlex.quote(manifest['commit_sha'])}
scenario={shlex.quote(scenario)}
printf '%s\\n' {shlex.quote(canonical_json(bound).decode())} > "$manifest"
before=$(sha256sum "$manifest" | awk '{{print $1}}')
mkdir "$scratch/dependencies"
stat() {{ if [[ $1 == -c && $2 == %u:%g:%a:%h ]]; then printf '0:0:400:1\\n'; else command stat "$@"; fi; }}
vm_build_preflight() {{ printf x >> "$scratch/solve"; return 97; }}
vm_build_use_proof() {{
  printf x >> "$scratch/use"
  [[ $2 == "$(sha256sum "$1" | awk '{{print $1}}')" && $scenario != reject ]]
}}
if prepare_vm_build_proof "$scratch/dependencies"; then status=success; else status=failed; fi
solve=0; use=0
if [[ -e $scratch/solve ]]; then solve=$(wc -c < "$scratch/solve" | tr -d ' '); fi
if [[ -e $scratch/use ]]; then use=$(wc -c < "$scratch/use" | tr -d ' '); fi
[[ $(sha256sum "$manifest" | awk '{{print $1}}') == "$before" ]]
printf 'status=%s\\nsolve=%s\\nuse=%s\\n' "$status" "$solve" "$use"
'''
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-s"], input=script, text=True, capture_output=True, timeout=30)
    assert result.returncode == 0, result.stderr
    values = dict(line.split("=", 1) for line in result.stdout.splitlines())
    assert values == {"status": "success" if scenario == "success" else "failed", "solve": "0", "use": "1" if scenario in {"success", "bad_sha", "reject"} else "0"}
