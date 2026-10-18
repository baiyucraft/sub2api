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


def manifest():
    return {
        "release_id": "274-aaaaaaaaaaaa-1792330000-aaaaaaaa",
        "commit_sha": "a" * 40,
        "release_asset_sha256": {BUILD_HELPER_ASSET: hashlib.sha256(BUILD_HELPER.read_bytes()).hexdigest()},
    }


class FakeRunner:
    def __init__(self, values=None, failure=None):
        self.values = values or {
            "build_dependency_status": "verified", "build_failure_code": "none",
            "build_dependency_count": "4", "build_dependency_verified_count": "4",
        }
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


def test_early_preflight_verifies_helper_and_removes_only_its_input():
    runner = FakeRunner()
    result = ensure_vm_build_dependencies(runner, manifest())
    assert set(result) == BUILD_DEPENDENCY_FIELDS
    assert len(runner.uploads) == 1 and runner.uploads[0][3] == 0o400
    command = runner.calls[1][1]
    assert "sha256sum" in command and "0:0:400:1" in command
    assert "source \"$helper\"" in command and "vm_build_preflight" in command
    assert "/build-dependencies-early" in command and "0:0:700" in command
    assert "docker build " not in command and "cat " not in command
    assert runner.calls[1][3] == 480
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
        "build_dependency_count": "4", "build_dependency_verified_count": "2",
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


def descriptor():
    return {
        "mediaType": "application/vnd.oci.image.manifest.v1+json", "size": 502,
        "digest": "sha256:" + "a" * 64,
        "platform": {"os": "linux", "architecture": "amd64"},
    }


def index():
    return {
        "schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
        "manifests": [descriptor()],
    }


def run_bash(body: str):
    script = "set -Eeuo pipefail\n" + BUILD_HELPER.read_text(encoding="utf-8") + r'''
scratch=$(mktemp -d)
chmod 700 "$scratch"
trap 'rm -rf -- "$scratch"' EXIT
if [[ $OSTYPE == msys* ]]; then
  vm_build_private_directory() { [[ -d $1 && ! -L $1 ]]; }
fi
timeout() {
  [[ $1 == --kill-after=10s && $2 == 90s ]] || return 97
  shift 2
  "$@"
}
''' + body
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-s"], input=script, text=True, capture_output=True, timeout=20)
    assert result.returncode == 0, result.stderr
    assert "secret-token-value" not in result.stdout + result.stderr
    return result.stdout.strip()


def jq_setup():
    jq = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")
    if not jq:
        pytest.skip("real jq is unavailable; set SUB2API_TEST_JQ")
    return f"jq() {{ {shlex.quote(Path(jq).as_posix())} \"$@\"; }}\n"


def test_four_metadata_inspections_use_fixed_refs_once_and_never_build_or_pull():
    body = jq_setup() + "fixture=" + shlex.quote(json.dumps(index())) + r'''
count=0
docker() {
  [[ $# == 5 && $1 == buildx && $2 == imagetools && $3 == inspect && $4 == --raw ]] || return 98
  [[ $5 == "${VM_BUILD_IMAGES[$count]}" ]] || return 99
  [[ $DOCKER_BUILDKIT == 1 && $NO_PROXY == "$no_proxy" ]] || return 96
  count=$((count + 1))
  printf '%s' "$fixture"
}
vm_build_preflight "$scratch"
printf '%s %s %s' "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT" "$count"
'''
    assert run_bash(body) == "none 4 4"


@pytest.mark.parametrize("damage", ["missing_arch", "duplicate_arch", "bad_digest", "bad_size", "bad_type", "single_manifest"])
def test_metadata_requires_exactly_one_valid_linux_amd64_descriptor(damage):
    value = index()
    if damage == "missing_arch":
        value["manifests"][0]["platform"]["architecture"] = "arm64"
    elif damage == "duplicate_arch":
        value["manifests"].append(descriptor())
    elif damage == "bad_digest":
        value["manifests"][0]["digest"] = "secret-token-value"
    elif damage == "bad_size":
        value["manifests"][0]["size"] = 0.1
    elif damage == "bad_type":
        value["mediaType"] = "secret-token-value"
    else:
        value = descriptor()
    body = jq_setup() + "fixture=" + shlex.quote(json.dumps(value)) + r'''
count=0
docker() { count=$((count + 1)); printf '%s' "$fixture"; }
if vm_build_preflight "$scratch"; then exit 98; fi
printf '%s %s %s' "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT" "$count"
'''
    assert run_bash(body) == "build_registry_metadata_invalid 0 1"


