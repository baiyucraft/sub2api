from __future__ import annotations

import importlib
import json
import os
import shutil
import subprocess
import sys
from collections import deque
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import pytest
import yaml


sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
vm = importlib.import_module("release.vm_lifecycle")
release_process = importlib.import_module("release.process")
BOOT_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
OTHER_BOOT_ID = "11111111-2222-3333-4444-555555555555"


class FakeClock:
    def __init__(self):
        self.now = 0.0
        self.sleeps = []

    def time(self):
        return 1700000000 + self.now

    def monotonic(self):
        return self.now

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.now += seconds


class VMHarness:
    def __init__(self, root, monkeypatch):
        self.root = root
        self.config_path = root / ".ssh.local"
        self.vmrun_path = root / "vmrun.exe"
        self.vmx_path = root / "release vm.vmx"
        self.vmrun_path.write_text("mock executable", encoding="utf-8")
        self.vmx_path.write_text("mock VM", encoding="utf-8")
        self.run_dir = root / "checkout-a" / "release-a"
        self.run_dir.mkdir(parents=True)
        self.shared = root / "shared-vm"
        self.shared.mkdir()
        self.clock = FakeClock()
        self.running = False
        self.boot_id = BOOT_ID
        self.events = []
        self.health = deque(["healthy"])
        self.shutdown_requested = False
        self.shutdown_attempts = 0
        self.shutdown_polls = deque([True, False])
        self.shutdown_error = None
        self.shutdown_effect_on_error = False
        self.preserve_error = None
        self.vmrun = mock.Mock(side_effect=self.vmware_command)
        self.ssh = mock.Mock()
        self.ssh.run.side_effect = self.ssh_command
        self.ssh_factory = mock.Mock(return_value=self.ssh)
        monkeypatch.setattr(vm, "SSH_CONFIG", self.config_path)
        monkeypatch.setattr(vm, "_shared_dir", lambda _settings: self.shared)
        monkeypatch.setattr(vm, "run_hidden", self.vmrun)
        monkeypatch.setattr(vm, "SSHRunner", self.ssh_factory)
        monkeypatch.setattr(vm, "time", self.clock)
        self.write_config(self.valid_config())

    def valid_config(self):
        return {
            "enabled": True,
            "vmrun_path": str(self.vmrun_path),
            "vmx_path": str(self.vmx_path),
            "startup_timeout_seconds": 30,
            "shutdown_timeout_seconds": 30,
        }

    def write_config(self, config):
        self.config_path.write_text(yaml.safe_dump({"servers": {}, "vm_lifecycle": config}), encoding="utf-8")

    def lease(self, run_dir=None, release_id="release-a", ssh=None, commit=None):
        return vm.VMLease(
            run_dir or self.run_dir, release_id, self.ssh if ssh is None else ssh,
            process_token="process-a", commit=commit,
        )

    def write_owner(self, value):
        (self.shared / "owner.json").write_text(json.dumps(value), encoding="utf-8")

    def owner(self):
        return json.loads((self.shared / "owner.json").read_text(encoding="utf-8"))

    def list_output(self, running):
        return f"Total running VMs: 1\n{self.vmx_path}\n" if running else "Total running VMs: 0\n"

    def vmware_command(self, command, **_kwargs):
        assert command[:3] == [str(self.vmrun_path), "-T", "ws"]
        operation = command[3]
        self.events.append(("vmrun", operation))
        if operation == "start":
            assert command[4:] == [str(self.vmx_path), "nogui"]
            self.running = True
            return subprocess.CompletedProcess(command, 0, "", "")
        assert operation == "list", "power-off/suspend/reset commands are forbidden in these tests"
        if self.shutdown_requested:
            self.running = self.shutdown_polls.popleft() if self.shutdown_polls else self.running
        return subprocess.CompletedProcess(command, 0, self.list_output(self.running), "")

    def ssh_command(self, node, script, allowed, **_kwargs):
        assert node == "local_vm"
        fields = frozenset(allowed)
        self.events.append(("ssh", fields))
        if fields == {"vm_ssh_ready", "vm_boot_id"}:
            return SimpleNamespace(values={"vm_ssh_ready": "true", "vm_boot_id": self.boot_id})
        if fields == {"vm_app_started"}:
            return SimpleNamespace(values={"vm_app_started": "true"})
        if fields == {"vm_app_health"}:
            state = self.health.popleft() if len(self.health) > 1 else self.health[0]
            return SimpleNamespace(values={"vm_app_health": state})
        if fields == {"vm_boot_id"}:
            return SimpleNamespace(values={"vm_boot_id": self.boot_id})
        if fields == {"vm_preserve_verified"}:
            if f'= {self.boot_id}\n' not in script:
                raise RuntimeError("local_vm stage failed with exit code 1; remote stderr withheld")
            if self.preserve_error is not None:
                raise self.preserve_error
            return SimpleNamespace(values={"vm_preserve_verified": "true"})
        assert fields == {"vm_shutdown_scheduled"}
        self.shutdown_attempts += 1
        # Simulate the remote identity check; the shell itself is never executed.
        if f'= {self.boot_id}\n' not in script:
            raise RuntimeError("local_vm stage failed with exit code 1; remote stderr withheld")
        if self.shutdown_error is not None:
            self.shutdown_requested = self.shutdown_effect_on_error
            raise self.shutdown_error
        self.shutdown_requested = True
        return SimpleNamespace(values={"vm_shutdown_scheduled": "true"})


@pytest.fixture
def h(tmp_path, monkeypatch):
    return VMHarness(tmp_path, monkeypatch)


def mark_symlink(monkeypatch, path):
    original = Path.is_symlink
    monkeypatch.setattr(Path, "is_symlink", lambda item: item == path or original(item))


@pytest.mark.parametrize("mode", ["missing_file", "missing_section", "disabled", "null_section"])
def test_unconfigured_lease_guard_and_view_are_noops(h, mode):
    if mode == "missing_file":
        h.config_path.unlink()
    elif mode == "missing_section":
        h.config_path.write_text("servers: {}\n", encoding="utf-8")
    else:
        h.write_config({"enabled": False} if mode == "disabled" else None)
    assert vm.load_settings() is None
    with h.lease() as lease:
        assert lease.enabled is False
        assert lease.ensure_ready() is None
        assert lease.complete() is None
    with vm.vm_guard():
        pass
    assert vm.lifecycle_view(h.run_dir) == {
        "vm_power_status": "unmanaged",
        "vm_started_by_release": False,
        "vm_cleanup_status": "not_applicable",
    }
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()
    h.ssh_factory.assert_not_called()
    assert not (h.shared / "owner.json").exists()


def test_valid_settings_resolve_vm_identity_and_defaults(h):
    config = h.valid_config()
    config.pop("startup_timeout_seconds")
    config.pop("shutdown_timeout_seconds")
    h.write_config(config)
    settings = vm.load_settings()
    assert settings.vmrun_path == h.vmrun_path.resolve()
    assert settings.vmx_path == h.vmx_path.resolve()
    assert (settings.startup_timeout_seconds, settings.shutdown_timeout_seconds) == (180, 180)
    assert len(settings.identity) == 64
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("config", [[], "invalid", 7, {}, {"enabled": "true"}, {"enabled": 1}, {"enabled": None}])
def test_invalid_configuration_shape_or_enabled_flag_is_rejected(h, config):
    h.write_config(config)
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()
    h.vmrun.assert_not_called()


