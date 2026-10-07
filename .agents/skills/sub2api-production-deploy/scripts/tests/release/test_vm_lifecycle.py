from __future__ import annotations

import importlib
import json
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
    check = f'test "$(cat /proc/sys/kernel/random/boot_id)" = {BOOT_ID}'
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
    'test "$(id -u)" = 0',
    'test -f "$lock" && test ! -L "$lock"',
    'test "$(stat -c \'%U:%G:%a:%h\' "$lock")" = root:root:600:1',
    "flock -xn 8",
    "^sub2api-(v2-|probe-|preview-)",
    'test "$(docker inspect -f \'{{.State.Status}}\' sub2api-dev)" = exited',
    "! ss -ltnH '( sport = :8211 )' | grep -q .",
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
