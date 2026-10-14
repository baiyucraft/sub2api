"""Freeze the reviewed 268 net objects while retaining real restore requirements.

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


BASE = "9bf9801a40c4e6732fa81fedbf6ad52f2fa06c62"
TARGET = "bd00d35ebc0b4182070510375ee6878101cfbc7a"
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
    "release/supervisor.py", "release/vm_lifecycle.py",
))
REVIEWED_PATHS = PROFILE_PATHS | POSTFLIGHT_PATHS | FINALIZATION_PATHS
REVIEWS = (
    (BASE, TARGET, "reviewed_profile_compatibility_changed", PROFILE_PATHS),
    (BASE, TARGET, "reviewed_reward_cost_postflight_changed", POSTFLIGHT_PATHS),
    (BASE, TARGET, "reviewed_consumed_finalization_changed", FINALIZATION_PATHS),
)

# Independent literals freeze both same-path identities, including absent old
# finalization.py. Nothing is derived from the classifier's registered sets.
EXACT_OBJECTS = {PREFIX + suffix: identities for suffix, identities in {
    "maintenance/181/mask-backup-units.sh": ("100755:5e57bd46a18907e8c3eea4b956b6945077280ead", "100755:01f3e60c1840e0a072ec61116ed96f1338fb69e2"),
    "maintenance/181/restore-backup-units.sh": ("100755:e9b323a55ca08cd511da67efb8632e7c308cc60d", "100755:5d2bdc66b266f24b116072fb8d48263247cbec81"),
    "maintenance/release/activity-reward-cost-postflight.sh": ("100644:67cf71f42906bb34b1eab9a2e0f786c245001fd8", "100644:72628b31caaa3082ce571bd2915138eeb553c1ef"),
    "maintenance/release/context.sh": ("100755:82912158913c673410d4ba286fc4ffe3dd30fcb1", "100755:8ff5438094c0066293c1d17d92f4d536deecfec1"),
    "maintenance/release/migration-195-assert.sh": ("100644:2cb19206b1742588cbe63a3d5dc7fbf1118a059d", "100644:d38162404c8fc3c39859a81e062b9e151f93f5a1"),
    "maintenance/release/migration-232-assert.sh": ("100644:214b170d502d814f419a97e84d9e7cf16ed7763e", "100644:34a6c064fa409f144a2c7de7933ed664f50c2d12"),
    "maintenance/release/migration-233-assert.sh": ("100644:14ad0da0fe8e5737e17b450ba9295204bf211692", "100644:7265611ad0abd81b860996433b76445daf157f79"),
    "maintenance/release/migration-234-assert.sh": ("100644:cad4676e7cf42bcc2e9bd1bd8a02bb5c8de4a4f3", "100644:0fd3a9c5d02cc876571dc29c3bac5e3f35de65b1"),
    "maintenance/release/migration-235-assert.sh": ("100644:5dc406414c6ffa285be3adb2831c956bbc1b87c2", "100644:8f78f616956804525e1432a046647036828df984"),
    "maintenance/release/migration-236-assert.sh": ("100644:66054843c9dd85e0f0aba50d45d8d0b13a19d33c", "100644:d79bd5f20042bcdaac2dba0ef5453f5ce744559b"),
    "maintenance/release/migration-237-assert.sh": ("100644:61d6706b2937084570937a7b9b44b33fbcb10bd9", "100644:eb4e13496ec0c13ca47ddc255bbe7552b6f4b40b"),
    "maintenance/release/migration-238-assert.sh": ("100644:c8a9be96f72ffd6bd94decf223f31eea3031d662", "100644:95537d4fcaae1cd998cd675fabe91201e049d621"),
    "maintenance/release/migration-239-assert.sh": ("100755:30e80e4717a68d0120409cf14f61c35818c2cafc", "100755:5fc4c3e6663ee0a28a239652eca05e904ecbd94e"),
    "maintenance/release/migration-240-assert.sh": ("100755:5d6927f0900967d995338c1392f2589a145a8b8f", "100755:daf8fcac92bd27ab7297dc8629e2404ffdcc752d"),
    "maintenance/release/migration-241-assert.sh": ("100755:f8ab7c6232369a2ea00bf398f4abf254d90a6dd5", "100755:ea3e8e21a2c6425b9e659767e1799bdc437d2ca1"),
    "maintenance/release/migration-242-assert.sh": ("100644:5046738595da867a15383c82db558f6aaadc2159", "100644:b56ac219f87608deca9a43baeca00c14d08940f4"),
    "maintenance/release/migration-243-assert.sh": ("100644:731ad6b967719fa2187d5e2ecd6529eab058a214", "100644:1c7b5792b9f45786ae1a53ec4eb662776956a27d"),
    "maintenance/release/migration-244-assert.sh": ("100644:f3e90c98ea5afecc99ffdcbaa1c472145500f154", "100644:7b56f7249f509fbba8827761ef44faf227689213"),
    "maintenance/release/migration-245-assert.sh": ("100644:f3f2c7f8f65a486ba13c6d92408db7a2cb965224", "100644:c82cd22a0d122f0b85d53c7b75dcf77d3db80b03"),
    "maintenance/release/migration-254-assert.sh": ("100644:bc21c9af2c1a9223f983bdb703c1c5f000eb2027", "100644:264b99ff343ee3b6854fb1b249d04f8511c21913"),
    "maintenance/release/migration-285-assert.sh": ("100644:ab01a70e195a93ec4d136ed5e7ea9908ea4391ef", "100644:6f33e0554d64a89f8f98a06176b6f14c4afa6d40"),
    "maintenance/release/prepare.sh": ("100755:731d684db7d7d60b801cae7f51e4d3fcab6fd6ac", "100755:a8c4e71da3f4f89fbbd3ed09e52e212e5c40c624"),
    "maintenance/release/promote-backup.sh": ("100755:2f7d2ce1a1b559d27c9fa6a58fbc9ffa8bc733dc", "100755:1fec0aec36288f683cb87d3981a0a89ab47a2ea0"),
    "maintenance/release/switch.sh": ("100755:5d5e7d55aa119fa8f5af17f5bad3504af1ec03f3", "100755:09e7781c50e0c1d3ccf53e740732b7b9dfe2df12"),
    "release/bootstrap_backup_dr_assets.sh": ("100644:f5b2cac5d3f973b886c99599b6b5c51e9100e007", "100644:ca7ba64a34871b9cc5562b5e3a600753293c7130"),
    "release/bootstrap_vm_signer.sh": ("100755:879afe9e2143695b6c939ceb952c3bf406d0a7fd", "100755:ebb50afcdd1f49f8a9a5bb83467be5b11cff4d99"),
    "release/cli.py": ("100644:ac87787e5dcee414d5cccd1b0eaa8bf0eb02af21", "100644:2780994d3351afe173abd438feccb49d1c3c9225"),
    "release/finalization.py": (None, "100644:917ec498b3cc8e3a00392fbabfc4c5cc048184df"),
    "release/gate.py": ("100644:b7a62ca6db0e8c3cb18b3d10eef29b742f754bfa", "100644:860d48658ad9dd3b34a1d40097f5c4bd3702b8c6"),
    "release/production-recovery-retention-clean.sh": ("100644:304d38a990b02cafaf72a48ca3fa93ab930a1619", "100644:b9a784368836eb75a14ad3fc91f870347c4ac5ca"),
    "release/production-space-clean.sh": ("100644:8abff7c4cb5c60c2e3eb838d241a7144f2607c55", "100644:7950a19d9c98b476be610c2890bef10f7caa74e5"),
    "release/production_cleanup.py": ("100644:772503590739947e01565e09f47be76f1de0b242", "100644:3a2e44e56346c2bc261fdbf70055e7469e9f8770"),
    "release/production_recovery_retention.py": ("100644:2edabe7b93fa9ec6c7128765c5d629d069da7981", "100644:a1e65beb54474008bea0282f31eaa6c2096850f7"),
    "release/profiles.py": ("100644:7bc592d855599d1087d6f624fe9d397a19b62251", "100644:d09b2cdaecbea75f9d0e7c5d5d00ebe979e17770"),
    "release/promote-dr-baseline.sh": ("100644:9a6e31d3869769146385b0418720223d62e210a2", "100644:378026fa705d49ea57ef01438ed6b4bed0540c10"),
    "release/sign-dr-evidence.sh": ("100644:bab43cc0600bec624abc699e7bcb3462baa18c66", "100644:be53de8e6aa685327085b4e0381cc6dcacc3bb07"),
    "release/sign-gate.sh": ("100644:cbc233dcc02f132b9c990f2443ab5935db873b05", "100644:cf46604cff007240d7821abaf86ce0af330ef6b9"),
    "release/supervisor.py": ("100644:ca14ebd152004d4e631622b92107c1438aa63628", "100644:a6652869efcba04499d860d0f1268dff4bd496ab"),
    "release/vm-only-validate.sh": ("100644:4e0f2c9ba61a0d58ca9e8854b243aaebc8217eb1", "100644:c9371eec973e00b4fbc8122334fc9de9ca2a3a22"),
    "release/vm-validate.sh": ("100755:242622e8aa9db7d3a911b47031540d4acdc6eef3", "100755:9c2a9c7619f1093b966c9361c62c0fb8863b4b13"),
    "release/vm_lifecycle.py": ("100644:ef6a6686f1de55cbc61c5ca52f98a3a0a1aef11e", "100644:9c387fcf4b50dd1f269709693ead991cd4990f3d"),
}.items()}
PROTECTED_PATHS = frozenset(PREFIX + suffix for suffix in (
    "maintenance/release/restore.sh", "maintenance/release/backup.sh",
    "maintenance/release/freeze-backup.sh", "maintenance/release/reconcile.sh",
    "maintenance/release/cleanup-state.sh", "maintenance/release/cleanup-slots.sh",
    "maintenance/release/apply-nginx-ingress.sh",
    "maintenance/release/rollback-nginx-ingress.sh",
    "release/production.py", "release/manifest.py", "release/state.py",
    "release/doctor.py", "release/production_snapshot.py",
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
)


class Profile268RecoveryReviewTest(unittest.TestCase):
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
            ("_PROFILE268_COMPATIBILITY_PATHS", PROFILE_PATHS),
            ("_PROFILE268_POSTFLIGHT_PATHS", POSTFLIGHT_PATHS),
            ("_PROFILE268_FINALIZATION_REVIEW_PATHS", FINALIZATION_PATHS),
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
        self.assertNotIn(PREFIX + "release/finalization.py", self.old)
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
        baseline = {"__name__": "profile268_pinned_prior_reviews", "__package__": "release"}
        exec(compile(source, TARGET + ":recovery_gate.py", "exec"), baseline)
        for name in HISTORICAL_REVIEW_NAMES:
            with self.subTest(name=name):
                self.assertEqual(getattr(gate, name), baseline[name])
        old_scoped = baseline["_REVIEWED_SCOPED_TRANCHES"]
        self.assertEqual(
            gate._REVIEWED_SCOPED_TRANCHES[:len(old_scoped) + len(REVIEWS)],
            (*old_scoped, *REVIEWS),
        )

    def test_exact_registration_remains_specialized_in_cli(self):
        from release import cli

        report = gate.classify(WORKSPACE, BASE, TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["estimated_extra_seconds"], 600)
        self.assertEqual(report["reason_codes"], [
            "migration_changed", "redis_changed", "release_state_machine_changed",
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

    def test_new_finalization_cannot_treat_directory_or_gitlink_as_absent(self):
        path = PREFIX + "release/finalization.py"
        self.assertNotIn(path, self.old)
        for mode in ("040000", "160000"):
            with self.subTest(mode=mode, side="base"):
                old = {**self.old, path: mode + ":" + "1" * 40}
                self.assert_blocked_trees(dict(self.new), observed_old=old, rejected_paths=[path])
            with self.subTest(mode=mode, side="target"):
                changed = {**self.new, path: mode + ":" + self.new[path].split(":")[1]}
                self.assert_blocked_trees(changed, rejected_paths=[path])

    def test_tree_parser_preserves_directory_and_gitlink_presence(self):
        directory = PREFIX + "release/finalization.py"
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