def test_invalid_yaml_is_rejected_without_printing_configuration(h):
    h.config_path.write_text("vm_lifecycle: [invalid\n", encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError) as error:
        vm.load_settings()
    assert str(error.value) == "vm_lifecycle_config_invalid"


@pytest.mark.parametrize("document", [None, [], "invalid", 7, True])
def test_non_mapping_configuration_document_is_rejected(h, document):
    h.config_path.write_text(yaml.safe_dump(document), encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("enabled", [True, False])
def test_unknown_configuration_key_is_rejected(h, enabled):
    h.write_config({**h.valid_config(), "enabled": enabled, "unknown_control": True})
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()


@pytest.mark.parametrize("key", ["startup_timeout_seconds", "shutdown_timeout_seconds"])
@pytest.mark.parametrize("value", [0, 29, 601, True, "180", 30.5, None])
def test_invalid_timeout_is_rejected(h, key, value):
    h.write_config({**h.valid_config(), key: value})
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()


@pytest.mark.parametrize("key", ["vmrun_path", "vmx_path"])
@pytest.mark.parametrize("value", ["relative/path.vmx", "", None, 7, "invalid\npath"])
def test_non_absolute_or_invalid_configuration_path_is_rejected(h, key, value):
    h.write_config({**h.valid_config(), key: value})
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()


@pytest.mark.parametrize("key", ["vmrun_path", "vmx_path"])
def test_missing_directory_and_symlink_configuration_paths_are_rejected(h, monkeypatch, key):
    for path in (h.root / "missing.vmx", h.root):
        h.write_config({**h.valid_config(), key: str(path)})
        with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
            vm.load_settings()
    h.write_config(h.valid_config())
    mark_symlink(monkeypatch, h.vmrun_path if key == "vmrun_path" else h.vmx_path)
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()


def test_non_vmx_extension_is_rejected(h):
    h.write_config({**h.valid_config(), "vmx_path": str(h.vmrun_path)})
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_config_invalid"):
        vm.load_settings()


def test_running_vm_is_preserved_without_start_or_shutdown(h):
    h.running = True
    with h.lease() as lease:
        lease.ensure_ready()
        lease.ensure_ready()
        assert lease.state["vm_started_by_release"] is False
        assert lease.complete() is None
        lease.complete()
    assert [call.args[0][3] for call in h.vmrun.call_args_list] == ["list", "list", "list"]
    assert h.ssh.run.call_count == 1
    assert h.shutdown_attempts == 0
    assert h.owner()["lease_status"] == "released"
    assert vm.lifecycle_view(h.run_dir)["vm_cleanup_status"] == "preserved"


def test_preexisting_vm_closed_externally_is_reported_stopped_without_shutdown(h):
    h.running = True
    with h.lease() as lease:
        lease.ensure_ready()
        h.running = False
        lease.complete()
    assert h.shutdown_attempts == 0
    assert h.ssh.run.call_count == 1
    assert [call.args[0][3] for call in h.vmrun.call_args_list] == ["list", "list", "list"]
    assert vm.lifecycle_view(h.run_dir) == {
        "vm_power_status": "stopped", "vm_started_by_release": False, "vm_cleanup_status": "preserved",
    }


def test_preserve_power_probe_failure_does_not_mark_lease_completed(h):
    h.running = True
    with h.lease() as lease:
        lease.ensure_ready()
        h.vmrun.side_effect = None
        h.vmrun.return_value = subprocess.CompletedProcess([], 0, "invalid VM list", "")
        with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_vmware_state_unknown"):
            lease.complete()
        assert lease.completed is False, "power confirmation must pass before the lease is marked completed"
    assert h.owner()["lease_status"] == "retained"
    assert h.shutdown_attempts == 0


@pytest.mark.parametrize("commit", [None, "a" * 40])
def test_optional_commit_is_persisted_in_owner_and_run_state(h, commit):
    h.running = True
    with h.lease(commit=commit) as lease:
        lease.ensure_ready()
        lease.complete()
    assert h.owner()["source_commit"] == commit
    state = json.loads((h.run_dir / "vm-lifecycle.json").read_text(encoding="utf-8"))
    assert state["source_commit"] == commit
    assert "source_commit" not in vm.lifecycle_view(h.run_dir)


def test_stopped_vm_starts_nogui_waits_for_existing_app_then_shuts_down_once(h):
    h.health = deque(["starting", "healthy"])
    with h.lease() as lease:
        lease.ensure_ready()
        assert lease.ready is True
        assert lease.state["vm_boot_id"] == BOOT_ID
        assert lease.state["vm_started_by_release"] is True
        assert lease.complete() is None
        lease.complete()
    assert h.events == [
        ("vmrun", "list"), ("vmrun", "start"), ("vmrun", "list"),
        ("ssh", frozenset({"vm_ssh_ready", "vm_boot_id"})),
        ("ssh", frozenset({"vm_app_started"})),
        ("ssh", frozenset({"vm_app_health"})), ("ssh", frozenset({"vm_app_health"})),
        ("ssh", frozenset({"vm_shutdown_scheduled"})),
        ("vmrun", "list"), ("vmrun", "list"),
    ]
    start = next(call for call in h.vmrun.call_args_list if call.args[0][3] == "start")
    assert start.args[0][-2:] == [str(h.vmx_path), "nogui"]
    assert h.shutdown_attempts == 1
    scripts = [call.args[1] for call in h.ssh.run.call_args_list]
    assert any("docker start sub2api-dev" in script for script in scripts)
    assert all("docker run" not in script and "docker create" not in script for script in scripts)
    assert all("vmrun" not in script for script in scripts)
    assert h.owner()["lease_status"] == "released"
    assert vm.lifecycle_view(h.run_dir) == {
        "vm_power_status": "stopped", "vm_started_by_release": True, "vm_cleanup_status": "verified",
    }


def test_default_ssh_runner_is_constructed_lazily_only_when_enabled(h):
    with vm.VMLease(h.run_dir, "release-a") as lease:
        h.ssh_factory.assert_not_called()
        lease.ensure_ready()
        h.ssh_factory.assert_called_once_with()
        lease.complete()


def test_boot_id_change_rejects_shutdown_before_stopping_app(h):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_shutdown_preflight_failed"):
        with h.lease() as lease:
            lease.ensure_ready()
            h.boot_id = OTHER_BOOT_ID
            lease.complete()
    script = h.ssh.run.call_args.args[1]
    check = f'required_value=$(cat /proc/sys/kernel/random/boot_id)\ntest "$required_value" = {BOOT_ID}'
    assert check in script
    assert script.index(check) < script.index("docker stop") < script.index("shutdown -h +1")
    assert OTHER_BOOT_ID not in script
    assert h.shutdown_requested is False
    assert h.running is True
    assert h.owner()["lease_status"] == "retained"


@pytest.mark.parametrize("error", [OSError("lost SSH"), EOFError("lost reply"), vm.paramiko.SSHException("lost channel"), RuntimeError("local_vm stage failed with exit code -1; remote stderr withheld")])
def test_lost_shutdown_reply_polls_vm_without_resending_shutdown(h, error):
    with h.lease() as lease:
        lease.ensure_ready()
        h.shutdown_error = error
        h.shutdown_effect_on_error = True
        lease.complete()
    assert h.shutdown_attempts == 1
    assert h.owner()["lease_status"] == "released"
    assert vm.lifecycle_view(h.run_dir)["vm_power_status"] == "stopped"
    assert [call.args[0][3] for call in h.vmrun.call_args_list].count("start") == 1


def test_lost_reply_with_vm_still_running_times_out_and_retains_owner(h):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_shutdown_timeout"):
        with h.lease() as lease:
            lease.ensure_ready()
            h.shutdown_error = OSError("lost reply")
            lease.complete()
    assert h.shutdown_attempts == 1
    assert h.owner()["lease_status"] == "retained"
    assert vm.lifecycle_view(h.run_dir)["vm_cleanup_status"] == "failed"
    assert h.clock.now >= 30


@pytest.mark.parametrize("required_check", [
    'required_value=$(id -u)\ntest "$required_value" = 0',
    'test -f "$lock"\ntest ! -L "$lock"',
    'required_value=$(stat -c \'%U:%G:%a:%h\' "$lock")\ntest "$required_value" = root:root:600:1',
    "flock -xn 8",
    'case "$name" in sub2api-v2-*|sub2api-probe-*|sub2api-preview-*) exit 1 ;; esac',
    'status=$(docker inspect -f \'{{.State.Status}}\' sub2api-dev)\ntest "$status" = exited',
    'listeners=$(ss -ltnH \'( sport = :8211 )\')\ntest -z "$listeners"',
])
def test_shutdown_preflight_rejection_never_forces_power_off(h, required_check):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_shutdown_preflight_failed"):
        with h.lease() as lease:
            lease.ensure_ready()
            before = h.vmrun.call_count
            h.shutdown_error = RuntimeError("local_vm stage failed with exit code 1; remote stderr withheld")
            try:
                lease.complete()
            finally:
                assert h.vmrun.call_count == before
    script = h.ssh.run.call_args.args[1]
    assert required_check in script
    assert script.index(required_check) < script.index("shutdown -h +1")
    assert h.shutdown_requested is False
    assert h.running is True
    assert h.owner()["lease_status"] == "retained"


def test_missing_shutdown_acknowledgement_is_rejected(h):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_shutdown_preflight_failed"):
        with h.lease() as lease:
            lease.ensure_ready()
            h.ssh.run.side_effect = None
            h.ssh.run.return_value = SimpleNamespace(values={"vm_shutdown_scheduled": "false"})
            lease.complete()
    assert h.owner()["lease_status"] == "retained"


@pytest.mark.parametrize("message", ["local_vm stage failed with exit code 2; remote stderr withheld", "remote shutdown preflight rejected"])
def test_known_shutdown_runtime_error_does_not_poll_or_retry(h, message):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_shutdown_preflight_failed"):
        with h.lease() as lease:
            lease.ensure_ready()
            calls = h.vmrun.call_count
            h.shutdown_error = RuntimeError(message)
            try:
                lease.complete()
            finally:
                assert h.vmrun.call_count == calls
    assert h.shutdown_attempts == 1
    assert h.clock.sleeps == []
    assert h.shutdown_requested is False


def test_failed_release_retains_owner_and_blocks_new_and_same_release_and_guard(h):
    with pytest.raises(RuntimeError, match="release failed"):
        with h.lease() as lease:
            lease.ensure_ready()
            raise RuntimeError("release failed")
    owner = h.owner()
    assert owner["lease_status"] == "retained"
    assert owner["vm_cleanup_status"] == "retained_failure"
    assert owner["process_token"] == "process-a"
    other = h.root / "checkout-b" / "release-b"
    other.mkdir(parents=True)
    calls = h.vmrun.call_count
    for run_dir, release_id in ((other, "release-b"), (h.run_dir, "release-a")):
        with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_reconciliation_required"):
            with h.lease(run_dir, release_id):
                pytest.fail("a retained owner was automatically adopted")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_reconciliation_required"):
        with vm.vm_guard():
            pytest.fail("cross-checkout guard adopted a retained owner")
    assert h.owner() == owner
    assert h.vmrun.call_count == calls
    assert h.shutdown_attempts == 0


def test_context_exit_without_complete_keeps_vm_and_ownership(h):
    with h.lease() as lease:
        lease.ensure_ready()
    assert h.running is True
    assert h.shutdown_attempts == 0
    assert h.owner()["lease_status"] == "retained"


def test_live_lease_excludes_cross_checkout_guard_and_second_lease(h):
    other = h.root / "checkout-b"
    other.mkdir()
    with h.lease():
        with pytest.raises(RuntimeError, match="another release process"):
            with vm.vm_guard():
                pytest.fail("guard bypassed live VM lock")
        with pytest.raises(RuntimeError, match="another release process"):
            with h.lease(other, "release-b"):
                pytest.fail("second checkout bypassed live VM lock")
    h.vmrun.assert_not_called()


def test_vm_guard_is_pure_lock_and_releases_it_without_creating_owner(h):
    with vm.vm_guard():
        with pytest.raises(RuntimeError, match="another release process"):
            with h.lease():
                pytest.fail("lease bypassed guard")
    with vm.vm_guard():
        pass
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()
    assert not (h.shared / "owner.json").exists()


@pytest.mark.parametrize("kind", ["bad_json", "list", "missing_schema", "wrong_schema", "oversized", "directory"])
@pytest.mark.parametrize("target", ["owner", "view"])
def test_malformed_state_is_rejected(h, kind, target):
    path = h.shared / "owner.json" if target == "owner" else h.run_dir / "vm-lifecycle.json"
    values = {"bad_json": "{", "list": "[]", "missing_schema": "{}", "wrong_schema": '{"schema": 2}', "oversized": " " * 16385}
    if kind == "directory":
        path.mkdir()
    else:
        path.write_text(values[kind], encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_state_invalid"):
        if target == "owner":
            with vm.vm_guard():
                pytest.fail("guard accepted invalid owner state")
        else:
            vm.lifecycle_view(h.run_dir)
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("target", ["owner", "view", "view_parent", "shared"])
def test_symlink_state_or_directory_is_rejected(h, monkeypatch, target):
    paths = {
        "owner": h.shared / "owner.json", "view": h.run_dir / "vm-lifecycle.json",
        "view_parent": h.run_dir, "shared": h.shared,
    }
    mark_symlink(monkeypatch, paths[target])
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_state_unsafe"):
        if target in {"owner", "shared"}:
            with vm.vm_guard():
                pytest.fail("guard accepted symlink state")
        else:
            vm.lifecycle_view(h.run_dir)
    h.vmrun.assert_not_called()


def test_lease_rejects_symlink_shared_directory(h, monkeypatch):
    mark_symlink(monkeypatch, h.shared)
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_state_unsafe"):
        with h.lease():
            pytest.fail("lease accepted symlink directory")
    h.vmrun.assert_not_called()


def test_lifecycle_view_returns_only_three_allowlisted_fields(h):
    value = {
        "schema": 1, "vm_power_status": "running", "vm_started_by_release": True,
        "vm_cleanup_status": "pending", "vm_boot_id": BOOT_ID, "vmx_path": "private",
        "process_token": "private-token", "lease_token": "private-owner", "password": "test-secret",
    }
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps(value), encoding="utf-8")
    assert vm.lifecycle_view(h.run_dir) == {
        "vm_power_status": "running", "vm_started_by_release": True, "vm_cleanup_status": "pending",
    }


@pytest.mark.parametrize("field,values", [
    ("vm_power_status", ("unmanaged", "checking", "starting", "waiting_for_ssh", "running", "stopping", "stopped", "unknown")),
    ("vm_cleanup_status", ("not_applicable", "pending", "preserved", "running", "verified", "retained_failure", "shutdown_reply_uncertain", "failed")),
])
def test_lifecycle_view_preserves_documented_enum_values(h, field, values):
    for value in values:
        (h.run_dir / "vm-lifecycle.json").write_text(json.dumps({"schema": 1, field: value}), encoding="utf-8")
        assert vm.lifecycle_view(h.run_dir)[field] == value


@pytest.mark.parametrize("field", ["vm_power_status", "vm_cleanup_status"])
@pytest.mark.parametrize("value", ["private arbitrary value", None, 7, True, ["private value"], {"private": "value"}])
def test_lifecycle_view_normalizes_unknown_values_to_enum(h, field, value):
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps({"schema": 1, field: value}), encoding="utf-8")
    assert vm.lifecycle_view(h.run_dir)[field] == "unknown"


