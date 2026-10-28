"""Literal review of probe connection authority and its frozen preserve path."""
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

BASE = "c88f06e57a829438595263d524e02219aa2f5a2e"
IMPLEMENTATION_PARENT = "12d4cf67d1c666a661458d282fd2284fa3d543eb"
TARGET = "91a13c6c64558f5d78ae8b75979af0e6a24b60b2"
REGISTRY = "3637415ab083105e1533dc17915319eeba9f6f46"
CURRENT_REGISTRY = "ac67368eaeb08d81a430cad58cc55d865e1622f4"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
CONFIG = PREFIX + "vm-probe-config.py"
PRESERVE = PREFIX + "vm_restore_probe_preserve.py"
HELPERS = frozenset({CONFIG, PRESERVE})
REASON = "reviewed_vm_probe_config_preserve_changed"
OBJECTS = {
    PREFIX + "cli.py": (
        "100644:c7b1c9b4bb633a7474bc9761b3d9b4f3a710558e",
        "100644:65ba379d893258d40266799ae4a35b1b2d82b068",
    ),
    PREFIX + "vm-validate.sh": (
        "100755:e45e1bbd9d972f16823b64054646500c91c5ea90",
        "100755:d48b6dc2ab97e2b470b8299318121f212a4f6e93",
    ),
    CONFIG: (None, "100644:2957c1eb31c48e11b73d707bdf553fec15b9809c"),
    PRESERVE: (None, "100644:d39f66afb85783263aafc75747bf03493a190c72"),
}
PATHS = frozenset(OBJECTS)
TRANCHE = (BASE, TARGET, REASON, PATHS)


def source_at(commit):
    return check_output_hidden(
        ["git", "show", commit + ":" + PREFIX + "recovery_gate.py"], cwd=WORKSPACE,
    ).decode("utf-8")


def namespace(source, name):
    values = {"__name__": name, "__package__": "release"}
    exec(compile(source, name, "exec"), values)
    return values


def test_exact_four_objects_and_two_sensitive_helpers_are_literal():
    assert gate._VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS == PATHS
    assert all(gate._is_recovery_sensitive_path(path) for path in HELPERS)
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (before.get(path), after.get(path)) for path in PATHS} == OBJECTS
    assert all(before.get(path) is None for path in HELPERS)
    # Other reviewed production deltas retain their historical reason.
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == REASON} == PATHS
    assert {path: reviewed[path] for path in PATHS} == {path: REASON for path in PATHS}
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    gate.assert_release_allowed(report)


def test_prior_registry_is_frozen_and_current_ast_only_appends_this_tranche():
    text = source_at(TARGET)
    prior = namespace(text, "vm_probe_config_prior")
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES", "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate, name) == prior[name]
    frozen = (*prior["_REVIEWED_SCOPED_TRANCHES"], TRANCHE)
    assert gate._REVIEWED_SCOPED_TRANCHES[:len(frozen)] == frozen
    for name, value in prior.items():
        if name.startswith("_") and name.endswith("_PATHS"):
            assert getattr(gate, name) == value

    # Freeze the complete live module to the pre-merge registry, including its
    # later reviewed tranches, while auditing this incident at its original
    # immutable registration. Neither check admits arbitrary new tail entries.
    live = ast.parse(Path(gate.__file__).read_text(encoding="utf-8"))
    assert ast.dump(live, include_attributes=False) == ast.dump(ast.parse(source_at(CURRENT_REGISTRY)), include_attributes=False)
    current = ast.parse(source_at(REGISTRY))
    retained, removed, appended = [], 0, 0
    for node in current.body:
        names = {target.id for target in node.targets if isinstance(target, ast.Name)} if isinstance(node, ast.Assign) else set()
        if "_VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS" in names:
            removed += 1
            continue
        if "_REVIEWED_SCOPED_TRANCHES" in names:
            assert isinstance(node.value, ast.Tuple)
            expected = ast.parse("(" + repr(BASE) + ", " + repr(TARGET) + ", " + repr(REASON) + ", _VM_PROBE_CONFIG_PRESERVE_REVIEW_PATHS)", mode="eval").body
            assert ast.dump(node.value.elts.pop(), include_attributes=False) == ast.dump(expected, include_attributes=False)
            appended += 1
        retained.append(node)
    assert (removed, appended) == (1, 1)
    current.body = retained
    assert ast.dump(current, include_attributes=False) == ast.dump(ast.parse(text), include_attributes=False)


