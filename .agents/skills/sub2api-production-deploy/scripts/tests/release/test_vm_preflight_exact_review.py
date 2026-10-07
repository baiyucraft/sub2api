from __future__ import annotations

import sys
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "0898fe8c6289f87a1a4d90cf74a50e296689960e"
TARGET = "dc464325a572c275c178632aeb847437169b3c58"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/release/"
PATHS = {PREFIX + x for x in ("cli.py", "supervisor.py", "vm_validate.py", "vm-space-clean.sh")}


def test_final_vm_preflight_review_is_exact_and_preserves_other_contracts():
    reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
    assert {path for path, reason in reviewed.items() if reason == "reviewed_vm_preflight_recovery_changed"} == PATHS
    assert gate._is_recovery_sensitive_path(PREFIX + "vm-space-clean.sh")
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    for path in PATHS:
        assert old[path] != new[path]
        assert old[path].split(":")[0] == new[path].split(":")[0]
    report = gate.classify(WORKSPACE, BASE, TARGET)
    assert report["mode"] == "specialized"
    assert "reviewed_vm_preflight_recovery_changed" in report["reason_codes"]
    assert "recovery_change_requires_review" not in report["reason_codes"]
    gate.assert_release_allowed(report)


def test_drift_of_each_reviewed_path_cannot_inherit_review():
    old, new = gate._tree_blobs(WORKSPACE, BASE), gate._tree_blobs(WORKSPACE, TARGET)
    future = "f" * 40
    for path in PATHS:
        for changed in ("100644:" + "0" * 40, "100755:" + new[path].split(":")[1], None):
            drift = dict(new)
            if changed is None:
                drift.pop(path)
            else:
                drift[path] = changed
            trees = {BASE: old, TARGET: new, future: drift}
            with (
                mock.patch.object(gate, "_tree_blobs", side_effect=lambda _workspace, sha: trees[sha]),
                mock.patch.object(gate, "_commit_exists", side_effect=lambda _workspace, sha: sha in trees),
            ):
                assert path not in gate._reviewed_compatibility_paths(WORKSPACE, BASE, future)