@pytest.mark.parametrize("value,expected", [(True, True), (False, False), (1, False), ("true", False), (None, False), ([], False), ({}, False)])
def test_lifecycle_view_started_flag_accepts_only_boolean_true(h, value, expected):
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps({"schema": 1, "vm_started_by_release": value}), encoding="utf-8")
    assert vm.lifecycle_view(h.run_dir)["vm_started_by_release"] is expected


@pytest.mark.parametrize("output", ["", "not a VM list", "Total running VMs: -1\n", "Total running VMs: one\n", "Total running VMs: 1\n", "Total running VMs: 0\nextra\n", "Total running VMs: 1\nrelative.vmx\n"])
def test_malformed_vmrun_list_fails_closed_without_starting_vm(h, output):
    h.vmrun.side_effect = None
    h.vmrun.return_value = subprocess.CompletedProcess([], 0, output, "")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_vmware_state_unknown"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.vmrun.call_count == 1
    h.ssh.run.assert_not_called()
    assert h.owner()["lease_status"] == "retained"


@pytest.mark.parametrize("error", [OSError("mock missing executable"), subprocess.TimeoutExpired("mock vmrun", 30)])
def test_vmrun_transport_or_command_timeout_preserves_owner(h, error):
    h.vmrun.side_effect = error
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_vmware_command_uncertain"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.owner()["lease_status"] == "retained"
    h.ssh.run.assert_not_called()


