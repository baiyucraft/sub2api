from __future__ import annotations

import hashlib
import json
import os
import re
import shlex
import shutil
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.process import run_hidden
from release.vm_build import (
    BUILD_DEPENDENCY_FIELDS, BUILD_FAILURE_CODES, BUILD_HELPER, BUILD_HELPER_ASSET,
    VMCandidateBuildError, ensure_vm_build_dependencies,
)
from release.vm_validate import _persist_vm_build_failure, _validate_completed_vm_worker
from test_vm_build_registry_proxy import bash_path
from test_vm_build_proof import DOCKERFILE_BYTES, mock_committed_files, proof_case, verified_case


@pytest.fixture(autouse=True)
def committed_files(monkeypatch):
    mock_committed_files(monkeypatch)


def manifest():
    return proof_case()[0]


class FakeRunner:
    def __init__(self, values=None, failure=None):
        self.proof = proof_case()[1]
        self.values = verified_case(self.proof).report if values is None else values
        self.failure = failure
        self.calls = []
        self.uploads = []

    def run(self, host, script, fields, timeout=120):
        self.calls.append((host, script, set(fields), timeout))
        if set(fields) == BUILD_DEPENDENCY_FIELDS:
            if self.failure:
                raise self.failure
            return SimpleNamespace(values=self.values)
        return SimpleNamespace(values={name: "true" for name in fields})

    def create_temp_dir(self, host, root, prefix):
        assert host == "local_vm" and root == "/opt/sub2api-deploy/release-input"
        return root + "/build-dependencies.fixture"

    def upload(self, host, data, path, mode):
        self.uploads.append((host, data, path, mode))

    def download_file(self, host, remote_path, local_path):
        from release.atomic import canonical_json
        assert host == "local_vm"
        assert remote_path == "/opt/sub2api-deploy/release-input/build-dependencies.fixture/proof.json"
        local_path.write_bytes(canonical_json(self.proof) + b"\n")


def test_early_preflight_verifies_helper_and_removes_only_its_input():
    runner = FakeRunner()
    result = ensure_vm_build_dependencies(runner, manifest())
    assert set(result.report) == BUILD_DEPENDENCY_FIELDS
    assert result.proof == runner.proof
    assert len(runner.uploads) == 2 and all(item[3] == 0o400 for item in runner.uploads)
    assert runner.uploads[1][1] == DOCKERFILE_BYTES
    command = runner.calls[1][1]
    assert "sha256sum" in command and "0:0:400:1" in command
    assert "source \"$helper\"" in command and "vm_build_preflight" in command
    assert "/build-dependencies-early" in command and "0:0:700" in command
    assert "docker build " not in command and "cat " not in command
    assert runner.calls[1][3] == 3000
    assert runner.calls[-1][1].startswith("rm -rf -- /opt/sub2api-deploy/release-input/build-dependencies.fixture")
    assert "release-logs" not in runner.calls[-1][1]


def test_missing_or_changed_helper_checksum_does_not_connect():
    runner = FakeRunner()
    value = manifest()
    value["release_asset_sha256"][BUILD_HELPER_ASSET] = "b" * 64
    with pytest.raises(VMCandidateBuildError, match="build_dependency_contract"):
        ensure_vm_build_dependencies(runner, value)
    assert not runner.calls and not runner.uploads


@pytest.mark.parametrize("code", sorted(BUILD_FAILURE_CODES - {"none"}))
def test_early_failure_preserves_only_fixed_report_without_retry(code):
    runner = FakeRunner({
        "build_dependency_status": "failed", "build_failure_code": code,
        "build_dependency_count": "5", "build_dependency_verified_count": "2",
        "build_dependency_proof_sha256": "none", "build_dependency_proof_size": "0",
        "build_failure_phase": "metadata", "build_failure_role": "golang", "build_failure_method": "unknown", "build_retry_count": "0",
    })
    with pytest.raises(VMCandidateBuildError) as error:
        ensure_vm_build_dependencies(runner, manifest())
    assert error.value.report["build_failure_code"] == code
    assert set(error.value.report) == BUILD_DEPENDENCY_FIELDS
    assert len([call for call in runner.calls if call[2] == BUILD_DEPENDENCY_FIELDS]) == 1
    assert runner.calls[-1][2] == {"input_removed"}