@pytest.mark.parametrize("message,status,code", [
    ("unexpected status: 502 Bad Gateway", 1, "build_registry_502"),
    ("failed to authorize: failed to fetch anonymous token: unexpected status: 502 Bad Gateway", 1, "build_registry_502"),
    ("failed to fetch anonymous token: EOF", 1, "build_registry_eof"),
    ("unauthorized: authentication required", 1, "build_registry_auth"),
    ("x509: certificate signed by unknown authority", 1, "build_registry_tls"),
    ("i/o timeout", 1, "build_timeout"),
    ("TLS handshake timeout", 1, "build_timeout"),
    ("unclassified", 124, "build_timeout"),
    ("no space left on device", 1, "build_space"),
    ("main.go:7: undefined: missing", 1, "build_compile"),
    ("unclassified", 1, "build_unknown"),
])
@pytest.mark.parametrize("phase", ["metadata", "candidate"])
def test_failure_codes_hide_raw_diagnostics_and_do_not_retry(message, status, code, phase):
    if phase == "metadata" and code == "build_compile":
        code = "build_unknown"
    body = f"fixture={shlex.quote(message + ' secret-token-value')}\nfixture_status={status}\n" + r'''
count=0
docker() { count=$((count + 1)); printf '%s' "$fixture" >&2; return "$fixture_status"; }
'''
    call = 'vm_build_preflight "$scratch"' if phase == "metadata" else 'vm_build_candidate "$scratch/build.log" --build-arg COMMIT=fixture -t fixture .'
    body += f"if {call}; then exit 98; fi\nprintf '%s %s' \"$VM_BUILD_FAILURE_CODE\" \"$count\"\n"
    assert run_bash(body) == code + " 1"


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


def test_failed_metadata_does_not_classify_successful_json_size_as_http_502():
    body = "fixture=" + shlex.quote(json.dumps(index())) + r'''
docker() { printf '%s' "$fixture"; printf 'unexpected EOF secret-token-value' >&2; return 1; }
if vm_build_preflight "$scratch"; then exit 98; fi
printf '%s' "$VM_BUILD_FAILURE_CODE"
'''
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
def test_build_progress_byte_counts_and_source_line_numbers_are_not_http_errors(number):
    diagnostic = f"transferring context: {number}B\nmain.go:{number}: undefined: missing"
    body = f"fixture={shlex.quote(diagnostic)}\n" + r'''
docker() { printf '%s' "$fixture"; return 1; }
if vm_build_candidate "$scratch/build.log" -t fixture .; then exit 98; fi
printf '%s' "$VM_BUILD_FAILURE_CODE"
'''
    assert run_bash(body) == "build_compile"


def test_completed_worker_failure_is_persisted_before_validation():
    source = (DEPLOY_ROOT / "release" / "vm_validate.py").read_text(encoding="utf-8")
    persist = source.index("_persist_vm_build_failure(worker, manifest, manifest_path.parent)")
    assert persist < source.index("_validate_completed_vm_worker(worker)", persist)


def test_helper_loading_and_build_preflight_cover_three_entrypoints():
    formal = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
    isolated = (DEPLOY_ROOT / "release" / "vm-only-validate.sh").read_text(encoding="utf-8")
    assert formal.count('vm_build_preflight "$state_dir/build-dependencies"') == 2
    assert formal.count('vm_build_candidate "$build_log"') == 2
    assert isolated.count('vm_build_preflight "$build_state_dir/build-dependencies"') == 1
    assert isolated.count('vm_build_candidate "$build_log"') == 1
    assert ".release_asset_sha256[$key]" in formal
    assert 'git show "$commit:$build_helper_relative"' in isolated
    assert '[[ $build_helper_actual == "$build_helper_expected" ]]' in isolated
