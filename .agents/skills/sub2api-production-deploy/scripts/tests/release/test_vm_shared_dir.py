"""Fixed VM ownership paths and existing-lock identity, without VM or SSH I/O."""
from __future__ import annotations

import json
import os
from pathlib import Path
import sys
from types import SimpleNamespace
from unittest import mock

import pytest
import yaml


sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import state
from release import vm_lifecycle as vm


@pytest.fixture
def fixed(tmp_path, monkeypatch):
    vmrun, vmx = tmp_path / "vmrun.exe", tmp_path / "release.vmx"
    vmrun.write_bytes(b"fixture")
    vmx.write_bytes(b"fixture")
    identity = vm.VMSettings(vmrun.resolve(), vmx.resolve()).identity
    shared = tmp_path / "shared" / identity
    shared.mkdir(parents=True)
    owner = {"schema": 1, "vm_identity": identity, "lease_status": "released"}
    (shared / "owner.json").write_text(json.dumps(owner), encoding="utf-8")
    (shared / "lease.lock").write_bytes(b"\0")
    config = {"enabled": True, "vmrun_path": str(vmrun), "vmx_path": str(vmx), "shared_dir": str(shared)}
    config_path = tmp_path / ".ssh.local"
    def write(changes=None):
        config_path.write_text(yaml.safe_dump({"vm_lifecycle": {**config, **(changes or {})}}), encoding="utf-8")
    write()
    monkeypatch.setattr(vm, "SSH_CONFIG", config_path)
    return SimpleNamespace(root=tmp_path, shared=shared, owner=owner, config=config, write=write)


def test_configured_shared_dir_is_independent_of_environment_and_checkout(fixed, monkeypatch):
    settings = vm.load_settings()
    monkeypatch.setenv("LOCALAPPDATA", str(fixed.root / "other-user-location"))
    monkeypatch.chdir(fixed.root)
    assert settings.shared_dir == fixed.shared
    assert vm._shared_dir(settings) == fixed.shared
    before = (fixed.shared / "lease.lock").stat()
    with vm.vm_guard():
        during = (fixed.shared / "lease.lock").stat()
        assert (during.st_dev, during.st_ino) == (before.st_dev, before.st_ino)
    assert json.loads((fixed.shared / "owner.json").read_text()) == fixed.owner


@pytest.mark.parametrize("value", [None, 7, "", "relative/path", "bad\npath"])
def test_shared_dir_requires_an_absolute_path_string(fixed, value):
    fixed.write({"shared_dir": value})
    with pytest.raises(vm.VMLifecycleError):
        vm.load_settings()


def test_missing_wrong_leaf_and_dotdot_paths_are_rejected(fixed):
    for path in (fixed.root / "missing" / fixed.shared.name, fixed.root,
                 fixed.shared / ".." / fixed.shared.name):
        fixed.write({"shared_dir": str(path)})
        with pytest.raises(vm.VMLifecycleError):
            vm.load_settings()


@pytest.mark.parametrize("name", ["owner.json", "lease.lock"])
def test_configured_files_must_already_exist_and_be_regular(fixed, name):
    path = fixed.shared / name
    path.unlink()
    with pytest.raises(vm.VMLifecycleError):
        vm.load_settings()
    assert not path.exists()
    path.mkdir()
    with pytest.raises(vm.VMLifecycleError):
        vm.load_settings()


def test_wrong_or_invalid_owner_is_rejected(fixed):
    for value in ({**fixed.owner, "vm_identity": "other-vm"}, {"vm_identity": fixed.shared.name}, []):
        (fixed.shared / "owner.json").write_text(json.dumps(value), encoding="utf-8")
        with pytest.raises(vm.VMLifecycleError):
            vm.load_settings()


@pytest.mark.parametrize("name", ["owner.json", "lease.lock"])
def test_hardlinked_shared_files_are_rejected(fixed, name):
    os.link(fixed.shared / name, fixed.root / "alias")
    with pytest.raises(vm.VMLifecycleError):
        vm.load_settings()


def test_reparse_ancestor_is_rejected(fixed, monkeypatch):
    original = Path.lstat
    def info(path):
        result = original(path)
        if path == fixed.shared.parent:
            return SimpleNamespace(st_mode=result.st_mode, st_file_attributes=0x400)
        return result
    monkeypatch.setattr(Path, "lstat", info)
    with pytest.raises(vm.VMLifecycleError):
        vm.load_settings()


def test_owner_drift_is_rechecked_after_lock_acquisition(fixed, monkeypatch):
    settings = vm.load_settings()
    original = state.RunLock.__enter__
    def enter(lock):
        result = original(lock)
        (fixed.shared / "owner.json").write_text(json.dumps({**fixed.owner, "vm_identity": "drift"}), encoding="utf-8")
        return result
    with monkeypatch.context() as patch:
        patch.setattr(state.RunLock, "__enter__", enter)
        with pytest.raises(vm.VMLifecycleError):
            with vm.shared_lock(settings, fixed.shared):
                pytest.fail("owner drift was accepted")
    with state.RunLock(fixed.shared / "lease.lock", require_existing=True):
        pass  # A failed post-lock check must release the descriptor/lock.