def test_nonzero_vmrun_exit_fails_closed(h):
    h.vmrun.side_effect = None
    h.vmrun.return_value = subprocess.CompletedProcess([], 1, "", "private stderr")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_vmware_command_failed") as error:
        with h.lease() as lease:
            lease.ensure_ready()
    assert "private stderr" not in str(error.value)
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("error", [OSError("SSH offline"), EOFError("SSH EOF"), vm.paramiko.SSHException("SSH handshake failed")])
def test_ssh_readiness_timeout_keeps_started_vm_without_shutdown(h, error):
    h.ssh.run.side_effect = error
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_ssh_ready_timeout"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.clock.now >= 30
    assert h.running is True
    assert h.shutdown_attempts == 0
    assert h.owner()["lease_status"] == "retained"


def test_ssh_authentication_failure_is_not_retried(h):
    h.ssh.run.side_effect = vm.paramiko.AuthenticationException("mock credentials rejected")
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_ssh_identity_failed"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.ssh.run.call_count == 1
    assert h.clock.sleeps == []


@pytest.mark.parametrize("boot_id", ["", "not-a-boot-id", BOOT_ID.upper()])
def test_invalid_ssh_boot_identity_is_rejected(h, boot_id):
    h.boot_id = boot_id
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_identity_not_verified"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.shutdown_attempts == 0


def test_ssh_ready_without_boot_id_is_rejected(h):
    h.ssh.run.side_effect = None
    h.ssh.run.return_value = SimpleNamespace(values={"vm_ssh_ready": "true"})
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_identity_not_verified"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.ssh.run.call_count == 1
    assert h.shutdown_attempts == 0


@pytest.mark.parametrize("health", ["unhealthy", "missing"])
def test_existing_app_health_timeout_or_unknown_state_retains_vm(h, health):
    h.health = deque([health])
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_app_ready_timeout"):
        with h.lease() as lease:
            lease.ensure_ready()
    assert h.shutdown_attempts == 0
    assert h.owner()["lease_status"] == "retained"


def test_complete_before_readiness_rejects_power_change(h):
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_owner_mismatch"):
        with h.lease() as lease:
            lease.complete()
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("key,value", [
    ("vm_identity", "another-vm"), ("release_id", "another-release"), ("source_commit", "b" * 40),
    ("pid", -1), ("process_token", "another-process"), ("lease_token", "another-owner"),
    ("initially_running", True), ("vm_started_by_release", False),
])
def test_owner_identity_mismatch_rejects_shutdown(h, key, value):
    with h.lease(commit="a" * 40) as lease:
        lease.ensure_ready()
        replacement = {**h.owner(), key: value}
        h.write_owner(replacement)
        calls = h.ssh.run.call_count
        with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_owner_mismatch"):
            lease.complete()
        assert h.ssh.run.call_count == calls
    assert h.shutdown_attempts == 0
    assert h.owner() == replacement


def test_owner_token_change_rejects_shutdown_without_overwriting_new_owner(h):
    replacement = None
    with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_owner_mismatch"):
        with h.lease() as lease:
            lease.ensure_ready()
            replacement = {**h.owner(), "release_id": "release-b", "lease_token": "replacement-owner"}
            h.write_owner(replacement)
            calls = h.ssh.run.call_count
            try:
                lease.complete()
            finally:
                assert h.ssh.run.call_count == calls
    assert h.shutdown_attempts == 0
    assert h.owner() == replacement, "__exit__ must not overwrite another owner's marker after rejecting its token"


@pytest.mark.parametrize("contents", ["{", "[]", '{"schema": 2}'])
def test_context_exit_preserves_unreadable_owner_marker(h, contents):
    with h.lease() as lease:
        lease.ensure_ready()
        (h.shared / "owner.json").write_text(contents, encoding="utf-8")
        with pytest.raises(vm.VMLifecycleError, match="vm_lifecycle_state_invalid"):
            lease.complete()
    assert (h.shared / "owner.json").read_text(encoding="utf-8") == contents
    assert json.loads((h.run_dir / "vm-lifecycle.json").read_text(encoding="utf-8"))["lease_status"] == "retained"
    assert h.shutdown_attempts == 0