@pytest.mark.parametrize("override", [
    {"build_failure_code": "secret-token-value"},
    {"build_dependency_count": "3"},
    {"build_dependency_verified_count": "3"},
    {"unexpected": "secret-token-value"},
    {"build_dependency_status": "failed", "build_failure_code": "none"},
    {"build_failure_code": []},
    {"build_dependency_verified_count": {}},
])
def test_bad_structured_results_fail_closed(override):
    runner = FakeRunner()
    runner.values.update(override)
    with pytest.raises(VMCandidateBuildError, match="build_dependency_contract") as error:
        ensure_vm_build_dependencies(runner, manifest())
    assert "secret-token-value" not in str(error.value) + json.dumps(error.value.report)


def test_ssh_failure_is_unproven_and_never_exposes_error_text():
    runner = FakeRunner(failure=RuntimeError("secret-token-value"))
    with pytest.raises(VMCandidateBuildError, match="build_preflight_unproven") as error:
        ensure_vm_build_dependencies(runner, manifest())
    assert "secret-token-value" not in str(error.value) + json.dumps(error.value.report)
    assert runner.calls[-1][2] == {"input_removed"}


@pytest.mark.parametrize("damage", ["digest", "size", "json", "noncanonical"])
def test_downloaded_proof_requires_report_hash_size_and_canonical_bytes(damage):
    from release.atomic import canonical_json
    runner = FakeRunner()
    def download(host, remote_path, local_path):
        raw = canonical_json(runner.proof) + b"\n"
        if damage == "digest":
            runner.proof["materialized_sha256"]["node"] = "7" * 64
            raw = canonical_json(runner.proof) + b"\n"
        elif damage == "size":
            raw += b" "
        elif damage == "json":
            raw = b"{secret-token-value}"
            runner.values["build_dependency_proof_sha256"] = hashlib.sha256(raw).hexdigest()
            runner.values["build_dependency_proof_size"] = str(len(raw))
        elif damage == "noncanonical":
            raw = json.dumps(runner.proof, indent=2).encode() + b"\n"
            runner.values["build_dependency_proof_sha256"] = hashlib.sha256(raw).hexdigest()
            runner.values["build_dependency_proof_size"] = str(len(raw))
        local_path.write_bytes(raw)
    runner.download_file = download
    with pytest.raises(VMCandidateBuildError) as error:
        ensure_vm_build_dependencies(runner, manifest())
    assert "secret-token-value" not in str(error.value) + json.dumps(error.value.report)
    assert runner.calls[-1][2] == {"input_removed"}


def run_bash(body: str):
    script = "set -Eeuo pipefail\nsource " + shlex.quote(BUILD_HELPER.as_posix()) + r'''
scratch=$(mktemp -d)
scratch=$(cd "$scratch" && pwd -P)
chmod 700 "$scratch"
trap 'rm -rf -- "$scratch"' EXIT
if [[ $OSTYPE == msys* ]]; then
  vm_build_private_directory() { [[ -d $1 && ! -L $1 ]]; }
  install() { [[ $1 == -d && $2 == -m && $3 == 700 ]] && mkdir -p "$4"; }
  stat() {
    if [[ $1 == -c && $2 == '%u:%a:%h' ]]; then printf '%s:400:1' "$(id -u)";
    else command stat "$@"; fi
  }
fi
timeout() { shift 2; "$@"; }
''' + body
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-s"], input=script, text=True, encoding="utf-8", capture_output=True, timeout=30)
    assert result.returncode == 0, result.stderr
    assert "secret-token-value" not in result.stdout + result.stderr
    return result.stdout.strip()


def jq_setup():
    jq = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")
    if not jq:
        pytest.skip("real jq is unavailable; set SUB2API_TEST_JQ")
    return f"jq() {{ {shlex.quote(Path(jq).as_posix())} \"$@\"; }}\n"