@pytest.mark.parametrize("consumer", ["guard", "lease", "finalization"])
def test_fixed_consumers_never_recreate_a_missing_lock(fixed, monkeypatch, consumer):
    settings = vm.load_settings()
    monkeypatch.setattr(vm, "load_settings", lambda: settings)
    run_dir = fixed.root / "run"
    run_dir.mkdir()
    (fixed.shared / "lease.lock").unlink()
    before = (fixed.shared / "owner.json").read_bytes()
    with mock.patch.object(vm, "SSHRunner") as ssh, mock.patch.object(vm, "run_hidden") as command:
        with pytest.raises(vm.VMLifecycleError):
            if consumer == "guard":
                with vm.vm_guard():
                    pytest.fail("missing lock accepted")
            elif consumer == "lease":
                with vm.VMLease(run_dir, "release-a"):
                    pytest.fail("missing lock accepted")
            else:
                with vm.RetainedVMFinalization(run_dir, "release-a", "a" * 40, {}, process_identity=lambda _: None):
                    pytest.fail("missing lock accepted")
        ssh.assert_not_called()
        command.assert_not_called()
    assert not (fixed.shared / "lease.lock").exists()
    assert (fixed.shared / "owner.json").read_bytes() == before
    assert list(run_dir.iterdir()) == []


def test_existing_lock_missing_parent_is_not_created(tmp_path):
    path = tmp_path / "missing-parent" / "lease.lock"
    with pytest.raises(FileNotFoundError):
        with state.RunLock(path, require_existing=True):
            pytest.fail("missing lock accepted")
    assert not path.parent.exists()


def test_existing_lock_keeps_inode_and_rejects_a_second_holder(tmp_path):
    path = tmp_path / "lease.lock"
    path.write_bytes(b"\0")
    before = path.stat()
    with state.RunLock(path, require_existing=True) as lock:
        during = os.fstat(lock.descriptor)
        assert (during.st_dev, during.st_ino) == (before.st_dev, before.st_ino)
        with pytest.raises(RuntimeError, match="another release"):
            with state.RunLock(path, require_existing=True):
                pytest.fail("duplicate holder accepted")
    assert lock.descriptor is None
    assert path.stat().st_ino == before.st_ino


@pytest.mark.parametrize("change", ["remove", "replace"])
def test_lock_changed_between_stat_and_open_is_not_created_or_written(tmp_path, monkeypatch, change):
    path = tmp_path / "lease.lock"
    path.write_bytes(b"original")
    original_open = os.open
    def open_existing(target, flags, mode=0o777, **kwargs):
        if Path(target) == path:
            assert flags & os.O_CREAT == 0
            path.rename(tmp_path / "original.lock")
            if change == "replace":
                path.write_bytes(b"replacement")
        return original_open(target, flags, mode, **kwargs)
    with monkeypatch.context() as patch:
        patch.setattr(state.os, "open", open_existing)
        with pytest.raises((OSError, RuntimeError)):
            with state.RunLock(path, require_existing=True):
                pytest.fail("changed lock accepted")
    assert (tmp_path / "original.lock").read_bytes() == b"original"
    assert not path.exists() if change == "remove" else path.read_bytes() == b"replacement"


def test_lock_identity_is_rechecked_after_native_lock(tmp_path, monkeypatch):
    path = tmp_path / "lease.lock"
    path.write_bytes(b"original")
    original = Path.lstat
    calls = []
    def info(target):
        result = original(target)
        if target == path:
            calls.append(target)
            if len(calls) >= 3:
                return SimpleNamespace(st_mode=result.st_mode, st_nlink=result.st_nlink,
                                       st_dev=result.st_dev, st_ino=result.st_ino + 1)
        return result
    with monkeypatch.context() as patch:
        patch.setattr(Path, "lstat", info)
        with pytest.raises(RuntimeError, match="identity_changed"):
            with state.RunLock(path, require_existing=True):
                pytest.fail("changed inode accepted")
    assert path.read_bytes() == b"original"
    with state.RunLock(path, require_existing=True):
        pass


def test_fsync_failure_closes_and_releases_existing_lock(tmp_path, monkeypatch):
    path = tmp_path / "lease.lock"
    path.write_bytes(b"\0")
    lock = state.RunLock(path, require_existing=True)
    with monkeypatch.context() as patch:
        patch.setattr(state.os, "fsync", mock.Mock(side_effect=OSError("injected")))
        with pytest.raises(OSError):
            lock.__enter__()
    assert lock.descriptor is None
    with state.RunLock(path, require_existing=True):
        pass
