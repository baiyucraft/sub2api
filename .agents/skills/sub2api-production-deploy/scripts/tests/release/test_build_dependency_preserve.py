"""Failure-lease admission for validator dependency proof failure only."""
import hashlib
import json
import os
from pathlib import Path
import shutil
from types import SimpleNamespace

import pytest

from test_vm_preserve_reconciliation import fixture, rewrite
from release import supervisor as s
from release import vm_lifecycle as vm
from release.process import run_hidden


def test_dependency_failure_preserves_originals_and_releases_last(fixture):
    f = fixture
    f.args.failed_stage = "build-dependencies"
    originals = {name: (f.run_dir / name).read_bytes() for name in ("manifest.json", "runner.json", "state.json")}
    result = s.reconcile_vm_preserve(f.args)
    assert result["status"] == "preserved" and result["production_not_started"] is True
    assert result["failed_stage"] == "build-dependencies"
    assert result["manifest_sha256"] == hashlib.sha256(originals["manifest.json"]).hexdigest()
    assert all((f.run_dir / name).read_bytes() == value for name, value in originals.items())
    lease = json.loads((f.run_dir / "vm-lifecycle.json").read_text())
    assert json.loads((f.shared / "owner.json").read_text()) == lease
    assert (lease["lease_status"], lease["vm_power_status"], lease["vm_cleanup_status"]) == ("released", "running", "preserved")
    assert [node for node, _ in f.calls] == ["local_vm", "racknerd"]
    checks = f.calls[0][1]
    for required in ('flock -n 8', 'flock -n 9', 'vm_v2_build_dependencies', 'stage=build_dependencies$',
                     '"$gate_root/build.log"', '"$gate_root/candidate-identity"', '"$gate_root/output/gate.sig"',
                     '"$gate_root/probe-data"', '"$gate_root/production-recovery"',
                     '"$raw_root/vm-validate.raw.log"', 'test "$required_value" = aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'):
        assert required in checks
    assert 'stage=candidate_build$' not in checks
    assert '.active-release' in f.calls[1][1]


@pytest.mark.parametrize("failure", ["vm", "production", "power", "live_runner", "started_vm", "production_drift", "local_drift"])
def test_dependency_failure_unproven_evidence_retains_owner(fixture, monkeypatch, failure):
    f = fixture
    f.args.failed_stage = "build-dependencies"
    before = (f.shared / "owner.json").read_bytes()
    if failure in {"vm", "production", "local_drift"}:
        original = s.SSHRunner().run
        def remote(node, *args, **kwargs):
            if node == ("local_vm" if failure == "vm" else "racknerd") and failure != "local_drift":
                raise RuntimeError("unproven")
            result = original(node, *args, **kwargs)
            if node == "racknerd" and failure == "local_drift":
                rewrite(f, "manifest.json", drift=True)
            return result
        monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=remote))
    elif failure == "power":
        monkeypatch.setattr(vm.VMLease, "_running", lambda _: False)
    elif failure == "live_runner":
        monkeypatch.setattr(s, "_process_token", lambda _: "dead-runner")
    elif failure == "started_vm":
        rewrite(f, "vm-lifecycle.json", initially_running=False, vm_started_by_release=True)
    else:
        monkeypatch.setattr(s, "ReleaseDoctor", lambda *_: SimpleNamespace(run=lambda *_a, **_k: {"racknerd_ready": "true", "production_current_image_id": "sha256:" + "c" * 64}))
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert not (f.run_dir / "vm-preserve-result.json").exists()


def test_original_candidate_contract_is_not_broadened():
    source = s._candidate_build_preserve_checks("release", "a" * 64)
    assert 'test "$required_capture_6" = candidate_build' in source
    assert 'test "$required_capture_7" = vm_v2_candidate_build' in source
    assert 'stage=candidate_build$' in source
    assert 'build_dependencies' not in source


@pytest.mark.parametrize("mutation", [None, "candidate_stage", "candidate_category", "candidate_detail", "zero_status", "large_status", "zero_line", "build.log", "candidate-identity", "candidate-version.log", "candidate-help.log", "dangling_build_log"])
def test_real_bash_stage_and_no_candidate_predicates(tmp_path, mutation):
    bash = shutil.which("bash") or ("C:/Program Files/Git/bin/bash.exe" if os.name == "nt" else None)
    if not bash or not Path(bash).exists():
        pytest.skip("Bash unavailable")
    (tmp_path / "stage").write_text("candidate_build" if mutation == "candidate_stage" else "build_dependencies")
    (tmp_path / "failure-category").write_text("vm_v2_candidate_build" if mutation == "candidate_category" else "vm_v2_build_dependencies")
    (tmp_path / "failure-detail").write_text("status=1 stage=candidate_build" if mutation == "candidate_detail" else "status=0 stage=build_dependencies" if mutation == "zero_status" else "status=256 stage=build_dependencies" if mutation == "large_status" else "status=1 stage=build_dependencies")
    (tmp_path / "failure-line").write_text("0" if mutation == "zero_line" else "52")
    if mutation in {"build.log", "candidate-identity", "candidate-version.log", "candidate-help.log"}:
        (tmp_path / mutation).write_text("candidate attempted")
    checks = s._build_dependency_preserve_checks("release", "a" * 64)
    start = checks.index('required_capture_6=')
    end = checks.index('for absent in', start)
    stage_checks = checks[start:end]
    candidate_checks = checks[checks.rindex('for absent in'):]
    setup = 'ln -s missing build.log\n' if mutation == "dangling_build_log" else ''
    result = run_hidden([bash, "-c", 'set -Eeuo pipefail\ngate_root=.\n' + setup + stage_checks + candidate_checks], cwd=tmp_path, capture_output=True, text=True)
    assert (result.returncode == 0) is (mutation is None), result.stderr
