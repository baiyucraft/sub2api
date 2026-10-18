"""Literal independent review of BuildKit dependency proof recovery changes."""
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
TARGET = "89e105dc1618e05018a26a8053f5a8b710db8e98"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
REASON = "reviewed_buildkit_dependency_proof_changed"
OBJECTS = {
    PREFIX + 'cli.py': (
        '100644:86183919ffd87c78ec9e3d47a110528a13a3b049',
        '100644:fc9cebde1de7a12f05d07ae982e65ab72cbb3f52',
    ),
    PREFIX + 'gate.py': (
        '100644:ec3e2550ef32a182deccfe44b9fd111fb4aad5a0',
        '100644:3b3b137b9a75fe97c1912347e13e3f69b3bd8b30',
    ),
    PREFIX + 'manifest.py': (
        '100644:f50b94e772097fa4b2693fd35cc50eb3e4dac3e1',
        '100644:6dc3802be8628b0ad41dc374373b3c4e9074e46c',
    ),
    PREFIX + 'supervisor.py': (
        '100644:9d30c86fb8cbe51dedd4706f27f0ea52f40e6aba',
        '100644:0d4bedb63eb03f38e291b794e5160ff616294758',
    ),
    PREFIX + 'vm-build-common.sh': (
        None,
        '100644:91acaae7ed7b3a458f3786085d2881344bffdc9b',
    ),
    PREFIX + 'vm-only-validate.sh': (
        '100644:1a4de877481ecc199ddf539272976182eafecee4',
        '100644:14c5941c38a8f72c91fea0e99e7fc58d216c8f57',
    ),
    PREFIX + 'vm-validate.sh': (
        '100755:11b79feee2ba54575be9642202a59060a01ec5c2',
        '100755:e45e1bbd9d972f16823b64054646500c91c5ea90',
    ),
    PREFIX + 'vm_build.py': (
        None,
        '100644:f413f0e679da47f5fb33fbc63d1f8b94ed6b9177',
    ),
    PREFIX + 'vm_only.py': (
        '100644:80f538802e72127ce892143700ae1844fb61b076',
        '100644:144c2b88e60acd9e370b13fc9153beeb10001ee0',
    ),
    PREFIX + 'vm_validate.py': (
        '100644:cd6dc5dbdc8f1c40e205ce5f46202c5f884673aa',
        '100644:875cf09e6caa7195ba0f399616459040a4174147',
    ),
}
PATHS = frozenset(OBJECTS)
NEW_HELPERS = frozenset({PREFIX + "vm-build-common.sh", PREFIX + "vm_build.py"})
TRANCHE = (BASE, TARGET, REASON, PATHS)


def test_exact_ten_objects_and_independent_group_are_frozen():
    assert len(OBJECTS) == 10
    assert gate._BUILDKIT_DEPENDENCY_REVIEW_PATHS == PATHS
    assert {path for path, (before, _) in OBJECTS.items() if before is None} == NEW_HELPERS
    before, after = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    assert {path: (before.get(path), after.get(path)) for path in PATHS} == OBJECTS
    sensitive = {path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
                 if gate._is_recovery_sensitive_path(path)}
    assert PATHS <= sensitive
    # This net production delta also contains previously reviewed profile-274
    # wiring. Only the ten independently reviewed paths receive this reason.
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == REASON} == PATHS
    assert sensitive - reviewed.keys() == set()


def test_all_historical_registry_prefixes_remain_unchanged():
    source = check_output_hidden(
        ["git", "show", TARGET + ":" + PREFIX + "recovery_gate.py"],
        cwd=WORKSPACE, text=True,
    )
    prior = {"__name__": "buildkit_dependency_review_prior", "__package__": "release"}
    exec(compile(source, TARGET + ":recovery_gate.py", "exec"), prior)
    for name in (
        "_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
        "_REVIEWED_MIGRATION_GATE_TRANCHES",
    ):
        assert getattr(gate, name) == prior[name]
    frozen = (*prior["_REVIEWED_SCOPED_TRANCHES"], TRANCHE)
    assert gate._REVIEWED_SCOPED_TRANCHES == frozen
    for name, value in prior.items():
        if name.endswith("_REVIEW_PATHS"):
            assert getattr(gate, name) == value


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
    new[".agents/skills/sub2api-production-deploy/scripts/tests/release/test_buildkit_dependency_recovery_review.py"] = "100644:" + "d" * 40
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
