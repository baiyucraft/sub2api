"""Profile 272 preserves history and binds inherited hooks to its exact manifest."""
from __future__ import annotations

import copy
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
import test_profile263_release_contract as bash_helpers

BASELINE = "285f5d22fdba9edb50eba1e483c2f909bd4589bb"
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


class Profile272ReleaseContractTest(unittest.TestCase):
    def test_additive_successor_preserves_profiles_and_migration_catalog(self):
        previous = {}
        source = subprocess.run(
            ["git", "show", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"],
            cwd=WORKSPACE, check=True, capture_output=True, timeout=10,
        ).stdout
        exec(compile(source, "profile271-baseline", "exec"), previous)
        self.assertEqual(previous["CURRENT_RELEASE_PROFILE"], "271")
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"272", "273", "274", "275", "276", "277", "278", "279", "280", "281"})
        for name, contract in previous["PROFILES"].items():
            with self.subTest(profile=name):
                self.assertEqual(PROFILES[name], contract)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "281")
        profile = get_profile("272")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.17-baiyu", "271", 2, []))
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog}
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 282"):
            get_profile("282")

    @unittest.skipUnless(JQ, "jq unavailable; set SUB2API_TEST_JQ for real filter validation")
    def test_inherited_hooks_validate_real_manifest_filters(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "272", "parent_profile": "271", "new_migrations": [],
                    "migration_catalog": catalog}
        for number in (297, 298, 299):
            helper = next((ROOT / "maintenance/release").glob(f"migration-{number}-assert.sh"))
            source = helper.read_text(encoding="utf-8")
            match = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)
            self.assertIsNotNone(match, helper.name)
            row = next(row for row in catalog if row["filename"].startswith(f"{number}_"))
            checksum = re.search(r"^catalog_checksum=([0-9a-f]{64})$", source, re.M)[1]
            self.assertEqual(checksum, row["checksum"])
            invalid = []
            for key, value in (("profile", "273"), ("parent_profile", "270"), ("new_migrations", [row["filename"]]), ("schema", 1)):
                invalid.append({**manifest, key: value})
            bad_checksum = copy.deepcopy(manifest)
            next(item for item in bad_checksum["migration_catalog"] if item["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid.extend((bad_checksum, {**manifest, "migration_catalog": []},
                            {**manifest, "migration_catalog": catalog + [row]}))
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), *((item, False) for item in invalid)):
                with self.subTest(helper=helper.name, document=document):
                    result = subprocess.run(
                        [JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", checksum, match[1]],
                        input=json.dumps(document), text=True, capture_output=True, timeout=10,
                    )
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)


class Profile272BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_vm_rejects_wrong_version_parent_and_migrations(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == []') [[ $migrations == none ]] ;;
    *) return 1 ;;
  esac
}
'''
        for version, parent, migrations, allowed in (
            ("0.2.17-baiyu", "271", "none", True),
            ("0.2.16-baiyu", "271", "none", False),
            ("0.2.17-baiyu", "270", "none", False),
            ("0.2.17-baiyu", "271", "unexpected", False),
        ):
            with self.subTest(version=version, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, "272", version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)
