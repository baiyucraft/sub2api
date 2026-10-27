"""Unified protection registration and admission; no remote side effects."""
from __future__ import annotations

import hashlib
import json
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, get_profile, get_release_profile
from release.migration_planner import discover_migration_catalog, migration_checksum, plan_migrations
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

MIGRATION = "298_group_scheduling_protection_policies.sql"


class Profile270ReleaseContractTest(unittest.TestCase):
    def test_current_registration_preserves_269_and_rejects_unknown_successor(self):
        self.assertEqual(CURRENT_RELEASE_PROFILE, "276")
        profile = get_profile("270")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.16-baiyu", "269", 2))
        self.assertEqual(profile["new_migrations"], [MIGRATION])
        previous = get_profile("269")
        self.assertEqual((previous["version"], previous["parent"], previous["new_migrations"]), ("0.2.16-baiyu", "268", ["297_upstream_cache_guard_account_scope.sql"]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("269")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 277"):
            get_profile("277")
        registered = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registered["current_profile"]["id"], "276")
        self.assertEqual(registered["current_profile"]["status"], "pending")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registered["historical_profiles"]["270"][key], profile[key])
            self.assertEqual(registered["historical_profiles"]["269"][key], previous[key])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registered["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        for marker in registered["migration_assertions"][f"backend/migrations/{MIGRATION}"]:
            self.assertIn(marker, raw.decode())
        catalog = discover_migration_catalog(WORKSPACE)
        entry = next(row for row in catalog if row["filename"] == MIGRATION)
        self.assertEqual(entry["checksum"], migration_checksum(raw))
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION])
        self.assertEqual(plan_migrations(catalog, {**snapshot, MIGRATION: entry["checksum"]})["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])
        self.assertIsNotNone(RELEASE_ID.fullmatch("270-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("272-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("277-aaaaaaaaaaaa-1-aaaaaaaa"))


class Profile270BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_validator_requires_exact_contract(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["297_upstream_cache_guard_account_scope.sql"]') [[ $migrations == account269 ]] ;;
    '.new_migrations == ["298_group_scheduling_protection_policies.sql"]') [[ $migrations == protection270 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("269", "0.2.16-baiyu", "268", "account269", True),
            ("270", "0.2.16-baiyu", "269", "protection270", True),
            ("270", "0.2.15-baiyu", "269", "protection270", False),
            ("270", "0.2.16-baiyu", "268", "protection270", False),
            ("270", "0.2.16-baiyu", "270", "protection270", False),
            ("270", "0.2.16-baiyu", "269", "account269", False),
            ("272", "0.2.16-baiyu", "271", "protection270", False),
        ):
            with self.subTest(profile=profile, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