def registry_setup(damage=None):
    def content(v):
        raw = json.dumps(v, separators=(",", ":"))
        return raw, "sha256:" + hashlib.sha256(raw.encode()).hexdigest()
    child = {
        "schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
        "config": {"digest": "sha256:" + "1" * 64, "size": 12, "mediaType": "application/vnd.oci.image.config.v1+json"},
        "layers": [{"digest": "sha256:" + "2" * 64, "size": 20, "mediaType": "application/vnd.oci.image.layer.v1.tar+gzip"}],
    }
    raw_child, child_digest = content(child)
    descriptor = {"mediaType": child["mediaType"], "size": len(raw_child), "digest": child_digest,
                  "platform": {"os": "linux", "architecture": "amd64"}}
    if damage == "missing_arch": descriptor["platform"]["architecture"] = "arm64"
    if damage == "bad_digest": descriptor["digest"] = "secret-token-value"
    if damage == "bad_size": descriptor["size"] = 0.5
    index = {"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": [descriptor]}
    if damage == "duplicate_arch": index["manifests"].append(descriptor.copy())
    raw_index, index_digest = content(index)
    root_descriptor = {"mediaType": index["mediaType"], "size": len(raw_index), "digest": index_digest}
    return jq_setup() + "\n".join(f"{k}={shlex.quote(v)}" for k, v in {
        "root_descriptor": json.dumps(root_descriptor), "raw_index": raw_index,
        "raw_child": raw_child, "index_digest": index_digest, "child_digest": child_digest,
    }.items()) + r'''
printf '# syntax=docker/dockerfile:1.7\nFROM scratch\n' >"$scratch/Dockerfile"
release_id=274-aaaaaaaaaaaa-1792330000-aaaaaaaa
commit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
identity_hash=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
vm_build_capture_identity() {
  jq -cnS --arg hash "$identity_hash" '{name:"default",driver:"docker",identity_sha256:$hash}' >"$1/builder.json"
}
docker() {
  printf x >>"$scratch/docker-calls"
  if [[ $1 == buildx && $2 == imagetools && $3 == inspect ]]; then
    case $4 in
      --format) printf '%s\n' "$root_descriptor";;
      --raw)
        case $5 in
          *@"$index_digest") printf '%s\n' "$raw_index";;
          *@"$child_digest") printf '%s\n' "$raw_child";;
          *) return 98;;
        esac;;
      *) return 99;;
    esac
  elif [[ $1 == buildx && $2 == build ]]; then
    local arg destination=
    for arg in "$@"; do
      if [[ $arg == type=local,dest=* ]]; then destination=${arg#type=local,dest=}; fi
    done
    if [[ -z $destination ]]; then
      [[ " $* " == *' --load '* ]] || return 97
      local pinned_count=0
      for arg in "$@"; do
        case $arg in
          NODE_IMAGE=*|GOLANG_IMAGE=*|ALPINE_IMAGE=*|POSTGRES_IMAGE=*|BUILDKIT_SYNTAX=*)
            [[ $arg == *@sha256:* ]] || return 94
            pinned_count=$((pinned_count+1));;
        esac
      done
      [[ $pinned_count == 5 ]] || return 95
      printf candidate >>"$scratch/candidate-calls"
      return 0
    fi
    mkdir -p "$destination"
    for arg in node golang alpine postgres; do printf 'ID=fixture-%s\n' "$arg" >"$destination/$arg"; done
  else
    return 96
  fi
}
'''


def test_real_solve_proof_contains_five_locked_roles_and_four_materializations():
    body = registry_setup() + r'''
vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"
jq -e '(.roles|length)==5 and (.materialized_sha256|length)==4 and .dependency_solve_count==1' "$scratch/proof.json" >/dev/null
printf '%s %s %s' "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT" "$(wc -c <"$scratch/docker-calls")"
'''
    assert run_bash(body) == "none 5 16"


def test_candidate_consumes_proof_and_uses_all_five_pinned_refs_once():
    body = registry_setup() + r'''
vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"
proof_hash=$(sha256sum "$scratch/proof.json" | awk '{print $1}')
vm_build_use_proof "$scratch/proof.json" "$proof_hash" "$release_id" "$commit" "$scratch/Dockerfile"
cd "$scratch"
vm_build_candidate "$scratch/build.log" --build-arg COMMIT="$commit" --build-arg VERSION=fixture --build-arg DATE=fixture -t fixture .
[[ $(cat "$scratch/candidate-calls") == candidate ]]
printf '%s %s' "$VM_BUILD_FAILURE_CODE" "$(wc -c <"$scratch/docker-calls")"
'''
    assert run_bash(body) == "none 17"


