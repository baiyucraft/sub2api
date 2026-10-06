from __future__ import annotations

import re
import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.production_recovery_retention import _validate


def report(mode: str = "dry-run") -> dict[str, str]:
    return {
        "cleanup_mode": mode,
        "cleanup_status": "ready" if mode == "dry-run" else "completed",
        "plan_sha256": "a" * 64,
        "retention_days": "3",
        "minimum_keep": "3",
        "cutoff_epoch": "1000",
        "valid_count": "12",
        "protected_count": "2",
        "candidate_count": "9",
        "candidate_bytes": "900",
        "candidate_ids": "199-aaaaaaaaaaaa-1-deadbeef",
        "deleted_count": "0" if mode == "dry-run" else "9",
        "deleted_bytes": "0" if mode == "dry-run" else "900",
        "candidate_count_after": "9" if mode == "dry-run" else "0",
        "root_free_before_bytes": "100",
        "root_free_after_bytes": "100",
        "root_free_delta_bytes": "0",
    }


class ProductionRecoveryRetentionTest(unittest.TestCase):
    def test_shell_and_python_accept_current_and_historical_profiles_only(self) -> None:
        script = (DEPLOY_ROOT / "release" / "production-recovery-retention-clean.sh").read_text(encoding="utf-8")
        pattern = re.search(r"^release_id_pattern='([^']+)'$", script, re.MULTILINE)[1]
        profiles = (182, 187, 191, 192, 194, 195, 197, 198, 199, 202, 206, 207,
                    208, 209, 210, 212, 213, 215, *range(232, 263))
        for profile in profiles:
            with self.subTest(profile=profile):
                values = report()
                values["candidate_ids"] = f"{profile}-aaaaaaaaaaaa-1-deadbeef"
                self.assertIsNotNone(re.fullmatch(pattern, values["candidate_ids"]))
                _validate(values, "dry-run", None)
        for candidate in ("263-aaaaaaaaaaaa-1-deadbeef", "0259-aaaaaaaaaaaa-1-deadbeef",
                          "259-aaaaaaaaaaaa-1-deadbeef/../outside"):
            with self.subTest(candidate=candidate):
                values = report()
                values["candidate_ids"] = candidate
                self.assertIsNone(re.fullmatch(pattern, candidate))
                with self.assertRaisesRegex(RuntimeError, "invalid candidate ID"):
                    _validate(values, "dry-run", None)

    def test_dry_run_policy_and_measurements_are_validated(self) -> None:
        _validate(report(), "dry-run", None)

    def test_apply_must_converge_all_candidates(self) -> None:
        values = report("apply")
        values["deleted_bytes"] = "899"
        with self.assertRaisesRegex(RuntimeError, "did not converge"):
            _validate(values, "apply", values["plan_sha256"])

    def test_plan_checksum_is_bound_for_apply(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "different plan checksum"):
            _validate(report(), "dry-run", "b" * 64)

    def test_apply_requires_the_dry_run_cutoff_epoch(self) -> None:
        from release.production_recovery_retention import cleanup_production_recovery_points

        with self.assertRaisesRegex(ValueError, "cutoff epoch"):
            cleanup_production_recovery_points("apply", "a" * 64)

    def test_shell_has_recovery_point_only_scope(self) -> None:
        script = (DEPLOY_ROOT / "release" / "production-recovery-retention-clean.sh").read_text(encoding="utf-8")
        self.assertIn("/opt/sub2api/backups/release-state", script)
        self.assertIn("/run/lock/sub2api-production-release.lock", script)
        self.assertIn("/run/lock/sub2api-backup-global.lock", script)
        self.assertIn("retention_days=3", script)
        self.assertIn("minimum_keep=3", script)
        self.assertIn(".reconciliation", script)
        self.assertIn("expected_plan_sha256", script)
        self.assertIn("requested_cutoff_epoch", script)
        self.assertIn("cutoff_epoch=$requested_cutoff_epoch", script)
        self.assertIn("sha256sum -c recovery-point.age.sha256", script)
        self.assertIn("rmdir -- \"$state_dir\"", script)
        self.assertNotIn("docker system prune", script)
        self.assertNotIn("docker volume", script)
        self.assertNotIn("rm -rf -- /opt/sub2api/backups", script)


if __name__ == "__main__":
    unittest.main()
