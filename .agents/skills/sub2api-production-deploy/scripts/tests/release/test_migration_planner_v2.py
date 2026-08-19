from __future__ import annotations

import tempfile
import unittest
import sys
from pathlib import Path

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.gate import _validate_v2_pending
from release.migration_planner import checksum_policy_sha256, catalog_sha256, discover_migration_catalog, plan_migrations


class MigrationPlannerV2Test(unittest.TestCase):
    def test_empty_catalog_and_pending_are_valid(self) -> None:
        catalog = [{"filename": "001_first.sql", "checksum": "a" * 64, "non_transactional": False}]
        plan = plan_migrations(catalog, {})
        self.assertEqual([item["filename"] for item in plan["pending"]], ["001_first.sql"])
        _validate_v2_pending(
            {"migration_catalog": catalog},
            {"migration_evidence": {"database_high_watermark": None, "pending": [], "existing_checksums_verified": True, "isolated_upgrade_verified": True, "final_schema_verified": True}},
        )

    def test_unknown_database_migration_is_separate_and_blocks(self) -> None:
        catalog = [{"filename": "001_first.sql", "checksum": "a" * 64, "non_transactional": False}]
        plan = plan_migrations(catalog, {"999_unknown.sql": "b" * 64})
        self.assertEqual(len(plan["unknown"]), 1)
        self.assertFalse(plan["existing_checksums_verified"])

    def test_whitespace_only_migration_is_not_catalogued(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            path = root / "backend" / "migrations"
            path.mkdir(parents=True)
            (path / "001_empty.sql").write_text(" \n\t ", encoding="utf-8")
            (path / "002_real.sql").write_text(" SELECT 1; \n", encoding="utf-8")
            catalog = discover_migration_catalog(root)
            self.assertEqual([item["filename"] for item in catalog], ["002_real.sql"])

    def test_policy_and_catalog_digests_are_stable(self) -> None:
        catalog = [{"filename": "001_first.sql", "checksum": "a" * 64, "non_transactional": False}]
        self.assertEqual(len(catalog_sha256(catalog)), 64)
        self.assertEqual(len(checksum_policy_sha256()), 64)
        self.assertEqual(catalog_sha256(catalog), catalog_sha256(catalog))

    def test_ordinary_pending_cannot_claim_hooks(self) -> None:
        catalog = [{"filename": "001_first.sql", "checksum": "a" * 64, "non_transactional": False}]
        evidence = {"migration_evidence": {"database_high_watermark": None, "pending": [{"filename": "001_first.sql", "checksum": "a" * 64, "preflight": True, "postflight": False}], "existing_checksums_verified": True, "isolated_upgrade_verified": True, "final_schema_verified": True}}
        with self.assertRaisesRegex(RuntimeError, "ordinary migration"):
            _validate_v2_pending({"migration_catalog": catalog}, evidence)


if __name__ == "__main__":
    unittest.main()
