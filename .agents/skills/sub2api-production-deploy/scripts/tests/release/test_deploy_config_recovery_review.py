"""Exact reviewed release objects; future drift still requires review."""
from pathlib import Path
import sys
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "7c526f7a9666ff7c10ea48c4611bc070eeb0e00f"
TARGET = "76db7bbb1d1f421f7ea6a47408c7525f95fe557b"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"


@pytest.fixture(scope="module")
def reviewed():
    groups = tuple(group for group in gate._REVIEWED_SCOPED_TRANCHES if group[:2] == (BASE, TARGET))
    assert all(before == BASE and after == TARGET for before, after, _, _ in groups)
    assert tuple(len(paths) for _, _, _, paths in groups) == (47, 2, 8)
    paths = set().union(*(paths for _, _, _, paths in groups))
    assert len(paths) == 57
    assert paths == {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                     if gate._is_recovery_sensitive_path(path)}
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    return groups, {path: old[path] for path in paths if path in old}, {path: new[path] for path in paths}


def classify(reviewed, old, new):
    groups, original_old, original_new = reviewed
    base, future = "b" * 40, "f" * 40
    trees = {BASE: original_old, TARGET: original_new, base: old, future: new}
    changed = sorted(path for path in old.keys() | new.keys() if old.get(path) != new.get(path))
    with (mock.patch.object(gate, "_tree_blobs", side_effect=lambda _, sha: trees[sha]),
          mock.patch.object(gate, "_changed_paths", return_value=changed),
          mock.patch.object(gate, "_commit_exists", side_effect=lambda _, sha: sha in trees),
          mock.patch.object(gate, "_is_ancestor", return_value=True),
          mock.patch.object(gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
          mock.patch.object(gate, "_REVIEWED_SCOPED_TRANCHES", groups)):
        return gate.classify(WORKSPACE, base, future)


def test_exact_review_remains_specialized(reviewed):
    _, old, new = reviewed
    report = classify(reviewed, old, new)
    assert report["mode"] == "specialized"
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


@pytest.mark.parametrize("change", ["config-bytes", "lock-mode", "base", "delete", "rename", "extra-restore"])
def test_review_rejects_unreviewed_objects(reviewed, change):
    _, original_old, original_new = reviewed
    old, new = dict(original_old), dict(original_new)
    config, lock = PREFIX + "release/deploy_config.py", PREFIX + "release/vm_lifecycle.py"
    if change == "config-bytes":
        new[config] = "100644:" + "0" * 40
    elif change == "lock-mode":
        new[lock] = "100755:" + new[lock].split(":")[1]
    elif change == "base":
        old[lock] = "100644:" + "0" * 40
    elif change == "delete":
        del new[lock]
    elif change == "rename":
        new[lock + ".moved"] = new.pop(lock)
    else:
        path = PREFIX + "maintenance/release/restore.sh"
        old[path], new[path] = "100755:" + "1" * 40, "100755:" + "2" * 40
    report = classify(reviewed, old, new)
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))
