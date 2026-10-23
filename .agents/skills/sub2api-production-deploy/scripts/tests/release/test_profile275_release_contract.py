"""Official 0.2.18 bytes, immutable predecessors and executable release admission."""
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
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum, pending_hooks, plan_migrations
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

BASELINE = "6d53559d88c46f557e4db5658ac20b61b049c546"
UPSTREAM = "5c185c5c5d4f5e409e2139806e0964ae944e03e8"
MIGRATION = "302_enable_gemini_image_generation_groups.sql"
OFFICIAL = "246_enable_gemini_image_generation_groups.sql"
RAW_SHA256 = "f49b458cfbd52bd17f5bb02e30e3813c8e192e4ae38bce6358713ef65f3874b5"
CATALOG_SHA256 = "87267be7ab657173768989e2b0b105c5569c5930a7f1ed124f4cf81e5198297c"
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


def git_blob(ref: str, path: str) -> bytes:
    return subprocess.run(["git", "cat-file", "blob", f"{ref}:{path}"], cwd=WORKSPACE,
                          check=True, capture_output=True, timeout=30).stdout


class Profile275ReleaseContractTest(unittest.TestCase):
    def test_successor_preserves_every_previous_profile_and_sql(self):
        previous = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"),
                     "profile274-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"275"})
        for name, contract in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "275")
        current = get_release_profile("275")
        self.assertEqual((current["version"], current["parent"], current["gate_schema"], current["new_migrations"]),
                         ("0.2.18-baiyu", "274", 2, [MIGRATION]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("274")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 276"):
            get_profile("276")
        names = subprocess.run(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"],
                               cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=30).stdout.splitlines()
        changed = subprocess.run(["git", "diff", "--name-only", BASELINE, "--", "backend/migrations/*.sql"],
                                 cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=30).stdout.splitlines()
        self.assertLessEqual(set(changed), {"backend/migrations/" + MIGRATION})
        current_names = {p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")}
        self.assertEqual(current_names, {Path(p).name for p in names if p.endswith(".sql")} | {MIGRATION})
        self.assertIsNotNone(RELEASE_ID.fullmatch("275-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("276-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_official_bytes_and_independent_checksum_contracts(self):
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(raw, git_blob(UPSTREAM, "backend/migrations/" + OFFICIAL))
        self.assertEqual(hashlib.sha256(raw).hexdigest(), RAW_SHA256)
        self.assertEqual(migration_checksum(raw), CATALOG_SHA256)
        self.assertFalse((WORKSPACE / "backend/migrations" / OFFICIAL).exists())
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "275")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registry["current_profile"][key], get_profile("275")[key])
            self.assertEqual(registry["historical_profiles"]["274"][key], get_profile("274")[key])
        self.assertEqual(registry["migration_contracts"][MIGRATION], RAW_SHA256)
        self.assertIn(MIGRATION, next(e for e in registry["extensions"] if e["id"] == "migration-profile-contract")["migration_files"])
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text().strip(), "0.2.18-baiyu")

    def test_partial_upgrade_checksum_conflict_and_inherited_301_restore(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([row["filename"] for row in plan["pending"]], [MIGRATION])
        snapshot[MIGRATION] = migration_checksum((WORKSPACE / "backend/migrations" / MIGRATION).read_bytes())
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])
        self.assertFalse(plan_migrations(catalog, {**snapshot, "999_unknown.sql": "0" * 64})["existing_checksums_verified"])
        snapshot.pop(MIGRATION)
        snapshot.pop("301_upstream_cache_guard_admissions.sql")
        plan = plan_migrations(catalog, snapshot)
        self.assertEqual([row["filename"] for row in plan["pending"]], ["301_upstream_cache_guard_admissions.sql", MIGRATION])
        hook = next(h for h in pending_hooks(plan["pending"]) if h["filename"].startswith("301_"))
        self.assertEqual(hook["rollback_policy"], "coordinated_restore")
        self.assertEqual(hook["script"], "migration-301-assert.sh")

    def test_downtime_is_required_before_manifest_side_effects(self):
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, get_profile("275"), "275-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        with self.assertRaisesRegex(RuntimeError, "requires downtime"):
            validate_manifest_profile_contract({"schema": 2, "deployment_mode": "blue-green"}, get_profile("275"))

    def test_manifest_binds_identity_and_all_ancestor_assertion_assets(self):
        profile = get_profile("275")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh": str(n % 10) * 64
                  for n in (297, 298, 299, 301)}
        manifest = {"schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
                    "release_id": "275-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "275", "version": profile["version"],
                    "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
                    "parent_profile": "274", "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
                    "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
                    "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets}
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.17-baiyu"}, {"parent_profile": "273"}, {"new_migrations": []},
                             {"new_migrations": ["301_upstream_cache_guard_admissions.sql"]}, {"profile": "276"}):
                with self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)

    @unittest.skipUnless(JQ, "jq unavailable; set SUB2API_TEST_JQ for real filter validation")
    def test_every_inherited_filter_binds_the_new_parent_and_migration(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "275", "parent_profile": "274", "new_migrations": [MIGRATION],
                    "deployment_mode": "downtime", "migration_catalog": catalog}
        for number in (297, 298, 299, 301):
            source = (ROOT / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            checksum_error = copy.deepcopy(manifest)
            next(r for r in checksum_error["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid = [{**manifest, key: value} for key, value in (("profile", "276"), ("parent_profile", "273"), ("new_migrations", []), ("new_migrations", [row["filename"]]), ("schema", 1))]
            invalid.extend((checksum_error, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}))
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression],
                                        input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, document, result.stderr))


class Profile275BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_exact_version_parent_migration_mode_and_unknown_successor(self):
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; mode=$5; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["302_enable_gemini_image_generation_groups.sql"] and .deployment_mode == "downtime"') [[ $migrations == image_groups && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        cases = (("275", "0.2.18-baiyu", "274", "image_groups", "downtime", True),
                 ("275", "0.2.17-baiyu", "274", "image_groups", "downtime", False),
                 ("275", "0.2.18-baiyu", "273", "image_groups", "downtime", False),
                 ("275", "0.2.18-baiyu", "274", "none", "downtime", False),
                 ("275", "0.2.18-baiyu", "274", "image_groups", "blue-green", False),
                 ("276", "0.2.18-baiyu", "275", "image_groups", "downtime", False))
        for profile, version, parent, migrations, mode, allowed in cases:
            result = self.execute(stub + checks, profile, version, parent, migrations, mode)
            self.assertEqual(result.returncode == 0, allowed, result.stderr)