def test_implementation_only_adds_the_two_literal_sensitive_markers():
    before_source, after_source = source_at(IMPLEMENTATION_PARENT), source_at(TARGET)
    before = namespace(before_source, "vm_probe_config_before")
    after = namespace(after_source, "vm_probe_config_after")
    assert PRESERVE == PREFIX + "vm_restore_probe_preserve.py"
    assert CONFIG == PREFIX + "vm-probe-config.py"
    assert after["_RELEASE_STATE_MACHINE_FILES"] == before["_RELEASE_STATE_MACHINE_FILES"] | {PRESERVE}
    assert after["_RECOVERY_RUNTIME_FILES"] == before["_RECOVERY_RUNTIME_FILES"] | {"vm-probe-config.py"}
    assert PRESERVE not in before["_RELEASE_STATE_MACHINE_FILES"]
    assert "vm-probe-config.py" not in before["_RECOVERY_RUNTIME_FILES"]
    for name in ("_RECOVERY_CONTROL_FILES", "_SPECIALIZED_FILES", "_REVIEWED_SCOPED_TRANCHES"):
        assert after[name] == before[name]

    implementation, matched = ast.parse(after_source), set()
    markers = {"_RELEASE_STATE_MACHINE_FILES": PRESERVE, "_RECOVERY_RUNTIME_FILES": "vm-probe-config.py"}
    for node in implementation.body:
        names = {target.id for target in node.targets if isinstance(target, ast.Name)} if isinstance(node, ast.Assign) else set()
        for name in names & markers.keys():
            assert isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Name)
            assert node.value.func.id == "frozenset" and len(node.value.args) == 1
            values = node.value.args[0]
            assert isinstance(values, ast.Set)
            matches = [value for value in values.elts if isinstance(value, ast.Constant) and value.value == markers[name]]
            assert len(matches) == 1
            values.elts.remove(matches[0])
            matched.add(name)
    assert matched == set(markers)
    assert ast.dump(implementation, include_attributes=False) == ast.dump(ast.parse(before_source), include_attributes=False)


def before_objects():
    return {path: old for path, (old, _) in OBJECTS.items() if old is not None}


def after_objects():
    return {path: new for path, (_, new) in OBJECTS.items()}


def report_for(old, new, *, source_available=True):
    base, future = "b" * 40, "f" * 40
    trees = {BASE: before_objects(), TARGET: after_objects(), base: old, future: new}
    paths = sorted(path for path in old.keys() | new.keys() if old.get(path) != new.get(path))
    with (
        mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, sha: trees[sha]),
        mock.patch.object(gate, "_changed_paths", return_value=paths),
        mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, sha: sha in trees and (source_available or sha != TARGET)),
        mock.patch.object(gate, "_is_ancestor", return_value=True),
    ):
        return gate.classify(WORKSPACE, base, future)


def rejected(report):
    assert report["mode"] == "full" and "recovery_change_requires_review" in report["reason_codes"]
    with pytest.raises(RuntimeError, match="recovery changes require review"):
        gate.assert_release_allowed(gate.require_full(report))


@pytest.mark.parametrize("path", sorted(PATHS))
@pytest.mark.parametrize("mutation", ["blob", "mode", "delete", "rename", "base"])
def test_every_path_rejects_future_and_baseline_drift(path, mutation):
    old, new = before_objects(), after_objects()
    if mutation == "blob": new[path] = new[path].split(":")[0] + ":" + "0" * 40
    elif mutation == "mode": new[path] = ("100644" if new[path].startswith("100755") else "100755") + ":" + new[path].split(":")[1]
    elif mutation == "delete": old = new.copy(); del new[path]
    elif mutation == "rename": old = new.copy(); new[path + ".moved"] = new.pop(path)
    else: old[path] = "100644:" + "0" * 40
    rejected(report_for(old, new))


@pytest.mark.parametrize("path", sorted(HELPERS))
@pytest.mark.parametrize("mode", ["100644", "040000", "120000", "160000"])
def test_new_helper_absence_cannot_match_existing_file_directory_symlink_or_gitlink(path, mode):
    old = before_objects(); old[path] = mode + ":" + "0" * 40
    rejected(report_for(old, after_objects()))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/ssh.py", "release/trust/extra.pub", "release/vm_lifecycle.py", "release/vm_build.py"])
def test_extra_sensitive_path_cannot_borrow_review(suffix):
    old, new = before_objects(), after_objects()
    path = ".agents/skills/sub2api-production-deploy/scripts/" + suffix
    old[path], new[path] = "100644:" + "1" * 40, "100644:" + "2" * 40
    rejected(report_for(old, new))


@pytest.mark.parametrize("path", sorted(PATHS))
def test_identical_object_at_other_directory_cannot_replace_reviewed_path(path):
    old, new = before_objects(), after_objects()
    moved = PREFIX + "other/" + path.rsplit("/", 1)[1]
    new[moved] = new.pop(path)
    base, future = "b" * 40, "f" * 40
    trees = {BASE: before_objects(), TARGET: after_objects(), base: old, future: new}
    with (
        mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, sha: trees[sha]),
        mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, sha: sha in trees),
        mock.patch.object(gate, "_is_ancestor", return_value=True),
    ):
        reviewed = gate._reviewed_compatibility_paths(WORKSPACE, base, future)
    assert path not in reviewed and moved not in reviewed
    # A future relocation deletes the now-existing sensitive canonical path.
    # An absent BASE helper added elsewhere cannot borrow review, but an
    # unrelated new path does not become sensitive merely by sharing its name.
    rejected(report_for(after_objects(), new))


def test_review_source_must_exist_and_old_delta_gets_no_new_reason():
    rejected(report_for(before_objects(), after_objects(), source_available=False))
    for target in (BASE, IMPLEMENTATION_PARENT, "85d7274703ad07570403e501ecb9b66c8f0218ea"):
        assert REASON not in gate._reviewed_compatibility_paths(WORKSPACE, BASE, target).values()


def test_exact_objects_in_metadata_descendant_remain_specialized():
    report = report_for(before_objects(), after_objects())
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    gate.assert_release_allowed(report)
