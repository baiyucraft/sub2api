from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.recovery_gate import changed_paths_sha256, classify, require_full, validate_report
from release import recovery_gate


PROFILE263_BASE = "218772daab271bb32df2ac6f9969ace0c9ccbce0"
PROFILE263_TARGET = "8499d5343c5a4510f88a3b13feb58f242d4cdd13"
SCRIPTS_PREFIX = ".agents/skills/sub2api-production-deploy/scripts/"
PROFILE263_PROFILE_PATHS = {
    SCRIPTS_PREFIX + suffix for suffix in (
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
    SCRIPTS_PREFIX + f"maintenance/release/migration-{number}-assert.sh"
    for number in (195, *range(232, 246), 254, 285)
}
PROFILE263_VM_PATHS = {
    SCRIPTS_PREFIX + "release/" + name for name in ("cli.py", "supervisor.py", "vm_lifecycle.py")
}
PROFILE263_SCOPED_REVIEWS = (
    (PROFILE263_BASE, PROFILE263_TARGET, "reviewed_profile_compatibility_changed", frozenset(PROFILE263_PROFILE_PATHS)),
    (PROFILE263_BASE, PROFILE263_TARGET, "reviewed_vm_lifecycle_changed", frozenset(PROFILE263_VM_PATHS)),
)


class RecoveryGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git("init")
        self.git("config", "user.email", "release-tests@example.invalid")
        self.git("config", "user.name", "Release Tests")
        self.write("README.md", "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline")
        self.base = self.git("rev-parse", "HEAD")

    def tearDown(self) -> None:
        self.temp.cleanup()

    def git(self, *args: str) -> str:
        return subprocess.check_output(
            ["git", *args],
            cwd=self.root,
            text=True,
            stderr=subprocess.DEVNULL,
        ).strip()

    def write(self, relative: str, content: str) -> None:
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")

    def commit_change(self, relative: str) -> str:
        self.write(relative, "changed\n")
        self.git("add", "-A")
        self.git("commit", "-m", relative)
        return self.git("rev-parse", "HEAD")

    def test_ordinary_change_uses_fast_mode(self) -> None:
        target = self.commit_change("backend/internal/service/example.go")
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "fast")
        self.assertEqual(report["reason_codes"], ["ordinary_change"])
        self.assertEqual(report["estimated_extra_seconds"], 0)

    def test_billing_inflight_cache_uses_specialized_mode(self) -> None:
        relative = "backend/internal/repository/billing_inflight_cache.go"
        target = self.commit_change(relative)
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["reason_codes"], ["redis_changed"])
        self.assertEqual(report["estimated_extra_seconds"], 600)
        self.assertEqual(report["changed_paths_sha256"], changed_paths_sha256([relative]))

    def test_billing_inflight_cache_path_contract_is_exact(self) -> None:
        base = self.base
        for relative in (
            "backend/internal/repository/example.go",
            "backend/internal/repository/billing_cache.go",
            "backend/internal/repository/billing_inflight_cache_test.go",
            "backend/internal/service/billing_inflight_cache.go",
        ):
            with self.subTest(relative=relative):
                target = self.commit_change(relative)
                report = classify(self.root, base, target)
                self.assertEqual(report["mode"], "fast")
                self.assertEqual(report["reason_codes"], ["ordinary_change"])
                self.assertEqual(report["estimated_extra_seconds"], 0)
                base = target

    def test_data_runtime_changes_use_specialized_mode(self) -> None:
        cases = {
            "backend/migrations/999_example.sql": "migration_changed",
            "deploy/docker-compose.yml": "compose_changed",
            "backend/internal/cache/redis_store.go": "redis_changed",
            "deploy/postgres.conf": "postgres_changed",
        }
        for relative, reason in cases.items():
            with self.subTest(relative=relative):
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    subprocess.run(["git", "init"], cwd=root, check=True, capture_output=True)
                    subprocess.run(["git", "config", "user.email", "release-tests@example.invalid"], cwd=root, check=True)
                    subprocess.run(["git", "config", "user.name", "Release Tests"], cwd=root, check=True)
                    baseline = root / "README.md"
                    baseline.write_text("baseline\n", encoding="utf-8")
                    subprocess.run(["git", "add", "-A"], cwd=root, check=True)
                    subprocess.run(["git", "commit", "-m", "baseline"], cwd=root, check=True, capture_output=True)
                    base = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
                    changed = root / relative
                    changed.parent.mkdir(parents=True, exist_ok=True)
                    changed.write_text("changed\n", encoding="utf-8")
                    subprocess.run(["git", "add", "-A"], cwd=root, check=True)
                    subprocess.run(["git", "commit", "-m", "change"], cwd=root, check=True, capture_output=True)
                    target = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
                    report = classify(root, base, target)
                self.assertEqual(report["mode"], "specialized")
                self.assertIn(reason, report["reason_codes"])
                self.assertEqual(report["estimated_extra_seconds"], 600)

    def test_unreviewed_recovery_change_requires_full_mode(self) -> None:
        target = self.commit_change(
            ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"
        )
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "full")
        self.assertEqual(
            report["reason_codes"],
            ["recovery_change_requires_review", "release_state_machine_changed"],
        )
        self.assertEqual(report["estimated_extra_seconds"], 2400)

    def test_reviewed_blob_pair_uses_specialized_and_extra_change_revokes_review(self) -> None:
        relative = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh"
        self.write(relative, "profiles=258\n")
        self.git("add", "-A")
        self.git("commit", "-m", "profile baseline")
        base = self.git("rev-parse", "HEAD")
        self.write(relative, "profiles=258,259\n")
        self.git("add", "-A")
        self.git("commit", "-m", "profile compatibility")
        reviewed = self.git("rev-parse", "HEAD")
        with mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ((base, reviewed),)):
            report = classify(self.root, base, reviewed)
            self.assertEqual(report["mode"], "specialized")
            self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
            target = self.commit_change(relative)
            self.assertEqual(classify(self.root, base, target)["mode"], "full")
            self.assertEqual(classify(self.root, self.base, reviewed)["mode"], "full")

    def test_review_does_not_cover_other_path_or_file_mode(self) -> None:
        relative = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh"
        self.write(relative, "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline helper")
        base = self.git("rev-parse", "HEAD")
        reviewed = self.commit_change(relative)
        with mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ((base, reviewed),)):
            other = relative.replace("context.sh", "restore.sh")
            target = self.commit_change(other)
            self.assertEqual(classify(self.root, base, target)["mode"], "full")
            self.git("update-index", "--chmod=+x", relative)
            self.git("commit", "-m", "mode changed")
            target = self.git("rev-parse", "HEAD")
            self.assertEqual(classify(self.root, base, target)["mode"], "full")

    def test_reviewed_metadata_and_algorithm_changes_keep_highest_tier(self) -> None:
        relative = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh"
        self.write(relative, "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline helper")
        base = self.git("rev-parse", "HEAD")
        reviewed = self.commit_change(relative)
        with mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ((base, reviewed),)):
            target = self.commit_change(".agents/skills/sub2api-production-deploy/scripts/release/gate.py")
            target = self.commit_change("backend/internal/cache/redis_store.go")
            report = classify(self.root, base, target)
            self.assertEqual(report["mode"], "full")
            self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
            self.assertIn("redis_changed", report["reason_codes"])

    def test_format_and_trust_files_require_full_review(self) -> None:
        base = self.base
        for relative in (
            "release/sign-gate.sh", "release/sign-dr-evidence.sh", "release/manifest.py",
            "release/production_snapshot.py", "release/drverify/main.go", "release/trust/vm-gate-ed25519.pub",
            "release/bootstrap_vm_signer.sh", "maintenance/release/reconcile.sh",
            "release/paths.py", "release/atomic.py", "release/migration_planner.py",
        ):
            with self.subTest(relative=relative):
                target = self.commit_change(".agents/skills/sub2api-production-deploy/scripts/" + relative)
                self.assertEqual(classify(self.root, base, target)["mode"], "full")
                base = target

    def test_sensitive_file_renamed_outside_recovery_scope_still_requires_review(self) -> None:
        relative = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"
        self.write(relative, "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline helper")
        base = self.git("rev-parse", "HEAD")
        self.git("mv", relative, "ordinary.txt")
        self.git("commit", "-m", "rename helper")
        self.assertEqual(classify(self.root, base, self.git("rev-parse", "HEAD"))["mode"], "full")

    def test_gate_policy_review_cannot_exempt_restoration_algorithm(self) -> None:
        prefix = ".agents/skills/sub2api-production-deploy/scripts/"
        policy = prefix + "release/cli.py"
        restore = prefix + "maintenance/release/restore.sh"
        self.write(policy, "baseline\n")
        self.write(restore, "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline helpers")
        base = self.git("rev-parse", "HEAD")
        target = self.commit_change(policy)
        with mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ((base, target),)):
            report = classify(self.root, base, target)
            self.assertEqual(report["mode"], "specialized")
            self.assertIn("reviewed_gate_policy_changed", report["reason_codes"])
        target = self.commit_change(restore)
        with mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ((base, target),)):
            self.assertEqual(classify(self.root, base, target)["mode"], "full")

    def test_registered_review_preserves_actual_profile_and_policy_scope(self) -> None:
        from release.paths import WORKSPACE

        report = classify(
            WORKSPACE,
            "16a4a030fe67f4de4a46bcadd02c9003fc954432",
            "efcd995d52fba41071412d15d198d9fa76afe967",
        )
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
        self.assertIn("reviewed_gate_policy_changed", report["reason_codes"])
        self.assertIn("redis_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])

    def test_registered_profile260_review_covers_exact_compatibility_diff(self) -> None:
        from release.paths import WORKSPACE

        report = classify(
            WORKSPACE,
            "71016a197ea2cd3fc44987d8db421afc3983c040",
            "d40e11c6073e1f541ffb087d295c027f24335076",
        )
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
        self.assertIn("migration_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])

    def test_registered_285_gate_review_covers_exact_migration_wiring(self) -> None:
        from release.paths import WORKSPACE

        report = classify(
            WORKSPACE,
            "71016a197ea2cd3fc44987d8db421afc3983c040",
            "7abbf4a504780f6f4ed73b54c9410f6f054f3380",
        )
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_migration_gate_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])

    def test_registered_profile261_review_covers_only_exact_sensitive_objects(self) -> None:
        from release.paths import WORKSPACE

        base = "f670e8051529776fa681b628eb05ce073d73c17c"
        target = "a541a5c4bb6ad72e7198d2802fc6dfac22b305ee"
        report = classify(WORKSPACE, base, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_profile_compatibility_changed", report["reason_codes"])
        self.assertIn("migration_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])
        reviewed = recovery_gate._reviewed_compatibility_paths(WORKSPACE, base, target)
        sensitive = {
            path for path in recovery_gate._changed_paths(WORKSPACE, base, target)
            if recovery_gate._is_recovery_sensitive_path(path)
        }
        self.assertEqual(len(sensitive), 35)
        self.assertTrue(sensitive.issubset(reviewed))
        prefix = ".agents/skills/sub2api-production-deploy/scripts/"
        for suffix in (
            "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
            "maintenance/release/reconcile.sh", "release/trust/vm-gate-ed25519.pub",
        ):
            self.assertNotIn(prefix + suffix, reviewed)

    def test_reviewed_profile_removal_or_cross_path_reuse_is_not_exempt(self) -> None:
        prefix = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/"
        relative = prefix + "context.sh"
        self.write(relative, "profiles=260\n")
        self.git("add", "-A")
        self.git("commit", "-m", "profile baseline")
        base = self.git("rev-parse", "HEAD")
        self.write(relative, "profiles=260,261\n")
        self.git("add", "-A")
        self.git("commit", "-m", "reviewed profile")
        reviewed = self.git("rev-parse", "HEAD")
        self.git("mv", relative, prefix + "restore.sh")
        self.git("commit", "-m", "reuse on other path")
        with mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ((base, reviewed),)):
            report = classify(self.root, base, self.git("rev-parse", "HEAD"))
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])

    def test_registered_profile262_review_covers_exact_production_sensitive_objects(self) -> None:
        from release.paths import WORKSPACE

        base = "bb47353679b65ba37dad6ce9fde06c01e8afed7e"
        target = "b710ec5b15b1baf0d01d43bed5aec1eb26836ab4"
        prefix = ".agents/skills/sub2api-production-deploy/scripts/"
        expected = {
            prefix + suffix for suffix in (
                "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
                "maintenance/release/context.sh", "maintenance/release/prepare.sh",
                "maintenance/release/promote-backup.sh", "release/bootstrap_backup_dr_assets.sh",
                "release/bootstrap_vm_signer.sh", "release/gate.py", "release/profiles.py",
                "release/production-recovery-retention-clean.sh", "release/production-space-clean.sh",
                "release/production_cleanup.py", "release/production_recovery_retention.py",
                "release/promote-dr-baseline.sh", "release/sign-dr-evidence.sh", "release/sign-gate.sh",
                "release/vm-only-validate.sh", "release/vm-validate.sh",
            )
        }
        expected.update(
            prefix + f"maintenance/release/migration-{number}-assert.sh"
            for number in (195, *range(232, 246), 254, 285)
        )
        paths = recovery_gate._changed_paths(WORKSPACE, base, target)
        sensitive = {path for path in paths if recovery_gate._is_recovery_sensitive_path(path)}
        self.assertEqual(sensitive, expected)
        reviewed = recovery_gate._reviewed_compatibility_paths(WORKSPACE, base, target)
        self.assertTrue(sensitive.issubset(reviewed))
        old, new = recovery_gate._tree_blobs(WORKSPACE, base), recovery_gate._tree_blobs(WORKSPACE, target)
        for path in sensitive:
            self.assertEqual(old[path].split(":")[0], new[path].split(":")[0], path)
        protected = {
            prefix + suffix for suffix in (
                "maintenance/release/restore.sh", "maintenance/release/cleanup-state.sh",
                "maintenance/release/reconcile.sh", "release/manifest.py", "release/migration_planner.py",
                "release/production.py", "release/cli.py", "release/doctor.py",
                "release/production_snapshot.py", "release/atomic.py", "release/paths.py",
            )
        }
        protected.update(path for path in old.keys() | new.keys() if path.startswith((prefix + "release/trust/", prefix + "release/drverify/")))
        for path in protected:
            self.assertEqual(old.get(path), new.get(path), path)
            self.assertNotIn(path, reviewed)
        report = classify(WORKSPACE, base, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["reason_codes"], ["migration_changed", "release_state_machine_changed", "reviewed_profile_compatibility_changed"])
        recovery_gate.assert_release_allowed(report)

    def test_profile262_review_rejects_blob_mode_deletion_and_cross_path_drift(self) -> None:
        from release.paths import WORKSPACE

        base = "bb47353679b65ba37dad6ce9fde06c01e8afed7e"
        target = "b710ec5b15b1baf0d01d43bed5aec1eb26836ab4"
        future = "f" * 40
        paths = recovery_gate._changed_paths(WORKSPACE, base, target)
        old, new = recovery_gate._tree_blobs(WORKSPACE, base), recovery_gate._tree_blobs(WORKSPACE, target)
        sensitive = sorted(path for path in paths if recovery_gate._is_recovery_sensitive_path(path))
        for path in sensitive:
            mode, _ = new[path].split(":")
            for mutation in ("blob", "mode", "delete", "move"):
                with self.subTest(path=path, mutation=mutation):
                    changed = dict(new)
                    if mutation == "blob":
                        changed[path] = mode + ":" + "0" * 40
                    elif mutation == "mode":
                        changed[path] = ("100644" if mode == "100755" else "100755") + ":" + new[path].split(":")[1]
                    else:
                        del changed[path]
                        if mutation == "move":
                            changed[".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"] = new[path]
                    affected = paths + ([".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"] if mutation == "move" else [])
                    trees = {base: old, target: new, future: changed}
                    with (
                        mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ((base, target),)),
                        mock.patch.object(recovery_gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_REVIEWED_SCOPED_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
                        mock.patch.object(recovery_gate, "_changed_paths", return_value=affected),
                        mock.patch.object(recovery_gate, "_commit_exists", return_value=True),
                        mock.patch.object(recovery_gate, "_is_ancestor", return_value=True),
                    ):
                        report = classify(WORKSPACE, base, future)
                        self.assertEqual(report["mode"], "full")
                        self.assertIn("recovery_change_requires_review", report["reason_codes"])
                        with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                            recovery_gate.assert_release_allowed(require_full(report))

    def test_registered_profile263_reviews_separate_profile_and_vm_exact_objects(self) -> None:
        from release.paths import WORKSPACE

        paths = recovery_gate._changed_paths(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
        sensitive = {path for path in paths if recovery_gate._is_recovery_sensitive_path(path)}
        self.assertEqual(len(PROFILE263_PROFILE_PATHS), 35)
        self.assertEqual(sensitive, PROFILE263_PROFILE_PATHS | PROFILE263_VM_PATHS)
        reviewed = recovery_gate._reviewed_compatibility_paths(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
        self.assertEqual(set(reviewed), sensitive)
        for path in PROFILE263_PROFILE_PATHS:
            self.assertEqual(reviewed[path], "reviewed_profile_compatibility_changed", path)
        for path in PROFILE263_VM_PATHS:
            self.assertEqual(reviewed[path], "reviewed_vm_lifecycle_changed", path)
        old = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_BASE)
        new = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_TARGET)
        self.assertNotIn(SCRIPTS_PREFIX + "release/vm_lifecycle.py", old)
        for path in sensitive & old.keys():
            self.assertEqual(old[path].split(":")[0], new[path].split(":")[0], path)
        protected = {
            SCRIPTS_PREFIX + suffix for suffix in (
                "maintenance/release/backup.sh", "maintenance/release/freeze-backup.sh",
                "maintenance/release/restore.sh", "maintenance/release/reconcile.sh",
                "maintenance/release/cleanup-state.sh", "maintenance/release/cleanup-slots.sh",
                "maintenance/release/apply-nginx-ingress.sh", "maintenance/release/rollback-nginx-ingress.sh",
                "release/manifest.py", "release/migration_planner.py", "release/production.py",
                "release/state.py", "release/doctor.py", "release/production_snapshot.py",
                "release/atomic.py", "release/paths.py",
            )
        }
        protected.update(
            path for path in old.keys() | new.keys()
            if path.startswith((SCRIPTS_PREFIX + "release/trust/", SCRIPTS_PREFIX + "release/drverify/"))
        )
        for path in protected:
            self.assertIn(path, old, path)
            self.assertEqual(old[path], new.get(path), path)
            self.assertNotIn(path, reviewed)
        report = classify(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["reason_codes"], [
            "compose_changed", "release_state_machine_changed",
            "reviewed_profile_compatibility_changed", "reviewed_vm_lifecycle_changed",
        ])
        recovery_gate.assert_release_allowed(report)

    def test_vm_lifecycle_single_file_change_requires_independent_review(self) -> None:
        relative = SCRIPTS_PREFIX + "release/vm_lifecycle.py"
        target = self.commit_change(relative)
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "full")
        self.assertIn("recovery_change_requires_review", report["reason_codes"])
        with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
            recovery_gate.assert_release_allowed(require_full(report))

    def test_tree_object_parser_preserves_gitlink_identity_and_mode(self) -> None:
        relative = SCRIPTS_PREFIX + "release/vm_lifecycle.py"
        self.git("update-index", "--add", "--cacheinfo", f"160000,{self.base},{relative}")
        self.git("commit", "-m", "gitlink baseline")
        target = self.git("rev-parse", "HEAD")
        self.assertTrue(self.git("ls-tree", "-r", target, "--", relative).startswith("160000 commit "))
        self.assertEqual(recovery_gate._tree_blobs(self.root, target)[relative], "160000:" + self.base)

    def test_vm_lifecycle_review_rejects_gitlink_in_place_of_missing_base(self) -> None:
        relative = SCRIPTS_PREFIX + "release/vm_lifecycle.py"
        reviewed = self.commit_change(relative)
        reviewed_identity = recovery_gate._tree_blobs(self.root, reviewed)[relative]
        self.git("update-index", "--add", "--cacheinfo", f"160000,{self.base},{relative}")
        self.git("commit", "-m", "unreviewed gitlink base")
        observed_base = self.git("rev-parse", "HEAD")
        mode, identity = reviewed_identity.split(":")
        self.git("update-index", "--cacheinfo", f"{mode},{identity},{relative}")
        self.git("commit", "-m", "restore reviewed blob after gitlink")
        target = self.git("rev-parse", "HEAD")
        with mock.patch.object(recovery_gate, "_REVIEWED_SCOPED_TRANCHES", (
            (self.base, reviewed, "reviewed_vm_lifecycle_changed", frozenset({relative})),
        )):
            report = classify(self.root, observed_base, target)
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])
            self.assertNotIn("reviewed_vm_lifecycle_changed", report["reason_codes"])
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                recovery_gate.assert_release_allowed(require_full(report))

    def test_tree_object_parser_preserves_directory_identity_and_mode(self) -> None:
        relative = SCRIPTS_PREFIX + "release/vm_lifecycle.py"
        target = self.commit_change(relative + "/old.py")
        identity = self.git("rev-parse", f"{target}:{relative}")
        self.assertEqual(recovery_gate._tree_blobs(self.root, target)[relative], "040000:" + identity)

    def test_vm_lifecycle_review_rejects_directory_in_place_of_missing_base(self) -> None:
        relative = SCRIPTS_PREFIX + "release/vm_lifecycle.py"
        reviewed = self.commit_change(relative)
        self.git("rm", relative)
        self.write(relative + "/old.py", "unreviewed directory\n")
        self.git("add", "-A")
        self.git("commit", "-m", "unreviewed directory base")
        observed_base = self.git("rev-parse", "HEAD")
        self.git("rm", "-r", relative)
        self.write(relative, "changed\n")
        self.git("add", "-A")
        self.git("commit", "-m", "restore reviewed blob after directory")
        target = self.git("rev-parse", "HEAD")
        with mock.patch.object(recovery_gate, "_REVIEWED_SCOPED_TRANCHES", (
            (self.base, reviewed, "reviewed_vm_lifecycle_changed", frozenset({relative})),
        )):
            report = classify(self.root, observed_base, target)
            self.assertEqual(report["mode"], "full")
            self.assertIn("recovery_change_requires_review", report["reason_codes"])
            self.assertNotIn("reviewed_vm_lifecycle_changed", report["reason_codes"])
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                recovery_gate.assert_release_allowed(require_full(report))

    def test_profile263_reviews_reject_every_blob_mode_deletion_cross_path_and_base_drift(self) -> None:
        from release.paths import WORKSPACE

        future = "f" * 40
        future_base = "e" * 40
        paths = recovery_gate._changed_paths(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
        old = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_BASE)
        new = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_TARGET)
        for path in sorted(PROFILE263_PROFILE_PATHS | PROFILE263_VM_PATHS):
            mode, identity = new[path].split(":")
            for mutation in ("blob", "mode", "delete", "move", "base_blob"):
                with self.subTest(path=path, mutation=mutation):
                    changed = dict(new)
                    observed_old = dict(old)
                    affected = list(paths)
                    observed_base = PROFILE263_BASE
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
                            other = SCRIPTS_PREFIX + "maintenance/release/restore.sh"
                            changed[other] = new[path]
                            affected.append(other)
                    trees = {PROFILE263_BASE: old, PROFILE263_TARGET: new, future: changed, future_base: observed_old}
                    with (
                        mock.patch.object(recovery_gate, "_REVIEWED_SCOPED_TRANCHES", PROFILE263_SCOPED_REVIEWS),
                        mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
                        mock.patch.object(recovery_gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
                        mock.patch.object(recovery_gate, "_changed_paths", return_value=affected),
                        mock.patch.object(recovery_gate, "_commit_exists", return_value=True),
                        mock.patch.object(recovery_gate, "_is_ancestor", return_value=True),
                    ):
                        report = classify(WORKSPACE, observed_base, future)
                        self.assertEqual(report["mode"], "full")
                        self.assertIn("recovery_change_requires_review", report["reason_codes"])
                        with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                            recovery_gate.assert_release_allowed(require_full(report))

    def test_scoped_profile263_tranche_does_not_learn_unlisted_paths(self) -> None:
        from release.paths import WORKSPACE

        old = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_BASE)
        new = recovery_gate._tree_blobs(WORKSPACE, PROFILE263_TARGET)
        restore = SCRIPTS_PREFIX + "maintenance/release/restore.sh"
        changed = {**new, restore: "100755:" + "0" * 40}
        paths = recovery_gate._changed_paths(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET) + [restore]
        trees = {PROFILE263_BASE: old, PROFILE263_TARGET: changed}
        with (
            mock.patch.object(recovery_gate, "_REVIEWED_SCOPED_TRANCHES", PROFILE263_SCOPED_REVIEWS),
            mock.patch.object(recovery_gate, "_REVIEWED_COMPATIBILITY_TRANCHES", ()),
            mock.patch.object(recovery_gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ()),
            mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ()),
            mock.patch.object(recovery_gate, "_tree_blobs", side_effect=lambda _root, commit: trees[commit]),
            mock.patch.object(recovery_gate, "_changed_paths", return_value=paths),
            mock.patch.object(recovery_gate, "_commit_exists", return_value=True),
            mock.patch.object(recovery_gate, "_is_ancestor", return_value=True),
        ):
            reviewed = recovery_gate._reviewed_compatibility_paths(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
            self.assertNotIn(restore, reviewed)
            report = classify(WORKSPACE, PROFILE263_BASE, PROFILE263_TARGET)
            self.assertEqual(report["mode"], "full")
            with self.assertRaisesRegex(RuntimeError, "recovery changes require review"):
                recovery_gate.assert_release_allowed(require_full(report))

    def test_migration_gate_review_cannot_exempt_recovery_algorithm_or_drift(self) -> None:
        prefix = ".agents/skills/sub2api-production-deploy/scripts/"
        gate = prefix + "release/gate.py"
        assertion = prefix + "maintenance/release/migration-285-assert.sh"
        self.write(gate, "baseline\n")
        self.git("add", "-A")
        self.git("commit", "-m", "migration gate baseline")
        base = self.git("rev-parse", "HEAD")
        self.commit_change(gate)
        reviewed = self.commit_change(assertion)
        with mock.patch.object(recovery_gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ((base, reviewed),)):
            report = classify(self.root, base, reviewed)
            self.assertEqual(report["mode"], "specialized")
            self.assertIn("reviewed_migration_gate_changed", report["reason_codes"])
            self.write(gate, "unreviewed drift\n")
            self.git("add", "-A")
            self.git("commit", "-m", "migration gate drift")
            drift = self.git("rev-parse", "HEAD")
            self.assertEqual(classify(self.root, base, drift)["mode"], "full")
            self.write(gate, "changed\n")
            self.git("add", "-A")
            self.git("update-index", "--chmod=+x", assertion)
            self.git("commit", "-m", "migration assertion mode drift")
            self.assertEqual(classify(self.root, base, self.git("rev-parse", "HEAD"))["mode"], "full")
            self.git("update-index", "--chmod=-x", assertion)
            self.git("commit", "-m", "restore assertion mode")
            self.git("rm", assertion)
            self.git("commit", "-m", "remove assertion")
            self.assertEqual(classify(self.root, reviewed, self.git("rev-parse", "HEAD"))["mode"], "full")
            target = self.commit_change(prefix + "maintenance/release/restore.sh")
        with mock.patch.object(recovery_gate, "_REVIEWED_MIGRATION_GATE_TRANCHES", ((base, target),)):
            self.assertEqual(classify(self.root, base, target)["mode"], "full")

    def test_reviewed_new_identity_helper_requires_exact_blob_and_mode(self) -> None:
        relative = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/runtime-identity.sh"
        target = self.commit_change(relative)
        with mock.patch.object(recovery_gate, "_REVIEWED_GATE_POLICY_TRANCHES", ((self.base, target),)):
            report = classify(self.root, self.base, target)
            self.assertEqual(report["mode"], "specialized")
            self.assertIn("reviewed_gate_policy_changed", report["reason_codes"])
            self.write(relative, "unreviewed\n")
            self.git("add", "-A")
            self.git("commit", "-m", "identity drift")
            self.assertEqual(classify(self.root, self.base, self.git("rev-parse", "HEAD"))["mode"], "full")
            self.write(relative, "changed\n")
            self.git("add", relative)
            self.git("update-index", "--chmod=+x", relative)
            self.git("commit", "-m", "identity mode drift")
            self.assertEqual(classify(self.root, self.base, self.git("rev-parse", "HEAD"))["mode"], "full")

    def test_registered_identity_review_covers_actual_production_net_diff(self) -> None:
        from release.paths import WORKSPACE

        report = classify(
            WORKSPACE,
            "16a4a030fe67f4de4a46bcadd02c9003fc954432",
            "5d1a13af1e81d3f73a485bcdd6ea659188942018",
        )
        self.assertEqual(report["mode"], "specialized")
        self.assertIn("reviewed_gate_policy_changed", report["reason_codes"])
        self.assertNotIn("recovery_change_requires_review", report["reason_codes"])

    def test_full_mode_requires_explicit_escalation(self) -> None:
        target = self.commit_change("backend/internal/service/example.go")
        report = require_full(classify(self.root, self.base, target))
        self.assertEqual(report["mode"], "full")
        self.assertIn("manual_full_drill", report["reason_codes"])
        self.assertEqual(report["estimated_extra_seconds"], 2400)

    def test_redis_and_recovery_changes_preserve_full_escalation(self) -> None:
        base = self.base
        cases = {
            "backend/internal/repository/billing_inflight_cache.go": ["redis_changed"],
            ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh": [
                "recovery_change_requires_review",
                "release_state_machine_changed",
            ],
        }
        for relative, reasons in cases.items():
            with self.subTest(relative=relative):
                target = self.commit_change(relative)
                classified = classify(self.root, base, target)
                report = require_full(classified)
                self.assertEqual(report["mode"], "full")
                self.assertEqual(report["reason_codes"], sorted(reasons + ["manual_full_drill"]))
                self.assertEqual(report["estimated_extra_seconds"], 2400)
                self.assertEqual(report["base_commit"], base)
                self.assertEqual(report["target_commit"], target)
                self.assertEqual(report["changed_paths_sha256"], classified["changed_paths_sha256"])
                base = target

    def test_unproven_production_commit_is_specialized(self) -> None:
        target = self.git("rev-parse", "HEAD")
        report = classify(self.root, None, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertIsNone(report["base_commit"])
        self.assertEqual(report["reason_codes"], ["production_commit_unproven"])
        self.assertEqual(report["changed_paths_sha256"], changed_paths_sha256([]))

    def test_unavailable_production_commit_is_specialized(self) -> None:
        target = self.git("rev-parse", "HEAD")
        unavailable = "f" * 40
        report = classify(self.root, unavailable, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(report["base_commit"], unavailable)
        self.assertEqual(report["reason_codes"], ["production_commit_unproven"])

    def test_release_documentation_does_not_trigger_full_mode(self) -> None:
        target = self.commit_change(
            ".agents/skills/sub2api-production-deploy/scripts/release/README.md"
        )
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "fast")
        self.assertEqual(report["reason_codes"], ["ordinary_change"])

    def test_report_contract_is_exact(self) -> None:
        target = self.git("rev-parse", "HEAD")
        report = classify(self.root, None, target)
        self.assertEqual(
            set(report),
            {
                "schema",
                "mode",
                "base_commit",
                "target_commit",
                "reason_codes",
                "changed_paths_sha256",
                "estimated_extra_seconds",
            },
        )
        invalid = dict(report, mode="slow")
        with self.assertRaisesRegex(RuntimeError, "identity"):
            validate_report(invalid, target_commit=target)


if __name__ == "__main__":
    unittest.main()
