"""Freeze the reviewed 269 net objects while retaining real restore requirements.

All identities below are from the reviewed production and candidate commits.
The tests neither discover future review scope nor use local .tmp evidence.
"""
from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import recovery_gate as gate
from release.paths import WORKSPACE
from release.process import check_output_hidden


BASE = "c4d857235cecdc3458c21b5eec9411f1fd115674"
TARGET = "f14ca8f65e3526370bf24a2fa0bca3ec490448ab"
PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
PROFILE_PATHS = frozenset(PREFIX + suffix for suffix in (
    "maintenance/181/mask-backup-units.sh",
    "maintenance/181/restore-backup-units.sh",
    "maintenance/release/context.sh",
    "maintenance/release/migration-195-assert.sh",
    "maintenance/release/migration-232-assert.sh",
    "maintenance/release/migration-233-assert.sh",
    "maintenance/release/migration-234-assert.sh",
    "maintenance/release/migration-235-assert.sh",
    "maintenance/release/migration-236-assert.sh",
    "maintenance/release/migration-237-assert.sh",
    "maintenance/release/migration-238-assert.sh",
    "maintenance/release/migration-239-assert.sh",
    "maintenance/release/migration-240-assert.sh",
    "maintenance/release/migration-241-assert.sh",
    "maintenance/release/migration-242-assert.sh",
    "maintenance/release/migration-243-assert.sh",
    "maintenance/release/migration-244-assert.sh",
    "maintenance/release/migration-245-assert.sh",
    "maintenance/release/migration-254-assert.sh",
    "maintenance/release/migration-285-assert.sh",
    "maintenance/release/prepare.sh",
    "maintenance/release/promote-backup.sh",
    "release/bootstrap_backup_dr_assets.sh",
    "release/bootstrap_vm_signer.sh",
    "release/gate.py",
    "release/production-recovery-retention-clean.sh",
    "release/production-space-clean.sh",
    "release/production_cleanup.py",
    "release/production_recovery_retention.py",
    "release/profiles.py",
    "release/promote-dr-baseline.sh",
    "release/sign-dr-evidence.sh",
    "release/sign-gate.sh",
    "release/vm-only-validate.sh",
    "release/vm-validate.sh",
))
POSTFLIGHT_PATHS = frozenset(PREFIX + suffix for suffix in (
    "maintenance/release/activity-reward-cost-postflight.sh",
    "maintenance/release/switch.sh",
))
FINALIZATION_PATHS = frozenset(PREFIX + suffix for suffix in (
    "release/cli.py", "release/finalization.py",
    "release/doctor.py", "release/vm_lifecycle.py",
))
REVIEWED_PATHS = PROFILE_PATHS | POSTFLIGHT_PATHS | FINALIZATION_PATHS
REVIEWS = (
    (BASE, TARGET, "reviewed_profile_compatibility_changed", PROFILE_PATHS),
    (BASE, TARGET, "reviewed_reward_cost_postflight_changed", POSTFLIGHT_PATHS),
    (BASE, TARGET, "reviewed_consumed_finalization_changed", FINALIZATION_PATHS),
)

