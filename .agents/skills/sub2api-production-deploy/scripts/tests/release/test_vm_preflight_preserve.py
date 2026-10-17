"""Failure retention and commit ordering for the audited post-restore incident."""
from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from test_vm_preserve_reconciliation import fixture  # noqa: F401
from release import supervisor as s
from release import vm_preflight_preserve as proof


def incident(f):
    commit = proof.INCIDENT_COMMIT
    f.args.failed_stage = "migration-preflight"
    for name in ("manifest.json", "runner.json", "vm-lifecycle.json"):
        doc = f.docs[name]
        if name == "manifest.json":
            doc.update(commit_sha=commit, profile="273", vm_validator_sha256=proof.INCIDENT_VALIDATOR,
                       release_asset_sha256={proof.ASSERTION_PATH: proof.INCIDENT_ASSERTION})
        elif name == "runner.json":
            doc.update(commit=commit, profile="273")
        else:
            doc.update(source_commit=commit)
        (f.run_dir / name).write_text(json.dumps(doc))
    (f.shared / "owner.json").write_text(json.dumps(f.docs["vm-lifecycle.json"]))
    return f


def test_incident_rechecks_then_releases_only_local_lease(fixture):
    f = incident(fixture)
    originals = {name: (f.run_dir / name).read_bytes() for name in ("manifest.json", "runner.json", "state.json")}
    result = s.reconcile_vm_preserve(f.args)
    assert result["failed_stage"] == "migration-preflight"
    assert result["production_not_started"] is True
    assert [node for node, _ in f.calls] == ["local_vm", "racknerd", "local_vm", "racknerd"]
    assert all((f.run_dir / name).read_bytes() == data for name, data in originals.items())
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "released"


@pytest.mark.parametrize("field,value", [
    ("commit_sha", "b" * 40), ("profile", "274"),
    ("vm_validator_sha256", "0" * 64), ("release_asset_sha256", {}),
])
def test_other_source_or_assertion_never_releases_owner(fixture, field, value):
    f = incident(fixture)
    original = (f.shared / "owner.json").read_bytes()
    doc = f.docs["manifest.json"].copy()
    doc[field] = value
    (f.run_dir / "manifest.json").write_text(json.dumps(doc))
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == original
    assert not (f.run_dir / "vm-preserve-result.json").exists()


@pytest.mark.parametrize("target", ["vm-preserve-result.json", "vm-lifecycle.json", "owner.json"])
def test_interrupted_commit_retains_shared_owner_and_audited_retry(fixture, monkeypatch, target):
    f = incident(fixture)
    before = (f.shared / "owner.json").read_bytes()
    write = s.atomic_write
    def fail(path, *args, **kwargs):
        if path.name == target:
            raise OSError("injected")
        return write(path, *args, **kwargs)
    monkeypatch.setattr(s, "atomic_write", fail)
    with pytest.raises(OSError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    monkeypatch.setattr(s, "atomic_write", write)
    assert s.reconcile_vm_preserve(f.args)["status"] == "preserved"


@pytest.mark.parametrize("target", ["manifest.json", "runner.json", "state.json", "vm-lifecycle.json", "owner.json"])
def test_evidence_changes_during_remote_proof_never_release(fixture, monkeypatch, target):
    f = incident(fixture)
    remote = s.SSHRunner().run
    def drift(node, *args, **kwargs):
        result = remote(node, *args, **kwargs)
        if node == "racknerd":
            path = f.shared / target if target == "owner.json" else f.run_dir / target
            doc = json.loads(path.read_text())
            doc["drift"] = True
            path.write_text(json.dumps(doc))
        return result
    monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=drift))
    with pytest.raises(RuntimeError):
        s.reconcile_vm_preserve(f.args)
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "retained"
    assert not (f.run_dir / "vm-preserve-result.json").exists()


@pytest.mark.parametrize("node", ["local_vm", "racknerd"])
def test_second_remote_check_failure_keeps_owner(fixture, monkeypatch, node):
    f = incident(fixture)
    before = (f.shared / "owner.json").read_bytes()
    remote = s.SSHRunner().run
    count = 0
    def fail_second(actual, *args, **kwargs):
        nonlocal count
        if actual == node:
            count += 1
            if count == 2:
                raise TimeoutError("injected")
        return remote(actual, *args, **kwargs)
    monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=fail_second))
    with pytest.raises(TimeoutError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert not (f.run_dir / "vm-preserve-result.json").exists()
