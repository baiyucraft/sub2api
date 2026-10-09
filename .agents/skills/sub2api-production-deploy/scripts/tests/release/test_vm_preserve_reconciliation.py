from __future__ import annotations

import hashlib
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


def test_candidate_build_failure_binds_manifest_and_preserves_failure_evidence(fixture):
    f = fixture
    f.args.failed_stage = "candidate-build"
    originals = {name: (f.run_dir / name).read_bytes() for name in ("manifest.json", "runner.json", "state.json")}
    expected_manifest = hashlib.sha256(originals["manifest.json"]).hexdigest()
    result = s.reconcile_vm_preserve(f.args)
    assert result["status"] == "preserved"
    assert result["failed_stage"] == "candidate-build"
    assert result["manifest_sha256"] == expected_manifest
    assert all((f.run_dir / name).read_bytes() == content for name, content in originals.items())
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "released"
    check = f.calls[0][1]
    assert expected_manifest in check
    assert 'test "$(cat "$gate_root/stage")" = candidate_build' in check
    assert 'test "$(cat "$gate_root/failure-category")" = vm_v2_candidate_build' in check
    assert "failure-detail" in check and "failure-line" in check
    assert '(( 10#$failure_status <= 255 ))' in check
    assert check.index("flock -n 8") < check.index("gate_root=/opt")
    assert check.index("flock -n 9") < check.index("gate_root=/opt")
    assert '"$gate_root/output/gate.json"' in check
    assert '"$gate_root/output/gate.sig"' in check
    assert '"$gate_root/output/candidate.tar.gz"' in check
    assert '"$gate_root/probe-data"' in check and '"$gate_root/production-recovery"' in check
    assert 'check_file "$gate_root/manifest.json" 400' in check
    assert 'check_file "$gate_root/stage" 600' in check
    assert 'check_file "$gate_root/failure-category" 400' in check
    assert 'check_file "$raw_root/vm-validate.raw.log" 600' in check
    assert "vm-space-clean.sh|sub2api-vm-validate|run-validator.sh|docker (build|buildx)|buildctl" in check
    assert all("docker stop" not in command and "rm " not in command and "poweroff" not in command for _, command in f.calls)


def test_default_preserve_still_refuses_started_validator(fixture):
    f = fixture
    s.reconcile_vm_preserve(f.args)
    check = f.calls[0][1]
    assert "test ! -e /opt/sub2api-deploy/release-gates/264-failed" in check
    assert 'test ! -e "$raw_root/vm-validate.raw.log"' in check
    assert "vm_v2_candidate_build" not in check


def test_unknown_failure_mode_never_checks_or_releases_owner(fixture):
    f = fixture
    before = (f.shared / "owner.json").read_bytes()
    f.args.failed_stage = "restore-probe"
    with pytest.raises(RuntimeError, match="invalid_failed_stage"):
        s.reconcile_vm_preserve(f.args)
    assert f.calls == []
    assert (f.shared / "owner.json").read_bytes() == before


@pytest.mark.parametrize("field", ["manifest.json", "runner.json", "state.json", "vm-lifecycle.json", "owner.json"])
def test_candidate_build_evidence_drift_after_remote_checks_retains_owner(fixture, monkeypatch, field):
    f = fixture
    f.args.failed_stage = "candidate-build"
    original_owner = (f.shared / "owner.json").read_bytes()
    original_remote = s.SSHRunner().run
    def remote(node, *args, **kwargs):
        result = original_remote(node, *args, **kwargs)
        if node == "racknerd":
            path = f.shared / field if field == "owner.json" else f.run_dir / field
            value = json.loads(path.read_text())
            value["drift"] = True
            path.write_text(json.dumps(value))
        return result
    monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=remote))
    with pytest.raises(RuntimeError, match="(local_evidence|owner)_changed"):
        s.reconcile_vm_preserve(f.args)
    owner = json.loads((f.shared / "owner.json").read_text())
    assert owner["lease_status"] == "retained"
    if field != "owner.json":
        assert (f.shared / "owner.json").read_bytes() == original_owner
    assert not (f.run_dir / "vm-preserve-result.json").exists()


@pytest.mark.parametrize("target", ["vm-preserve-result.json", "vm-lifecycle.json", "owner.json"])
def test_candidate_build_write_failures_leave_owner_retained_and_allow_audited_retry(fixture, monkeypatch, target):
    f = fixture
    f.args.failed_stage = "candidate-build"
    before = (f.shared / "owner.json").read_bytes()
    original_write = s.atomic_write
    def failed_write(path, *args, **kwargs):
        if path.name == target:
            raise OSError("injected")
        return original_write(path, *args, **kwargs)
    monkeypatch.setattr(s, "atomic_write", failed_write)
    with pytest.raises(OSError):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert json.loads((f.run_dir / "state.json").read_text())["status"] == "failed"
    monkeypatch.setattr(s, "atomic_write", original_write)
    assert s.reconcile_vm_preserve(f.args)["status"] == "preserved"


