"""Literal review of isolated probe ownership and the frozen preserve helper."""
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
TARGET = "85d7274703ad07570403e501ecb9b66c8f0218ea"
REGISTRY = "2b3adebac40c86c683999a895a3a765813e8623e"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
HELPER = PREFIX + "vm_fast_fixture_preserve.py"
REASON = "reviewed_vm_fixture_ownership_preserve_changed"
OBJECTS = {
    PREFIX + "cli.py": (
        "100644:c7b1c9b4bb633a7474bc9761b3d9b4f3a710558e",
        "100644:0a28894545be615437e07bc94ae1c4b83cb993ff",
    ),
    PREFIX + "vm-validate.sh": (
        "100755:e45e1bbd9d972f16823b64054646500c91c5ea90",
        "100755:840a1eda0a603c13fa9d941edfddbfee46e81d00",
    ),
    HELPER: (None, "100644:b651bbeb4020a740afb504dddb13e606cefeb3f8"),
}
PATHS = frozenset(OBJECTS)
TRANCHE = (BASE, TARGET, REASON, PATHS)


def source_at(commit):
    return check_output_hidden(
        ["git", "show", commit + ":" + PREFIX + "recovery_gate.py"], cwd=WORKSPACE,
    ).decode("utf-8")


def test_exact_three_objects_and_sensitive_helper_are_literal():
    assert gate._VM_FAST_FIXTURE_PRESERVE_REVIEW_PATHS == PATHS
    assert gate._is_recovery_sensitive_path(HELPER)
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {p: (before.get(p), after.get(p)) for p in PATHS} == OBJECTS
    sensitive = {p for p in gate._changed_paths(WORKSPACE, BASE, TARGET) if gate._is_recovery_sensitive_path(p)}
    assert sensitive == PATHS
    assert gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET) == {p: REASON for p in PATHS}
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    gate.assert_release_allowed(report)


def test_prior_registry_is_frozen_and_ast_only_adds_this_tranche():
    prior = {"__name__": "vm_fast_fixture_prior", "__package__": "release"}
    text = source_at(TARGET)
    exec(compile(text, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES", "_REVIEWED_MIGRATION_GATE_TRANCHES"):
        assert getattr(gate, name) == prior[name]
    frozen = (*prior["_REVIEWED_SCOPED_TRANCHES"], TRANCHE)
    assert gate._REVIEWED_SCOPED_TRANCHES[:len(frozen)] == frozen
    for name, value in prior.items():
        if name.startswith("_") and name.endswith("_PATHS"):
            assert getattr(gate, name) == value
    # Audit the historical registration at its immutable commit. New reviewed
    # tranches must not rewrite the proof for this older incident.
    current = ast.parse(source_at(REGISTRY))
    retained = []
    for node in current.body:
        names = {t.id for t in node.targets if isinstance(t, ast.Name)} if isinstance(node, ast.Assign) else set()
        if "_VM_FAST_FIXTURE_PRESERVE_REVIEW_PATHS" in names:
            continue
        if "_REVIEWED_SCOPED_TRANCHES" in names:
            assert isinstance(node.value, ast.Tuple)
            node.value.elts.pop()
        retained.append(node)
    current.body = retained
    assert ast.dump(current, include_attributes=False) == ast.dump(ast.parse(text), include_attributes=False)


def test_implementation_only_adds_the_literal_sensitive_helper():
    before_source, after_source = source_at(BASE), source_at(TARGET)
    before = {"__name__": "vm_fast_fixture_sensitive_before", "__package__": "release"}
    after = {"__name__": "vm_fast_fixture_sensitive_after", "__package__": "release"}
    exec(compile(before_source, BASE + ":recovery_gate.py", "exec"), before)
    exec(compile(after_source, TARGET + ":recovery_gate.py", "exec"), after)
    assert HELPER == PREFIX + "vm_fast_fixture_preserve.py"
    assert after["_RELEASE_STATE_MACHINE_FILES"] == before["_RELEASE_STATE_MACHINE_FILES"] | {HELPER}
    assert HELPER not in before["_RELEASE_STATE_MACHINE_FILES"]
    for name in ("_RECOVERY_CONTROL_FILES", "_RECOVERY_RUNTIME_FILES", "_SPECIALIZED_FILES"):
        assert after[name] == before[name]

    implementation = ast.parse(after_source)
    matched = 0
    for node in implementation.body:
        names = {t.id for t in node.targets if isinstance(t, ast.Name)} if isinstance(node, ast.Assign) else set()
        if "_RELEASE_STATE_MACHINE_FILES" not in names:
            continue
        assert isinstance(node.value, ast.Call) and isinstance(node.value.func, ast.Name)
        assert node.value.func.id == "frozenset" and len(node.value.args) == 1
        values = node.value.args[0]
        assert isinstance(values, ast.Set)
        matches = [value for value in values.elts if isinstance(value, ast.Constant) and value.value == HELPER]
        assert len(matches) == 1
        values.elts.remove(matches[0])
        matched += 1
    assert matched == 1
    assert ast.dump(implementation, include_attributes=False) == ast.dump(ast.parse(before_source), include_attributes=False)


def before_objects():
    return {p: a for p, (a, _) in OBJECTS.items() if a is not None}


def after_objects():
    return {p: b for p, (_, b) in OBJECTS.items()}


def report_for(old, new, *, source_available=True):
    base, future = "b" * 40, "f" * 40
    trees = {BASE: before_objects(), TARGET: after_objects(), base: old, future: new}
    paths = sorted(p for p in old.keys() | new.keys() if old.get(p) != new.get(p))
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


@pytest.mark.parametrize("mode", ["040000", "120000", "160000"])
def test_new_helper_absence_cannot_match_directory_symlink_or_gitlink(mode):
    old = before_objects(); old[HELPER] = mode + ":" + "0" * 40
    rejected(report_for(old, after_objects()))


@pytest.mark.parametrize("suffix", ["maintenance/release/restore.sh", "release/ssh.py", "release/trust/extra.pub", "release/vm_lifecycle.py", "release/vm_build.py"])
def test_extra_sensitive_path_cannot_borrow_review(suffix):
    old, new = before_objects(), after_objects()
    path = ".agents/skills/sub2api-production-deploy/scripts/" + suffix
    old[path], new[path] = "100644:" + "1" * 40, "100644:" + "2" * 40
    rejected(report_for(old, new))


def test_review_source_must_exist_and_old_delta_gets_no_new_reason():
    rejected(report_for(before_objects(), after_objects(), source_available=False))
    for target in (BASE, "3c945e40fbb2f388efb0a28b47b9e280f6da671d"):
        assert REASON not in gate._reviewed_compatibility_paths(WORKSPACE, BASE, target).values()


def test_exact_objects_in_metadata_descendant_remain_specialized():
    report = report_for(before_objects(), after_objects())
    assert report["mode"] == "specialized" and REASON in report["reason_codes"]
    gate.assert_release_allowed(report)