# Independent literals freeze both same-path identities. All reviewed paths
# exist in both trees; nothing is derived from the classifier's registered sets.
EXACT_OBJECTS = {PREFIX + suffix: identities for suffix, identities in {
    "maintenance/181/mask-backup-units.sh": ("100755:01f3e60c1840e0a072ec61116ed96f1338fb69e2", "100755:169344c6bf16ec3492f26252593c5dd71ef51e4d"),
    "maintenance/181/restore-backup-units.sh": ("100755:5d2bdc66b266f24b116072fb8d48263247cbec81", "100755:aca209163e6d95fa7055690c70660646964fe883"),
    "maintenance/release/activity-reward-cost-postflight.sh": ("100644:72628b31caaa3082ce571bd2915138eeb553c1ef", "100644:16ada1115114aa727503a20d254ec151b4dd4bdf"),
    "maintenance/release/context.sh": ("100755:8ff5438094c0066293c1d17d92f4d536deecfec1", "100755:7beaa8dfb3bc596d1dda3d28015494ac0e17d5a9"),
    "maintenance/release/migration-195-assert.sh": ("100644:d38162404c8fc3c39859a81e062b9e151f93f5a1", "100644:13e58925f6993fbd76c67cb50f03448c6f57deb3"),
    "maintenance/release/migration-232-assert.sh": ("100644:34a6c064fa409f144a2c7de7933ed664f50c2d12", "100644:6239cb1bec2e77d97591d676b56930386ce3270d"),
    "maintenance/release/migration-233-assert.sh": ("100644:7265611ad0abd81b860996433b76445daf157f79", "100644:ccb4815db256b333d86c30544f12f68b740aafd2"),
    "maintenance/release/migration-234-assert.sh": ("100644:0fd3a9c5d02cc876571dc29c3bac5e3f35de65b1", "100644:acb9883decbfc40d129f21c9b8964e76e5955f23"),
    "maintenance/release/migration-235-assert.sh": ("100644:8f78f616956804525e1432a046647036828df984", "100644:689bc89631acb1c1fce0ff2b81b69d68329d4cf0"),
    "maintenance/release/migration-236-assert.sh": ("100644:d79bd5f20042bcdaac2dba0ef5453f5ce744559b", "100644:b60a72352cce8dd8909629e39f138e1929f443b6"),
    "maintenance/release/migration-237-assert.sh": ("100644:eb4e13496ec0c13ca47ddc255bbe7552b6f4b40b", "100644:334529d6bdad6daa9c82a0189d6cb8895aec00b7"),
    "maintenance/release/migration-238-assert.sh": ("100644:95537d4fcaae1cd998cd675fabe91201e049d621", "100644:70cdee32929de89dc5fe958077ddd278862b248c"),
    "maintenance/release/migration-239-assert.sh": ("100755:5fc4c3e6663ee0a28a239652eca05e904ecbd94e", "100755:35dffa94e5084bd2b3abf804dd3543718dbc7a8e"),
    "maintenance/release/migration-240-assert.sh": ("100755:daf8fcac92bd27ab7297dc8629e2404ffdcc752d", "100755:502f2aee7c9d8a88417ac80815e013f2f7dba1f6"),
    "maintenance/release/migration-241-assert.sh": ("100755:ea3e8e21a2c6425b9e659767e1799bdc437d2ca1", "100755:ffb2e19a2e7824cb040d9576841e5a9e28246572"),
    "maintenance/release/migration-242-assert.sh": ("100644:b56ac219f87608deca9a43baeca00c14d08940f4", "100644:360a0d3b8e9ea20267298286df58a484ee33c91d"),
    "maintenance/release/migration-243-assert.sh": ("100644:1c7b5792b9f45786ae1a53ec4eb662776956a27d", "100644:dd2c7dcbe1136a759f579a782e7d4bb0861614ce"),
    "maintenance/release/migration-244-assert.sh": ("100644:7b56f7249f509fbba8827761ef44faf227689213", "100644:96e2d8a210420cef1f10ca6ad05c5efa892cc9cf"),
    "maintenance/release/migration-245-assert.sh": ("100644:c82cd22a0d122f0b85d53c7b75dcf77d3db80b03", "100644:134fd8e83c7175e8cf996a51c4c93331192d5fe7"),
    "maintenance/release/migration-254-assert.sh": ("100644:264b99ff343ee3b6854fb1b249d04f8511c21913", "100644:3e84e0a7a8665e0b73b6bd1f3e604e8764408cf8"),
    "maintenance/release/migration-285-assert.sh": ("100644:6f33e0554d64a89f8f98a06176b6f14c4afa6d40", "100644:8dad04c53e9bcc1f45368938416dcb0494337c35"),
    "maintenance/release/prepare.sh": ("100755:a8c4e71da3f4f89fbbd3ed09e52e212e5c40c624", "100755:a4b5a387a60f73e47f140b70d48ecb97f14ce769"),
    "maintenance/release/promote-backup.sh": ("100755:1fec0aec36288f683cb87d3981a0a89ab47a2ea0", "100755:e05d308d7a1a89bc5ba6d46debf566790606569a"),
    "maintenance/release/switch.sh": ("100755:09e7781c50e0c1d3ccf53e740732b7b9dfe2df12", "100755:46c1c329d8f7b4d0e77dd9182a7e2d02ef5e853c"),
    "release/bootstrap_backup_dr_assets.sh": ("100644:ca7ba64a34871b9cc5562b5e3a600753293c7130", "100644:264a37c0cdf19e4be98358e948d9b8f1a9874559"),
    "release/bootstrap_vm_signer.sh": ("100755:ebb50afcdd1f49f8a9a5bb83467be5b11cff4d99", "100755:dfca72d6f7709cd1c6d7f6552488a832a7ddd2f3"),
    "release/cli.py": ("100644:2780994d3351afe173abd438feccb49d1c3c9225", "100644:247dc93184bce4053b8e9e4048c78ad306c36751"),
    "release/doctor.py": ("100644:86fcbdfc5ee2c0e200cd45075da66818eb0b7472", "100644:e28a47f3630e647bd0e544bc98ac72db9648e3cc"),
    "release/finalization.py": ("100644:917ec498b3cc8e3a00392fbabfc4c5cc048184df", "100644:b29f06bdceca0c497acd9f53844295527d013c5d"),
    "release/gate.py": ("100644:860d48658ad9dd3b34a1d40097f5c4bd3702b8c6", "100644:a3398d0b5b93427eee9cd93fcc7cdcc1013053e5"),
    "release/production-recovery-retention-clean.sh": ("100644:b9a784368836eb75a14ad3fc91f870347c4ac5ca", "100644:64409ecb071d4d3ad5a32b63a7d326373a85f0c5"),
    "release/production-space-clean.sh": ("100644:7950a19d9c98b476be610c2890bef10f7caa74e5", "100644:4fd8a6524cb56c839e8bc21efc7264582a0a6e18"),
    "release/production_cleanup.py": ("100644:3a2e44e56346c2bc261fdbf70055e7469e9f8770", "100644:c4b0d10ccfdd83344e7ca29a185ee471c65fda40"),
    "release/production_recovery_retention.py": ("100644:a1e65beb54474008bea0282f31eaa6c2096850f7", "100644:ab6626653cf906f83217773dbb981e51bc4e7cf1"),
    "release/profiles.py": ("100644:d09b2cdaecbea75f9d0e7c5d5d00ebe979e17770", "100644:c0d48c184bb33166434cdfec8a49108b7ab85b7c"),
    "release/promote-dr-baseline.sh": ("100644:378026fa705d49ea57ef01438ed6b4bed0540c10", "100644:2c9ae41f4a403293993bdda506888a7b40906aae"),
    "release/sign-dr-evidence.sh": ("100644:be53de8e6aa685327085b4e0381cc6dcacc3bb07", "100644:c506b8a1f1bb9534df953935419fd8512e5302f5"),
    "release/sign-gate.sh": ("100644:cf46604cff007240d7821abaf86ce0af330ef6b9", "100644:2d62a6c159878276832ff9e552a34d50f5ef4515"),
    "release/vm-only-validate.sh": ("100644:c9371eec973e00b4fbc8122334fc9de9ca2a3a22", "100644:e6c7abc75e2edcf905270d1c399800da64697ac7"),
    "release/vm-validate.sh": ("100755:9c2a9c7619f1093b966c9361c62c0fb8863b4b13", "100755:06d2371cdf3ed5753a7975ca11d0fb3e313ef4b1"),
    "release/vm_lifecycle.py": ("100644:9c387fcf4b50dd1f269709693ead991cd4990f3d", "100644:b4b80e5b7291b967656aff9216930bb17afe3b2b"),
}.items()}