@pytest.mark.parametrize("failure", ["vm", "production", "power", "live_runner", "local_lock"])
def test_candidate_build_incomplete_proof_never_releases_owner(fixture, monkeypatch, failure):
    f = fixture
    f.args.failed_stage = "candidate-build"
    before = (f.shared / "owner.json").read_bytes()
    if failure in {"vm", "production"}:
        original_remote = s.SSHRunner().run
        def remote(node, *args, **kwargs):
            if node == ("local_vm" if failure == "vm" else "racknerd"):
                raise TimeoutError("injected")
            return original_remote(node, *args, **kwargs)
        monkeypatch.setattr(s, "SSHRunner", lambda: SimpleNamespace(run=remote))
    elif failure == "power":
        monkeypatch.setattr(vm.VMLease, "_running", lambda _: False)
    elif failure == "live_runner":
        monkeypatch.setattr(s, "_process_token", lambda _: "dead-runner")
    else:
        def unavailable_lock(_path):
            raise RuntimeError("lock held")
        monkeypatch.setattr(s, "RunLock", unavailable_lock)
    with pytest.raises((RuntimeError, TimeoutError)):
        s.reconcile_vm_preserve(f.args)
    assert (f.shared / "owner.json").read_bytes() == before
    assert not (f.run_dir / "vm-preserve-result.json").exists()


@pytest.mark.parametrize("mode", [None, "pre-validator", "candidate-build"])
def test_cli_preserve_failure_mode_is_explicit_and_defaults_safely(fixture, monkeypatch, mode):
    from release import cli
    argv = ["release.py", "reconcile-vm-preserve", fixture.run_dir.name]
    if mode is not None:
        argv.extend(["--failed-stage", mode])
    calls = []
    monkeypatch.setattr(sys, "argv", argv)
    monkeypatch.setattr(s, "reconcile_vm_preserve", lambda args: calls.append(args))
    cli.main()
    assert calls[0].failed_stage == (mode or "pre-validator")


def test_cli_rejects_other_failed_stages_before_reconciliation(fixture, monkeypatch):
    from release import cli
    monkeypatch.setattr(sys, "argv", ["release.py", "reconcile-vm-preserve", fixture.run_dir.name, "--failed-stage", "migration-apply"])
    monkeypatch.setattr(s, "reconcile_vm_preserve", lambda _: pytest.fail("unexpected reconciliation"))
    with pytest.raises(SystemExit) as error:
        cli.main()
    assert error.value.code == 2


AUDIT_PASS = {"vm_preserve_candidate_integration": "pass", "checks": "97", "failure_phase": "none", "fixture_failure_line": "0", "cleanup": "pass"}


@pytest.mark.parametrize("stage,hook,audit_only", [
    ("candidate-build", None, True), ("candidate-build", lambda *_: AUDIT_PASS, False),
    ("candidate-build", "not-callable", True), ("pre-validator", lambda *_: AUDIT_PASS, True),
    ("candidate-build", lambda *_: AUDIT_PASS, 1),
])
def test_invalid_audit_modes_reject_before_locks_or_connection(fixture, monkeypatch, stage, hook, audit_only):
    f = fixture
    f.args.failed_stage = stage
    monkeypatch.setattr(vm, "load_settings", lambda: pytest.fail("unexpected configuration access"))
    monkeypatch.setattr(s, "RunLock", lambda *_: pytest.fail("unexpected lock"))
    with pytest.raises(RuntimeError, match="invalid_audit_mode"):
        s.reconcile_vm_preserve(f.args, audit_fixture=hook, audit_only=audit_only)
    assert not f.calls


def forbid_audit_writes(monkeypatch):
    def forbidden(*_args, **_kwargs):
        pytest.fail("audit must not write formal state or events")
    for name in ("_write_json", "atomic_write", "_event_logger"):
        monkeypatch.setattr(s, name, forbidden)


def test_audit_only_rechecks_under_local_locks_and_never_writes(fixture, monkeypatch):
    f = fixture
    f.args.failed_stage = "candidate-build"
    original = {path: path.read_bytes() for path in [*f.run_dir.iterdir(), f.shared / "owner.json"]}
    forbid_audit_writes(monkeypatch)
    def hook(_ssh, identifier):
        assert identifier == f.run_dir.name
        assert [node for node, _ in f.calls] == ["local_vm", "racknerd"]
        for path in (f.run_dir.parent / ".release.lock", f.shared / "lease.lock"):
            with pytest.raises(RuntimeError):
                with s.RunLock(path):
                    pytest.fail("audit lost its local lock")
        return AUDIT_PASS
    assert s.reconcile_vm_preserve(f.args, audit_fixture=hook, audit_only=True)["status"] == "audited"
    assert [node for node, _ in f.calls] == ["local_vm", "racknerd", "local_vm", "racknerd"]
    assert f.calls[0] == f.calls[2] and f.calls[1] == f.calls[3]
    assert all(path.read_bytes() == content for path, content in original.items())
    assert not (f.run_dir / "vm-preserve-result.json").exists()


