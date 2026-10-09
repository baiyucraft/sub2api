"""Local profile 264 lineage and executable admission contracts; no remote writes."""
from __future__ import annotations

import hashlib
import json
import sys
import subprocess
import unittest
from pathlib import Path

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID

BASELINE = "0898fe8c6289f87a1a4d90cf74a50e296689960e"
MIGRATION = "289_activity_reward_costs.sql"


class Profile264ReleaseContractTest(unittest.TestCase):
    def test_additive_lineage_preserves_all_historical_contracts(self):
        source = subprocess.run(["git", "cat-file", "blob", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"], cwd=WORKSPACE, check=True, capture_output=True, timeout=10).stdout
        baseline = {}
        exec(compile(source, "baseline-profiles", "exec"), baseline)
        self.assertEqual(set(PROFILES), set(baseline["PROFILES"]) | {"264", "265"})
        for name, profile in baseline["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "265")
        profile = get_profile("264")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.14-baiyu", "263", 2))
        self.assertEqual(profile["new_migrations"], [MIGRATION])
        self.assertEqual(profile["release_policy"], get_profile("263")["release_policy"])
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("263")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 266"):
            get_profile("266")

    def test_registration_retains_previous_profile_and_raw_migration_hash(self):
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registration["current_profile"]["id"], "265")
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["264"][field], get_profile("264")[field])
            self.assertEqual(registration["historical_profiles"]["263"][field], get_profile("263")[field])
        self.assertEqual(registration["migration_contracts"][MIGRATION], hashlib.sha256((WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()).hexdigest())
        for name in ("daily-activity-rewards", "extra-cost-ledger", "migration-profile-contract"):
            extension = next(item for item in registration["extensions"] if item["id"] == name)
            self.assertIn(MIGRATION, extension["migration_files"], name)

    def test_cleanup_accepts_current_and_retains_previous_rejects_unknown(self):
        for guard in (RELEASE_ID,):
            for profile, allowed in (("263", True), ("264", True), ("265", True), ("266", False)):
                self.assertEqual(guard.fullmatch(f"{profile}-aaaaaaaaaaaa-1-aaaaaaaa") is not None, allowed)


if __name__ == "__main__":
    unittest.main()
