"""Exact reviewed source separation and the frozen failed-VM control entry."""
from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden

BASE = "7d4ecf44f99e17dc0fa7b83585862c357695ceff"
TARGET = "8cbb42d9f358875854a6b0d87a44cdb8b84b6ea4"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
OBJECTS = {
    PREFIX + "cli.py": (
        "100644:ce017a5d663037a38b921e427c628b3945e45634",
        "100644:86183919ffd87c78ec9e3d47a110528a13a3b049",
    ),
    PREFIX + "vm-validate.sh": (
        "100755:c03ced74644ef62f61d29850eebcd990dbdea347",
        "100755:11b79feee2ba54575be9642202a59060a01ec5c2",
    ),
    PREFIX + "vm_failed_preserve.py": (
        None, "100644:98224816cff0350d54ac36bcabc413c177e588b9",
    ),
}
PATHS = frozenset(OBJECTS)
REASON = "reviewed_vm_preflight_recovery_changed"


def test_exact_objects_and_only_three_sensitive_paths():
    assert gate._VM_PLAN_SOURCES_REVIEW_PATHS == PATHS
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {p: (old.get(p), new.get(p)) for p in PATHS} == OBJECTS
    assert {p for p in gate._changed_paths(WORKSPACE, BASE, TARGET)
            if gate._is_recovery_sensitive_path(p)} == PATHS
    assert {p for p, reason in gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET).items()
            if reason == REASON} == PATHS


def test_review_requires_specialized_and_all_previous_reviews_remain_fixed():
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)
    source = check_output_hidden(["git", "show", TARGET + ":" + PREFIX + "recovery_gate.py"],
                                 cwd=WORKSPACE, text=True)
    prior = {"__name__": "vm_plan_sources_prior", "__package__": "release"}
    exec(compile(source, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
                 "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate, name) == prior[name]
    assert gate._REVIEWED_SCOPED_TRANCHES == (
        *prior["_REVIEWED_SCOPED_TRANCHES"], (BASE, TARGET, REASON, PATHS))


def report_for(old, new, paths):
    base, future = "b" * 40, "f" * 40
    trees = {BASE: {p: before for p, (before, _) in OBJECTS.items() if before},
             TARGET: {p: after for p, (_, after) in OBJECTS.items()}, base: old, future: new}
    with (mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, sha: trees[sha]),
          mock.patch.object(gate, "_changed_paths", return_value=paths),
          mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, sha: sha in trees),
          mock.patch.object(gate, "_is_ancestor", return_value=True)):
        return gate.classify(WORKSPACE, base, future)


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "base"])
def test_each_source_drift_stays_blocked(path, mutation):
    old = {p: before for p, (before, _) in OBJECTS.items() if before}
    new = {p: after for p, (_, after) in OBJECTS.items()}
    if mutation == "blob":
        new[path] = new[path].split(":")[0] + ":" + "0" * 40
    elif mutation == "mode":
        new[path] = ("100644" if new[path].startswith("100755") else "100755") + ":" + new[path].split(":")[1]
    elif mutation == "delete":
        # Deletion is a future change from the reviewed target, including the
        # newly added helper which did not exist in the original base.
        old = new.copy()
        del new[path]
    elif mutation == "rename":
        old = new.copy()
        new[path + ".moved"] = new.pop(path)
    else:
        old[path] = "100644:" + "0" * 40
    report = report_for(old, new, sorted(set(old) | set(new)))
    assert report["mode"] == "full" and "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


def test_additional_sensitive_path_cannot_borrow_review():
    old = {p: before for p, (before, _) in OBJECTS.items() if before}
    new = {p: after for p, (_, after) in OBJECTS.items()}
    extra = PREFIX + "vm_lifecycle.py"
    old[extra], new[extra] = "100644:" + "1" * 40, "100644:" + "2" * 40
    report = report_for(old, new, sorted(set(old) | set(new)))
    assert report["mode"] == "full"


def test_metadata_descendant_preserves_only_the_frozen_objects():
    old = {p: before for p, (before, _) in OBJECTS.items() if before}
    new = {p: after for p, (_, after) in OBJECTS.items()}
    report = report_for(old, new, sorted(set(old) | set(new)))
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
