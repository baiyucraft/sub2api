from __future__ import annotations

import tempfile
import unittest
import sys
from pathlib import Path

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.gate import _validate_v2_pending
from release.migration_planner import HOOK_REGISTRY, checksum_policy_sha256, catalog_sha256, discover_migration_catalog, pending_hooks, plan_migrations


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

    def test_balance_notification_migration_has_semantic_release_hook(self) -> None:
        filename = "254_enable_balance_notifications_for_existing_users.sql"
        hook = HOOK_REGISTRY[filename]
        self.assertEqual(hook["script"], "migration-254-assert.sh")
        self.assertEqual(hook["rollback_policy"], "coordinated_restore")
        self.assertEqual([item["filename"] for item in pending_hooks([{"filename": filename}])], [filename])

        script = (DEPLOY_ROOT / "maintenance" / "release" / hook["script"]).read_text(encoding="utf-8")
        self.assertIn("balance_notify_enabled=FALSE", script)
        self.assertIn("soft_deleted_digest", script)
        self.assertIn("auth_cache_invalidation_outbox", script)
        self.assertIn("OLD.balance_notify_extra_emails IS NOT DISTINCT FROM NEW.balance_notify_extra_emails", script)

        vm_validate = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn(filename, vm_validate)
        self.assertIn("254_*) script=migration-254-assert.sh", vm_validate)
        self.assertIn('254_*) run_hook_v2 "$filename" migration-254-assert.sh postflight verified', vm_validate)


if __name__ == "__main__":
    unittest.main()
