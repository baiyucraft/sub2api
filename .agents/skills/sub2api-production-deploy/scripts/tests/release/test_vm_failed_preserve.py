"""Original evidence, fail-closed proof and final owner commit for Gate failure."""
from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import supervisor as s
from release import vm_failed_preserve as p
from release import vm_lifecycle as vm


@pytest.fixture
def case(tmp_path, monkeypatch):
    root, shared = tmp_path / "releases", tmp_path / "shared"
    run = root / p.INCIDENT_ID
    run.mkdir(parents=True)
    shared.mkdir()
    lease = dict(schema=1, vm_identity="vm-id", source_commit=p.INCIDENT_COMMIT,
                 release_id=p.INCIDENT_ID, pid=42, process_token="dead-worker", lease_token="lease",
                 initially_running=False, vm_started_by_release=True, lease_status="retained",
                 vm_cleanup_status="retained_failure", vm_power_status="running",
                 vm_boot_id="aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", updated_at=1)
    docs = {
        "manifest.json": dict(schema=2, release_id=p.INCIDENT_ID, commit_sha=p.INCIDENT_COMMIT,
                              profile="273", vm_validator_sha256=p.INCIDENT_VALIDATOR,
                              runner_sha256="r" * 64, production_current_image_id="sha256:" + "b" * 64),
        "runner.json": dict(schema=1, release_id=p.INCIDENT_ID, commit=p.INCIDENT_COMMIT,
                            profile="273", runner_sha256="r" * 64, pid=42, process_token="dead-worker",
                            status="failed", exit_code=1),
        "state.json": dict(schema=1, release_id=p.INCIDENT_ID, stage="vm_validate", status="failed", history=[]),
        "vm-lifecycle.json": lease,
    }
    for name, doc in docs.items():
        (run / name).write_bytes(json.dumps(doc).encode())
    (shared / "owner.json").write_bytes((run / "vm-lifecycle.json").read_bytes())
    hashes = {name: hashlib.sha256((run / name).read_bytes()).hexdigest() for name in docs}
    monkeypatch.setattr(p, "ORIGINAL_SHA256", hashes)
    monkeypatch.setattr(s, "RUN_ROOT", root)
    monkeypatch.setattr(vm, "load_settings", lambda: SimpleNamespace(identity="vm-id"))
    monkeypatch.setattr(vm, "_shared_dir", lambda _: shared)
    monkeypatch.setattr(vm.VMLease, "_running", lambda _: True)
    monkeypatch.setattr(p, "_process_identity", lambda _: None)
    monkeypatch.setattr(p, "_helper_identity", lambda: dict(commit_sha="c" * 40, assets={}, runner_sha256="d" * 64))
    doctors = []
    def doctor(_profile, commit):
        def check(nodes, **kwargs):
            doctors.append((commit, nodes, kwargs))
            if nodes == ("local",):
                return dict(local_ready="true", host_keys_ready="true")
            return dict(racknerd_ready="true", production_current_image_id="sha256:" + "b" * 64)
        return SimpleNamespace(run=check)
    monkeypatch.setattr(p, "ReleaseDoctor", doctor)
    calls = []
    def remote(node, script, fields, **kwargs):
        calls.append((node, script))
        return SimpleNamespace(values={next(iter(fields)): "verified"})
    monkeypatch.setattr(p, "SSHRunner", lambda: SimpleNamespace(run=remote))
    return SimpleNamespace(run=run, shared=shared, docs=docs, args=SimpleNamespace(release_id=p.INCIDENT_ID),
                           hashes=hashes, calls=calls, doctors=doctors, remote=remote)