@pytest.mark.parametrize("initially_running", [True, False])
def test_completion_save_failure_does_not_mark_lease_completed(h, monkeypatch, initially_running):
    h.running = initially_running
    with h.lease() as lease:
        lease.ensure_ready()
        save = lease._save

        def fail_release_save(**changes):
            if changes.get("lease_status") == "released":
                raise OSError("mock state write failed")
            return save(**changes)

        monkeypatch.setattr(lease, "_save", fail_release_save)
        with pytest.raises(OSError, match="mock state write failed"):
            lease.complete()
        assert lease.completed is False
    assert h.owner()["lease_status"] == "retained"


def make_retained(h):
    with h.lease(commit="a" * 40) as lease:
        lease.ensure_ready()
    original = {**h.owner(), "pid": 987654321}
    h.write_owner(original)
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps(original), encoding="utf-8")
    runner = {
        "schema": 1, "release_id": "release-a", "commit": "a" * 40,
        "pid": original["pid"], "process_token": "process-a", "status": "failed", "exit_code": 1,
    }
    (h.run_dir / "runner.json").write_text(json.dumps(runner), encoding="utf-8")
    h.vmrun.reset_mock()
    h.ssh.run.reset_mock()
    h.events.clear()
    h.original_documents = {path: path.read_bytes() for path in (h.run_dir / "runner.json", h.run_dir / "vm-lifecycle.json")}
    return runner


@pytest.fixture
def retained(h):
    return make_retained(h)


@pytest.fixture
def retained_running(h):
    h.running = True
    return make_retained(h)


def finalizer(h, retained, **kwargs):
    return vm.RetainedVMFinalization(h.run_dir, "release-a", "a" * 40, retained,
                                    process_identity=kwargs.pop("process_identity", lambda _pid: None), ssh=h.ssh, **kwargs)


def test_retained_finalization_only_shuts_down_dead_owned_vm_and_preserves_accident(h, retained):
    original = (h.shared / "owner.json").read_bytes()
    with finalizer(h, retained) as lease:
        result = lease.complete(evidence_sha256="b" * 64)
    assert result["status"] == "verified"
    assert result["evidence_sha256"] == "b" * 64
    assert result["vm_boot_id"] == BOOT_ID
    assert h.shutdown_attempts == 1
    assert h.owner()["lease_status"] == "released"
    assert (h.run_dir / "vm-finalization-owner.json").read_bytes() == original
    assert all(path.read_bytes() == content for path, content in h.original_documents.items())
    assert vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40, evidence_sha256="b" * 64) == result
    assert all(call.args[0][3] == "list" for call in h.vmrun.call_args_list)


def test_retained_finalization_successful_retry_only_verifies_stopped(h, retained):
    with finalizer(h, retained) as lease:
        first = lease.complete(evidence_sha256="b" * 64)
    h.ssh.run.reset_mock()
    with finalizer(h, retained) as lease:
        assert lease.completed is True
        assert lease.complete(evidence_sha256="b" * 64) == first
    h.ssh.run.assert_not_called()
    assert h.shutdown_attempts == 1


@pytest.mark.parametrize("callback", [lambda _pid: "process-a", lambda _pid: (_ for _ in ()).throw(OSError("unknown"))])
def test_retained_finalization_rejects_live_or_unprovable_worker(h, retained, callback):
    original = h.owner()
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_worker_(alive|unproven)"):
        with finalizer(h, retained, process_identity=callback):
            pytest.fail("worker identity was not rejected")
    assert h.owner() == original
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("field,value", [
    ("release_id", "other"), ("source_commit", "b" * 40), ("pid", 999),
    ("process_token", "other"), ("lease_token", "other"), ("vm_boot_id", OTHER_BOOT_ID),
    ("vm_identity", "other"), ("initially_running", True), ("vm_started_by_release", False),
    ("lease_status", "released"),
])
def test_retained_finalization_rejects_owner_or_vm_identity_drift(h, retained, field, value):
    owner = {**h.owner(), field: value}
    h.write_owner(owner)
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven"):
        with finalizer(h, retained):
            pytest.fail("changed ownership was accepted")
    assert h.owner() == owner
    h.ssh.run.assert_not_called()
    assert h.shutdown_attempts == 0


def test_retained_finalization_rejects_boot_drift_before_record_or_shutdown(h, retained):
    h.boot_id = OTHER_BOOT_ID
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_boot_changed"):
        with finalizer(h, retained):
            pytest.fail("rebooted VM was accepted")
    assert not (h.run_dir / "vm-finalization.json").exists()
    assert h.owner()["lease_status"] == "retained"
    assert h.shutdown_attempts == 0


def test_retained_finalization_never_starts_an_externally_stopped_vm(h, retained):
    h.running = False
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_running_unproven"):
        with finalizer(h, retained):
            pytest.fail("stopped VM was accepted")
    assert [call.args[0][3] for call in h.vmrun.call_args_list] == ["list"]
    h.ssh.run.assert_not_called()


def test_retained_finalization_rejects_originally_running_vm(h, retained):
    owner = {**h.owner(), "initially_running": True, "vm_started_by_release": False}
    h.write_owner(owner)
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps(owner), encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven"):
        with finalizer(h, retained):
            pytest.fail("preexisting VM was accepted for shutdown")
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("action", ["cleanup_failed", "evidence_invalid", "boot_changed", "original_changed", "worker_revived"])
def test_retained_finalization_preflight_failure_retains_original_owner(h, retained, action):
    original = h.owner()
    with pytest.raises((RuntimeError, vm.VMLifecycleError)):
        with finalizer(h, retained) as lease:
            if action == "cleanup_failed":
                raise RuntimeError("release task cleanup failed")
            if action == "boot_changed":
                h.boot_id = OTHER_BOOT_ID
            if action == "original_changed":
                (h.run_dir / "runner.json").write_text(json.dumps({**retained, "exit_code": 2}), encoding="utf-8")
            if action == "worker_revived":
                lease.process_identity = lambda _pid: "process-a"
            lease.complete(evidence_sha256="invalid" if action == "evidence_invalid" else "b" * 64)
    assert h.owner() == original
    assert h.running is True
    assert h.shutdown_requested is False


@pytest.mark.parametrize("error", [RuntimeError("local_vm stage failed with exit code 1; remote stderr withheld"), OSError("SSH lost")])
def test_retained_finalization_shutdown_failure_retains_vm_and_never_resends(h, retained, error):
    original = h.owner()
    h.shutdown_error = error
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_shutdown_(preflight_failed|timeout)"):
        with finalizer(h, retained) as lease:
            lease.complete(evidence_sha256="b" * 64)
    assert h.owner() == original
    assert h.running is True
    assert json.loads((h.run_dir / "vm-finalization.json").read_text())["status"] == "failed"
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_shutdown_reconciliation_required"):
        with finalizer(h, retained):
            pytest.fail("ambiguous shutdown was automatically retried")
    assert h.shutdown_attempts == 1


