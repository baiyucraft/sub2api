from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from release import atomic, plugin_supervisor, supervisor, terminal_receipt as receipt
from test_replaceable_state_readers import app_worker_case, atomic_failure, plugin_worker_case


def unmanaged_lifecycle(root):
    return {"vm_power_status": "unmanaged", "vm_started_by_release": False, "vm_cleanup_status": "not_applicable"}


def terminal_case(tmp_path, monkeypatch, *, plugin=False, legacy=False, status="verified"):
    module = plugin_supervisor if plugin else supervisor
    monkeypatch.setattr(module, "PLUGIN_RUN_ROOT" if plugin else "RUN_ROOT", tmp_path)
    monkeypatch.setattr(module, "lifecycle_view", unmanaged_lifecycle)
    monkeypatch.setattr(module, "_process_token", lambda pid: None)
    run_dir = tmp_path / ("plugin-test" if plugin else "274-test")
    run_dir.mkdir()
    manifest = {"release_id": run_dir.name, "profile": "274", "commit_sha": "a" * 40}
    (run_dir / "manifest.json").write_bytes(atomic.canonical_json(manifest) + b"\n")
    runner = {"schema": 1, "release_id": run_dir.name, "status": status, "exit_code": 0,
              "pid": 123, "process_token": "test-worker"}
    if not legacy:
        runner["terminal_receipt_schema"] = 1
    raw = atomic.canonical_json(runner) + b"\n"
    (run_dir / "runner.json").write_bytes(raw)
    if plugin:
        (run_dir / "state.json").write_bytes(atomic.canonical_json({"status": "verified", "stage": "verified"}) + b"\n")
    return module, run_dir, runner, raw


@pytest.mark.parametrize("plugin", [False, True])
def test_new_terminal_requires_receipt_for_status_wait_and_public_verify(tmp_path, monkeypatch, plugin):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch, plugin=plugin)
    view = module.status_view(root.name)
    assert view["runner_terminal_receipt"] == "terminal_receipt_missing"
    assert view["runner_status"] == "blocked_reconciliation" and view["runner_exit"] == 1
    if plugin:
        assert module.terminal_exit(view) == 2
        assert module.wait(argparse.Namespace(release_id=root.name, timeout=1)) == 2
        monkeypatch.setattr(module, "_verified_result", lambda root: {"status": "verified"})
        with pytest.raises(RuntimeError, match="terminal_receipt_missing"):
            module.verify_result(argparse.Namespace(release_id=root.name))
    else:
        with pytest.raises(SystemExit) as error:
            module.wait(argparse.Namespace(release_id=root.name, timeout=1))
        assert error.value.code == 1
        monkeypatch.setattr(module, "_verified_evidence_view", lambda identifier: {"status": "verified"})
        with pytest.raises(RuntimeError, match="terminal_receipt_missing"):
            module.verified_result_view(root.name)
    receipt.publish_terminal_receipt(root, runner, raw)
    assert module.status_view(root.name)["runner_terminal_receipt"] == "verified"
    if plugin:
        assert module.wait(argparse.Namespace(release_id=root.name, timeout=1)) == 0
        module.verify_result(argparse.Namespace(release_id=root.name))
    else:
        module.wait(argparse.Namespace(release_id=root.name, timeout=1))
        assert module.verified_result_view(root.name)["status"] == "verified"


@pytest.mark.parametrize("plugin", [False, True])
def test_live_terminal_worker_waits_for_receipt_but_dead_worker_fails_closed(tmp_path, monkeypatch, plugin):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch, plugin=plugin)
    monkeypatch.setattr(module, "_process_token", lambda pid: "test-worker")
    live = module.status_view(root.name)
    assert live["runner_alive"] is True and live["runner_status"] == "running"
    if plugin:
        assert module.terminal_exit(live) is None
    monkeypatch.setattr(module, "_process_token", lambda pid: None)
    dead = module.status_view(root.name)
    assert dead["runner_alive"] is False and dead["runner_status"] == "blocked_reconciliation"


@pytest.mark.parametrize("schema", [None, 0, 2, True, "1"])
def test_unknown_receipt_schema_never_falls_back_to_historical_rules(tmp_path, monkeypatch, schema):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    runner["terminal_receipt_schema"] = schema
    raw = atomic.canonical_json(runner) + b"\n"
    atomic.atomic_write(root / "runner.json", raw)
    assert module.status_view(root.name)["runner_terminal_receipt"] == "terminal_receipt_schema_invalid"
    with pytest.raises(RuntimeError, match="schema_invalid"):
        receipt.publish_terminal_receipt(root, runner, raw)
    assert not (root / receipt.RECEIPT_NAME).exists()


