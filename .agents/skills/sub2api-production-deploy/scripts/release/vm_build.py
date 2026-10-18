from __future__ import annotations

import hashlib
import json
import re
import shlex
import tempfile
from dataclasses import dataclass
from pathlib import Path

from .atomic import canonical_json
from .manifest import RELEASE_ID, git_blob_sha256
from .paths import RELEASE_PACKAGE_ROOT, WORKSPACE, skill_asset_path
from .process import check_output_hidden
from .ssh import SSHRunner


BUILD_HELPER = RELEASE_PACKAGE_ROOT / "vm-build-common.sh"
BUILD_HELPER_ASSET = skill_asset_path("release/vm-build-common.sh")
BUILD_DEPENDENCY_REFS = {
    "frontend": "docker.io/docker/dockerfile:1.7",
    "node": "docker.m.daocloud.io/library/node:24-alpine",
    "golang": "docker.m.daocloud.io/library/golang:1.27.2-alpine",
    "alpine": "docker.m.daocloud.io/library/alpine:3.21",
    "postgres": "docker.m.daocloud.io/library/postgres:18-alpine",
}
BUILD_DEPENDENCY_COUNT = str(len(BUILD_DEPENDENCY_REFS))
BUILD_DEPENDENCY_CONTRACT = "buildkit-dependencies-v1"
BUILD_PROOF_MAX_BYTES = 262144
BUILD_FAILURE_CODES = frozenset({
    "none", "build_registry_502", "build_registry_eof", "build_registry_auth",
    "build_registry_tls", "build_timeout", "build_space", "build_compile",
    "build_unknown", "build_registry_metadata_invalid", "build_dependency_contract",
    "build_preflight_unproven",
})
BUILD_DEPENDENCY_FIELDS = frozenset({
    "build_dependency_status", "build_failure_code", "build_dependency_count",
    "build_dependency_verified_count",
    "build_dependency_proof_sha256", "build_dependency_proof_size",
    "build_failure_phase", "build_failure_role", "build_failure_method", "build_retry_count",
})
BUILD_FAILURE_PHASES = frozenset({"none", "contract", "identity", "metadata", "manifest", "config", "layer", "dependency", "candidate", "run"})
BUILD_FAILURE_ROLES = frozenset({"none", *BUILD_DEPENDENCY_REFS})
BUILD_FAILURE_METHODS = frozenset({"none", "HEAD", "GET", "unknown"})
_SHA256 = re.compile(r"[0-9a-f]{64}")
_DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
_CONFIG_TYPES = frozenset({"application/vnd.oci.image.config.v1+json", "application/vnd.docker.container.image.v1+json"})
_LAYER_TYPES = frozenset({
    "application/vnd.oci.image.layer.v1.tar", "application/vnd.oci.image.layer.v1.tar+gzip",
    "application/vnd.oci.image.layer.v1.tar+zstd", "application/vnd.docker.image.rootfs.diff.tar.gzip",
})


@dataclass(frozen=True)
class VerifiedBuildDependencies:
    report: dict[str, str]
    proof: dict


class VMCandidateBuildError(RuntimeError):
    """A fixed, allowlisted reason; never a registry response or credential."""

    def __init__(self, report: dict[str, str]):
        self.report = {
            "build_dependency_status": "failed", "build_dependency_count": BUILD_DEPENDENCY_COUNT,
            "build_dependency_verified_count": "0", "build_dependency_proof_sha256": "none",
            "build_dependency_proof_size": "0", **report,
        }
        self.report = {"build_failure_phase": "contract", "build_failure_role": "none",
                       "build_failure_method": "unknown", "build_retry_count": "0", **self.report}
        super().__init__(f"VM candidate build dependency failed: {report['build_failure_code']}")


def _contract_error() -> RuntimeError:
    return RuntimeError("build dependency proof contract is invalid")


def _is_hash(value: object) -> bool:
    return isinstance(value, str) and _SHA256.fullmatch(value) is not None


def _helper_checksum(manifest: dict) -> str:
    if manifest.get("scope") == "vm-only":
        return git_blob_sha256(str(manifest.get("commit_sha", "")), BUILD_HELPER_ASSET)
    assets = manifest.get("release_asset_sha256")
    return assets.get(BUILD_HELPER_ASSET, "") if isinstance(assets, dict) else ""


