from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from contextlib import contextmanager
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from release import atomic, cli, finalization, manifest, plugin_state, plugin_supervisor, state, supervisor
from release import vm_failed_preserve, vm_lifecycle, vm_validate
from release.worker_errors import failure_details


def document(marker: str) -> dict:
    return {
        "schema": 1, "release_id": "274-test", "stage": "initialized", "status": "running",
        "generation": 1, "history": [], "marker": marker,
    }


def encoded(value: dict) -> bytes:
    return atomic.canonical_json(value) + b"\n"


@contextmanager
def replace_after_open(monkeypatch, target: Path, replacement: bytes, active=lambda: True):
    """Deterministic native replacement while the real old read handle is open."""
    regular_reader = atomic._regular_reader
    replaced = []

    @contextmanager
    def interleaved(path, max_bytes):
        with regular_reader(path, max_bytes) as opened:
            if Path(path) == target and not replaced and active():
                replaced.append(True)
                atomic.atomic_write(target, replacement)
            yield opened

    with monkeypatch.context() as patch:
        patch.setattr(atomic, "_regular_reader", interleaved)
        yield replaced


_SUPERVISOR_TARGETS = [
    "runner.json", "state.json", "release-state.json", "manifest.json", "gate/production-result.json",
    "build-dependencies.json", "vm-build-failure.json",
]


@pytest.mark.parametrize("name", _SUPERVISOR_TARGETS)
def test_app_control_reader_keeps_complete_open_snapshot(tmp_path, monkeypatch, name):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    path = tmp_path / "274-test" / name
    path.parent.mkdir(parents=True)
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        value, raw = supervisor._json_snapshot(path, required=True)
    assert replaced and value == old and raw == encoded(old)
    assert supervisor._read_json(path, required=True) == new


@pytest.mark.parametrize("name", ["runner.json", "state.json"])
def test_plugin_control_reader_shares_delete(tmp_path, monkeypatch, name):
    path = tmp_path / name
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        value = plugin_supervisor._read_json(path, required=True)
    assert replaced and value == old
    assert plugin_supervisor._read_json(path, required=True) == new


@pytest.mark.parametrize("name", ["vm-lifecycle.json", "owner.json", "vm-finalization.json"])
def test_vm_owner_and_lifecycle_json_bytes_share_one_snapshot(tmp_path, monkeypatch, name):
    path = tmp_path / name
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        value, raw = vm_lifecycle._state_snapshot(path, finalization=name == "vm-finalization.json")
    assert replaced and value == old and raw == encoded(old)
    assert vm_lifecycle._read_state(path, finalization=name == "vm-finalization.json") == new


def test_vm_hash_validates_and_hashes_same_handle(tmp_path, monkeypatch):
    path = tmp_path / "runner.json"
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        digest = vm_lifecycle._file_sha256(path)
    assert replaced and digest == hashlib.sha256(encoded(old)).hexdigest()
    assert vm_lifecycle._file_sha256(path) == hashlib.sha256(encoded(new)).hexdigest()


@pytest.mark.parametrize("name", [".retention-plan.json", "manifest.json", "runner.json", "release-state.json", "production-result.json"])
def test_retention_reads_shared_delete_snapshot(tmp_path, monkeypatch, name):
    path = tmp_path / name
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        value = cli._retention_read_json(path)
    assert replaced and value == old
    assert cli._retention_read_json(path) == new


@pytest.mark.parametrize("loader", [state.RunState.load, plugin_state.PluginRunState.load])
def test_state_loaders_use_complete_shared_snapshot(tmp_path, monkeypatch, loader):
    path = tmp_path / "state.json"
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        loaded = loader(path)
    assert replaced and loaded.value == old
    assert loader(path).value == new


@pytest.mark.parametrize("loader", [state.RunState.load, plugin_state.PluginRunState.load])
def test_old_uncapped_state_reader_does_not_acquire_default_four_megabyte_limit(tmp_path, loader):
    path = tmp_path / "state.json"
    value = {**document("large"), "evidence": "x" * (4 * 1024 * 1024 + 1)}
    path.write_bytes(encoded(value))
    assert loader(path).value == value


def test_once_manifest_reader_uses_old_complete_snapshot_without_republishing(tmp_path, monkeypatch):
    path = tmp_path / "manifest.json"
    old, new = document("old"), document("new-and-larger")
    path.write_bytes(encoded(old))
    with replace_after_open(monkeypatch, path, encoded(new)) as replaced:
        manifest.write_manifest_once(path, old)
    assert replaced and atomic.read_replaceable_bytes(path) == encoded(new)
    with pytest.raises(RuntimeError, match="different content"):
        manifest.write_manifest_once(path, old)


