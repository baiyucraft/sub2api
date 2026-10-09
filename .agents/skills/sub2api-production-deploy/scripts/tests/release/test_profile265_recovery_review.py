"""Frozen profile 265 review does not authorize future recovery changes."""
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE

BASE = "aeb410bccc524164bdafbd3db865d4f5f588aced"
TARGET = "6f5daaaed6e0cbfae2011cfa55b47688f33634eb"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
PROFILE_PATHS = frozenset({
    PREFIX + suffix for suffix in (
        "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
        "maintenance/release/context.sh", "maintenance/release/prepare.sh",
        "maintenance/release/promote-backup.sh", "release/bootstrap_backup_dr_assets.sh",
        "release/bootstrap_vm_signer.sh", "release/gate.py", "release/profiles.py",
        "release/production-recovery-retention-clean.sh", "release/production-space-clean.sh",
        "release/production_cleanup.py", "release/production_recovery_retention.py",
        "release/promote-dr-baseline.sh", "release/sign-dr-evidence.sh", "release/sign-gate.sh",
        "release/vm-only-validate.sh", "release/vm-validate.sh",
    )
} | {
    PREFIX + f"maintenance/release/migration-{number}-assert.sh"
    for number in (195, *range(232, 246), 254, 285)
})
POSTFLIGHT_PATHS = frozenset({
    PREFIX + "maintenance/release/" + name
    for name in ("activity-reward-cost-postflight.sh", "switch.sh")
})
REVIEWS = (
    (BASE, TARGET, "reviewed_profile_compatibility_changed", PROFILE_PATHS),
    (BASE, TARGET, "reviewed_reward_cost_postflight_changed", POSTFLIGHT_PATHS),
)


class Profile265RecoveryReviewTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.old = gate._tree_blobs(WORKSPACE, BASE)
        cls.new = gate._tree_blobs(WORKSPACE, TARGET)
        cls.paths = gate._changed_paths(WORKSPACE, BASE, TARGET)

    def test_exact_paths_and_protected_assets(self):
        paths = gate._changed_paths(WORKSPACE, BASE, TARGET)
        sensitive = {p for p in paths if gate._is_recovery_sensitive_path(p)}
        self.assertEqual(sensitive, PROFILE_PATHS | POSTFLIGHT_PATHS)
        self.assertEqual(len(sensitive), 37)
        self.assertEqual(gate._PROFILE265_COMPATIBILITY_PATHS, PROFILE_PATHS)
        self.assertEqual(gate._PROFILE265_POSTFLIGHT_PATHS, POSTFLIGHT_PATHS)
        self.assertTrue(set(REVIEWS).issubset(gate._REVIEWED_SCOPED_TRANCHES))
        old = gate._tree_blobs(WORKSPACE, BASE)
        new = gate._tree_blobs(WORKSPACE, TARGET)
        for path in sensitive:
            self.assertEqual(old[path].split(":")[0], new[path].split(":")[0], path)
        for suffix in (
            "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
            "maintenance/release/reconcile.sh", "maintenance/release/backup.sh",
            "maintenance/release/apply-nginx-ingress.sh", "release/supervisor.py",
            "release/vm_lifecycle.py", "release/production.py", "release/manifest.py",
            "release/trust/vm-gate-ed25519.pub",
        ):
            path = PREFIX + suffix
            self.assertEqual(old[path], new[path], path)
            self.assertNotIn(path, sensitive)
        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("migration_changed", report["reason_codes"])
        self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
        self.assertIn("reviewed_reward_cost_postflight_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])
        gate.assert_release_allowed(report)

    def assert_blocked_mutation(self, path, mutation):
        old, new = self.old, self.new
        affected = list(self.paths)
        if path not in affected:
            affected.append(path)
        future, future_base = "f" * 40, "e" * 40
        changed, observed_old = dict(new), dict(old)
        mode, identity = new[path].split(":")
        observed_base = BASE
        if mutation == "blob":
            changed[path] = mode + ":" + "0" * 40
        elif mutation == "mode":
            changed[path] = ("100644" if mode == "100755" else "100755") + ":" + identity
        elif mutation == "base_blob":
            observed_base = future_base
            observed_old[path] = mode + ":" + "0" * 40
        else:
            del changed[path]
            if mutation == "move":
                other = PREFIX + "maintenance/release/restore.sh"
                changed[other] = new[path]
                affected.append(other)
        trees = {BASE: old, TARGET: new, future: changed, future_base: observed_old}
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
            report = gate.classify(WORKSPACE, observed_base, future)
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                gate.assert_release_allowed(gate.require_full(report))

    def test_each_path_rejects_future_blob_mode_delete_move_and_base_drift(self):
        for path in sorted(PROFILE_PATHS | POSTFLIGHT_PATHS):
            for mutation in ("blob", "mode", "delete", "move", "base_blob"):
                with self.subTest(path=path, mutation=mutation):
                    self.assert_blocked_mutation(path, mutation)

    def test_unlisted_algorithms_ingress_and_trust_cannot_inherit_review(self):
        for suffix in (
            "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
            "maintenance/release/reconcile.sh", "maintenance/release/apply-nginx-ingress.sh",
            "release/trust/vm-gate-ed25519.pub", "release/production.py",
        ):
            with self.subTest(path=suffix):
                self.assert_blocked_mutation(PREFIX + suffix, "blob")


if __name__ == "__main__":
    unittest.main()
