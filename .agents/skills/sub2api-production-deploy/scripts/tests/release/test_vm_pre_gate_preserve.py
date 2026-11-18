from __future__ import annotations

import base64
import json
import shutil
import subprocess
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import supervisor as s
from release import vm_pre_gate_preserve as p
from release import vm_lifecycle as vm


@pytest.fixture
def bash_executable():
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(git_bash) if git_bash.is_file() else shutil.which("bash")
    if bash is None:
        pytest.skip("Bash required to validate generated remote probes")
    return bash


def test_generated_probes_parse_with_bash(bash_executable):
    identifier = "286-abcdef123456-100-deadbeef"
    vm_script = p._vm_probe_script(identifier, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
    production_script = p._production_probe_script(identifier)
    for script in (vm_script, production_script):
        result = subprocess.run([bash_executable, "-n"], input=script.encode(), capture_output=True, timeout=10)
        assert result.returncode == 0 and not result.stderr, result.stderr.decode(errors="replace")
    compile(vm_script.split("\n", 2)[2].removesuffix("VM_PRE_GATE\n"), "vm_pre_gate_probe", "exec")


def test_production_probe_refuses_existing_release_paths(bash_executable, tmp_path, monkeypatch):
    identifier = "286-abcdef123456-100-deadbeef"
    monkeypatch.setattr(p, "PRODUCTION_RELEASE_ROOT", "./releases")
    monkeypatch.setattr(p, "PRODUCTION_STATE_ROOT", "./state")
    script = p._production_probe_script(identifier).replace(
        "/run/lock/sub2api-production-release.lock", "./production.lock", 1,
    )
    # Keep the generated shell assertions and real filesystem redirects; isolate
    # production-only identity, lock metadata, and Docker from the fixture.
    script = """id() { printf '0\\n'; }
stat() { printf '0:0:600:1:8:10\\n'; }
flock() { :; }
realpath() { printf '%s\\n' "$3"; }
docker() { printf 'docker reached\\n' >&2; return 77; }
""" + script
    for root in ("releases", "state"):
        (tmp_path / root).mkdir()

    def probe():
        return subprocess.run([bash_executable], input=script.encode(), cwd=tmp_path, capture_output=True, timeout=10)

    lock = tmp_path / "production.lock"
    missing_lock = probe()
    assert missing_lock.returncode != 0 and b"docker reached" not in missing_lock.stderr and not lock.exists()
    lock.touch()
    clean_paths = probe()
    assert clean_paths.returncode == 77 and b"docker reached" in clean_paths.stderr
    for blocker in (f"releases/{identifier}", f"state/{identifier}", "releases/.active-release",
                    f"state/.{identifier}.staging.abc"):
        path = tmp_path / blocker
        path.mkdir()
        blocked = probe()
        assert blocked.returncode != 0 and b"docker reached" not in blocked.stderr, blocker
        path.rmdir()


@pytest.fixture(params=["286", "287"])
def case(tmp_path, monkeypatch, request):
    profile = request.param
    commit = profile + "a" * 37
    identifier = f"{profile}-{commit[:12]}-100-deadbeef"
    root, shared = tmp_path / "releases", tmp_path / "shared"
    run = root / identifier
    run.mkdir(parents=True)
    shared.mkdir()
    boot = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
    identity, digest = "vm-id", "b" * 64
    lease = dict(schema=1, vm_identity=identity, source_commit=commit, release_id=identifier, pid=42,
                 process_token="old-worker", lease_token="old-lease", initially_running=False,
                 vm_started_by_release=True, lease_status="retained", vm_cleanup_status="retained_failure",
                 vm_power_status="running", vm_boot_id=boot, updated_at=300)
    originals = {
        "manifest.json": dict(schema=2, commit_sha=commit, profile=profile, release_id=identifier,
                              runner_sha256=digest, production_current_image_id=None,
                              production_snapshot_sha256=None),
        "runner.json": dict(schema=1, release_id=identifier, commit=commit, profile=profile, runner_sha256=digest,
                            pid=42, process_token="old-worker", status="failed", exit_code=1,
                            started_at=100, finished_at=200, terminal_receipt_schema=1),
        "state.json": dict(schema=1, release_id=identifier, status="not_started", stage="init", history=[]),
        "vm-lifecycle.json": lease,
    }
    for name, value in originals.items():
        (run / name).write_bytes(p.canonical_json(value))
    owner = shared / "owner.json"
    owner.write_bytes((run / "vm-lifecycle.json").read_bytes())
    settings = SimpleNamespace(identity=identity)
    monkeypatch.setattr(s, "RUN_ROOT", root)
    monkeypatch.setattr(s, "_process_token", lambda _: None)
    monkeypatch.setattr(vm, "load_settings", lambda: settings)
    monkeypatch.setattr(vm, "_shared_dir", lambda _: shared)
    monkeypatch.setattr(vm.VMLease, "_running", lambda _: True)
    monkeypatch.setattr(p, "_helper_identity", lambda: {"commit_sha": "e" * 40, "runner_sha256": "f" * 64})
    image = "sha256:" + "c" * 64
    snapshot = {"current_image_id": image, "production_current_commit_sha": "0" * 40,
                "schema_migrations": [{"filename": "001_init.sql", "checksum": "d" * 64}]}
    encode = lambda: base64.b64encode(p.canonical_json(snapshot)).decode("ascii")
    calls = []
    results = {
        "local_vm": dict(vm_pre_gate_preserve="verified", vm_boot_id=boot, vm_process_count="0",
                         vm_candidate_absent="true", vm_probe_resources_absent="true", vm_health="healthy"),
        "production": dict(production_pre_gate_preserve="verified", production_claim_absent="true",
                           production_state_absent="true", production_candidate_absent="true",
                           production_health="healthy", production_image_id=image, production_route_sha256="a" * 64,
                           production_active_slot_sha256="b" * 64, production_nginx="active",
                           production_backup_timer="enabled", snapshot_b64=encode()),
    }

    def remote(node, script, fields, **_kwargs):
        calls.append((node, script))
        return SimpleNamespace(values=results[node].copy())

    def doctor(_profile, _commit, **_kwargs):
        def check(nodes, **_options):
            if nodes == ("local",):
                return {"local_ready": "true", "host_keys_ready": "true"}
            return {"racknerd_ready": "true", "production_current_image_id": image, "production_snapshot_b64": encode()}
        return SimpleNamespace(run=check)

    def ssh(*, require_environment):
        assert require_environment
        return SimpleNamespace(run=remote, assert_configuration_current=lambda: None,
                               deployment_environment=SimpleNamespace(direct_route={"target_node": "production"}))

    monkeypatch.setattr(p, "SSHRunner", ssh)
    monkeypatch.setattr(p, "ReleaseDoctor", doctor)
    return SimpleNamespace(args=SimpleNamespace(release_id=identifier), run=run, owner=owner,
                           calls=calls, originals=originals, results=results, snapshot=snapshot)


def test_releases_owner_last_without_rewriting_failure(case, monkeypatch):
    before = {name: (case.run / name).read_bytes() for name in case.originals}
    writes = []
    atomic_write = p.atomic_write

    def record(path, *args):
        writes.append(path.name)
        return atomic_write(path, *args)

    monkeypatch.setattr(p, "atomic_write", record)
    report = p.reconcile(case.args)
    assert writes == [p.CHECKS_NAME, p.RESULT_NAME, "owner.json"]
    assert report["application_deployed"] is False and report["runner_status"] == "failed"
    assert [node for node, _ in case.calls] == ["local_vm", "production"] * 2
    checks = json.loads((case.run / p.CHECKS_NAME).read_bytes())
    assert checks["remote"]["production"]["migration_snapshot_sha256"] == p.snapshot_sha256(case.snapshot)
    assert all((case.run / name).read_bytes() == raw for name, raw in before.items())
    assert json.loads(case.owner.read_bytes())["lease_status"] == "released"
    writes.clear()
    case.args.verify_only = True
    assert p.reconcile(case.args) == report
    assert writes == []


@pytest.mark.parametrize("target", [p.CHECKS_NAME, p.RESULT_NAME, "owner.json"])
def test_write_failure_retains_owner(case, monkeypatch, target):
    initial = case.owner.read_bytes()
    atomic_write = p.atomic_write

    def fail(path, *args):
        if path.name == target:
            raise OSError("injected")
        return atomic_write(path, *args)

    monkeypatch.setattr(p, "atomic_write", fail)
    with pytest.raises(OSError, match="injected"):
        p.reconcile(case.args)
    assert case.owner.read_bytes() == initial
    monkeypatch.setattr(p, "atomic_write", atomic_write)
    assert p.reconcile(case.args)["lease_status"] == "released"


def test_committed_owner_cleanup_error_requires_readonly_replay(case, monkeypatch):
    atomic_write = p.atomic_write

    def write_then_fail(path, *args):
        atomic_write(path, *args)
        if path == case.owner:
            raise OSError("cleanup failed after replacement")

    monkeypatch.setattr(p, "atomic_write", write_then_fail)
    with pytest.raises(OSError, match="cleanup failed"):
        p.reconcile(case.args)
    assert vm._read_state(case.owner)["lease_status"] == "released"
    case.args.verify_only = True
    monkeypatch.setattr(p, "atomic_write", lambda *_: pytest.fail("verification cannot write"))
    assert p.reconcile(case.args)["lease_status"] == "released"


@pytest.mark.parametrize("fault", ["result", "worker", "boot", "route", "snapshot", "owner", "verify"])
def test_unsafe_state_is_refused(case, monkeypatch, fault):
    before = case.owner.read_bytes()
    if fault == "result":
        (case.run / "production-result.json").write_text("{}")
    elif fault == "worker":
        monkeypatch.setattr(s, "_process_token", lambda _: "old-worker")
    elif fault == "boot":
        case.results["local_vm"]["vm_boot_id"] = "changed"
    elif fault == "route":
        case.results["production"]["production_route_sha256"] = "unproven"
    elif fault == "snapshot":
        case.snapshot["schema_migrations"].clear()
    elif fault == "owner":
        case.owner.write_bytes(before + b"\n")
        before = case.owner.read_bytes()
    else:
        case.args.verify_only = True
    with pytest.raises(RuntimeError, match="vm_pre_gate_"):
        p.reconcile(case.args)
    assert case.owner.read_bytes() == before
    assert not (case.run / p.RESULT_NAME).exists()


@pytest.mark.parametrize(("command", "verify"), [
    ("reconcile-vm-pre-gate-preserve", False), ("verify-vm-pre-gate-preserve", True),
])
def test_cli_routes_explicit_verification(monkeypatch, command, verify):
    from release import cli

    seen = []
    identifier = "286-aaaaaaaaaaaa-100-deadbeef"
    monkeypatch.setattr("sys.argv", ["release.py", command, identifier])
    monkeypatch.setattr(cli, "pin_deployment_environment", lambda *_: None)
    monkeypatch.setattr(p, "reconcile", lambda args: seen.append(args))
    cli.main()
    assert len(seen) == 1 and seen[0].release_id == identifier
    assert getattr(seen[0], "verify_only", False) is verify
