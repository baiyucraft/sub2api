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