PROTECTED_PATHS = frozenset(PREFIX + suffix for suffix in (
    "maintenance/release/restore.sh", "maintenance/release/backup.sh",
    "maintenance/release/freeze-backup.sh", "maintenance/release/reconcile.sh",
    "maintenance/release/cleanup-state.sh", "maintenance/release/cleanup-slots.sh",
    "maintenance/release/apply-nginx-ingress.sh",
    "maintenance/release/rollback-nginx-ingress.sh",
    "release/production.py", "release/manifest.py", "release/state.py",
    "release/supervisor.py", "release/production_snapshot.py",
    "release/trust/vm-gate-ed25519.pub",
))
HISTORICAL_REVIEW_NAMES = (
    "_REVIEWED_COMPATIBILITY_TRANCHES", "_REVIEWED_GATE_POLICY_TRANCHES",
    "_REVIEWED_MIGRATION_GATE_TRANCHES", "_MIGRATION_GATE_REVIEW_PATHS",
    "_PROFILE263_COMPATIBILITY_PATHS", "_VM_LIFECYCLE_REVIEW_PATHS",
    "_PROFILE264_COMPATIBILITY_PATHS", "_PROFILE265_COMPATIBILITY_PATHS",
    "_PROFILE265_POSTFLIGHT_PATHS", "_REWARD_COST_POSTFLIGHT_REVIEW_PATHS",
    "_VM_PREFLIGHT_RECOVERY_REVIEW_PATHS", "_PROFILE265_BUILD_RECOVERY_REVIEW_PATHS",
    "_PROFILE265_REGISTRY_PROXY_REVIEW_PATHS",
    "_PROFILE268_COMPATIBILITY_PATHS", "_PROFILE268_POSTFLIGHT_PATHS",
    "_PROFILE268_FINALIZATION_REVIEW_PATHS",
)