def _validate_descriptor(value: object, allowed_media_types: frozenset[str]) -> None:
    if (
        not isinstance(value, dict) or set(value) != {"digest", "size", "media_type"}
        or not isinstance(value["digest"], str) or not _DIGEST.fullmatch(value["digest"])
        or type(value["size"]) is not int or not 0 < value["size"] <= 10737418240
        or not isinstance(value["media_type"], str) or value["media_type"] not in allowed_media_types
    ):
        raise _contract_error()


def validate_build_dependency_proof(proof: object, manifest: dict) -> dict:
    """Validate only the bounded public proof, independently of shell output."""
    if (
        not isinstance(proof, dict)
        or set(proof) != {"schema", "release_id", "commit_sha", "platform", "dockerfile_sha256",
                          "helper_sha256", "builder", "roles", "materialized_sha256", "dependency_solve_count"}
        or type(proof["schema"]) is not int or proof["schema"] != 1
        or type(proof["dependency_solve_count"]) is not int or proof["dependency_solve_count"] != 1
        or proof["release_id"] != manifest.get("release_id")
        or not isinstance(proof["release_id"], str) or not RELEASE_ID.fullmatch(proof["release_id"])
        or proof["commit_sha"] != manifest.get("commit_sha")
        or not isinstance(proof["commit_sha"], str) or not re.fullmatch(r"[0-9a-f]{40}", proof["commit_sha"])
        or proof["release_id"].split("-")[1] != proof["commit_sha"][:12]
        or proof["platform"] != "linux/amd64"
        or not _is_hash(proof["dockerfile_sha256"]) or not _is_hash(proof["helper_sha256"])
        or proof["helper_sha256"] != _helper_checksum(manifest)
        or proof["dockerfile_sha256"] != git_blob_sha256(proof["commit_sha"], "Dockerfile")
    ):
        raise _contract_error()
    builder = proof["builder"]
    if (not isinstance(builder, dict) or set(builder) != {"name", "driver", "identity_sha256"}
            or builder["name"] != "default" or builder["driver"] != "docker" or not _is_hash(builder["identity_sha256"])):
        raise _contract_error()
    roles = proof["roles"]
    if not isinstance(roles, dict) or set(roles) != set(BUILD_DEPENDENCY_REFS):
        raise _contract_error()
    for role, declared_ref in BUILD_DEPENDENCY_REFS.items():
        item = roles[role]
        if (
            not isinstance(item, dict)
            or set(item) != {"declared_ref", "index_digest", "manifest_digest", "config", "layers", "pinned_ref"}
            or item["declared_ref"] != declared_ref
            or not isinstance(item["index_digest"], str) or not _DIGEST.fullmatch(item["index_digest"])
            or not isinstance(item["manifest_digest"], str) or not _DIGEST.fullmatch(item["manifest_digest"])
            or item["pinned_ref"] != declared_ref.rsplit(":", 1)[0] + "@" + item["manifest_digest"]
            or not isinstance(item["layers"], list) or not 1 <= len(item["layers"]) <= 128
        ):
            raise _contract_error()
        _validate_descriptor(item["config"], _CONFIG_TYPES)
        for layer in item["layers"]:
            _validate_descriptor(layer, _LAYER_TYPES)
    materialized = proof["materialized_sha256"]
    if (not isinstance(materialized, dict) or set(materialized) != set(BUILD_DEPENDENCY_REFS) - {"frontend"}
            or not all(_is_hash(value) for value in materialized.values())):
        raise _contract_error()
    if len(canonical_json(proof) + b"\n") > BUILD_PROOF_MAX_BYTES:
        raise _contract_error()
    return json.loads(canonical_json(proof))


def validate_build_dependency_binding(manifest: dict, *, required: bool = False) -> None:
    if "build_dependency_contract" in manifest and manifest["build_dependency_contract"] != BUILD_DEPENDENCY_CONTRACT:
        raise _contract_error()
    present = {key for key in ("build_dependencies", "build_dependencies_sha256") if key in manifest}
    if not present and not required:
        return
    if present != {"build_dependencies", "build_dependencies_sha256"}:
        raise _contract_error()
    proof = validate_build_dependency_proof(manifest["build_dependencies"], manifest)
    if manifest["build_dependencies_sha256"] != hashlib.sha256(canonical_json(proof) + b"\n").hexdigest():
        raise _contract_error()


