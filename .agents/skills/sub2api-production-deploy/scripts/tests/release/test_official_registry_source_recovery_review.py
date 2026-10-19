"""Literal independent review of official registry source selection only."""
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
TARGET = "3dcb4c0de414bfc2165b55722e22599287a20b80"
PRIOR = "f2bd040a6d51156f52202e25f8d444f2ebfeb142"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
REASON = "reviewed_official_registry_source_changed"
OBJECTS = {'.agents/skills/sub2api-production-deploy/scripts/release/vm-build-common.sh': (None,
                                                                                 '100644:5db51d982eac1984c42993fa4e14ef366ce20565'),
 '.agents/skills/sub2api-production-deploy/scripts/release/vm_build.py': (None,
                                                                          '100644:00bca4534ea51a5152cfbced4b11b855a910ba27')}
PATHS = frozenset(OBJECTS)
TRANCHE = (BASE, TARGET, REASON, PATHS)


def before_objects():
    return {path: before for path, (before, _) in OBJECTS.items() if before is not None}


def after_objects():
    return {path: after for path, (_, after) in OBJECTS.items()}


def test_exact_two_source_objects_and_production_absence_are_frozen():
    assert PATHS == {PREFIX + "vm-build-common.sh", PREFIX + "vm_build.py"}
    assert gate._OFFICIAL_REGISTRY_SOURCE_REVIEW_PATHS == PATHS
    assert all(before is None for before, _ in OBJECTS.values())
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (before.get(path), after.get(path)) for path in PATHS} == OBJECTS
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == REASON} == PATHS
    sensitive = {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                 if gate._is_recovery_sensitive_path(path)}
    assert PATHS <= sensitive and not sensitive - reviewed.keys()


def test_every_historical_literal_group_and_path_set_is_unchanged():
    source = check_output_hidden(["git", "show", TARGET + ":" + PREFIX + "recovery_gate.py"],
                                 cwd=WORKSPACE, text=True)
    prior = {"__name__": "official_registry_source_review_prior", "__package__": "release"}
    exec(compile(source, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
                 "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate, name) == prior[name]
    frozen = prior["_REVIEWED_SCOPED_TRANCHES"]
    assert gate._REVIEWED_SCOPED_TRANCHES[:len(frozen)] == frozen
    assert gate._REVIEWED_SCOPED_TRANCHES[len(frozen)] == TRANCHE
    for name, value in prior.items():
        if name.startswith("_") and name.endswith("_PATHS"):
            assert getattr(gate, name) == value


def test_actual_delta_remains_specialized_without_waiving_recovery():
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def report_for(old, new, *, source_available=True):
    base, future = "b" * 40, "f" * 40
    old, new = dict(old), dict(new)
    metadata = PREFIX + "recovery_gate.py"
    old[metadata], new[metadata] = "100644:" + "9" * 40, "100644:" + "e" * 40
    trees = {BASE: before_objects(), TARGET: after_objects(), base: old, future: new}
    paths = sorted(path for path in old.keys() | new.keys() if old.get(path) != new.get(path))
    with (
        mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, sha: trees[sha]),
        mock.patch.object(gate, "_changed_paths", return_value=paths),
        mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, sha: sha in trees and (source_available or sha != TARGET)),
        mock.patch.object(gate, "_is_ancestor", return_value=True),
    ):
        return gate.classify(WORKSPACE, base, future)


def assert_rejected(report):
    assert report["mode"] == "full"
    assert "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "baseline"])
def test_each_path_rejects_any_unreviewed_object_or_baseline_change(path, mutation):
    old, new = before_objects(), after_objects()
    if mutation == "blob":
        new[path] = "100644:" + "0" * 40
    elif mutation == "mode":
        new[path] = "100755:" + new[path].split(":")[1]
    elif mutation == "delete":
        old[path] = new.pop(path)
    elif mutation == "rename":
        old[path] = new[path]
        new[path + ".moved"] = new.pop(path)
    else:
        old[path] = "100644:" + "0" * 40
    assert_rejected(report_for(old, new))


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mode", ["040000", "120000", "160000"])
def test_initially_absent_source_cannot_match_existing_nonblob_object(path, mode):
    old, new = before_objects(), after_objects()
    old[path] = mode + ":" + "0" * 40
    assert_rejected(report_for(old, new))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/ssh.py",
                                     "release/trust/extra.pub", "release/vm_lifecycle.py"])
def test_additional_sensitive_changes_cannot_borrow_source_review(suffix):
    old, new = before_objects(), after_objects()
    path = ".agents/skills/sub2api-production-deploy/scripts/" + suffix
    old[path], new[path] = "100644:" + "1" * 40, "100644:" + "2" * 40
    assert_rejected(report_for(old, new))


def test_missing_reviewed_implementation_commit_is_rejected():
    assert_rejected(report_for(before_objects(), after_objects(), source_available=False))


def test_exact_metadata_descendant_accepts_only_registered_source_objects():
    report = report_for(before_objects(), after_objects())
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def test_prior_source_blobs_do_not_receive_the_new_official_source_reason():
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, PRIOR)
    assert not {path for path, reason in reviewed.items() if reason == REASON}