class Profile269RecoveryReviewTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.old = gate._tree_blobs(WORKSPACE, BASE)
        cls.new = gate._tree_blobs(WORKSPACE, TARGET)
        cls.paths = gate._changed_paths(WORKSPACE, BASE, TARGET)

    def test_frozen_groups_and_same_path_old_new_modes_and_blobs(self):
        self.assertEqual((len(PROFILE_PATHS), len(POSTFLIGHT_PATHS), len(FINALIZATION_PATHS)), (35, 2, 4))
        self.assertEqual(len(REVIEWED_PATHS), 41)
        self.assertEqual(set(EXACT_OBJECTS), REVIEWED_PATHS)
        for name, paths in (
            ("_PROFILE269_COMPATIBILITY_PATHS", PROFILE_PATHS),
            ("_PROFILE269_POSTFLIGHT_PATHS", POSTFLIGHT_PATHS),
            ("_PROFILE269_FINALIZATION_REVIEW_PATHS", FINALIZATION_PATHS),
        ):
            self.assertIsInstance(getattr(gate, name), frozenset)
            self.assertEqual(getattr(gate, name), paths)
        matching = tuple(review for review in gate._REVIEWED_SCOPED_TRANCHES if review[:2] == (BASE, TARGET))
        self.assertEqual(matching, REVIEWS)
        sensitive = {path for path in self.paths if gate._is_recovery_sensitive_path(path)}
        self.assertEqual(sensitive, REVIEWED_PATHS)
        for path, (old, new) in EXACT_OBJECTS.items():
            with self.subTest(path=path):
                self.assertEqual(self.old.get(path), old)
                self.assertEqual(self.new.get(path), new)
                self.assertNotEqual(old, new)
                if old is not None:
                    self.assertEqual(old.split(":")[0], new.split(":")[0])
        reviewed = gate._reviewed_compatibility_paths(WORKSPACE, BASE, TARGET)
        self.assertEqual(set(reviewed), REVIEWED_PATHS)
        for _before, _after, reason, paths in REVIEWS:
            for path in paths:
                self.assertEqual(reviewed[path], reason, path)

    def test_historical_review_contracts_are_unchanged(self):
        source = check_output_hidden(
            ["git", "show", TARGET + ":" + PREFIX + "release/recovery_gate.py"],
            cwd=WORKSPACE, text=True,
        )
        baseline = {"__name__": "profile269_pinned_prior_reviews", "__package__": "release"}
        exec(compile(source, TARGET + ":recovery_gate.py", "exec"), baseline)
        for name in HISTORICAL_REVIEW_NAMES:
            with self.subTest(name=name):
                self.assertEqual(getattr(gate, name), baseline[name])
        old_scoped = baseline["_REVIEWED_SCOPED_TRANCHES"]
        frozen_scoped = (*old_scoped, *REVIEWS)
        self.assertEqual(
            gate._REVIEWED_SCOPED_TRANCHES[:len(frozen_scoped)],
            frozen_scoped,
        )

    def test_exact_registration_remains_specialized_in_cli(self):
        from release import cli

        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["estimated_extra_seconds"], 600)
        self.assertEqual(report["reason_codes"], [
            "migration_changed", "postgres_changed", "release_state_machine_changed",
            "reviewed_consumed_finalization_changed", "reviewed_profile_compatibility_changed",
            "reviewed_reward_cost_postflight_changed",
        ])
        gate.assert_release_allowed(report)
        # Even with no pending migrations the actual CLI policy cannot downgrade
        # reviewed helper changes to fast, which skips the real restore input.
        with mock.patch.object(cli, "classify_recovery_gate", return_value=report):
            policy = cli._recovery_gate_report(
                TARGET, {"production_current_commit_sha": BASE, "schema_migrations": []}, [],
            )
        self.assertEqual(policy, report)
        self.assertNotEqual(policy["mode"], "fast")

    def assert_blocked_trees(self, changed, *, observed_old=None, extra_paths=(), rejected_paths=()):
        future, future_base = "f" * 40, "e" * 40
        trees = {BASE: self.old, TARGET: self.new, future: changed}
        observed_base = BASE
        if observed_old is not None:
            trees[future_base] = observed_old
            observed_base = future_base
        affected = sorted(set(self.paths) | set(extra_paths))
        with (
            mock.patch.object(gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
            mock.patch.object(gate, "_changed_paths", return_value=affected),
            mock.patch.object(gate, "_commit_exists", side_effect=lambda _root, commit: commit in trees),
            mock.patch.object(gate, "_is_ancestor", return_value=True),
        ):
            reviewed = gate._reviewed_compatibility_paths(WORKSPACE, observed_base, future)
            for path in rejected_paths:
                self.assertNotIn(path, reviewed)
            report = gate.classify(WORKSPACE, observed_base, future)
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                gate.assert_release_allowed(report)
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                gate.assert_release_allowed(gate.require_full(report))

    def assert_blocked_mutation(self, path, mutation):
        changed, observed_old = dict(self.new), None
        mode, identity = self.new[path].split(":")
        extra_paths = [path]
        rejected_paths = [path]
        if mutation == "blob":
            changed[path] = mode + ":" + "0" * 40
        elif mutation == "mode":
            changed[path] = ("100755" if mode == "100644" else "100644") + ":" + identity
        elif mutation in {"base_blob", "base_mode"}:
            observed_old = dict(self.old)
            original = self.old.get(path)
            old_mode, old_blob = original.split(":") if original else (mode, "0" * 40)
            if mutation == "base_blob":
                observed_old[path] = old_mode + ":" + "0" * 40
            else:
                observed_old[path] = ("100755" if old_mode == "100644" else "100644") + ":" + old_blob
        elif mutation in {"delete", "move"}:
            del changed[path]
            if mutation == "move":
                destination = PREFIX + "maintenance/release/unregistered-" + Path(path).name
                changed[destination] = self.new[path]
                extra_paths.append(destination)
                rejected_paths.append(destination)
        else:
            self.fail("unknown mutation: " + mutation)
        self.assert_blocked_trees(
            changed, observed_old=observed_old, extra_paths=extra_paths, rejected_paths=rejected_paths,
        )

    def test_every_path_rejects_blob_mode_delete_move_and_base_drift(self):
        for path in sorted(REVIEWED_PATHS):
            for mutation in ("blob", "mode", "delete", "move", "base_blob", "base_mode"):
                with self.subTest(path=path, mutation=mutation):
                    self.assert_blocked_mutation(path, mutation)

    def test_unlisted_restore_backup_ingress_trust_and_production_do_not_inherit(self):
        for path in sorted(PROTECTED_PATHS):
            with self.subTest(path=path):
                self.assertIn(path, self.old)
                self.assertEqual(self.old[path], self.new[path])
                self.assertNotIn(path, REVIEWED_PATHS)
                self.assert_blocked_mutation(path, "blob")

    def test_tree_parser_preserves_directory_and_gitlink_presence(self):
        directory = PREFIX + "release/finalization-future.py"
        gitlink = PREFIX + "release/finalization-gitlink"
        records = (
            ("040000 tree " + "1" * 40 + "\t" + directory + "\0")
            + ("160000 commit " + "2" * 40 + "\t" + gitlink + "\0")
        ).encode("utf-8")
        with mock.patch.object(gate, "check_output_hidden", return_value=records):
            tree = gate._tree_blobs(WORKSPACE, "f" * 40)
        self.assertEqual(tree[directory], "040000:" + "1" * 40)
        self.assertEqual(tree[gitlink], "160000:" + "2" * 40)


if __name__ == "__main__":
    unittest.main()
