"""Additive health-history index, immutable predecessors, and executable profile admission."""
from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.migration_planner import discover_migration_catalog, plan_migrations
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

BASELINE = "8afa2ffe47800773f781279e945bf95d6c2e1067"
MIGRATION = "300_upstream_health_history_index.sql"


class Profile273ReleaseContractTest(unittest.TestCase):
    def test_history_checksums_and_additive_successor(self):
        previous = {}
        source = subprocess.run(
            ["git", "show", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"],
            cwd=WORKSPACE, check=True, capture_output=True, timeout=10,
        ).stdout
        exec(compile(source, "profile272-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"273", "274", "275"})
        for name, contract in previous["PROFILES"].items():
            with self.subTest(profile=name):
                self.assertEqual(PROFILES[name], contract)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "275")
        profile = get_profile("273")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.17-baiyu", "272", 2, [MIGRATION]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("272")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 276"):
            get_profile("276")
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] not in (MIGRATION, "301_upstream_cache_guard_admissions.sql")}
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION, "301_upstream_cache_guard_admissions.sql"])
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "275")
        for key in ("version", "parent", "new_migrations"):
            self.assertEqual(registry["historical_profiles"]["273"][key], profile[key])
        migration_owner = next(e for e in registry["extensions"] if e["id"] == "migration-profile-contract")
        feature_owner = next(e for e in registry["extensions"] if e["id"] == "upstream-account-table-refresh-performance")
        self.assertIn(MIGRATION, migration_owner["migration_files"])
        self.assertIn(MIGRATION, feature_owner["migration_files"])
        self.assertTrue(registry["migration_assertions"]["backend/migrations/" + MIGRATION])
        for name, contract in previous["PROFILES"].items():
            if name in registry["historical_profiles"]:
                for key in ("version", "parent", "new_migrations"):
                    self.assertEqual(registry["historical_profiles"][name].get(key), contract.get(key))
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registry["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        self.assertIn("(upstream_key_id, observed_at DESC, id DESC)", raw.decode())
        self.assertNotIn("UNIQUE", raw.decode())
        self.assertIsNotNone(RELEASE_ID.fullmatch("273-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("276-aaaaaaaaaaaa-1-aaaaaaaa"))


class Profile273BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_vm_exact_version_parent_migration_and_unknown_successor(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["300_upstream_health_history_index.sql"]') [[ $migrations == history_index ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migration, allowed in (
            ("273", "0.2.17-baiyu", "272", "history_index", True),
            ("273", "0.2.16-baiyu", "272", "history_index", False),
            ("273", "0.2.17-baiyu", "271", "history_index", False),
            ("273", "0.2.17-baiyu", "272", "none", False),
            ("275", "0.2.17-baiyu", "274", "history_index", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migration=migration):
                result = self.execute(stub + checks, profile, version, parent, migration)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)