@pytest.mark.parametrize("plugin", [False, True])
def test_historical_runner_has_explicit_legacy_boundary_and_is_never_backfilled(tmp_path, monkeypatch, plugin):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch, plugin=plugin, legacy=True)
    receipt.publish_terminal_receipt(root, runner, raw)
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert module.status_view(root.name)["runner_terminal_receipt"] == "legacy_schema_1"
    assert module.status_view(root.name)["runner_status"] == "verified"


@pytest.mark.parametrize("mutation", ["runner_bytes", "receipt_hash", "duplicate_field", "foreign_release", "hardlink"])
def test_receipt_is_bound_to_exact_runner_snapshot_and_safe_single_link_file(tmp_path, monkeypatch, mutation):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    receipt.publish_terminal_receipt(root, runner, raw)
    target = root / receipt.RECEIPT_NAME
    if mutation == "runner_bytes":
        atomic.atomic_write(root / "runner.json", raw + b" ")
    elif mutation == "hardlink":
        os.link(target, root / "receipt-alias")
    else:
        doc = json.loads(target.read_bytes())
        if mutation == "receipt_hash":
            doc["runner_sha256"] = "0" * 64
        elif mutation == "foreign_release":
            doc["release_id"] = "another-release"
        payload = atomic.canonical_json(doc) + b"\n"
        if mutation == "duplicate_field":
            payload = payload.replace(b'{"', b'{"schema":1,"', 1)
        target.write_bytes(payload)
    view = module.status_view(root.name)
    assert view["runner_status"] == "blocked_reconciliation"
    assert view["runner_terminal_receipt"] in {"terminal_receipt_mismatch", "terminal_receipt_invalid"}


def test_receipt_fsync_failure_keeps_only_staging_file_and_never_publishes(tmp_path, monkeypatch):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    primary = OSError("private fsync failure")
    with mock.patch.object(receipt.os, "fsync", side_effect=primary), pytest.raises(OSError) as caught:
        receipt.publish_terminal_receipt(root, runner, raw)
    assert caught.value is primary
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert len(list(root.glob(".runner-terminal-receipt.*"))) == 1
    assert module.status_view(root.name)["runner_status"] == "blocked_reconciliation"


def test_receipt_is_exclusive_and_never_rewrites_existing_witness(tmp_path, monkeypatch):
    _, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    receipt.publish_terminal_receipt(root, runner, raw)
    first = (root / receipt.RECEIPT_NAME).read_bytes()
    with pytest.raises(OSError):
        receipt.publish_terminal_receipt(root, runner, raw)
    assert (root / receipt.RECEIPT_NAME).read_bytes() == first


def test_receipt_fdopen_failure_closes_owned_descriptor_once_and_preserves_staging(tmp_path, monkeypatch):
    _, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    primary = OSError("private fdopen failure")
    with (
        mock.patch.object(receipt.os, "fdopen", side_effect=primary),
        mock.patch.object(receipt.os, "close", wraps=os.close) as close,
        pytest.raises(OSError) as caught,
    ):
        receipt.publish_terminal_receipt(root, runner, raw)
    assert caught.value is primary and close.call_count == 1
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert len(list(root.glob(".runner-terminal-receipt.*"))) == 1


