"""Split admission storage preserves history and requires coordinated downtime."""
from __future__ import annotations

import copy
import hashlib
import json
import os
import re
import shutil
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
from release.manifest import create_manifest, validate_manifest_profile_contract
import test_profile263_release_contract as bash_helpers

BASELINE = "5deebb2929c2a694145cc2726e895f01c43d28c1"
MIGRATION = "301_upstream_cache_guard_admissions.sql"
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


class Profile274ReleaseContractTest(unittest.TestCase):
    def test_frozen_predecessors_and_new_catalog(self):
        previous = {}
        source = subprocess.run(["git", "show", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"],
                                cwd=WORKSPACE, check=True, capture_output=True, timeout=10).stdout
        exec(compile(source, "profile273-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"274", "275", "276"})
        for name, contract in previous["PROFILES"].items():
            self.assertEqual(PROFILES[name], contract, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "276")
        profile = get_profile("274")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.17-baiyu", "273", 2, [MIGRATION]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("273")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 277"):
            get_profile("277")
        old_files = subprocess.run(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"],
                                   cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=10).stdout.splitlines()
        changed = subprocess.run(["git", "diff", "--name-only", BASELINE, "--", "backend/migrations/*.sql"], cwd=WORKSPACE,
                                 check=True, capture_output=True, text=True, timeout=10).stdout.splitlines()
        self.assertLessEqual(set(changed), {"backend/migrations/" + MIGRATION, "backend/migrations/302_enable_gemini_image_generation_groups.sql", "backend/migrations/303_group_strip_optional_image_tools.sql"})
        current = {str(p.relative_to(WORKSPACE)).replace("\\", "/") for p in (WORKSPACE / "backend/migrations").glob("*.sql")}
        self.assertEqual(current, {name for name in old_files if name.endswith(".sql")} | {"backend/migrations/" + MIGRATION, "backend/migrations/302_enable_gemini_image_generation_groups.sql", "backend/migrations/303_group_strip_optional_image_tools.sql"})
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION])
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "276")
        for key in ("version", "parent", "new_migrations", "release_policy"):
            self.assertEqual(registry["historical_profiles"]["274"][key], profile[key])
        self.assertEqual(registry["migration_contracts"][MIGRATION], hashlib.sha256((WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()).hexdigest())
        for owner in ("migration-profile-contract", "upstream-cache-affinity-guard"):
            self.assertIn(MIGRATION, next(e for e in registry["extensions"] if e["id"] == owner)["migration_files"])
        self.assertIsNotNone(RELEASE_ID.fullmatch("274-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("277-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_profile_requires_downtime_before_manifest_or_side_effects(self):
        profile = get_profile("274")
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, profile, "274-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        with self.assertRaisesRegex(RuntimeError, "requires downtime"):
            validate_manifest_profile_contract({"schema": 2, "deployment_mode": "blue-green"}, profile)

    @unittest.skipUnless(JQ, "jq unavailable; set SUB2API_TEST_JQ for real filter validation")
    def test_exact_manifest_filters_for_all_inherited_hooks(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "274", "parent_profile": "273", "new_migrations": [MIGRATION],
                    "deployment_mode": "downtime", "migration_catalog": catalog}
        for number in (297, 298, 299, 301):
            source = (ROOT / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            bad_checksum = copy.deepcopy(manifest)
            next(r for r in bad_checksum["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid = [{**manifest, key: value} for key, value in (("profile", "275"), ("parent_profile", "272"), ("new_migrations", []), ("schema", 1))]
            invalid.extend((bad_checksum, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}))
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression],
                                        input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, document, result.stderr))


class Profile274BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_exact_version_parent_migration_and_downtime(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; mode=$5; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["301_upstream_cache_guard_admissions.sql"] and .deployment_mode == "downtime"') [[ $migrations == admissions && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migration, mode, allowed in (
            ("274", "0.2.17-baiyu", "273", "admissions", "downtime", True),
            ("274", "0.2.17-baiyu", "273", "admissions", "blue-green", False),
            ("274", "0.2.16-baiyu", "273", "admissions", "downtime", False),
            ("274", "0.2.17-baiyu", "272", "admissions", "downtime", False),
            ("274", "0.2.17-baiyu", "273", "none", "downtime", False),
            ("275", "0.2.17-baiyu", "274", "admissions", "downtime", False),
        ):
            result = self.execute(stub + checks, profile, version, parent, migration, mode)
            self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
