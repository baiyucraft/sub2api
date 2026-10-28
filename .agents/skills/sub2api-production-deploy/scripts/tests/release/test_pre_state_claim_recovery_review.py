"""Frozen five-path review; future recovery objects still require review."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE


BASE = "f984872ebb536c84d1234bbe0d7b1de01d1401d5"
TARGET = "cd240aaa340bb9fea2b5fcc90df892cbab39a044"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
REASON = "reviewed_pre_state_claim_recovery_changed"
OBJECTS = {
    "maintenance/181/mask-backup-units.sh": ("100755:06c5c4afd3ea635a4cf14578df53e74064c4fa2a", "100755:cdb2354a503c50a5433316e7d22a8ce85a2dc8fc"),
    "maintenance/181/restore-backup-units.sh": ("100755:eefa3472a730f2d5bb3fbf765a41999dd01123c6", "100755:d5c12b305a72d191aee77d768b730d3cd4e1921d"),
    "release/production.py": ("100644:30af9435414f8e6864a9d90d4875bd74c8e48069", "100644:e501f2e8535fca6d1aa2319fb9ab0b09c47d0ac1"),
    "release/supervisor.py": ("100644:352c84ccbcd339783e9ce65dd69f210e3b8c7a24", "100644:9d43ffd764a6c61e239ebef548ad6124c9266281"),
    "release/vm_lifecycle.py": ("100644:538215f02981f2a8908ce8c531b8c462cbf75df2", "100644:5a569ed80d8e0d80403c18c8762196dd1b8a8f86"),
}
PATHS = frozenset(PREFIX + suffix for suffix in OBJECTS)
TRANCHE = (BASE, TARGET, REASON, PATHS)


@pytest.fixture(scope="module")
def reviewed():
    assert len(PATHS) == 5
    assert gate._PRE_STATE_CLAIM_RECOVERY_REVIEW_PATHS == PATHS
    assert tuple(group for group in gate._REVIEWED_SCOPED_TRANCHES if group[:2] == (BASE, TARGET)) == (TRANCHE,)
    expected = {PREFIX + suffix: pair for suffix, pair in OBJECTS.items()}
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (old.get(path), new.get(path)) for path in expected} == expected
    return expected


def classify(reviewed, old, new):
    base, future = "b" * 40, "f" * 40
    trees = {BASE: {path: pair[0] for path, pair in reviewed.items()},
             TARGET: {path: pair[1] for path, pair in reviewed.items()}, base: old, future: new}
    changed = sorted(path for path in old.keys() | new.keys() if old.get(path) != new.get(path))
    with (mock.patch.object(gate, "_tree_blobs", side_effect=lambda _, sha: trees[sha]),
          mock.patch.object(gate, "_changed_paths", return_value=changed),
          mock.patch.object(gate, "_commit_exists", side_effect=lambda _, sha: sha in trees),
          mock.patch.object(gate, "_is_ancestor", return_value=True),
          mock.patch.object(gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_SCOPED_TRANCHES", (TRANCHE,))):
        return gate.classify(WORKSPACE, base, future)


def test_exact_five_objects_are_specialized(reviewed):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    report = classify(reviewed, old, new)
    assert report["mode"] == "specialized"
    assert REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def test_actual_net_fifty_path_closure_is_specialized(reviewed):
    paths = {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET) if gate._is_recovery_sensitive_path(path)}
    assert len(paths) == 50
    matched = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert paths == set(matched)
    assert {path for path, reason in matched.items() if reason == REASON} == PATHS
    assert len({path for path, reason in matched.items() if reason != REASON}) == 45
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized"
    assert REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "base-blob", "base-mode"])
def test_any_object_or_baseline_drift_requires_full_review(reviewed, path, mutation):
    old = {key: pair[0] for key, pair in reviewed.items()}
    new = {key: pair[1] for key, pair in reviewed.items()}
    if mutation == "blob":
        new[path] = new[path].split(":")[0] + ":" + "0" * 40
    elif mutation == "mode":
        new[path] = ("100644" if new[path].startswith("100755") else "100755") + ":" + new[path].split(":")[1]
    elif mutation == "delete":
        old = dict(new)
        del new[path]
    elif mutation == "rename":
        old = dict(new)
        new[path + ".moved"] = new.pop(path)
    elif mutation == "base-blob":
        old[path] = old[path].split(":")[0] + ":" + "0" * 40
    else:
        old[path] = ("100644" if old[path].startswith("100755") else "100755") + ":" + old[path].split(":")[1]
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/trust/unregistered.pub"])
def test_additional_sensitive_paths_cannot_borrow_review(reviewed, suffix):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    old[PREFIX + suffix], new[PREFIX + suffix] = "100644:" + "1" * 40, "100644:" + "2" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]


def test_descendant_test_only_change_keeps_exact_object_review(reviewed):
    old = {path: pair[0] for path, pair in reviewed.items()}
    new = {path: pair[1] for path, pair in reviewed.items()}
    new[PREFIX + "tests/release/test_pre_state_claim_recovery_review.py"] = "100644:" + "3" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "specialized"
    gate.assert_release_allowed(report)