def test_app_actual_terminal_publish_then_error_is_rejected_by_all_public_consumers(tmp_path, monkeypatch):
    args, root, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    monkeypatch.setattr(supervisor, "lifecycle_view", unmanaged_lifecycle)
    primary = atomic_failure("verify_backup", True)
    update = supervisor._update_runner

    def publish(path, **changes):
        result = update(path, **changes)
        if changes["status"] == "verified":
            raise primary
        return result

    monkeypatch.setattr(supervisor, "_update_runner", publish)
    with pytest.raises(PermissionError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    assert json.loads(atomic.read_replaceable_bytes(root / "runner.json"))["status"] == "verified"
    assert supervisor.status_view(root.name)["runner_status"] == "blocked_reconciliation"
    with pytest.raises(SystemExit):
        supervisor.wait(argparse.Namespace(release_id=root.name, timeout=1))
    with pytest.raises(RuntimeError, match="terminal_receipt_missing"):
        supervisor.verified_result_view(root.name)
    assert not any(call.kwargs.get("event") == "worker_finished" for call in logger.emit.call_args_list)


def test_app_finished_log_failure_prevents_success_receipt(tmp_path, monkeypatch):
    args, root, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    monkeypatch.setattr(supervisor, "lifecycle_view", unmanaged_lifecycle)
    primary = OSError("private log failure")

    def emit(**kwargs):
        if kwargs["event"] == "worker_finished":
            raise primary

    logger.emit.side_effect = emit
    with pytest.raises(OSError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert supervisor.status_view(root.name)["runner_status"] == "blocked_reconciliation"


@pytest.mark.parametrize("failure", ["terminal_commit", "lock_exit", "stdout_flush"])
def test_plugin_standalone_authorize_partial_success_never_gains_receipt_or_rewrites_terminal(tmp_path, monkeypatch, failure):
    args, root = plugin_worker_case(tmp_path, monkeypatch)
    monkeypatch.setattr(plugin_supervisor, "lifecycle_view", lambda root: {})
    monkeypatch.setattr(plugin_supervisor, "_process_token", lambda pid: None)
    old_runner = json.loads((root / "runner.json").read_bytes())
    old_runner["terminal_receipt_schema"] = 1
    (root / "runner.json").write_bytes(atomic.canonical_json(old_runner) + b"\n")
    fake_state = mock.Mock(value={"status": "running", "stage": "awaiting_production_authorization"})
    primary = atomic_failure("cleanup_backup", True)
    update = plugin_supervisor._update_runner
    attempts = []

    def publish(path, **changes):
        attempts.append(changes["status"])
        result = update(path, **changes)
        if changes["status"] == "verified" and failure == "terminal_commit":
            raise primary
        return result

    def authorize(*args):
        fake_state.value.update(status="verified", stage="verified")

    lock = mock.MagicMock()
    if failure == "lock_exit":
        lock.__exit__.side_effect = primary
    monkeypatch.setattr(plugin_supervisor, "RunLock", lambda *args: lock)
    monkeypatch.setattr(plugin_supervisor, "_update_runner", publish)
    if failure == "stdout_flush":
        buffered_output = mock.Mock()
        buffered_output.write.return_value = 1
        buffered_output.flush.side_effect = primary
        monkeypatch.setattr(sys, "stdout", buffered_output)
    with (
        mock.patch.object(plugin_supervisor.PluginRunState, "load", return_value=fake_state),
        mock.patch.object(plugin_supervisor, "vm_guard", return_value=mock.MagicMock()),
        mock.patch.object(plugin_supervisor, "_identity"),
        mock.patch.object(plugin_supervisor, "PluginAPIClient"),
        mock.patch.object(plugin_supervisor, "_authorize_stage", side_effect=authorize),
        mock.patch.object(plugin_supervisor, "_verified_result", return_value={"status": "verified"}),
        pytest.raises(PermissionError) as caught,
    ):
        plugin_supervisor.authorize(args)
    assert caught.value is primary and attempts == ["verified"]
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert plugin_supervisor.status_view(root.name)["runner_status"] == "blocked_reconciliation"


def test_app_worker_pending_output_flush_failure_prevents_receipt(tmp_path, monkeypatch):
    args, root, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    monkeypatch.setattr(supervisor, "lifecycle_view", unmanaged_lifecycle)
    primary = BrokenPipeError("fixture buffered stdout flush")
    buffered_output = mock.Mock()
    buffered_output.flush.side_effect = primary
    with mock.patch.object(sys, "stdout", buffered_output), pytest.raises(BrokenPipeError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    assert not (root / receipt.RECEIPT_NAME).exists()
    assert supervisor.status_view(root.name)["runner_status"] == "blocked_reconciliation"


def test_recovered_public_result_requires_new_schema_receipt_before_gate_calls(tmp_path, monkeypatch):
    _, root, runner, raw = terminal_case(tmp_path, monkeypatch, status="recovered")
    for name in ("state.json", "release-state.json", "gate/production-result.json"):
        path = root / name
        path.parent.mkdir(exist_ok=True)
        path.write_bytes(b"{}\n")
    with pytest.raises(RuntimeError, match="terminal_receipt_missing"):
        supervisor.recovered_result_view(root.name)


@pytest.mark.skipif(os.name == "nt", reason="POSIX exclusive hard-link publication branch")
def test_posix_receipt_alias_cleanup_failure_is_invalid_and_not_a_success(tmp_path, monkeypatch):
    module, root, runner, raw = terminal_case(tmp_path, monkeypatch)
    unlink = Path.unlink

    def fail_alias(path, *args, **kwargs):
        if path.name.startswith(".runner-terminal-receipt."):
            raise OSError("alias cleanup failed")
        return unlink(path, *args, **kwargs)

    with mock.patch.object(Path, "unlink", fail_alias), pytest.raises(OSError):
        receipt.publish_terminal_receipt(root, runner, raw)
    assert (root / receipt.RECEIPT_NAME).stat().st_nlink == 2
    assert module.status_view(root.name)["runner_terminal_receipt"] == "terminal_receipt_invalid"