@pytest.mark.parametrize("override", [
    "--build-arg NODE_IMAGE=evil", "--build-arg BUILDKIT_SYNTAX=evil", "--platform linux/arm64",
    "--builder other", "--build-arg COMMIT=wrong", "--file other", "--output type=registry",
])
def test_later_candidate_arguments_cannot_override_verified_inputs(override):
    body = registry_setup() + r'''
vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"
proof_hash=$(sha256sum "$scratch/proof.json" | awk '{print $1}')
vm_build_use_proof "$scratch/proof.json" "$proof_hash" "$release_id" "$commit" "$scratch/Dockerfile"
cd "$scratch"
'''
    body += f'if vm_build_candidate "$scratch/build.log" --build-arg COMMIT="$commit" --build-arg VERSION=fixture --build-arg DATE=fixture -t fixture {override} .; then exit 98; fi\n'
    body += '[[ ! -e $scratch/candidate-calls ]]\nprintf "%s" "$VM_BUILD_FAILURE_CODE"\n'
    assert run_bash(body) == "build_dependency_contract"


@pytest.mark.parametrize("damage", ["missing_arch", "duplicate_arch", "bad_digest", "bad_size"])
def test_metadata_requires_one_amd64_and_verified_descriptor_content(damage):
    body = registry_setup(damage) + r'''
if vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"; then exit 98; fi
[[ ! -e $scratch/proof.json && ! -e $scratch/solve.log ]]
printf '%s %s' "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT"
'''
    assert run_bash(body) == "build_registry_metadata_invalid 0"


@pytest.mark.parametrize("target", ["index", "child"])
def test_wrong_raw_content_never_materializes(target):
    body = registry_setup() + ("raw_index+=x\n" if target == "index" else "raw_child+=x\n") + r'''
if vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"; then exit 98; fi
[[ ! -e $scratch/proof.json && ! -e $scratch/solve.log ]]
printf '%s' "$VM_BUILD_FAILURE_CODE"
'''
    assert run_bash(body) == "build_registry_metadata_invalid"


@pytest.mark.parametrize("message,status,code", [
    ("unexpected status: 502 Bad Gateway", 1, "build_registry_502"),
    ("failed to fetch anonymous token: EOF", 1, "build_registry_eof"),
    ("unauthorized: authentication required", 1, "build_registry_auth"),
    ("x509: certificate signed by unknown authority", 1, "build_registry_tls"),
    ("TLS handshake timeout", 1, "build_timeout"),
    ("unclassified", 124, "build_timeout"),
    ("no space left on device", 1, "build_space"),
    ("main.go:7: undefined: missing", 1, "build_compile"),
    ("unclassified", 1, "build_unknown"),
])
@pytest.mark.parametrize("phase", ["metadata", "dependency", "candidate"])
def test_fixed_failure_codes_never_return_raw_diagnostics(message, status, code, phase):
    if phase != "candidate" and code == "build_compile": code = "build_unknown"
    body = f"printf '%s' {shlex.quote(message + ' secret-token-value')} >\"$scratch/error\"\n"
    body += f"vm_build_failure_code {phase} {status} \"$scratch/error\""
    assert run_bash(body) == code


@pytest.mark.parametrize("change", ["identity", "commit", "helper", "role", "digest"])
def test_proof_drift_is_rejected_before_candidate(change):
    body = registry_setup() + r'''
vm_build_preflight "$scratch" "$release_id" "$commit" "$scratch/Dockerfile"
proof_hash=$(sha256sum "$scratch/proof.json" | awk '{print $1}')
'''
    if change == "identity": body += "identity_hash=" + "c" * 64 + "\n"
    elif change == "commit": body += "commit=" + "c" * 40 + "\n"
    elif change == "helper": body += 'VM_BUILD_HELPER_PATH="$scratch/Dockerfile"\n'
    elif change == "role": body += 'VM_BUILD_IMAGES[0]=forbidden.example/node:24-alpine\n'
    else: body += "proof_hash=" + "c" * 64 + "\n"
    body += r'''
if vm_build_use_proof "$scratch/proof.json" "$proof_hash" "$release_id" "$commit" "$scratch/Dockerfile"; then exit 98; fi
printf '%s' "$VM_BUILD_FAILURE_CODE"
'''
    assert run_bash(body) == "build_dependency_contract"