@pytest.mark.parametrize("name", ["manifest.json", "runner.json", "state.json", "release-state.json", "vm-lifecycle.json", "production-result.json", "vm-finalization.json"])
def test_finalization_mutable_hash_uses_shared_snapshot(tmp_path, monkeypatch, name):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    path = tmp_path / "274-test" / name
    path.parent.mkdir()
    old, new = encoded(document("old")), encoded(document("new-and-larger"))
    path.write_bytes(old)
    with replace_after_open(monkeypatch, path, new) as replaced:
        digest = finalization._hash_file(path)
    assert replaced and digest == hashlib.sha256(old).hexdigest()
    assert finalization._hash_file(path) == hashlib.sha256(new).hexdigest()


@pytest.mark.parametrize("name", ["candidate.tar.gz", "gate.json", "gate.sig"])
def test_finalization_immutable_binary_hash_keeps_streaming_path(tmp_path, monkeypatch, name):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    path = tmp_path / "274-test" / name
    path.parent.mkdir()
    payload = b"immutable binary\x00\xff" * 100
    path.write_bytes(payload)
    with mock.patch.object(finalization, "hash_replaceable_file", side_effect=AssertionError("immutable reader migrated")):
        assert finalization._hash_file(path) == hashlib.sha256(payload).hexdigest()


def test_failed_preserve_hash_uses_shared_snapshot(tmp_path, monkeypatch):
    path = tmp_path / "runner.json"
    old, new = encoded(document("old")), encoded(document("new-and-larger"))
    path.write_bytes(old)
    expected = {path.name: hashlib.sha256(old).hexdigest()}
    monkeypatch.setattr(vm_failed_preserve, "ORIGINAL_SHA256", expected)
    with replace_after_open(monkeypatch, path, new) as replaced:
        assert vm_failed_preserve._hash_originals(tmp_path) == expected
    assert replaced
    with pytest.raises(RuntimeError, match="original_changed"):
        vm_failed_preserve._hash_originals(tmp_path)


@pytest.mark.parametrize("consumer", ["supervisor", "vm", "retention", "plugin"])
def test_explicit_json_limits_still_reject_oversized_complete_files(tmp_path, monkeypatch, consumer):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    path = tmp_path / "state.json"
    limit = 16384 if consumer == "vm" else 2 * 1024 * 1024
    path.write_bytes(b" " * (limit + 1))
    if consumer == "retention":
        assert cli._retention_read_json(path) == {}
    else:
        call = {"supervisor": supervisor._read_json, "vm": vm_lifecycle._read_state,
                "plugin": plugin_supervisor._read_json}[consumer]
        with pytest.raises(RuntimeError):
            call(path)


def test_concurrent_growth_errors_keep_consumer_failure_contract(tmp_path, monkeypatch):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    path = tmp_path / "runner.json"
    path.write_bytes(encoded(document("old")))
    with mock.patch.object(supervisor, "read_replaceable_bytes", side_effect=ValueError("growth")):
        with pytest.raises(RuntimeError, match="invalid state file"):
            supervisor._read_json(path)
    with mock.patch.object(cli, "read_replaceable_text", side_effect=ValueError("growth")):
        assert cli._retention_read_json(path) == {}


def app_worker_case(tmp_path, monkeypatch):
    monkeypatch.setattr(supervisor, "RUN_ROOT", tmp_path)
    run_dir = tmp_path / "274-test"
    run_dir.mkdir()
    args = argparse.Namespace(release_id=run_dir.name, profile="274", commit="a" * 40, deployment_mode="downtime")
    manifest_value = {"release_id": run_dir.name, "profile": args.profile, "commit_sha": args.commit,
                      "deployment_mode": args.deployment_mode}
    (run_dir / "manifest.json").write_bytes(encoded(manifest_value))
    (run_dir / "runner.json").write_bytes(encoded({"release_id": run_dir.name, "status": "starting",
                                                   "pid": os.getpid(), "process_token": "test-worker"}))
    logger = mock.Mock()
    monkeypatch.setattr(supervisor, "_event_logger", lambda *args: logger)
    monkeypatch.setattr(supervisor, "RunLock", lambda *args: mock.MagicMock())
    lease = mock.MagicMock()
    lease.enabled = False
    lease.__enter__.return_value = lease
    lease_factory = mock.Mock(return_value=lease)
    monkeypatch.setattr(supervisor, "VMLease", lease_factory)
    deploy = mock.Mock()
    monkeypatch.setattr(cli, "deploy", deploy)
    monkeypatch.setattr(supervisor, "_process_token", lambda pid: None)
    return args, run_dir, logger, lease_factory, deploy


