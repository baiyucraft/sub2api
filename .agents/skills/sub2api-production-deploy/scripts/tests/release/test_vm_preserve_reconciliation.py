from __future__ import annotations

import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import supervisor as s
from release import vm_lifecycle as vm


@pytest.fixture
def fixture(tmp_path, monkeypatch):
    root = tmp_path / "releases"
    run_dir = root / "264-failed"
    shared = tmp_path / "vm"
    run_dir.mkdir(parents=True)
    shared.mkdir()
    settings = SimpleNamespace(identity="vm-id")
    monkeypatch.setattr(s, "RUN_ROOT", root)
    monkeypatch.setattr(vm, "load_settings", lambda: settings)
    monkeypatch.setattr(vm, "_shared_dir", lambda _: shared)
    monkeypatch.setattr(vm.VMLease, "_running", lambda _: True)
    monkeypatch.setattr(s, "_process_token", lambda _: None)
    commit = "a" * 40
    docs = {
        "manifest.json": {"schema": 2, "release_id": run_dir.name, "commit_sha": commit, "profile": "264", "production_current_image_id": "sha256:" + "b" * 64},
        "runner.json": {"schema": 1, "release_id": run_dir.name, "commit": commit, "profile": "264", "status": "failed", "exit_code": 1, "pid": 42, "process_token": "dead-runner"},
        "state.json": {"schema": 1, "release_id": run_dir.name, "status": "failed", "stage": "vm_validate", "history": []},
        "vm-lifecycle.json": {"schema": 1, "vm_identity": "vm-id", "release_id": run_dir.name, "source_commit": commit, "pid": 42, "process_token": "dead-runner", "lease_token": "lease", "initially_running": True, "vm_started_by_release": False, "vm_boot_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "lease_status": "retained", "vm_cleanup_status": "retained_failure"},
    }
    for name, value in docs.items():
        (run_dir / name).write_text(json.dumps(value))
    (shared / "owner.json").write_text(json.dumps(docs["vm-lifecycle.json"]))
    calls = []
    def remote(node, script, fields, **kwargs):
        calls.append((node, script))
        return SimpleNamespace(values={next(iter(fields)): "verified"})
    monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=remote))
    monkeypatch.setattr(s, "ReleaseDoctor", lambda *_: SimpleNamespace(run=lambda _, **_kwargs: {"racknerd_ready": "true", "production_current_image_id": "sha256:" + "b" * 64}))
    return SimpleNamespace(run_dir=run_dir, shared=shared, calls=calls, docs=docs, args=SimpleNamespace(release_id=run_dir.name))


def rewrite(f, name, **changes):
    value = {**f.docs[name], **changes}
    (f.run_dir / name).write_text(json.dumps(value))


def test_verified_pre_validator_failure_releases_only_lease(fixture):
    f = fixture
    original_state = (f.run_dir / "state.json").read_bytes()
    result = s.reconcile_vm_preserve(f.args)
    assert result["status"] == "preserved"
    assert (f.run_dir / "state.json").read_bytes() == original_state
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "released"
    assert json.loads((f.run_dir / "vm-lifecycle.json").read_text())["vm_started_by_release"] is False
    assert [c[0] for c in f.calls] == ["local_vm", "racknerd"]
    assert all("flock -n 9" in c[1] for c in f.calls)
    assert "flock -n 8" in f.calls[0][1]
    assert ".sub2api-release-unit.lock" in f.calls[0][1]
    assert 'test ! -e "$raw_root/vm-validate.raw.log"' in f.calls[0][1]
    assert 'test ! -L "$raw_root/vm-validate.raw.log"' in f.calls[0][1]
    assert "release-gates/264-failed" in f.calls[0][1]
    assert ".active-release" in f.calls[1][1]
    assert all("docker stop" not in c[1] and "rm " not in c[1] and "poweroff" not in c[1] for c in f.calls)


@pytest.mark.parametrize("name,changes", [
    ("manifest.json", {"commit_sha": "short"}),
    ("runner.json", {"status": "running"}),
    ("runner.json", {"commit": "b" * 40}),
    ("runner.json", {"exit_code": 0}),
    ("runner.json", {"pid": None}),
    ("state.json", {"history": [{"stage": "migration_and_switch"}]}),
    ("state.json", {"history": None}),
    ("state.json", {"stage": "candidate_build"}),
    ("vm-lifecycle.json", {"initially_running": False}),
    ("vm-lifecycle.json", {"vm_started_by_release": True}),
    ("vm-lifecycle.json", {"source_commit": "b" * 40}),
    ("vm-lifecycle.json", {"vm_boot_id": None}),
])
def test_unproven_identity_or_state_is_retained(fixture, name, changes):
    f = fixture
    before = (f.shared / "owner.json").read_bytes()
    rewrite(f, name, **changes)
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert f.calls == []


@pytest.mark.parametrize("path", ["gate", "release-state.json"])
def test_existing_gate_or_production_state_blocks(fixture, path):
    f = fixture
    (f.run_dir / path).mkdir() if path == "gate" else (f.run_dir / path).write_text("{}")
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert f.calls == []


def test_terminal_label_does_not_hide_live_process(fixture, monkeypatch):
    f = fixture
    monkeypatch.setattr(s, "_process_token", lambda _: "dead-runner")
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert f.calls == []


@pytest.mark.parametrize("node", ["local_vm", "racknerd"])
def test_remote_failure_preserves_owner(fixture, monkeypatch, node):
    f = fixture
    before = (f.shared / "owner.json").read_bytes()
    def remote(actual, *_args, **_kwargs):
        if actual == node:
            raise TimeoutError()
        return SimpleNamespace(values={"vm_preserve_preflight": "verified"})
    monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=remote))
    with pytest.raises(TimeoutError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert not (f.run_dir / "vm-preserve-result.json").exists()


def test_owner_write_failure_keeps_shared_claim_and_allows_audited_retry(fixture, monkeypatch):
    f = fixture
    original = s.atomic_write
    def fail_owner(path, *args, **kwargs):
        if path == f.shared / "owner.json":
            raise OSError("injected")
        return original(path, *args, **kwargs)
    monkeypatch.setattr(s, "atomic_write", fail_owner)
    with pytest.raises(OSError):
        s.reconcile_vm_preserve(f.args)
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "retained"
    monkeypatch.setattr(s, "atomic_write", original)
    assert s.reconcile_vm_preserve(f.args)["status"] == "preserved"


@pytest.mark.parametrize("fields", [{"racknerd_ready": "false"}, {"racknerd_ready": "true", "production_current_image_id": "sha256:" + "c" * 64}])
def test_changed_production_or_unhealthy_app_blocks_release(fixture, monkeypatch, fields):
    f = fixture
    before = (f.shared / "owner.json").read_bytes()
    monkeypatch.setattr(s, "ReleaseDoctor", lambda *_: SimpleNamespace(run=lambda _, **_kwargs: fields))
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before


def test_pre_gate_policy_allows_healthy_legacy_ingress_to_release_failure_lease(fixture, monkeypatch):
    f = fixture
    def check(nodes, *, require_ingress_policy=True):
        assert nodes == ("racknerd",)
        assert require_ingress_policy is False
        return {"racknerd_ready": "true", "production_current_image_id": "sha256:" + "b" * 64, "nginx_ingress_policy": "needs_update"}
    monkeypatch.setattr(s, "ReleaseDoctor", lambda *_: SimpleNamespace(run=check))
    assert s.reconcile_vm_preserve(f.args)["status"] == "preserved"