def test_failure_is_persisted_locally_with_only_fixed_code(tmp_path):
    value = manifest()
    _persist_vm_build_failure({"worker_exit_code": "1", "gate_build_failure_code": "build_registry_502"}, value, tmp_path)
    report = json.loads((tmp_path / "vm-build-failure.json").read_text())
    assert report["build_failure_code"] == "build_registry_502" and report["release_id"] == value["release_id"]
    _persist_vm_build_failure({"worker_exit_code": "1", "gate_stage": "candidate_build", "gate_build_failure_code": "secret-token-value"}, value, tmp_path)
    assert "secret-token-value" not in (tmp_path / "vm-build-failure.json").read_text()
    assert json.loads((tmp_path / "vm-build-failure.json").read_text())["build_failure_code"] == "build_preflight_unproven"


@pytest.mark.parametrize("stage", ["migration_preflight", "restore", "old_image_health", "gate_signing", "unknown"])
@pytest.mark.parametrize("code", ["none", "secret-token-value"])
def test_nonbuild_worker_failure_keeps_original_vm_failure(stage, code, tmp_path):
    worker = {"worker_exit_code": "1", "gate_stage": stage, "gate_build_failure_code": code}
    _persist_vm_build_failure(worker, manifest(), tmp_path)
    assert not (tmp_path / "vm-build-failure.json").exists()
    with pytest.raises(RuntimeError) as error:
        _validate_completed_vm_worker(worker)
    assert str(error.value) == "remote VM validator failed; inspect structured Gate evidence"


def test_json_sizes_are_not_scanned_for_http_failure():
    body = 'printf \'{"size":502}\' >"$scratch/metadata"\nprintf \'unexpected EOF\' >"$scratch/error"\nvm_build_failure_code metadata 1 "$scratch/error"'
    assert run_bash(body) == "build_registry_eof"


@pytest.mark.parametrize("metadata,expected", [("0:700", "pass"), ("0:755", "fail"), ("1:700", "fail"), ("invalid", "fail")])
def test_private_directory_contract_checks_owner_and_mode(metadata, expected):
    # Reinsert the production function after the Windows-only fixture shim.
    function = re.search(r"(?ms)^vm_build_private_directory\(\) \{\n.*?^\}", BUILD_HELPER.read_text(encoding="utf-8"))
    assert function is not None
    body = function.group(0) + f"\nstat() {{ printf '%s' {shlex.quote(metadata)}; }}\nid() {{ printf '%s' 0; }}\n" + r'''
if vm_build_private_directory "$scratch"; then printf pass; else printf fail; fi
'''
    assert run_bash(body) == expected


def test_build_log_is_exclusive_and_preserves_existing_evidence():
    body = r'''
printf 'evidence' >"$scratch/build.log"
docker() { printf 'must not run'; return 99; }
if vm_build_candidate "$scratch/build.log" -t fixture .; then exit 98; fi
[[ $(cat "$scratch/build.log") == evidence ]]
printf '%s' "$VM_BUILD_FAILURE_CODE"
'''
    assert run_bash(body) == "build_dependency_contract"


@pytest.mark.parametrize("number", [401, 403, 502])
def test_build_byte_counts_and_source_lines_are_not_http_errors(number):
    diagnostic = f"transferring context: {number}B\nmain.go:{number}: undefined: missing"
    body = f"printf '%s' {shlex.quote(diagnostic)} >\"$scratch/error\"\nvm_build_failure_code candidate 1 \"$scratch/error\""
    assert run_bash(body) == "build_compile"


def test_completed_worker_failure_is_persisted_before_validation():
    source = (DEPLOY_ROOT / "release" / "vm_validate.py").read_text(encoding="utf-8")
    persist = source.index("_persist_vm_build_failure(worker, manifest, manifest_path.parent)")
    assert persist < source.index("_validate_completed_vm_worker(worker)", persist)


def test_helper_loading_and_build_preflight_cover_three_entrypoints():
    formal = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
    isolated = (DEPLOY_ROOT / "release" / "vm-only-validate.sh").read_text(encoding="utf-8")
    assert formal.count('vm_build_use_proof') >= 1
    assert formal.count('vm_build_candidate "$build_log"') == 2
    assert isolated.count('vm_build_use_proof') == 1
    assert isolated.count('vm_build_candidate "$build_log"') == 1
    assert ".release_asset_sha256[$key]" in formal
    assert 'git show "$commit:$build_helper_relative"' in isolated
    assert '[[ $build_helper_actual == "$build_helper_expected" ]]' in isolated