def test_retained_finalization_lost_shutdown_reply_accepts_only_proven_stop(h, retained):
    h.shutdown_error = EOFError("lost reply")
    h.shutdown_effect_on_error = True
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    assert h.shutdown_attempts == 1
    assert h.owner()["lease_status"] == "released"


def test_retained_finalization_owner_commit_failure_retries_only_local_commit(h, retained, monkeypatch):
    write = vm.atomic_write

    def fail_owner(path, content, *args, **kwargs):
        if path == h.shared / "owner.json":
            raise OSError("owner commit failed")
        return write(path, content, *args, **kwargs)

    monkeypatch.setattr(vm, "atomic_write", fail_owner)
    with pytest.raises(OSError, match="owner commit failed"):
        with finalizer(h, retained) as lease:
            lease.complete(evidence_sha256="b" * 64)
    assert h.owner()["lease_status"] == "retained"
    assert json.loads((h.run_dir / "vm-finalization.json").read_text())["status"] == "verified"
    monkeypatch.setattr(vm, "atomic_write", write)
    h.ssh.run.reset_mock()
    with finalizer(h, retained) as lease:
        assert lease.completed is False
        lease.complete(evidence_sha256="b" * 64)
    h.ssh.run.assert_not_called()
    assert h.shutdown_attempts == 1
    assert h.owner()["lease_status"] == "released"


def test_retained_finalization_verify_rejects_changed_record_owner_or_accident(h, retained):
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_evidence_mismatch"):
        vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40, evidence_sha256="c" * 64)
    original_owner = h.owner()
    h.write_owner({**original_owner, "finalization_sha256": "c" * 64})
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven"):
        vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40)
    h.write_owner(original_owner)
    path = h.run_dir / "runner.json"
    path.write_text(json.dumps({**retained, "exit_code": 2}), encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_record_unproven"):
        vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40)


def test_retained_finalization_verify_checks_current_vm_power_without_changing_it(h, retained):
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    h.shutdown_requested = False
    h.running = True
    h.ssh.run.reset_mock()
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_stopped_unproven"):
        vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40)
    h.ssh.run.assert_not_called()
    assert h.running is True
    assert h.shutdown_attempts == 1


def test_retained_finalization_remote_shutdown_requires_owned_gate_lock(h, retained):
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    script = next(call.args[1] for call in h.ssh.run.call_args_list if call.args[2] == {"vm_shutdown_scheduled"})
    for check in (
        '=$(stat -c \'%U:%G:%a\' "$gate_root") || exit $?',
        '=$(stat -c \'%U:%G:%a:%h\' "$gate_lock") || exit $?',
        '=$(stat -Lc \'%U:%G:%a:%h\' /proc/self/fd/9) || exit $?',
        "flock -xn 9", "run-validator.sh",
    ):
        assert check in script
        assert script.index(check) < script.index("docker stop") < script.index("shutdown -h +1")


def preserve_finalizer(h, runner, **kwargs):
    return finalizer(h, runner, preserve_running_vm=True, **kwargs)


def assert_preserve_only_observed(h):
    assert h.shutdown_attempts == 0
    assert h.shutdown_requested is False
    assert all(call.args[0][3] == "list" for call in h.vmrun.call_args_list)
    assert all(call.args[2] in ({"vm_boot_id"}, {"vm_preserve_verified"}) for call in h.ssh.run.call_args_list)


def test_preserve_retained_vm_commits_owner_last_and_keeps_failure_bytes(h, retained_running):
    original = (h.shared / "owner.json").read_bytes()
    with preserve_finalizer(h, retained_running) as lease:
        with pytest.raises(vm.VMLifecycleError, match="vm_finalization_start_forbidden"):
            lease.ensure_ready()
        result = lease.complete(evidence_sha256="b" * 64)
    assert (result["schema"], result["mode"], result["status"]) == (2, "preserve-running", "verified")
    assert (result["vm_power_status"], result["vm_cleanup_status"]) == ("running", "preserved")
    assert result["vm_started_by_release"] is False
    assert result["shutdown_requested"] is False
    assert (h.owner()["lease_status"], h.owner()["vm_power_status"], h.owner()["vm_cleanup_status"]) == ("released", "running", "preserved")
    assert (h.run_dir / "vm-finalization-owner.json").read_bytes() == original
    assert all(path.read_bytes() == content for path, content in h.original_documents.items())
    assert h.running is True
    assert vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40, evidence_sha256="b" * 64) == result
    assert_preserve_only_observed(h)


def test_preserve_success_retry_only_reprobes_same_running_vm(h, retained_running):
    with preserve_finalizer(h, retained_running) as lease:
        first = lease.complete(evidence_sha256="b" * 64)
    before = {path: path.read_bytes() for path in h.run_dir.glob("*.json")}
    h.ssh.run.reset_mock()
    with preserve_finalizer(h, retained_running) as lease:
        assert lease.completed is True
        assert lease.complete(evidence_sha256="b" * 64) == first
    assert [call.args[2] for call in h.ssh.run.call_args_list] == [{"vm_preserve_verified"}, {"vm_preserve_verified"}]
    assert all(path.read_bytes() == content for path, content in before.items())
    assert_preserve_only_observed(h)


def test_preserve_rejects_release_started_vm_and_default_rejects_preexisting_vm(h, retained):
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven"):
        with preserve_finalizer(h, retained):
            pytest.fail("owned VM accepted as preserved")
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


