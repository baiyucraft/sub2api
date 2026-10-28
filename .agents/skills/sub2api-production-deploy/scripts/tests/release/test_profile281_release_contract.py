"""Official 0.2.19 merge: additive profile, byte-preserving migrations and executable admission."""
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
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.manifest import create_manifest, validate_manifest_profile_contract
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum, plan_migrations
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

BASELINE = "b7ceea117d58df5f947cfa59107181b2615421ae"
UPSTREAM = "a9240d5016c226b341242a8c54a01a8605cbbc72"
MIGRATIONS = ["305_scoped_admin_api_keys.sql", "306_group_image_count_billing.sql"]
OFFICIAL = ["247_scoped_admin_api_keys.sql", "248_group_image_count_billing.sql"]
RAW_SHA256 = ["0c4c22f564f276fb305a54cf2d6c4b1a6179d748d5d8b74a3b99676282355bf6", "c91d6d31937658e71c68fa3af6a26fc47380f61243a681ac275aeb4706b242ab"]
CATALOG_SHA256 = ["03445774e2dfad32da9e23279193d1bca02953896bc3d3b8a87ba0b36b6ed577", "a872dde339b19f311feb5fa6fd85c8ff7b11d69794c35a18e2086d9846b3499c"]
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


def git_blob(ref: str, path: str) -> bytes:
    return subprocess.run(["git", "cat-file", "blob", f"{ref}:{path}"], cwd=WORKSPACE,
                          check=True, capture_output=True, timeout=30).stdout


class Profile281ReleaseContractTest(unittest.TestCase):
    def test_successor_preserves_every_previous_profile_and_sql(self):
        previous = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"),
                     "profile280-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"281"})
        for name, contract in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "281")
        profile = get_release_profile("281")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.19-baiyu", "280", 2, MIGRATIONS))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("280")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 282"):
            get_profile("282")
        names = subprocess.run(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"],
                               cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=30).stdout.splitlines()
        historical = [p for p in names if p.endswith(".sql")]
        self.assertEqual({p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")},
                         {Path(p).name for p in historical} | set(MIGRATIONS))
        for path in historical:
            self.assertEqual((WORKSPACE / path).read_bytes(), git_blob(BASELINE, path), path)
        self.assertTrue(RELEASE_ID.fullmatch("280-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertTrue(RELEASE_ID.fullmatch("281-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertFalse(RELEASE_ID.fullmatch("282-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_official_raw_bytes_and_independent_checksums(self):
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "281")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registry["current_profile"][key], get_profile("281")[key])
            self.assertEqual(registry["historical_profiles"]["280"][key], get_profile("280")[key])
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text(encoding="utf-8").strip(), "0.2.19-baiyu")
        self.assertEqual(git_blob(UPSTREAM, "backend/cmd/server/VERSION").decode().strip(), "0.2.19")
        contract = next(e for e in registry["extensions"] if e["id"] == "migration-profile-contract")
        for filename, official, digest, checksum in zip(MIGRATIONS, OFFICIAL, RAW_SHA256, CATALOG_SHA256):
            raw = (WORKSPACE / "backend/migrations" / filename).read_bytes()
            self.assertEqual(raw, git_blob(UPSTREAM, "backend/migrations/" + official))
            self.assertEqual(hashlib.sha256(raw).hexdigest(), digest)
            self.assertEqual(migration_checksum(raw), checksum)
            self.assertEqual(registry["migration_contracts"][filename], digest)
            self.assertIn(filename, contract["migration_files"])
            self.assertFalse((WORKSPACE / "backend/migrations" / official).exists())

    def test_partial_upgrade_is_ordered_and_rejects_checksum_conflicts(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] not in MIGRATIONS}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([row["filename"] for row in plan["pending"]], MIGRATIONS)
        snapshot[MIGRATIONS[0]] = CATALOG_SHA256[0]
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], MIGRATIONS[1:])
        snapshot[MIGRATIONS[1]] = CATALOG_SHA256[1]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        for name in MIGRATIONS:
            self.assertFalse(plan_migrations(catalog, {**snapshot, name: "0" * 64})["existing_checksums_verified"])

    def test_downtime_and_all_ancestor_assertions_remain_required(self):
        profile = get_profile("281")
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, profile, "281-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh": str(n % 10) * 64 for n in (297, 298, 299, 301)}
        manifest = {"schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
                    "release_id": "281-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "281", "version": profile["version"],
                    "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
                    "parent_profile": "280", "new_migrations": MIGRATIONS, "release_policy": profile["release_policy"],
                    "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
                    "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets}
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.22-baiyu"}, {"parent_profile": "279"}, {"new_migrations": []}, {"new_migrations": list(reversed(MIGRATIONS))}):
                with self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)

    @unittest.skipUnless(JQ, "jq unavailable; set SUB2API_TEST_JQ for real filter validation")
    def test_all_inherited_jq_filters_accept_only_exact_281_contract(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "281", "parent_profile": "280", "new_migrations": MIGRATIONS,
                    "deployment_mode": "downtime", "migration_catalog": catalog}
        for number in (297, 298, 299, 301):
            source = (ROOT / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            invalid = [{**manifest, key: value} for key, value in (("profile", "282"), ("parent_profile", "279"), ("new_migrations", []), ("new_migrations", list(reversed(MIGRATIONS))), ("schema", 1))]
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            wrong_checksum = copy.deepcopy(manifest)
            next(r for r in wrong_checksum["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid += [wrong_checksum, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}]
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression],
                                        input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, document.get("profile"), document.get("parent_profile"), result.stderr))


class Profile281BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_actual_validator_exact_version_parent_migrations_and_mode(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; mode=$5; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["305_scoped_admin_api_keys.sql", "306_group_image_count_billing.sql"] and .deployment_mode == "downtime"') [[ $migrations == new_migrations && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        cases = (("281", "0.2.19-baiyu", "280", "new_migrations", "downtime", True),
                 ("281", "0.2.22-baiyu", "280", "new_migrations", "downtime", False),
                 ("281", "0.2.19-baiyu", "279", "new_migrations", "downtime", False),
                 ("281", "0.2.19-baiyu", "280", "none", "downtime", False),
                 ("281", "0.2.19-baiyu", "280", "new_migrations", "blue-green", False),
                 ("282", "0.2.19-baiyu", "280", "new_migrations", "downtime", False))
        for profile, version, parent, migrations, mode, allowed in cases:
            result = self.execute(stub + checks, profile, version, parent, migrations, mode)
            self.assertEqual(result.returncode == 0, allowed, (profile, result.stderr))

    def test_current_profile_guard_rejects_historical_unknown_and_malformed(self):
        validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        guard = next(line.strip() for line in validator.splitlines() if line.strip() == '[[ "$profile" == 281 ]]')
        prepare = (ROOT / "maintenance/release/prepare.sh").read_text(encoding="utf-8")
        self.assertIn('[[ $profile == 281 ]]', prepare)
        for value, allowed in (("281", True), ("280", False), ("282", False), ("0281", False), ("281;true", False)):
            result = self.execute('profile=$1\n' + guard, value)
            self.assertEqual(result.returncode == 0, allowed, (value, result.stderr))
