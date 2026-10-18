"""Literal independent review of Windows replaceable state I/O recovery changes."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden


BASE = "5deebb2929c2a694145cc2726e895f01c43d28c1"
TARGET = "48d2f9660f51b9037f6875fffcb6e51bb280ce29"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
REASON = "reviewed_windows_state_io_changed"
OBJECTS = {
    PREFIX + 'atomic.py': (
        '100644:1094f2ec9e986ae589c18aa2bc8c0ba59ac6684f',
        '100644:b2dd92c438bd03299fe33ca60d8a51b43749c010',
    ),
    PREFIX + 'cli.py': (
        '100644:86183919ffd87c78ec9e3d47a110528a13a3b049',
        '100644:c7b1c9b4bb633a7474bc9761b3d9b4f3a710558e',
    ),
    PREFIX + 'finalization.py': (
        '100644:2afc5725f3abb07fa3006bb2cefbf3cf3cc6be8f',
        '100644:09a89647d4da305a83408c8aa300ab06be2569fb',
    ),
    PREFIX + 'manifest.py': (
        '100644:f50b94e772097fa4b2693fd35cc50eb3e4dac3e1',
        '100644:86d82cea49a5d12d72bf8e8cc9d6086b4895708c',
    ),
    PREFIX + 'plugin_state.py': (
        '100644:c9ff9cec3576dbbefc59d8349c14bfa96e036c58',
        '100644:46fbe2f23f7572cbe1d6efb33ab9a8fe280274b0',
    ),
    PREFIX + 'plugin_supervisor.py': (
        '100644:584749fb3e8fddcaf05de0fcc5a6b1aadc22afe3',
        '100644:1f25cb833b03be4a66509642753b313242a6dde6',
    ),
    PREFIX + 'production_cleanup.py': (
        '100644:c605c4a911a7b936251c40096a7737d810ad3f8d',
        '100644:9a3f43cac80d87e8c4b608f04a0cb4e359db74f0',
    ),
    PREFIX + 'state.py': (
        '100644:accfee3168bcfd9c1bb9a405f462a71c401c850b',
        '100644:7190a76bd225a80cc2be48afb651a8abfa1c925f',
    ),
    PREFIX + 'supervisor.py': (
        '100644:9d30c86fb8cbe51dedd4706f27f0ea52f40e6aba',
        '100644:2c0830535cc87de0b3b3ab36c6bcfb47fc0c268d',
    ),
    PREFIX + 'terminal_receipt.py': (
        None,
        '100644:b851b250493d0ecd929fab75b9ef20e138fea2be',
    ),
    PREFIX + 'vm_failed_preserve.py': (
        '100644:98224816cff0350d54ac36bcabc413c177e588b9',
        '100644:403713db57575ae411b0febe4898e7870919a3af',
    ),
    PREFIX + 'vm_lifecycle.py': (
        '100644:d3e65faf323b9fd59dbff11cf1b29612a7dd44b0',
        '100644:29ee7862baf0b9c9e276c267b719457d5a6d2687',
    ),
    PREFIX + 'vm_validate.py': (
        '100644:cd6dc5dbdc8f1c40e205ce5f46202c5f884673aa',
        '100644:236bc215c05b2667b26c6c5f8cee90c9d8f0646b',
    ),
    PREFIX + 'worker_errors.py': (
        None,
        '100644:475089c5d7a7005d843d6c09f4f0bb82afb2ada4',
    ),
}
PATHS = frozenset(OBJECTS)
NEW_HELPERS = frozenset({PREFIX + "worker_errors.py", PREFIX + "terminal_receipt.py"})
TRANCHE = (BASE, TARGET, REASON, PATHS)


def test_exact_fourteen_objects_and_independent_group_are_frozen():
    assert len(OBJECTS) == 14
    assert gate._WINDOWS_STATE_IO_REVIEW_PATHS == PATHS
    assert NEW_HELPERS <= gate._RECOVERY_CONTROL_FILES
    assert all(gate._is_recovery_sensitive_path(path) for path in PATHS)
    assert {path for path, (before, _) in OBJECTS.items() if before is None} == NEW_HELPERS
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (before.get(path), after.get(path)) for path in PATHS} == OBJECTS
    sensitive = {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                 if gate._is_recovery_sensitive_path(path)}
    assert PATHS <= sensitive
    # This net production delta also contains previously reviewed profile-274
    # wiring. Only the fourteen independently reviewed paths receive this reason.
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == REASON} == PATHS
    assert sensitive - reviewed.keys() == set()


def test_all_historical_registry_prefixes_remain_unchanged():
    source = check_output_hidden(
        ["git", "show", TARGET + ":" + PREFIX + "recovery_gate.py"],
        cwd=WORKSPACE, text=True,
    )
    prior = {"__name__": "windows_state_review_prior", "__package__": "release"}
    exec(compile(source, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in (
        "_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
        "_REVIEWED_MIGRATION_GATE_TRANCHES",
    ):
        assert getattr(gate, name) == prior[name]
    frozen = (*prior["_REVIEWED_SCOPED_TRANCHES"], TRANCHE)
    assert gate._REVIEWED_SCOPED_TRANCHES[:len(frozen)] == frozen
    for name, value in prior.items():
        if name.endswith("_PATHS"):
            assert getattr(gate, name) == value
    assert gate._RECOVERY_CONTROL_FILES == prior["_RECOVERY_CONTROL_FILES"] | NEW_HELPERS
    for name in ("_RELEASE_STATE_MACHINE_FILES", "_RECOVERY_RUNTIME_FILES", "_SPECIALIZED_FILES"):
        assert getattr(gate, name) == prior[name]


def test_actual_review_is_specialized_and_does_not_waive_recovery():
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def before_objects():
    return {path: before for path, (before, _) in OBJECTS.items() if before is not None}


def after_objects():
    return {path: after for path, (_, after) in OBJECTS.items()}


def report_for(old, new):
    """Model a metadata descendant; ancestry cannot authorize changed bytes."""
    base, future = "b" * 40, "f" * 40
    old, new = dict(old), dict(new)
    metadata = PREFIX + "recovery_gate.py"
    old[metadata], new[metadata] = "100644:" + "9" * 40, "100644:" + "e" * 40
    new[".agents/skills/sub2api-production-deploy/scripts/tests/release/test_windows_state_recovery_review.py"] = "100644:" + "d" * 40
    trees = {BASE: before_objects(), TARGET: after_objects(), base: old, future: new}
    paths = sorted(path for path in old.keys() | new.keys() if old.get(path) != new.get(path))
    with (
        mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, sha: trees[sha]),
        mock.patch.object(gate, "_changed_paths", return_value=paths),
        mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, sha: sha in trees),
        mock.patch.object(gate, "_is_ancestor", return_value=True),
    ):
        return gate.classify(WORKSPACE, base, future)


def assert_review_rejected(report):
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "base"])
def test_every_path_rejects_blob_mode_deletion_rename_and_baseline_drift(path, mutation):
    old, new = before_objects(), after_objects()
    if mutation == "blob":
        new[path] = new[path].split(":")[0] + ":" + "0" * 40
    elif mutation == "mode":
        mode, blob = new[path].split(":")
        new[path] = ("100644" if mode == "100755" else "100755") + ":" + blob
    elif mutation == "delete":
        # A deletion has the reviewed target object as its immediate before
        # value, including for helpers absent from the original production base.
        old[path] = new.pop(path)
    elif mutation == "rename":
        old[path] = new[path]
        new[path + ".moved"] = new.pop(path)
    else:
        old[path] = "100644:" + "0" * 40
    assert_review_rejected(report_for(old, new))


@pytest.mark.parametrize("suffix", [
    "maintenance/release/restore.sh", "release/ssh.py", "release/trust/extra.pub",
    "release/vm_lifecycle.py",
])
def test_additional_sensitive_path_cannot_borrow_the_review(suffix):
    old, new = before_objects(), after_objects()
    extra = ".agents/skills/sub2api-production-deploy/scripts/" + suffix
    old[extra], new[extra] = "100644:" + "1" * 40, "100644:" + "2" * 40
    assert_review_rejected(report_for(old, new))


@pytest.mark.parametrize("path", sorted(NEW_HELPERS))
@pytest.mark.parametrize("mode", ["040000", "120000", "160000"])
def test_new_helper_requires_absence_not_an_existing_nonregular_object(path, mode):
    old, new = before_objects(), after_objects()
    old[path] = mode + ":" + "0" * 40
    assert_review_rejected(report_for(old, new))


def test_metadata_descendant_only_accepts_the_exact_reviewed_objects():
    report = report_for(before_objects(), after_objects())
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)
