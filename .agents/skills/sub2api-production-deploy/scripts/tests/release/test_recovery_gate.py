from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.recovery_gate import changed_paths_sha256, classify, require_full, validate_report


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

    def test_release_or_recovery_change_uses_specialized_mode(self) -> None:
        target = self.commit_change(
            ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/restore.sh"
        )
        report = classify(self.root, self.base, target)
        self.assertEqual(report["mode"], "specialized")
        self.assertEqual(
            report["reason_codes"],
            ["recovery_logic_changed", "release_state_machine_changed"],
        )
        self.assertEqual(report["estimated_extra_seconds"], 600)

    def test_full_mode_requires_explicit_escalation(self) -> None:
        target = self.commit_change("backend/internal/service/example.go")
        report = require_full(classify(self.root, self.base, target))
        self.assertEqual(report["mode"], "full")
        self.assertIn("manual_full_drill", report["reason_codes"])
        self.assertEqual(report["estimated_extra_seconds"], 2400)

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
