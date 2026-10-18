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

MIGRATION = "299_upstream_cache_guard_revisions.sql"


class Profile271ReleaseContractTest(unittest.TestCase):
    def test_historical_registration_preserves_271_and_current_272(self):
        self.assertEqual(CURRENT_RELEASE_PROFILE, "274")
        profile = get_profile("271")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.16-baiyu", "270", 2))
        self.assertEqual(profile["new_migrations"], [MIGRATION])
        previous = get_profile("270")
        self.assertEqual((previous["version"], previous["parent"], previous["new_migrations"]), ("0.2.16-baiyu", "269", ["298_group_scheduling_protection_policies.sql"]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("270")
        current = get_profile("272")
        self.assertEqual((current["version"], current["parent"], current["gate_schema"]), ("0.2.17-baiyu", "271", 2))
        self.assertEqual(current["new_migrations"], [])
        registered = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registered["current_profile"]["id"], "274")
        self.assertEqual(registered["current_profile"]["status"], "pending")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registered["historical_profiles"]["271"][key], profile[key])
            self.assertEqual(registered["historical_profiles"]["270"][key], previous[key])
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
        self.assertIsNotNone(RELEASE_ID.fullmatch("271-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("272-aaaaaaaaaaaa-1-aaaaaaaa"))


class Profile271BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_baseline_staging_binds_profile_and_gate(self):
        source = (ROOT / "release/stage-dr-baseline-candidate.sh").read_text(encoding="utf-8")
        self.assertIn("^(271|272|273|274)-", source)
        self.assertIn('baseline_profile=${release_id%%-*}', source)
        self.assertIn('root="/srv/sub2api-backups/releases/$baseline_profile"', source)
        self.assertIn('$gate_profile == "$baseline_profile"', source)
        self.assertIn('^candidates/${baseline_profile}-', source)
        self.assertNotIn('releases/270', source)

    def test_validator_rejects_wrong_version_parent_migrations_and_unknown_profile(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["299_upstream_cache_guard_revisions.sql"]') [[ $migrations == revisions271 ]] ;;
    '.new_migrations == []') [[ $migrations == none ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migration, allowed in (
            ("271", "0.2.16-baiyu", "270", "revisions271", True),
            ("271", "0.2.15-baiyu", "270", "revisions271", False),
            ("271", "0.2.16-baiyu", "269", "revisions271", False),
            ("271", "0.2.16-baiyu", "270", "wrong", False),
            ("272", "0.2.17-baiyu", "271", "none", True),
        ):
            with self.subTest(profile=profile, parent=parent, migration=migration):
                result = self.execute(stub + checks, profile, version, parent, migration)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)

if __name__ == "__main__":
    unittest.main()