def test_preserve_keeps_all_originals_and_owner_is_final_commit(case, monkeypatch):
    before = {name: (case.run / name).read_bytes() for name in case.docs}
    writes = []
    original = p.atomic_write
    def write(path, *args, **kwargs):
        writes.append(path.name)
        if path.name == "owner.json":
            assert json.loads((case.run / p.RESULT_NAME).read_bytes())["status"] == "verified"
            assert (case.run / p.CHECKS_NAME).exists()
        return original(path, *args, **kwargs)
    monkeypatch.setattr(p, "atomic_write", write)
    result = p.reconcile(case.args)
    assert result["lease_status"] == "released"
    assert writes == [p.CHECKS_NAME, p.RESULT_NAME, "owner.json"]
    assert all((case.run / name).read_bytes() == data for name, data in before.items())
    owner = vm._read_state(case.shared / "owner.json")
    assert owner["vm_started_by_release"] is True
    assert owner["lease_status"] == "released" and owner["vm_power_status"] == "running"
    vm._assert_free(case.shared)  # The following release may acquire a fresh lease.
    assert [node for node, _ in case.calls] == ["local_vm", "racknerd"] * 3
    assert not any(word in script for _, script in case.calls for word in ("docker stop", "poweroff", "rm -", "shutdown "))
    assert case.doctors[0][0] == "c" * 40 and case.doctors[0][1] == ("local",)


def test_verified_replay_is_readonly_and_rechecks_originals(case, monkeypatch):
    p.reconcile(case.args)
    before = {path: path.read_bytes() for path in (*case.run.iterdir(), case.shared / "owner.json") if path.is_file()}
    monkeypatch.setattr(p, "atomic_write", lambda *_a, **_k: pytest.fail("replay must be read-only"))
    case.args.verify_only = True
    assert p.reconcile(case.args)["status"] == "verified"
    assert all(path.read_bytes() == data for path, data in before.items())
    assert [node for node, _ in case.calls[-4:]] == ["local_vm", "racknerd"] * 2
    (case.run / "runner.json").write_bytes(b"{}")
    with pytest.raises(RuntimeError, match="original_changed"):
        p.reconcile(case.args)


@pytest.mark.parametrize("name", ["manifest.json", "runner.json", "state.json", "vm-lifecycle.json"])
def test_original_byte_drift_keeps_owner(case, name):
    before = (case.shared / "owner.json").read_bytes()
    (case.run / name).write_bytes((case.run / name).read_bytes() + b" ")
    with pytest.raises(RuntimeError, match="original_changed"):
        p.reconcile(case.args)
    assert (case.shared / "owner.json").read_bytes() == before
    assert not case.calls


@pytest.mark.parametrize("target", [p.CHECKS_NAME, p.RESULT_NAME, "owner.json"])
def test_interrupted_write_retains_owner_and_exact_retry(case, monkeypatch, target):
    before = (case.shared / "owner.json").read_bytes()
    original = p.atomic_write
    def fail(path, *args, **kwargs):
        if path.name == target:
            raise OSError("injected")
        return original(path, *args, **kwargs)
    monkeypatch.setattr(p, "atomic_write", fail)
    with pytest.raises(OSError):
        p.reconcile(case.args)
    assert (case.shared / "owner.json").read_bytes() == before
    monkeypatch.setattr(p, "atomic_write", original)
    assert p.reconcile(case.args)["lease_status"] == "released"


@pytest.mark.parametrize("node", ["local_vm", "racknerd"])
@pytest.mark.parametrize("round_number", [1, 2, 3])
def test_probe_or_production_failure_never_commits_owner(case, monkeypatch, node, round_number):
    before = (case.shared / "owner.json").read_bytes()
    count = 0
    def remote(actual, *args, **kwargs):
        nonlocal count
        if actual == node:
            count += 1
            if count == round_number:
                raise RuntimeError("probe residue or production claim")
        return case.remote(actual, *args, **kwargs)
    monkeypatch.setattr(p, "SSHRunner", lambda: SimpleNamespace(run=remote))
    with pytest.raises(RuntimeError):
        p.reconcile(case.args)
    assert (case.shared / "owner.json").read_bytes() == before


