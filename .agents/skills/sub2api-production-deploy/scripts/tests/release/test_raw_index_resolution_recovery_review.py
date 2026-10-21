"""Literal independent review of raw root-index resolution, with frozen history."""
from __future__ import annotations

import ast
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden

BASE = "5deebb2929c2a694145cc2726e895f01c43d28c1"
TARGET = "32aeb69419151beb04610e7ae4452b955c17bf3e"
PRIOR = "89c2a1ccb1ec1a87d99f36b9f58397b0a2c5ecb9"
REGISTRY = "c88f06e57a829438595263d524e02219aa2f5a2e"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
HELPER = PREFIX + "vm-build-common.sh"
REASON = "reviewed_raw_index_resolution_changed"
OBJECTS = {HELPER: (None, "100644:789b66786b903716d2756b328891688e193ebf33")}
PATHS = frozenset(OBJECTS)
TRANCHE = (BASE, TARGET, REASON, PATHS)


def before_objects():
    return {path: before for path, (before, _) in OBJECTS.items() if before is not None}


def after_objects():
    return {path: after for path, (_, after) in OBJECTS.items()}


def implementation_gate_source():
    return check_output_hidden(
        ["git", "show", TARGET + ":" + PREFIX + "recovery_gate.py"],
        cwd=WORKSPACE,
    ).decode("utf-8")


def test_exact_single_helper_and_production_absence_are_literal():
    assert PATHS == {HELPER}
    assert gate._RAW_INDEX_RESOLUTION_REVIEW_PATHS == PATHS
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (before.get(path), after.get(path)) for path in PATHS} == OBJECTS
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == REASON} == PATHS
    sensitive = {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                 if gate._is_recovery_sensitive_path(path)}
    assert PATHS <= sensitive and not sensitive - reviewed.keys()


def test_all_prior_tranches_and_path_sets_are_frozen():
    prior = {"__name__": "raw_index_resolution_review_prior", "__package__": "release"}
    exec(compile(implementation_gate_source(), TARGET + ":recovery_gate.py", "exec"), prior)
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
                 "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate, name) == prior[name]
    frozen = (*prior["_REVIEWED_SCOPED_TRANCHES"], TRANCHE)
    assert gate._REVIEWED_SCOPED_TRANCHES[:len(frozen)] == frozen
    for name, value in prior.items():
        if name.startswith("_") and name.endswith("_PATHS"):
            assert getattr(gate, name) == value
    assert len(gate._OFFICIAL_REGISTRY_SOURCE_REVIEW_PATHS) == 2
    assert len(gate._WINDOWS_STATE_IO_REVIEW_PATHS) == 14
    assert len(gate._BUILDKIT_DEPENDENCY_REVIEW_PATHS) == 10


def test_classifier_ast_only_adds_one_path_set_and_one_tranche():
    # Audit this historical registration at its immutable commit. Later
    # independently reviewed tranches must not rewrite the original proof.
    registered = check_output_hidden(
        ["git", "show", REGISTRY + ":" + PREFIX + "recovery_gate.py"],
        cwd=WORKSPACE,
    ).decode("utf-8")
    current = ast.parse(registered)
    prior = ast.parse(implementation_gate_source())
    retained = []
    for node in current.body:
        names = {target.id for target in node.targets if isinstance(target, ast.Name)} if isinstance(node, ast.Assign) else set()
        if "_RAW_INDEX_RESOLUTION_REVIEW_PATHS" in names:
            continue
        if "_REVIEWED_SCOPED_TRANCHES" in names:
            assert isinstance(node.value, ast.Tuple)
            node.value.elts.pop()
        retained.append(node)
    current.body = retained
    assert ast.dump(current, include_attributes=False) == ast.dump(prior, include_attributes=False)


def test_actual_delta_is_specialized_and_python_dto_keeps_its_prior_reason():
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert reviewed[PREFIX + "vm_build.py"] == "reviewed_official_registry_source_changed"


def report_for(old, new, *, source_available=True):
    """Exact objects may pass in a metadata descendant; ancestry grants nothing."""
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


@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "baseline"])
def test_helper_rejects_future_bytes_mode_deletion_rename_and_baseline_drift(mutation):
    old, new = before_objects(), after_objects()
    if mutation == "blob":
        new[HELPER] = "100644:" + "0" * 40
    elif mutation == "mode":
        new[HELPER] = "100755:" + new[HELPER].split(":")[1]
    elif mutation == "delete":
        old[HELPER] = new.pop(HELPER)
    elif mutation == "rename":
        old[HELPER] = new[HELPER]
        new[HELPER + ".moved"] = new.pop(HELPER)
    else:
        old[HELPER] = "100644:" + "0" * 40
    assert_rejected(report_for(old, new))


@pytest.mark.parametrize("mode", ["040000", "120000", "160000"])
def test_before_absence_cannot_match_a_directory_symlink_or_gitlink(mode):
    old, new = before_objects(), after_objects()
    old[HELPER] = mode + ":" + "0" * 40
    assert_rejected(report_for(old, new))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/ssh.py",
                                     "release/trust/extra.pub", "release/vm_lifecycle.py",
                                     "release/vm_build.py"])
def test_other_sensitive_assets_cannot_borrow_this_single_path_review(suffix):
    old, new = before_objects(), after_objects()
    path = ".agents/skills/sub2api-production-deploy/scripts/" + suffix
    old[path], new[path] = "100644:" + "1" * 40, "100644:" + "2" * 40
    assert_rejected(report_for(old, new))


def test_missing_reviewed_source_commit_is_rejected():
    assert_rejected(report_for(before_objects(), after_objects(), source_available=False))


def test_exact_metadata_descendant_accepts_only_the_reviewed_helper():
    report = report_for(before_objects(), after_objects())
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


@pytest.mark.parametrize("prior", [PRIOR,
    "3dcb4c0de414bfc2165b55722e22599287a20b80",
    "f2bd040a6d51156f52202e25f8d444f2ebfeb142",
    "89e105dc1618e05018a26a8053f5a8b710db8e98"])
def test_historical_helper_blobs_never_receive_the_new_reason(prior):
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, prior)
    assert not {path for path, reason in reviewed.items() if reason == REASON}


def test_exact_official_source_history_retains_its_original_reason():
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, PRIOR)
    assert reviewed[HELPER] == "reviewed_official_registry_source_changed"
    assert reviewed[PREFIX + "vm_build.py"] == "reviewed_official_registry_source_changed"