def bind_build_dependencies(manifest: dict, verified: VerifiedBuildDependencies) -> dict:
    if (not isinstance(verified, VerifiedBuildDependencies)
            or not validate_build_dependency_report(verified.report)
            or verified.report["build_dependency_status"] != "verified"):
        raise _contract_error()
    proof = validate_build_dependency_proof(verified.proof, manifest)
    data = canonical_json(proof) + b"\n"
    digest = hashlib.sha256(data).hexdigest()
    if (verified.report["build_dependency_proof_sha256"] != digest
            or verified.report["build_dependency_proof_size"] != str(len(data))):
        raise _contract_error()
    value = dict(manifest)
    for name, incoming in (("build_dependencies", proof), ("build_dependencies_sha256", digest)):
        if name in value and value[name] != incoming:
            raise _contract_error()
        value[name] = incoming
    validate_build_dependency_binding(value, required=True)
    return value


def validate_build_dependency_report(report: object) -> bool:
    if (
        not isinstance(report, dict) or set(report) != BUILD_DEPENDENCY_FIELDS
        or not all(isinstance(value, str) for value in report.values())
        or report["build_dependency_status"] not in {"verified", "failed"}
        or report["build_failure_code"] not in BUILD_FAILURE_CODES
        or report["build_dependency_count"] != BUILD_DEPENDENCY_COUNT
        or report["build_failure_phase"] not in BUILD_FAILURE_PHASES
        or report["build_failure_role"] not in BUILD_FAILURE_ROLES
        or report["build_failure_method"] not in BUILD_FAILURE_METHODS
        or report["build_retry_count"] != "0"
        or report["build_dependency_verified_count"] not in {str(i) for i in range(len(BUILD_DEPENDENCY_REFS) + 1)}
    ):
        return False
    if report["build_dependency_status"] == "failed":
        return (report["build_failure_code"] != "none" and report["build_dependency_proof_sha256"] == "none"
                and report["build_dependency_proof_size"] == "0" and report["build_failure_phase"] != "none"
                and report["build_failure_method"] != "none")
    return (
        report["build_failure_code"] == "none" and report["build_dependency_verified_count"] == BUILD_DEPENDENCY_COUNT
        and report["build_failure_phase"] == report["build_failure_role"] == report["build_failure_method"] == "none"
        and _is_hash(report["build_dependency_proof_sha256"])
        and re.fullmatch(r"[1-9][0-9]{0,5}", report["build_dependency_proof_size"]) is not None
        and int(report["build_dependency_proof_size"]) <= BUILD_PROOF_MAX_BYTES
    )


