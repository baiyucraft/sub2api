"""Reviewed profile wiring must never authorize different recovery bytes."""
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "f984872ebb536c84d1234bbe0d7b1de01d1401d5"
TARGET = "3cbeebf3de3b79a1692f11ab2a2c557642c2624f"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"


@pytest.fixture(scope="module")
def reviewed():
    groups = tuple(group for group in gate._REVIEWED_SCOPED_TRANCHES if group[:2] == (BASE, TARGET))
    assert tuple(len(paths) for _, _, _, paths in groups) == (46, 1)
    paths = set().union(*(paths for _, _, _, paths in groups))
    assert len(paths) == 47
    assert paths == {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                     if gate._is_recovery_sensitive_path(path)}
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    return groups, {p: old[p] for p in paths}, {p: new[p] for p in paths}


def classify(reviewed, old, new):
    groups, original_old, original_new = reviewed
    base, future = "b" * 40, "f" * 40
    trees = {BASE: original_old, TARGET: original_new, base: old, future: new}
    changed = sorted(p for p in old.keys() | new.keys() if old.get(p) != new.get(p))
    with (mock.patch.object(gate, "_tree_blobs", side_effect=lambda _, sha: trees[sha]),
          mock.patch.object(gate, "_changed_paths", return_value=changed),
          mock.patch.object(gate, "_commit_exists", side_effect=lambda _, sha: sha in trees),
          mock.patch.object(gate, "_is_ancestor", return_value=True),
          mock.patch.object(gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_SCOPED_TRANCHES", groups)):
        return gate.classify(WORKSPACE, base, future)


def test_exact_review_requires_real_specialized_gate(reviewed):
    _, old, new = reviewed
    report = classify(reviewed, old, new)
    assert report["mode"] == "specialized"
    assert "reviewed_prepare_gate_policy_changed" in report["reason_codes"]
    assert "reviewed_profile_compatibility_changed" in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


@pytest.mark.parametrize("change", ["prepare-bytes", "mode", "base", "delete", "rename", "extra-restore"])
def test_unreviewed_bytes_modes_and_paths_remain_blocked(reviewed, change):
    _, original_old, original_new = reviewed
    old, new = dict(original_old), dict(original_new)
    path = PREFIX + "maintenance/release/prepare.sh"
    if change == "prepare-bytes":
        new[path] = "100755:" + "0" * 40
    elif change == "mode":
        new[path] = "100644:" + new[path].split(":")[1]
    elif change == "base":
        old[path] = "100755:" + "0" * 40
    elif change == "delete":
        del new[path]
    elif change == "rename":
        new[path + ".moved"] = new.pop(path)
    else:
        restore = PREFIX + "maintenance/release/restore.sh"
        old[restore], new[restore] = "100755:" + "1" * 40, "100755:" + "2" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))