@pytest.mark.parametrize("failure", ["exception", "cleanup", "checks", "status"])
def test_unproven_audit_hook_never_writes_or_releases(fixture, monkeypatch, failure):
    f = fixture
    f.args.failed_stage = "candidate-build"
    original = (f.shared / "owner.json").read_bytes()
    forbid_audit_writes(monkeypatch)
    def hook(*_):
        if failure == "exception":
            raise OSError("fixture failed")
        result = dict(AUDIT_PASS)
        result[{"cleanup": "cleanup", "checks": "checks", "status": "vm_preserve_candidate_integration"}[failure]] = "unproven"
        return result
    with pytest.raises((RuntimeError, OSError)):
        s.reconcile_vm_preserve(f.args, audit_fixture=hook, audit_only=True)
    assert (f.shared / "owner.json").read_bytes() == original


@pytest.mark.parametrize("failure", ["vm", "production", "doctor", "power", "live_runner", "local_gate"])
def test_post_audit_preflight_drift_never_writes_or_releases(fixture, monkeypatch, failure):
    f = fixture
    f.args.failed_stage = "candidate-build"
    original = (f.shared / "owner.json").read_bytes()
    forbid_audit_writes(monkeypatch)
    def hook(*_):
        if failure in {"vm", "production"}:
            original_remote = s.SSHRunner().run
            def remote(node, *args, **kwargs):
                if node == ("local_vm" if failure == "vm" else "racknerd"):
                    raise TimeoutError("post-audit remote proof failed")
                return original_remote(node, *args, **kwargs)
            # The same SSH instance was supplied, so modify its bound method.
            _[0].run = remote
        elif failure == "doctor":
            monkeypatch.setattr(s, "ReleaseDoctor", lambda *_: SimpleNamespace(run=lambda *_args, **_kw: {"racknerd_ready": "false"}))
        elif failure == "power":
            monkeypatch.setattr(vm.VMLease, "_running", lambda _: False)
        elif failure == "live_runner":
            monkeypatch.setattr(s, "_process_token", lambda _: "dead-runner")
        else:
            (f.run_dir / "gate").mkdir()
        return AUDIT_PASS
    with pytest.raises((RuntimeError, TimeoutError)):
        s.reconcile_vm_preserve(f.args, audit_fixture=hook, audit_only=True)
    assert (f.shared / "owner.json").read_bytes() == original


@pytest.mark.parametrize("field", ["manifest.json", "runner.json", "state.json", "vm-lifecycle.json", "owner.json"])
def test_post_audit_raw_evidence_drift_is_rejected_even_for_equivalent_json(fixture, monkeypatch, field):
    f = fixture
    f.args.failed_stage = "candidate-build"
    forbid_audit_writes(monkeypatch)
    def hook(*_):
        path = f.shared / field if field == "owner.json" else f.run_dir / field
        path.write_bytes(path.read_bytes() + b"\n")
        return AUDIT_PASS
    with pytest.raises(RuntimeError, match="local_evidence_changed"):
        s.reconcile_vm_preserve(f.args, audit_fixture=hook, audit_only=True)
    assert json.loads((f.shared / "owner.json").read_text())["lease_status"] == "retained"


def test_candidate_fixture_entry_uses_formal_audit_and_existing_ssh_only(monkeypatch):
    import vm_preserve_candidate_build_integration as integration
    monkeypatch.setattr(sys, "argv", ["candidate-fixture", "264-failed"])
    monkeypatch.setattr(integration, "capture_check", lambda: ("generated-check", b"{}"))
    monkeypatch.setattr(integration, "locked_fixture_script", lambda *_: "locked-fixture")
    monkeypatch.setattr(integration, "SSHRunner", lambda: pytest.fail("direct SSH bypass"))
    calls = []
    runner = SimpleNamespace(run=lambda node, script, fields, **_kwargs: (calls.append((node, script, fields)) or SimpleNamespace(values=AUDIT_PASS)))
    def reconcile(args, **kwargs):
        assert args.release_id == "264-failed" and args.failed_stage == "candidate-build"
        assert kwargs["audit_only"] is True
        assert kwargs["audit_fixture"](runner, args.release_id) == AUDIT_PASS
    monkeypatch.setattr(s, "reconcile_vm_preserve", reconcile)
    integration.main()
    assert calls == [("local_vm", "locked-fixture", set(AUDIT_PASS))]


def test_candidate_fixture_creates_and_cleans_only_inside_real_vm_locks():
    import vm_preserve_candidate_build_integration as integration
    check, manifest = integration.capture_check()
    script = integration.locked_fixture_script(check, manifest)
    assert script.index("flock -n 8") < script.index("flock -n 9") < script.index("mktemp -d")
    assert 'test "$(realpath -e -- "$root")" = "$root"' in script
    assert script.index("trap cleanup_fixture EXIT") < script.index('bash "$root/run-fixture.sh"')
    assert script.rstrip().endswith("trap - EXIT")
    assert integration.fixture_shell("placeholder")[1] == 97