def atomic_failure(stage="replace_existing", committed=None):
    error = PermissionError("private native error must not be logged")
    error.atomic_write_stage = stage
    error.atomic_write_committed = committed
    error.atomic_write_state_uncertain = committed is None
    error.atomic_write_objects = {"target": True, "temporary": None, "backup": False,
                                  "secret_path": "never log this"}
    return error


def test_app_running_commit_failure_prevents_vm_and_deploy_and_preserves_primary(tmp_path, monkeypatch):
    args, run_dir, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    primary, secondary = atomic_failure(), atomic_failure("cleanup_backup", True)
    update = supervisor._update_runner

    def publish(path, **changes):
        if changes["status"] == "running":
            raise primary
        if changes["status"] == "failed":
            raise secondary
        return update(path, **changes)

    monkeypatch.setattr(supervisor, "_update_runner", publish)
    with pytest.raises(PermissionError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    lease.assert_not_called()
    deploy.assert_not_called()
    assert primary.release_secondary_errors[-1]["atomic_write_committed"] is True
    assert primary.release_secondary_errors[-1]["operation"] == "terminal_runner_commit"
    assert "private native error" not in repr(logger.mock_calls)
    assert "secret_path" not in repr(logger.mock_calls)


def test_app_failure_state_read_and_logging_failure_do_not_mask_running_commit_error(tmp_path, monkeypatch):
    args, _, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    primary = atomic_failure()
    update, read = supervisor._update_runner, supervisor._read_json

    def publish(path, **changes):
        if changes["status"] == "running":
            raise primary
        return update(path, **changes)

    def read_state(path, *args, **kwargs):
        if path.name == "release-state.json":
            raise ValueError("secondary read failure")
        return read(path, *args, **kwargs)

    monkeypatch.setattr(supervisor, "_update_runner", publish)
    monkeypatch.setattr(supervisor, "_read_json", read_state)
    logger.emit.side_effect = lambda **kw: (_ for _ in ()).throw(OSError("secondary log failure")) if kw["event"] in {"worker_failed", "worker_finished"} else None
    with pytest.raises(PermissionError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    assert {entry["operation"] for entry in primary.release_secondary_errors} == {
        "read_failure_state", "log_worker_failure", "log_worker_finished",
    }
    lease.assert_not_called()
    deploy.assert_not_called()


def test_app_terminal_commit_failure_propagates_and_never_logs_verified_finished(tmp_path, monkeypatch):
    args, run_dir, logger, lease, deploy = app_worker_case(tmp_path, monkeypatch)
    primary = atomic_failure("cleanup_backup", True)
    update = supervisor._update_runner

    def publish(path, **changes):
        if changes["status"] == "verified":
            raise primary
        return update(path, **changes)

    monkeypatch.setattr(supervisor, "_update_runner", publish)
    with pytest.raises(PermissionError) as caught:
        supervisor.worker(args)
    assert caught.value is primary
    deploy.assert_called_once()
    assert not any(call.kwargs.get("event") == "worker_finished" for call in logger.emit.call_args_list)
    failure = [call.kwargs for call in logger.emit.call_args_list if call.kwargs.get("event") == "worker_terminal_commit_failed"]
    assert failure[0]["exit_code"] == 1 and failure[0]["details"]["atomic_write_committed"] is True
    assert json.loads(atomic.read_replaceable_bytes(run_dir / "runner.json"))["status"] == "running"


def plugin_worker_case(tmp_path, monkeypatch):
    monkeypatch.setattr(plugin_supervisor, "PLUGIN_RUN_ROOT", tmp_path)
    monkeypatch.setattr(plugin_supervisor, "RUN_ROOT", tmp_path / "host")
    run_dir = tmp_path / "plugin-test"
    run_dir.mkdir()
    args = argparse.Namespace(release_id=run_dir.name, commit="a" * 40)
    (run_dir / "manifest.json").write_bytes(encoded({"release_id": run_dir.name, "source_commit": args.commit}))
    (run_dir / "runner.json").write_bytes(encoded({"release_id": run_dir.name, "status": "starting"}))
    plugin_state.PluginRunState.create(run_dir / "state.json", run_dir.name, "plugin", args.commit)
    monkeypatch.setattr(plugin_supervisor, "RunLock", lambda *args: mock.MagicMock())
    return args, run_dir


def test_plugin_running_commit_failure_blocks_package_api_vm_and_keeps_primary(tmp_path, monkeypatch):
    args, run_dir = plugin_worker_case(tmp_path, monkeypatch)
    primary, secondary = atomic_failure(), atomic_failure("verify_backup", True)
    update = plugin_supervisor._update_runner

    def publish(path, **changes):
        if changes["status"] == "running":
            raise primary
        if changes["status"] == "failed":
            raise secondary
        return update(path, **changes)

    monkeypatch.setattr(plugin_supervisor, "_update_runner", publish)
    with (
        mock.patch.object(plugin_supervisor, "build_packages") as build,
        mock.patch.object(plugin_supervisor, "PluginAPIClient") as api,
        mock.patch.object(plugin_supervisor, "VMLease") as vm,
        mock.patch.object(plugin_supervisor.PluginRunState, "load", side_effect=ValueError("secondary state failure")),
        pytest.raises(PermissionError) as caught,
    ):
        plugin_supervisor.worker(args)
    assert caught.value is primary
    build.assert_not_called()
    api.assert_not_called()
    vm.assert_not_called()
    assert {entry["operation"] for entry in primary.release_secondary_errors} == {
        "commit_plugin_failure_state", "terminal_runner_commit",
    }


def test_plugin_terminal_commit_failure_is_not_success(tmp_path, monkeypatch):
    args, run_dir = plugin_worker_case(tmp_path, monkeypatch)
    primary = atomic_failure("verify_target", True)
    update = plugin_supervisor._update_runner
    fake_state = mock.Mock(value={"status": "running", "stage": "initialized"})
    identity = mock.Mock(version="1.0")

    def publish(path, **changes):
        if changes["status"] == "verified":
            raise primary
        return update(path, **changes)

    monkeypatch.setattr(plugin_supervisor, "_update_runner", publish)
    lease = mock.MagicMock()
    lease.enabled = False
    lease.__enter__.return_value = lease
    with (
        mock.patch.object(plugin_supervisor, "build_packages", return_value={"amd64": identity}),
        mock.patch.object(plugin_supervisor.PluginRunState, "load", return_value=fake_state),
        mock.patch.object(plugin_supervisor, "package_manifest", return_value={"version": "1.0", "signature_key_id": "test", "arches": {}}),
        mock.patch.object(plugin_supervisor, "PluginAPIClient"),
        mock.patch.object(plugin_supervisor, "VMLease", return_value=lease),
        mock.patch.object(plugin_supervisor, "authorize"),
        pytest.raises(PermissionError) as caught,
    ):
        plugin_supervisor.worker(args)
    assert caught.value is primary
    assert json.loads(atomic.read_replaceable_bytes(run_dir / "runner.json"))["status"] == "running"


def test_failure_details_never_include_native_message_or_unapproved_object_fields():
    details = failure_details(atomic_failure())
    assert details == {
        "error_type": "PermissionError", "atomic_write_stage": "replace_existing",
        "atomic_write_committed": None, "atomic_write_state_uncertain": True,
        "atomic_write_objects": {"target": True, "temporary": None, "backup": False},
    }


@pytest.mark.parametrize("drift_before_open", [False, True])
def test_vm_upload_uses_the_one_final_validated_manifest_snapshot(tmp_path, monkeypatch, drift_before_open):
    path = tmp_path / "manifest.json"
    value = {"schema": 1, "profile": "274", "release_id": "274-test", "marker": "validated"}
    raw = encoded(value)
    changed = encoded({**value, "marker": "unvalidated-and-larger"})
    path.write_bytes(raw)
    armed = []
    runner = mock.Mock()
    runner.create_temp_dir.return_value = "/private-fixture/input"
    stop = RuntimeError("fixture upload boundary")
    runner.upload.side_effect = stop

    def ensure(*args):
        if drift_before_open:
            atomic.atomic_write(path, changed)
        armed.append(True)
        return value

    monkeypatch.setattr(vm_validate, "SSHRunner", lambda: runner)
    monkeypatch.setattr(vm_validate, "install_vm_validator", lambda runner: None)
    monkeypatch.setattr(vm_validate, "validate_manifest_profile_contract", lambda *args: None)
    monkeypatch.setattr(vm_validate, "get_profile", lambda name: {})
    monkeypatch.setattr(vm_validate, "space_cleaner_checksum", lambda value: "a" * 64)
    monkeypatch.setattr(vm_validate, "_ensure_build_dependencies", ensure)
    monkeypatch.setattr(sys, "argv", ["vm_validate", "--manifest", str(path), "--output", str(tmp_path / "output")])
    if drift_before_open:
        with pytest.raises(RuntimeError, match="manifest changed before upload"):
            vm_validate.main()
        runner.run.assert_not_called()
        runner.upload.assert_not_called()
    else:
        with replace_after_open(monkeypatch, path, changed, active=lambda: bool(armed)) as replaced:
            with pytest.raises(RuntimeError) as caught:
                vm_validate.main()
        assert caught.value is stop and replaced
        assert runner.upload.call_args.args == ("local_vm", raw, "/private-fixture/input/manifest.json", 0o400)
        assert atomic.read_replaceable_bytes(path) == changed