def ensure_vm_build_dependencies(runner: SSHRunner, manifest: dict) -> VerifiedBuildDependencies:
    """Materialize the locked dependencies once before preparing restore inputs.

    Root-only diagnostics remain in release-logs. Only our input directory is
    removed. This function neither starts a validator nor retries a build.
    """
    release_id = str(manifest.get("release_id", ""))
    commit = manifest.get("commit_sha")
    try:
        checksum = _helper_checksum(manifest)
        data = BUILD_HELPER.read_bytes()
        if (not RELEASE_ID.fullmatch(release_id) or not isinstance(commit, str)
                or not re.fullmatch(r"[0-9a-f]{40}", commit)
                or release_id.split("-")[1] != commit[:12]
                or not _is_hash(checksum) or hashlib.sha256(data).hexdigest() != checksum):
            raise _contract_error()
        dockerfile = check_output_hidden(["git", "show", f"{commit}:Dockerfile"], cwd=WORKSPACE)
        if (not isinstance(dockerfile, bytes)
                or hashlib.sha256(dockerfile).hexdigest() != git_blob_sha256(commit, "Dockerfile")):
            raise _contract_error()
    except Exception:
        raise VMCandidateBuildError({"build_failure_code": "build_dependency_contract"}) from None
    try:
        runner.run(
            "local_vm",
            "set -Eeuo pipefail\n"
            "[[ $(id -u) == 0 ]]\n"
            "root=/opt/sub2api-deploy/release-input\n"
            "[[ ! -L $root ]]\n"
            "install -d -m 700 \"$root\"\n"
            "[[ -d $root && $(stat -c '%u:%g:%a' \"$root\") == 0:0:700 ]]\n"
            "printf 'input_root_ready=true\\n'\n",
            {"input_root_ready"},
        )
        remote_root = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "build-dependencies")
    except Exception:
        raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
    remote_helper = f"{remote_root}/vm-build-common.sh"
    remote_dockerfile = f"{remote_root}/Dockerfile"
    remote_proof = f"{remote_root}/proof.json"
    log_root = f"/opt/sub2api-deploy/release-logs/{release_id}"
    log_dir = f"{log_root}/build-dependencies-early"
    try:
        runner.upload("local_vm", data, remote_helper, 0o400)
        runner.upload("local_vm", dockerfile, remote_dockerfile, 0o400)
        script = f'''set -Eeuo pipefail
umask 077
helper={shlex.quote(remote_helper)}
[[ -f $helper && ! -L $helper && $(stat -c '%u:%g:%a:%h' "$helper") == 0:0:400:1 ]]
[[ $(sha256sum "$helper" | awk '{{print $1}}') == {shlex.quote(checksum)} ]]
dockerfile={shlex.quote(remote_dockerfile)}
[[ -f $dockerfile && ! -L $dockerfile && $(stat -c '%u:%g:%a:%h' "$dockerfile") == 0:0:400:1 ]]
[[ $(sha256sum "$dockerfile" | awk '{{print $1}}') == {shlex.quote(hashlib.sha256(dockerfile).hexdigest())} ]]
for directory in /opt/sub2api-deploy/release-logs {shlex.quote(log_root)}; do
  [[ ! -L $directory ]]
  if [[ ! -e $directory ]]; then install -d -o 0 -g 0 -m 700 "$directory"; fi
  [[ -d $directory && $(stat -c '%u:%g:%a' "$directory") == 0:0:700 ]]
done
log_dir={shlex.quote(log_dir)}
[[ ! -e $log_dir && ! -L $log_dir ]]
install -d -o 0 -g 0 -m 700 "$log_dir"
source "$helper"
proof_sha=none
proof_size=0
if vm_build_preflight "$log_dir" {shlex.quote(release_id)} {shlex.quote(commit)} "$dockerfile"; then
  build_dependency_status=verified
  proof="$log_dir/proof.json"
  [[ -f $proof && ! -L $proof && $(stat -c '%u:%g:%a:%h' "$proof") == 0:0:400:1 ]]
  proof_size=$(stat -c '%s' "$proof")
  [[ $proof_size =~ ^[1-9][0-9]{{0,5}}$ && $proof_size -le {BUILD_PROOF_MAX_BYTES} ]]
  proof_sha=$(sha256sum "$proof" | awk '{{print $1}}')
  [[ $proof_sha =~ ^[0-9a-f]{{64}}$ ]]
  [[ ! -e {shlex.quote(remote_proof)} && ! -L {shlex.quote(remote_proof)} ]]
  install -o 0 -g 0 -m 400 "$proof" {shlex.quote(remote_proof)}
else
  build_dependency_status=failed
fi
printf 'build_dependency_status=%s\\nbuild_failure_code=%s\\nbuild_dependency_count=5\\nbuild_dependency_verified_count=%s\\nbuild_dependency_proof_sha256=%s\\nbuild_dependency_proof_size=%s\\nbuild_failure_phase=%s\\nbuild_failure_role=%s\\nbuild_failure_method=%s\\nbuild_retry_count=0\\n' "$build_dependency_status" "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT" "$proof_sha" "$proof_size" "$VM_BUILD_FAILURE_PHASE" "$VM_BUILD_FAILURE_ROLE" "$VM_BUILD_FAILURE_METHOD"
'''
        report = runner.run("local_vm", script, BUILD_DEPENDENCY_FIELDS, timeout=3000).values
        if not validate_build_dependency_report(report):
            raise VMCandidateBuildError({"build_failure_code": "build_dependency_contract"})
        if report["build_dependency_status"] != "verified":
            raise VMCandidateBuildError(report)
        with tempfile.TemporaryDirectory(prefix="sub2api-build-proof-") as directory:
            local_proof = Path(directory) / "proof.json"
            runner.download_file("local_vm", remote_proof, local_proof)
            if local_proof.is_symlink() or not local_proof.is_file():
                raise _contract_error()
            if local_proof.stat().st_size != int(report["build_dependency_proof_size"]):
                raise _contract_error()
            raw = local_proof.read_bytes()
            if hashlib.sha256(raw).hexdigest() != report["build_dependency_proof_sha256"]:
                raise _contract_error()
            proof = validate_build_dependency_proof(json.loads(raw), manifest)
            if raw != canonical_json(proof) + b"\n":
                raise _contract_error()
            return VerifiedBuildDependencies(dict(report), proof)
    except VMCandidateBuildError:
        raise
    except Exception:
        raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
    finally:
        # create_temp_dir registered exactly this private input; diagnostics live
        # outside it. Do not clean candidate/Gate/other releases.
        try:
            runner.run(
                "local_vm",
                f"rm -rf -- {shlex.quote(remote_root)} && printf 'input_removed=true\\n'",
                {"input_removed"},
            )
        except Exception:
            raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