def test_shutdown_default_rejects_actual_preexisting_retained_fixture(h, retained_running):
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven"):
        with finalizer(h, retained_running):
            pytest.fail("preexisting VM accepted for shutdown")
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("flag", [None, 0, 1, "true"])
def test_preserve_requires_explicit_boolean(h, retained_running, flag):
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_mode_unproven"):
        finalizer(h, retained_running, preserve_running_vm=flag)
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("change", [
    {"schema": True}, {"schema": 2}, {"initially_running": 1}, {"vm_started_by_release": 0},
    {"lease_status": "released"}, {"vm_power_status": "unknown"}, {"vm_cleanup_status": "verified"},
    {"release_id": "other"}, {"pid": 999}, {"process_token": "other"}, {"lease_token": ""},
])
def test_preserve_rejects_matching_but_ineligible_owner_and_lifecycle(h, retained_running, change):
    owner = {**h.owner(), **change}
    h.write_owner(owner)
    (h.run_dir / "vm-lifecycle.json").write_text(json.dumps(owner), encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_owner_unproven|vm_lifecycle_state_invalid"):
        with preserve_finalizer(h, retained_running):
            pytest.fail("ineligible evidence was accepted")
    assert h.owner() == owner
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("change", [
    {"schema": True}, {"schema": 3}, {"schema": 2}, {"schema": 2, "mode": None},
    {"schema": 2, "mode": "shutdown-owned"}, {"schema": 1, "mode": "preserve-running"},
])
def test_preserve_rejects_unknown_record_schema_or_mode(h, retained_running, change):
    (h.run_dir / "vm-finalization.json").write_text(json.dumps(change), encoding="utf-8")
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_mode_unproven"):
        with preserve_finalizer(h, retained_running):
            pytest.fail("unknown record accepted")
    assert h.owner()["lease_status"] == "retained"
    h.vmrun.assert_not_called()


@pytest.mark.parametrize("verified", [False, True])
def test_preserve_record_cannot_be_replayed_in_shutdown_mode(h, retained_running, verified):
    if verified:
        with preserve_finalizer(h, retained_running) as lease:
            lease.complete(evidence_sha256="b" * 64)
    else:
        (h.run_dir / "vm-finalization.json").write_text(json.dumps({"schema": 2, "mode": "preserve-running", "status": "failed"}), encoding="utf-8")
    owner = h.owner()
    h.vmrun.reset_mock()
    h.ssh.run.reset_mock()
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_mode_mismatch"):
        with finalizer(h, retained_running):
            pytest.fail("cross-mode retry accepted")
    assert h.owner() == owner
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("verified", [False, True])
def test_shutdown_record_cannot_be_replayed_in_preserve_mode(h, retained, verified):
    if verified:
        with finalizer(h, retained) as lease:
            lease.complete(evidence_sha256="b" * 64)
    else:
        (h.run_dir / "vm-finalization.json").write_text(json.dumps({"schema": 1, "status": "failed"}), encoding="utf-8")
    owner = h.owner()
    h.vmrun.reset_mock()
    h.ssh.run.reset_mock()
    with pytest.raises(vm.VMLifecycleError, match="vm_finalization_mode_mismatch"):
        with preserve_finalizer(h, retained):
            pytest.fail("cross-mode retry accepted")
    assert h.owner() == owner
    h.vmrun.assert_not_called()
    h.ssh.run.assert_not_called()


@pytest.mark.parametrize("failure", ["boot", "power", "ssh", "worker", "owner", "original", "evidence", "cleanup"])
def test_preserve_failure_keeps_retained_owner_and_never_changes_power(h, retained_running, failure):
    before = h.owner()
    with pytest.raises((RuntimeError, vm.VMLifecycleError)):
        with preserve_finalizer(h, retained_running) as lease:
            if failure == "boot":
                h.boot_id = OTHER_BOOT_ID
            elif failure == "power":
                h.running = False
            elif failure == "ssh":
                h.preserve_error = EOFError("uncertain query")
            elif failure == "worker":
                lease.process_identity = lambda _pid: "process-a"
            elif failure == "owner":
                h.write_owner({**before, "lease_token": "drift"})
            elif failure == "original":
                (h.run_dir / "runner.json").write_text(json.dumps({**retained_running, "exit_code": 2}), encoding="utf-8")
            elif failure == "cleanup":
                raise RuntimeError("cleanup failed")
            lease.complete(evidence_sha256="invalid" if failure == "evidence" else "b" * 64)
    expected = {**before, "lease_token": "drift"} if failure == "owner" else before
    assert h.owner() == expected
    assert_preserve_only_observed(h)
    path = h.run_dir / "vm-finalization.json"
    assert not path.exists() or json.loads(path.read_bytes())["status"] != "verified"


@pytest.mark.parametrize("phase,drift", [(phase, drift) for phase in (1, 2) for drift in ("owner", "runner", "callback")] + [(2, "record")])
def test_preserve_rechecks_bound_evidence_after_each_completion_callback(h, retained_running, phase, drift):
    calls = 0
    before = h.owner()

    def callback():
        nonlocal calls
        calls += 1
        if calls != phase:
            return
        if drift == "owner":
            h.write_owner({**before, "lease_token": "drift"})
        elif drift == "runner":
            (h.run_dir / "runner.json").write_text(json.dumps({**retained_running, "exit_code": 2}), encoding="utf-8")
        elif drift == "record":
            path = h.run_dir / "vm-finalization.json"
            record = json.loads(path.read_bytes())
            path.write_text(json.dumps({**record, "evidence_sha256": "c" * 64}), encoding="utf-8")
        else:
            raise RuntimeError("evidence drift")

    with pytest.raises((RuntimeError, vm.VMLifecycleError)):
        with preserve_finalizer(h, retained_running) as lease:
            lease.complete(evidence_sha256="b" * 64, before_owner_commit=callback)
    assert h.owner()["lease_status"] == "retained"
    assert_preserve_only_observed(h)


def test_preserve_owner_commit_failure_retries_only_readonly_probe_and_owner_commit(h, retained_running, monkeypatch):
    write = vm.atomic_write

    def fail_owner(path, content, *args, **kwargs):
        if path == h.shared / "owner.json":
            raise OSError("owner commit failed")
        return write(path, content, *args, **kwargs)

    monkeypatch.setattr(vm, "atomic_write", fail_owner)
    with pytest.raises(OSError, match="owner commit failed"):
        with preserve_finalizer(h, retained_running) as lease:
            lease.complete(evidence_sha256="b" * 64)
    assert h.owner()["lease_status"] == "retained"
    record_bytes = (h.run_dir / "vm-finalization.json").read_bytes()
    assert json.loads(record_bytes)["status"] == "verified"
    monkeypatch.setattr(vm, "atomic_write", write)
    h.ssh.run.reset_mock()
    with preserve_finalizer(h, retained_running) as lease:
        assert lease.completed is False
        with pytest.raises(vm.VMLifecycleError, match="vm_finalization_evidence_mismatch"):
            lease.complete(evidence_sha256="c" * 64)
        lease.complete(evidence_sha256="b" * 64)
    assert [call.args[2] for call in h.ssh.run.call_args_list] == [{"vm_preserve_verified"}, {"vm_preserve_verified"}]
    assert (h.run_dir / "vm-finalization.json").read_bytes() == record_bytes
    assert h.owner()["lease_status"] == "released"
    assert_preserve_only_observed(h)


@pytest.mark.parametrize("drift", ["owner", "runner", "record", "callback"])
def test_preserve_verified_owner_retry_rechecks_after_callback(h, retained_running, monkeypatch, drift):
    write = vm.atomic_write

    def fail_owner(path, content, *args, **kwargs):
        if path == h.shared / "owner.json":
            raise OSError("owner commit failed")
        return write(path, content, *args, **kwargs)

    monkeypatch.setattr(vm, "atomic_write", fail_owner)
    with pytest.raises(OSError):
        with preserve_finalizer(h, retained_running) as lease:
            lease.complete(evidence_sha256="b" * 64)
    monkeypatch.setattr(vm, "atomic_write", write)

    def callback():
        if drift == "owner":
            h.write_owner({**h.owner(), "lease_token": "drift"})
        elif drift == "runner":
            (h.run_dir / "runner.json").write_text(json.dumps({**retained_running, "exit_code": 2}), encoding="utf-8")
        elif drift == "record":
            path = h.run_dir / "vm-finalization.json"
            path.write_text(json.dumps({**json.loads(path.read_bytes()), "evidence_sha256": "c" * 64}), encoding="utf-8")
        else:
            raise RuntimeError("evidence changed")

    with pytest.raises((RuntimeError, vm.VMLifecycleError)):
        with preserve_finalizer(h, retained_running) as lease:
            lease.complete(evidence_sha256="b" * 64, before_owner_commit=callback)
    assert h.owner()["lease_status"] == "retained"
    assert_preserve_only_observed(h)


@pytest.mark.parametrize("failure", ["boot", "power", "ssh", "live_worker", "unknown_worker"])
def test_preserve_entry_drift_keeps_owner_and_creates_no_completion(h, retained_running, failure):
    before = h.owner()
    kwargs = {}
    if failure == "boot":
        h.boot_id = OTHER_BOOT_ID
    elif failure == "power":
        h.running = False
    elif failure == "ssh":
        h.ssh.run.side_effect = OSError("identity query failed")
    elif failure == "live_worker":
        kwargs["process_identity"] = lambda _pid: "process-a"
    else:
        kwargs["process_identity"] = lambda _pid: (_ for _ in ()).throw(OSError("unknown"))
    with pytest.raises((vm.VMLifecycleError, OSError)):
        with preserve_finalizer(h, retained_running, **kwargs):
            pytest.fail("entry evidence drift accepted")
    assert h.owner() == before
    assert not (h.run_dir / "vm-finalization.json").exists()
    assert not (h.run_dir / "vm-finalization-owner.json").exists()
    assert_preserve_only_observed(h)


@pytest.mark.parametrize("failure", ["boot", "power", "ssh", "owner", "original", "mode"])
def test_preserve_verified_view_rejects_current_drift_without_writes(h, retained_running, failure):
    with preserve_finalizer(h, retained_running) as lease:
        lease.complete(evidence_sha256="b" * 64)
    if failure == "boot":
        h.boot_id = OTHER_BOOT_ID
    elif failure == "power":
        h.running = False
    elif failure == "ssh":
        h.preserve_error = OSError("unknown")
    elif failure == "owner":
        h.write_owner({**h.owner(), "finalization_sha256": "c" * 64})
    elif failure == "original":
        (h.run_dir / "runner.json").write_text(json.dumps({**retained_running, "exit_code": 2}), encoding="utf-8")
    else:
        path = h.run_dir / "vm-finalization.json"
        record = json.loads(path.read_bytes())
        path.write_text(json.dumps({**record, "schema": 1}), encoding="utf-8")
    before = {path: path.read_bytes() for path in [*h.run_dir.glob("*.json"), h.shared / "owner.json"]}
    with pytest.raises(vm.VMLifecycleError):
        vm.verify_retained_finalization(h.run_dir, "release-a", "a" * 40)
    assert all(path.read_bytes() == content for path, content in before.items())
    assert_preserve_only_observed(h)


@pytest.mark.parametrize("variable,kind", [("lock", "-f"), ("gate_root", "-d"), ("gate_lock", "-f")])
@pytest.mark.parametrize("invalid_path", ["missing", "wrong_type"])
def test_retained_shutdown_path_assertions_exit_before_next_action(h, retained, variable, kind, invalid_path):
    """Execute the actual assertion pair: missing first operands must stop Bash."""
    bash = shutil.which("bash")
    if not bash and os.name == "nt":
        candidate = Path("C:/Program Files/Git/bin/bash.exe")
        bash = str(candidate) if candidate.is_file() else None
    if not bash:
        pytest.skip("Bash is unavailable")
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    remote_script = next(call.args[1] for call in h.ssh.run.call_args_list if call.args[2] == {"vm_shutdown_scheduled"})
    lines = remote_script.splitlines()
    assertion = f'test {kind} "${variable}"'
    start = lines.index(assertion)
    assert lines[start + 1] == f'test ! -L "${variable}"'
    target = h.root / f"invalid-{variable}"
    if invalid_path == "wrong_type":
        if kind == "-f":
            target.mkdir()
        else:
            target.write_text("not a directory", encoding="utf-8")
    environment = dict(os.environ)
    environment.pop("BASH_ENV", None)
    environment.pop("ENV", None)
    environment["FINALIZATION_TEST_PATH"] = target.as_posix()
    script = (f'set -Eeuo pipefail\n{variable}="$FINALIZATION_TEST_PATH"\n'
              + "\n".join(lines[start:start + 2]) + "\nprintf 'DANGEROUS_ACTION_REACHED\\n'\n")
    result = release_process.run_hidden([bash, "-c", script], env=environment,
                                        capture_output=True, text=True, timeout=10, check=False)
    assert result.returncode != 0
    assert "DANGEROUS_ACTION_REACHED" not in result.stdout


@pytest.mark.parametrize("invalid_path", ["missing", "wrong_type"])
def test_normal_owned_vm_shutdown_lock_failure_exits_before_next_action(h, invalid_path):
    candidate = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(candidate) if candidate.is_file() else shutil.which("bash")
    if not bash:
        pytest.skip("Bash is unavailable")
    with h.lease() as lease:
        lease.ensure_ready()
        lease.complete()
    script = next(call.args[1] for call in h.ssh.run.call_args_list if call.args[2] == {"vm_shutdown_scheduled"})
    lines = script.splitlines()
    start = lines.index('test -f "$lock"')
    assert lines[start + 1] == 'test ! -L "$lock"'
    target = h.root / "invalid-normal-lock"
    if invalid_path == "wrong_type":
        target.mkdir()
    environment = {**os.environ, "NORMAL_LOCK_TEST_PATH": target.as_posix()}
    environment.pop("BASH_ENV", None)
    environment.pop("ENV", None)
    code = ('set -Eeuo pipefail\nlock="$NORMAL_LOCK_TEST_PATH"\n' + "\n".join(lines[start:start + 2])
            + "\nprintf 'LATER_ACTION_REACHED\\n'\n")
    result = release_process.run_hidden([bash, "-c", code], env=environment, capture_output=True, text=True, timeout=10)
    assert result.returncode != 0
    assert "LATER_ACTION_REACHED" not in result.stdout


@pytest.mark.parametrize("command,assignment", [
    ("ps", "processes=$(ps -eo args=)"),
    ("docker", "names=$(docker ps -a --format '{{.Names}}')"),
    ("ss", "listeners=$(ss -ltnH '( sport = :8211 )')"),
])
def test_retained_shutdown_query_failure_stops_before_next_action(h, retained, command, assignment):
    bash = shutil.which("bash")
    if not bash and os.name == "nt":
        candidate = Path("C:/Program Files/Git/bin/bash.exe")
        bash = str(candidate) if candidate.is_file() else None
    if not bash:
        pytest.skip("Bash is unavailable")
    with finalizer(h, retained) as lease:
        lease.complete(evidence_sha256="b" * 64)
    remote_script = next(call.args[1] for call in h.ssh.run.call_args_list if call.args[2] == {"vm_shutdown_scheduled"})
    assert assignment in remote_script.splitlines()
    environment = dict(os.environ)
    environment.pop("BASH_ENV", None)
    environment.pop("ENV", None)
    # A failed query can emit empty (apparently harmless) output; its exit code
    # must still reject completion before any container stop or shutdown.
    script = (f'set -Eeuo pipefail\n{command}() {{ return 17; }}\n'
              + assignment + "\nprintf 'DANGEROUS_ACTION_REACHED\\n'\n")
    result = release_process.run_hidden([bash, "-c", script], env=environment,
                                        capture_output=True, text=True, timeout=10, check=False)
    assert result.returncode == 17
    assert "DANGEROUS_ACTION_REACHED" not in result.stdout
