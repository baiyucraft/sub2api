from contextlib import contextmanager
from copy import deepcopy
import hashlib
import json
from pathlib import Path
import sys
from unittest.mock import patch

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import full_recovery_session as session


def put(path, data):
    path.write_text(json.dumps(data), encoding="utf-8")


class Site:
    def __init__(self, directory, owner):
        self.directory = directory
        self.owner = owner
        self.foreign_values = {key: "f" * 64 for key in ("docker", "data_dev", "backup", "production")}
        self.changes = {}
        self.diagnostics = {}
        self.last_diagnostics = {}
        self.expected_diagnostics = None
        self.removed = False
        self.locked = False
        self.clean_count = 0

    @contextmanager
    def locks(self):
        assert not self.locked
        self.locked = True
        try:
            yield
        finally:
            self.locked = False

    def observe(self, identity):
        assert self.locked
        self.last_diagnostics = {} if self.removed else self.diagnostics.copy()
        return {"schema": 1, "identity": deepcopy(identity), "local_holder": "exited", "remote_holder": "exited",
                "unit": "inactive", "guest_processes": 0, "mount_references": 0, "process_references": 0,
                "rootfs": None if self.removed else {"path": f"/var/lib/sub2api-full-recovery-drills/{identity['drill_id']}/rootfs",
                "device": 20, "inode": 350, "uid": 0, "gid": 0, "mode": 0o700},
                "production_before_sha256": "f" * 64, "production_now_sha256": "f" * 64, **self.changes}

    def foreign(self):
        assert self.locked
        return self.foreign_values.copy()

    def cleanup(self, rootfs):
        assert self.locked and not self.removed
        assert self.expected_diagnostics == {
            name: {"sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)}
            for name, raw in self.diagnostics.items()
        }
        assert (self.directory / "failed-closure-cleanup-intent.json").exists()
        assert json.loads(self.owner.read_bytes())["lease_status"] == "active"
        self.clean_count += 1
        self.removed = True
        raw = json.dumps({"schema": 1, "status": "deleted", "rootfs": rootfs}).encode()
        (self.directory / "failed-closure-remote-receipt.json").write_bytes(raw)
        return {"schema": 1, "status": "deleted", "rootfs": rootfs,
                "remote_receipt_sha256": hashlib.sha256(raw).hexdigest()}


@pytest.fixture
def plan(tmp_path):
    tmp_path = tmp_path.resolve()
    directory, archive = tmp_path / "run", tmp_path / "archive"
    directory.mkdir(mode=0o700)
    archive.mkdir(mode=0o700)
    owner = tmp_path / "owner.json"
    (directory / "hold-locks.py").write_bytes(b"old holder\n")
    boot = "fc76d570-38d5-4d0d-b327-b3aa223d3ceb"
    target = "a" * 40
    release = "123-123456789012-456-abcdef12"
    drill = "full-dr-7f35fe46-4179-4c79-8a83-d6151e8122cb"
    owner_value = {"schema": 1, "vm_identity": "b" * 64, "vm_boot_id": boot, "lease_status": "active",
                   "lease_token": "d" * 32, "vm_power_status": "running", "initially_running": True,
                   "vm_started_by_release": False, "pid": 658, "process_token": "win:54321",
                   "source_commit": target, "release_id": release}
    put(owner, owner_value)
    remote = {"pid": 245, "starttime": "21875", "boot_id": boot,
              "locks": {"8": {"fd": 8, "mode": "READ", "device": 2, "inode": 930},
                        "9": {"fd": 9, "mode": "WRITE", "device": 2, "inode": 931}}}
    put(directory / "locks-status.json", {"schema": 1, "status": "failed_held", "drill_id": drill,
        "target_commit": target, "pid": 658, "process_token": "win:54321", "vm_boot_id": boot, "remote": remote,
        "holder_sha256": hashlib.sha256(b"old holder\n").hexdigest()})
    put(directory / "drill-intent.json", {"schema": 1, "drill_id": drill, "target_commit": target,
        "source_release_id": release, "bundle_sha256": "c" * 64})
    value = session.inspect_session(directory, owner)
    site = Site(directory, owner)
    diagnostic = {"failure.json": json.dumps({"schema": 1, "status": "blocked", "stage": "restore",
        "error_type": "FullDrillBlocked", "reason_code": "storage_exhausted"}).encode(),
        "restore.started": b"schema=1\nphase=restore\n"}
    site.diagnostics = diagnostic.copy()
    with site.locks():
        digest = session.prepare_failure_archive(directory, value, site.observe(value["identity"]), diagnostic,
                                                 archive_dir=archive, foreign=site.foreign())
    return directory, owner, archive, site, digest


def apply(plan):
    directory, owner, archive, site, digest = plan
    return session.apply_failed_closure(directory, owner, digest, site, archive_dir=archive)


def test_only_owned_root_removed_and_verified_receipt_precedes_owner(plan):
    directory, owner, _, site, _ = plan
    save_owner = session._persist_owner

    def verify_order(path, value):
        receipt = (directory / "failed-closure-receipt.json").read_bytes()
        assert value["full_dr_failure_closure_sha256"] == hashlib.sha256(receipt).hexdigest()
        return save_owner(path, value)

    with patch.object(session, "_persist_owner", side_effect=verify_order):
        first = apply(plan)
        assert apply(plan) == first
    assert first["status"] == "failed_closed"
    assert json.loads(owner.read_bytes())["lease_status"] == "released"
    assert site.clean_count == 1


@pytest.mark.parametrize("changes", [
    {"local_holder": "running"}, {"remote_holder": "unknown"}, {"unit": "active"},
    {"guest_processes": 1}, {"mount_references": 1}, {"process_references": 1}, {"mount_references": False},
    {"production_now_sha256": "a" * 64}, {"identity": {}}, {"rootfs": None},
])
def test_unproven_worker_ownership_or_production_blocks_without_cleanup(plan, changes):
    _, owner, _, site, _ = plan
    site.changes = changes
    before = owner.read_bytes()
    with pytest.raises(session.FullRecoverySessionBlocked):
        apply(plan)
    assert not site.removed and owner.read_bytes() == before


@pytest.mark.parametrize("name", ["owner", "holder", "archive", "foreign"])
def test_before_cleanup_validates_frozen_objects(plan, name):
    directory, owner, archive, site, _ = plan
    if name == "foreign":
        site.foreign_values["data_dev"] = "e" * 64
    else:
        path = {"owner": owner, "holder": directory / "hold-locks.py",
                "archive": archive / "failed-closure-diagnostics.json"}[name]
        path.write_bytes(path.read_bytes() + b"\n")
    previous_owner = owner.read_bytes()
    with pytest.raises(session.FullRecoverySessionBlocked):
        apply(plan)
    assert not site.removed and owner.read_bytes() == previous_owner


def test_mutation_without_verified_receipt_remains_blocked(plan):
    directory, owner, _, site, _ = plan
    write = session.atomic_write

    def break_receipt(path, raw):
        if path.name == "failed-closure-receipt.json":
            raise OSError("simulated storage failure after removal")
        write(path, raw)

    before = owner.read_bytes()
    with patch.object(session, "atomic_write", side_effect=break_receipt), pytest.raises(OSError):
        apply(plan)
    assert site.removed and owner.read_bytes() == before
    assert (directory / "failed-closure-cleanup-intent.json").exists()
    with pytest.raises(session.FullRecoverySessionBlocked):
        apply(plan)
    assert site.clean_count == 1


@pytest.mark.parametrize("change", ["edited", "missing", "added"])
def test_diagnostic_drift_blocks_before_intent(plan, change):
    directory, owner, _, site, _ = plan
    if change == "edited":
        site.diagnostics["restore.started"] += b"\n"
    elif change == "missing":
        del site.diagnostics["restore.started"]
    else:
        site.diagnostics["restore.done"] = b"schema=1\nphase=restore\n"
    before = owner.read_bytes()
    with pytest.raises(session.FullRecoverySessionBlocked, match="closure_diagnostics_changed"):
        apply(plan)
    assert not (directory / "failed-closure-cleanup-intent.json").exists()
    assert site.clean_count == 0 and owner.read_bytes() == before


def test_verified_receipt_allows_owner_write_retry_only(plan):
    _, owner, _, site, _ = plan
    before = owner.read_bytes()
    with patch.object(session, "_persist_owner", side_effect=OSError), pytest.raises(OSError):
        apply(plan)
    assert site.removed and owner.read_bytes() == before
    assert apply(plan)["status"] == "failed_closed"
    assert site.clean_count == 1


@pytest.mark.parametrize("during_replay", [False, True])
def test_remote_receipt_drift_blocks_owner_release(plan, during_replay):
    directory, owner, _, site, _ = plan
    before = owner.read_bytes()
    remote = directory / "failed-closure-remote-receipt.json"
    if during_replay:
        with patch.object(session, "_persist_owner", side_effect=OSError), pytest.raises(OSError):
            apply(plan)
        remote.write_bytes(remote.read_bytes() + b"\n")
    else:
        observe = site.observe

        def drift_after_cleanup(identity):
            if site.removed:
                remote.write_bytes(remote.read_bytes() + b"\n")
            return observe(identity)

        site.observe = drift_after_cleanup
    with pytest.raises(session.FullRecoverySessionBlocked, match="closure_remote_cleanup_receipt_drift"):
        apply(plan)
    assert site.clean_count == 1 and owner.read_bytes() == before
