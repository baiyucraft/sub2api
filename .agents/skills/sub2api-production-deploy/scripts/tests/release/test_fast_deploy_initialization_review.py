"""Freeze the independently reviewed optional-input initialization only."""
from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden


BASE = "8544cef6f5e87f29929465316467b2765350cd52"
TARGET = "a0f7ca7666ddd2e6485536be7a8ef6696344c0ac"
PATH = ".agents/skills/sub2api-production-deploy/scripts/release/cli.py"
BEFORE = "100644:1005635c3406dc73d42d95a0f19123d1e151b778"
AFTER = "100644:ce017a5d663037a38b921e427c628b3945e45634"
REASON = "reviewed_vm_preflight_recovery_changed"
EXTRA = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"


class FastDeployInitializationReviewTest(unittest.TestCase):
    def test_fixed_single_path_and_independent_git_objects(self):
        self.assertEqual(gate._FAST_DEPLOY_INITIALIZATION_REVIEW_PATHS, frozenset({PATH}))
        self.assertEqual(
            tuple(item for item in gate._REVIEWED_SCOPED_TRANCHES if item[:2] == (BASE, TARGET)),
            ((BASE, TARGET, REASON, frozenset({PATH})),),
        )
        self.assertEqual(gate._tree_blobs(WORKSPACE, BASE)[PATH], BEFORE)
        self.assertEqual(gate._tree_blobs(WORKSPACE, TARGET)[PATH], AFTER)
        sensitive = {
            path for path in gate._changed_paths(WORKSPACE, BASE, TARGET)
            if gate._is_recovery_sensitive_path(path)
        }
        self.assertEqual(sensitive, {PATH})

    def test_actual_review_requires_specialized_not_fast(self):
        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertIn(REASON, report["reason_codes"])
        self.assertIn("release_state_machine_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])
        gate.assert_release_allowed(report)

    def test_all_prior_registrations_are_unchanged_and_only_one_is_appended(self):
        source = check_output_hidden(
            ["git", "show", TARGET + ":.agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py"],
            cwd=WORKSPACE, text=True,
        )
        prior = {"__name__": "fast_initialization_pinned_prior_reviews", "__package__": "release"}
        exec(compile(source, TARGET + ":recovery_gate.py", "exec"), prior)
        for name in ("_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES", "_REVIEWED_MIGRATION_GATE_TRANCHES"):
            self.assertEqual(getattr(gate, name), prior[name])
        self.assertEqual(
            gate._REVIEWED_SCOPED_TRANCHES,
            (*prior["_REVIEWED_SCOPED_TRANCHES"], (BASE, TARGET, REASON, frozenset({PATH}))),
        )

    def _classify_mutation(self, before, after, paths):
        observed_base, future = "b" * 40, "f" * 40
        trees = {BASE: {PATH: BEFORE}, TARGET: {PATH: AFTER}, observed_base: before, future: after}
        with (
            mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
            mock.patch.object(gate, "_changed_paths", return_value=paths),
            mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, commit: commit in trees),
            mock.patch.object(gate, "_is_ancestor", return_value=True),
        ):
            return gate.classify(WORKSPACE, observed_base, future)

    def _assert_blocked(self, before, after, paths):
        report = self._classify_mutation(before, after, paths)
        self.assertEqual(report["mode"], "full")
        self.assertIn("recovery_change_requires_review", report["reason_codes"])
        for candidate in (report, gate.require_full(report)):
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                gate.assert_release_allowed(candidate)

    def test_future_blob_and_mode_drift_are_blocked(self):
        for identity in ("100644:" + "0" * 40, "100755:" + AFTER.split(":")[1]):
            with self.subTest(identity=identity):
                self._assert_blocked({PATH: BEFORE}, {PATH: identity}, [PATH])

    def test_deletion_and_rename_are_blocked(self):
        self._assert_blocked({PATH: BEFORE}, {}, [PATH])
        moved = PATH + ".moved"
        self._assert_blocked({PATH: BEFORE}, {moved: AFTER}, [PATH, moved])

    def test_extra_sensitive_path_cannot_borrow_the_review(self):
        self._assert_blocked({PATH: BEFORE}, {PATH: AFTER, EXTRA: AFTER}, [PATH, EXTRA])

    def test_base_blob_and_mode_drift_are_blocked(self):
        for identity in ("100644:" + "0" * 40, "100755:" + BEFORE.split(":")[1]):
            with self.subTest(identity=identity):
                self._assert_blocked({PATH: identity}, {PATH: AFTER}, [PATH])

    def test_same_objects_survive_metadata_only_descendant_commit(self):
        report = self._classify_mutation({PATH: BEFORE}, {PATH: AFTER}, [PATH])
        self.assertEqual(report["mode"], "specialized")
        self.assertIn(REASON, report["reason_codes"])


if __name__ == "__main__":
    unittest.main()
