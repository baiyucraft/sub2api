"""The registry proxy review covers one frozen validator net blob only."""
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "aeb410bccc524164bdafbd3db865d4f5f588aced"
WIRING = "6f5daaaed6e0cbfae2011cfa55b47688f33634eb"
BUILD = "99d9fcf97125ef6efc6543692697e85e27412396"
TARGET = "67545c6d6e02f5210574ced9ea5c366688d28b87"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
VALIDATOR = PREFIX + "release/vm-validate.sh"
PATHS = frozenset({VALIDATOR})
BUILD_PATHS = frozenset(PREFIX + "release/" + name for name in ("cli.py", "supervisor.py", "vm-validate.sh"))
OLD_REVIEWS = (
    (BASE, WIRING, "reviewed_profile_compatibility_changed", gate._PROFILE265_COMPATIBILITY_PATHS),
    (BASE, WIRING, "reviewed_reward_cost_postflight_changed", gate._PROFILE265_POSTFLIGHT_PATHS),
    (BASE, BUILD, "reviewed_vm_preflight_recovery_changed", BUILD_PATHS),
)
REVIEW = (BASE, TARGET, "reviewed_vm_preflight_recovery_changed", PATHS)
REVIEWS = (*OLD_REVIEWS, REVIEW)


class Profile265RegistryProxyReviewTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.old = gate._tree_blobs(WORKSPACE, BASE)
        cls.new = gate._tree_blobs(WORKSPACE, TARGET)
        cls.wiring = gate._tree_blobs(WORKSPACE, WIRING)
        cls.build = gate._tree_blobs(WORKSPACE, BUILD)
        cls.paths = gate._changed_paths(WORKSPACE, BASE, TARGET)

    def test_validator_only_net_blob_mode_and_old_reviews_are_frozen(self):
        self.assertEqual(gate._PROFILE265_REGISTRY_PROXY_REVIEW_PATHS, PATHS)
        self.assertEqual(gate._PROFILE265_BUILD_RECOVERY_REVIEW_PATHS, BUILD_PATHS)
        self.assertEqual(gate._REVIEWED_SCOPED_TRANCHES[:4], REVIEWS)
        self.assertEqual(len(gate._PROFILE265_COMPATIBILITY_PATHS), 35)
        self.assertEqual(len(gate._PROFILE265_POSTFLIGHT_PATHS), 2)
        self.assertEqual(self.old[VALIDATOR], "100755:49dbe1513aafe049a76c5ae7425ae5f8824d73eb")
        self.assertEqual(self.new[VALIDATOR], "100755:242622e8aa9db7d3a911b47031540d4acdc6eef3")
        self.assertEqual(self.build[VALIDATOR], "100755:5b1648b631108c7487c3b2425085a3550a4bbbcf")
        for path in BUILD_PATHS - PATHS:
            self.assertEqual(self.new[path], self.build[path])
        reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
        self.assertEqual(reviewed[VALIDATOR], "reviewed_vm_preflight_recovery_changed")
        sensitive = {path for path in self.paths if gate._is_recovery_sensitive_path(path)}
        self.assertEqual(sensitive, gate._PROFILE265_COMPATIBILITY_PATHS | gate._PROFILE265_POSTFLIGHT_PATHS | BUILD_PATHS)
        self.assertEqual(len(sensitive), 39)
        self.assertTrue(sensitive.issubset(reviewed))
        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
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
        trees = {BASE: self.old, WIRING: self.wiring, BUILD: self.build, TARGET: self.new, future: changed, future_base: observed_old}
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

    def test_validator_rejects_future_blob_mode_delete_move_and_base_drift(self):
        for mutation in ("blob", "mode", "delete", "move", "base_blob"):
            with self.subTest(mutation=mutation):
                self.assert_blocked(VALIDATOR, mutation)

    def test_unregistered_recovery_ingress_and_trust_stay_blocked(self):
        for suffix in (
            "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
            "maintenance/release/reconcile.sh", "maintenance/release/apply-nginx-ingress.sh",
            "release/trust/vm-gate-ed25519.pub", "release/vm_lifecycle.py", "release/production.py",
        ):
            with self.subTest(path=suffix):
                self.assert_blocked(PREFIX + suffix, "blob")
