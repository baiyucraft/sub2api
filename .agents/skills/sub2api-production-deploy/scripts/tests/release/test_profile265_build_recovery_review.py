"""The reviewed build/preserve net blobs grant no future recovery exemption."""
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "aeb410bccc524164bdafbd3db865d4f5f588aced"
LEGACY = "6f5daaaed6e0cbfae2011cfa55b47688f33634eb"
TARGET = "99d9fcf97125ef6efc6543692697e85e27412396"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
PATHS = frozenset(PREFIX + "release/" + name for name in ("cli.py", "supervisor.py", "vm-validate.sh"))
REVIEW = (BASE, TARGET, "reviewed_vm_preflight_recovery_changed", PATHS)
REVIEWS = (
    (BASE, LEGACY, "reviewed_profile_compatibility_changed", gate._PROFILE265_COMPATIBILITY_PATHS),
    (BASE, LEGACY, "reviewed_reward_cost_postflight_changed", gate._PROFILE265_POSTFLIGHT_PATHS),
    REVIEW,
)


class Profile265BuildRecoveryReviewTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.old = gate._tree_blobs(WORKSPACE, BASE)
        cls.new = gate._tree_blobs(WORKSPACE, TARGET)
        cls.legacy = gate._tree_blobs(WORKSPACE, LEGACY)
        cls.paths = gate._changed_paths(WORKSPACE, BASE, TARGET)

    def test_exact_net_paths_blobs_modes_and_legacy_reviews(self):
        self.assertEqual(gate._PROFILE265_BUILD_RECOVERY_REVIEW_PATHS, PATHS)
        self.assertTrue(set(REVIEWS).issubset(gate._REVIEWED_SCOPED_TRANCHES))
        self.assertEqual(len(gate._PROFILE265_COMPATIBILITY_PATHS), 35)
        self.assertEqual(len(gate._PROFILE265_POSTFLIGHT_PATHS), 2)
        sensitive = {path for path in self.paths if gate._is_recovery_sensitive_path(path)}
        self.assertEqual(sensitive, gate._PROFILE265_COMPATIBILITY_PATHS | gate._PROFILE265_POSTFLIGHT_PATHS | PATHS)
        self.assertEqual(len(sensitive), 39)
        expected = {
            "cli.py": "100644:ac87787e5dcee414d5cccd1b0eaa8bf0eb02af21",
            "supervisor.py": "100644:ca14ebd152004d4e631622b92107c1438aa63628",
            "vm-validate.sh": "100755:5b1648b631108c7487c3b2425085a3550a4bbbcf",
        }
        for name, identity in expected.items():
            path = PREFIX + "release/" + name
            self.assertEqual(self.new[path], identity)
            self.assertEqual(self.old[path].split(":")[0], identity.split(":")[0])
        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_vm_preflight_recovery_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])
        gate.assert_release_allowed(report)

    def assert_blocked(self, path, mutation):
        changed, observed_old = dict(self.new), dict(self.old)
        affected = list(self.paths)
        if path not in affected:
            affected.append(path)
        future, future_base = "f" * 40, "e" * 40
        mode, identity = self.new[path].split(":")
        base = BASE
        if mutation == "blob":
            changed[path] = mode + ":" + "0" * 40
        elif mutation == "mode":
            changed[path] = ("100755" if mode == "100644" else "100644") + ":" + identity
        elif mutation == "base_blob":
            base = future_base
            observed_old[path] = mode + ":" + "0" * 40
        else:
            del changed[path]
            if mutation == "move":
                destination = PREFIX + "maintenance/release/restore.sh"
                changed[destination] = self.new[path]
                affected.append(destination)
        trees = {BASE: self.old, LEGACY: self.legacy, TARGET: self.new, future: changed, future_base: observed_old}
        with (
            mock.patch.object(gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
            mock.patch.object(gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
            mock.patch.object(gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
            mock.patch.object(gate, "_REVIEWED_SCOPED_TRANCHES", REVIEWS),
            mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
            mock.patch.object(gate, "_changed_paths", return_value=affected),
            mock.patch.object(gate, "_commit_exists", return_value=True),
            mock.patch.object(gate, "_is_ancestor", return_value=True),
        ):
            report = gate.classify(WORKSPACE, base, future)
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                gate.assert_release_allowed(gate.require_full(report))

    def test_each_path_rejects_blob_mode_delete_move_and_base_drift(self):
        for path in sorted(PATHS):
            for mutation in ("blob", "mode", "delete", "move", "base_blob"):
                with self.subTest(path=path, mutation=mutation):
                    self.assert_blocked(path, mutation)

    def test_unregistered_recovery_ingress_and_trust_stay_blocked(self):
        for suffix in (
            "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
            "maintenance/release/reconcile.sh", "maintenance/release/apply-nginx-ingress.sh",
            "release/trust/vm-gate-ed25519.pub", "release/vm_lifecycle.py", "release/production.py",
        ):
            with self.subTest(path=suffix):
                self.assert_blocked(PREFIX + suffix, "blob")


if __name__ == "__main__":
    unittest.main()