def test_live_or_uncertain_worker_is_rejected(case, monkeypatch):
    monkeypatch.setattr(p, "_process_identity", lambda _: "dead-worker")
    with pytest.raises(RuntimeError, match="worker_alive"):
        p.reconcile(case.args)
    assert not case.calls
    def uncertain(_):
        raise PermissionError("unproven process")
    monkeypatch.setattr(p, "_process_identity", uncertain)
    with pytest.raises(PermissionError):
        p.reconcile(case.args)
    assert not case.calls


@pytest.mark.parametrize("name", ["gate", "release-state.json", "finalization-result.json", "vm-preserve-result.json"])
def test_gate_production_or_other_reconciliation_rejected(case, name):
    (case.run / name).mkdir() if name == "gate" else (case.run / name).write_text("{}")
    with pytest.raises(RuntimeError, match="production_state_present"):
        p.reconcile(case.args)
    assert not case.calls


def test_uncommitted_helper_or_helper_drift_rejected(case, monkeypatch):
    monkeypatch.setattr(p, "ReleaseDoctor", lambda *_: SimpleNamespace(run=lambda *_a, **_k: (_ for _ in ()).throw(RuntimeError("dirty or unpushed"))))
    with pytest.raises(RuntimeError, match="dirty or unpushed"):
        p.reconcile(case.args)
    assert not case.calls


@pytest.mark.parametrize("target", ["owner.json", "runner.json"])
def test_evidence_drift_during_remote_probe_retains_owner(case, monkeypatch, target):
    path = case.shared / target if target == "owner.json" else case.run / target
    def remote(node, *args, **kwargs):
        result = case.remote(node, *args, **kwargs)
        if node == "racknerd":
            path.write_bytes(path.read_bytes() + b" ")
        return result
    monkeypatch.setattr(p, "SSHRunner", lambda: SimpleNamespace(run=remote))
    with pytest.raises(RuntimeError):
        p.reconcile(case.args)
    assert vm._read_state(case.shared / "owner.json")["lease_status"] == "retained"


def test_unknown_incident_and_uncommitted_verify_rejected(case):
    case.args.release_id = "273-unknown"
    with pytest.raises(RuntimeError, match="unknown_incident"):
        p.reconcile(case.args)
    case.args.release_id = p.INCIDENT_ID
    case.args.verify_only = True
    with pytest.raises(RuntimeError, match="not_committed"):
        p.reconcile(case.args)
    assert not case.calls


@pytest.mark.parametrize("target", [p.CHECKS_NAME, p.RESULT_NAME])
def test_replay_rejects_independent_record_drift(case, target):
    p.reconcile(case.args)
    record = case.run / target
    value = json.loads(record.read_bytes())
    value["drift"] = True
    record.write_bytes(json.dumps(value).encode())
    with pytest.raises(RuntimeError, match="(checks|result)_changed"):
        p.reconcile(case.args)


def test_replay_refuses_new_owner(case):
    p.reconcile(case.args)
    owner = vm._read_state(case.shared / "owner.json")
    owner.update(release_id="new-release", lease_status="active")
    (case.shared / "owner.json").write_text(json.dumps(owner))
    with pytest.raises(RuntimeError, match="owner_changed"):
        p.reconcile(case.args)


@pytest.mark.parametrize("target", [p.CHECKS_NAME, p.RESULT_NAME])
def test_replay_rejects_independent_record_byte_drift(case, target):
    p.reconcile(case.args)
    record = case.run / target
    record.write_bytes(record.read_bytes() + b" ")
    with pytest.raises(RuntimeError, match="(checks|result)_changed"):
        p.reconcile(case.args)


@pytest.mark.parametrize("command,verify", [
    ("reconcile-vm-failed-preserve", False), ("verify-vm-failed-preserve", True),
])
def test_cli_dispatch_keeps_verification_explicit(monkeypatch, command, verify):
    from release import cli
    seen = []
    monkeypatch.setattr(p, "reconcile", lambda args: seen.append(args))
    monkeypatch.setattr("sys.argv", ["release.py", command, p.INCIDENT_ID])
    cli.main()
    assert len(seen) == 1 and seen[0].release_id == p.INCIDENT_ID
    assert getattr(seen[0], "verify_only", False) is verify
