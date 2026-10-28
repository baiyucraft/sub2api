"""Optional image tool policy is additive; historical releases remain immutable."""
from __future__ import annotations

import hashlib
import copy
import json
import re
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.manifest import validate_manifest_profile_contract
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum, plan_migrations
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers
import test_profile275_release_contract as predecessor

BASELINE = "1cbfdb66a6819335ae2109b3fbbcd702155e308d"
MIGRATION = "303_group_strip_optional_image_tools.sql"


class Profile276ReleaseContractTest(unittest.TestCase):
    def test_frozen_history_and_current_release_identity(self):
        previous = {}
        exec(compile(predecessor.git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "profile275-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"276", "277", "278", "279", "280", "281"})
        for name, profile in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "281")
        profile = get_profile("276")
        self.assertEqual((profile["version"], profile["parent"], profile["new_migrations"], profile["gate_schema"]),
                         ("0.2.18-baiyu", "275", [MIGRATION], 2))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("275")
        self.assertEqual(get_profile("277")["name"], "277")
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("277")
        self.assertEqual(get_profile("278")["name"], "278")
        self.assertEqual(get_release_profile("281")["name"], "281")
        self.assertIsNotNone(RELEASE_ID.fullmatch("276-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("277-aaaaaaaaaaaa-1-aaaaaaaa"))
        names = subprocess.run(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"], cwd=WORKSPACE, check=True, capture_output=True, text=True).stdout.splitlines()
        for name in names:
            if name.endswith(".sql"):
                self.assertEqual((WORKSPACE / name).read_bytes(), predecessor.git_blob(BASELINE, name), name)
        self.assertEqual({p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")},
                         {Path(name).name for name in names if name.endswith(".sql")} | {MIGRATION, "304_upstream_confidence_fingerprint.sql"})

    def test_registry_checksums_and_pending_migration(self):
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["historical_profiles"]["276"]["version"], "0.2.18-baiyu")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registry["historical_profiles"]["276"][key], get_profile("276")[key])
            self.assertEqual(registry["historical_profiles"]["275"][key], get_profile("275")[key])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registry["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {r["filename"]: r["checksum"] for r in catalog if r["filename"] != MIGRATION}
        self.assertEqual([r["filename"] for r in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION])
        snapshot[MIGRATION] = migration_checksum(raw)
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])

    def test_manifest_binds_new_identity_and_ancestor_hooks(self):
        profile = get_profile("276")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh": "a" * 64 for n in (297, 298, 299, 301)}
        manifest = {"schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
                    "release_id": "276-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "276", "version": profile["version"],
                    "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
                    "parent_profile": "275", "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
                    "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
                    "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets}
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for change in ({"parent_profile": "274"}, {"new_migrations": []}, {"profile": "277"}, {"deployment_mode": "blue-green"}):
                with self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **change}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)

    def test_every_inherited_filter_binds_new_identity(self):
        if not predecessor.JQ:
            self.skipTest("jq unavailable")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "276", "parent_profile": "275", "new_migrations": [MIGRATION],
                    "deployment_mode": "downtime", "migration_catalog": catalog}
        for number in (297, 298, 299, 301):
            source = (ROOT / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            checksum_error = copy.deepcopy(manifest)
            next(r for r in checksum_error["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid = [{**manifest, key: value} for key, value in (("profile", "277"), ("parent_profile", "274"), ("new_migrations", []), ("schema", 1))]
            invalid.extend((checksum_error, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}))
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), *((m, False) for m in invalid)):
                result = subprocess.run([predecessor.JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression],
                                        input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, document, result.stderr))


class Profile276BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_exact_version_parent_migration_and_unknown_successor(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; mode=$5; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["303_group_strip_optional_image_tools.sql"] and .deployment_mode == "downtime"') [[ $migrations == optional_image && $mode == downtime ]] ;;
    '.new_migrations == ["304_upstream_confidence_fingerprint.sql"] and .deployment_mode == "downtime"') [[ $migrations == fingerprint && $mode == downtime ]] ;;
    '.new_migrations == [] and .deployment_mode == "downtime"') [[ $migrations == none && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, mode, allowed in (
            ("276", "0.2.18-baiyu", "275", "optional_image", "downtime", True),
            ("276", "0.2.17-baiyu", "275", "optional_image", "downtime", False),
            ("276", "0.2.18-baiyu", "274", "optional_image", "downtime", False),
            ("276", "0.2.18-baiyu", "275", "none", "downtime", False),
            ("276", "0.2.18-baiyu", "275", "optional_image", "blue-green", False),
            ("276", "0.2.18-baiyu", "275", "optional_image", "downtime", True),
            ("277", "0.2.19-baiyu", "276", "fingerprint", "downtime", True),
            ("277", "0.2.19-baiyu", "275", "fingerprint", "downtime", False),
            ("280", "0.2.21-baiyu", "278", "none", "downtime", False)):
            result = self.execute(stub + checks, profile, version, parent, migrations, mode)
            self.assertEqual(result.returncode == 0, allowed, result.stderr)
