"""Official MCP OAuth migration is additive and preserves every historical release contract."""
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
from test_profile281_release_contract import git_blob

BASELINE = "5193a6bfe3ee78c521885de2e9aa8baf3bacd5dd"
MIGRATION = "308_mcp_oauth.sql"
RAW_SHA256 = "881ff2e8ecf430202c593c699b1441db1d2196ac2c312d8a3849acb6c97609e5"
CATALOG_SHA256 = "751a1dcee6108afcecbd580b6e3140cca3480853fecd2174861b02f3a71e54a1"
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


class Profile283ReleaseContractTest(unittest.TestCase):
    def test_successor_preserves_every_previous_profile_and_sql(self):
        previous = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"),
                     "profile282-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"283"})
        for name, contract in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "283")
        profile = get_release_profile("283")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]),
                         ("0.2.20-baiyu", "282", 2, [MIGRATION]))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("282")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 284"):
            get_profile("284")
        historical = subprocess.run(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"],
                                   cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=30).stdout.splitlines()
        historical = [path for path in historical if path.endswith(".sql")]
        self.assertEqual(len(historical), 358)
        self.assertEqual({p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")},
                         {Path(p).name for p in historical} | {MIGRATION})
        for path in historical:
            self.assertEqual((WORKSPACE / path).read_bytes(), git_blob(BASELINE, path), path)
        self.assertTrue(RELEASE_ID.fullmatch("282-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertTrue(RELEASE_ID.fullmatch("283-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertFalse(RELEASE_ID.fullmatch("284-aaaaaaaaaaaa-1-aaaaaaaa"))
        protected = ".agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py"
        self.assertEqual((WORKSPACE / protected).read_bytes(), git_blob(BASELINE, protected))

    def test_registry_and_migration_have_independent_checksums(self):
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "283")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registry["current_profile"][key], get_profile("283")[key])
            self.assertEqual(registry["historical_profiles"]["282"][key], get_profile("282")[key])
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text(encoding="utf-8").strip(), "0.2.20-baiyu")
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(raw, git_blob("6b4b37db43443b5e88e424fe3c69602cc3810dfb", "backend/migrations/249_mcp_oauth.sql"))
        self.assertEqual(hashlib.sha256(raw).hexdigest(), RAW_SHA256)
        self.assertEqual(migration_checksum(raw), CATALOG_SHA256)
        self.assertEqual(registry["migration_contracts"][MIGRATION], RAW_SHA256)
        for extension in ("migration-profile-contract", "mcp-oauth-migration"):
            entry = next(e for e in registry["extensions"] if e["id"] == extension)
            self.assertIn(MIGRATION, entry["migration_files"])

    def test_missing_applied_and_checksum_conflicting_migration(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([row["filename"] for row in plan["pending"]], [MIGRATION])
        snapshot[MIGRATION] = CATALOG_SHA256
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])

    def test_downtime_and_all_ancestor_assertions_remain_required(self):
        profile = get_profile("283")
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, profile, "283-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh": str(n % 10) * 64 for n in (297, 298, 299, 301)}
        manifest = {"schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
                    "release_id": "283-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "283", "version": profile["version"],
                    "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
                    "parent_profile": "282", "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
                    "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
                    "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets}
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.22-baiyu"}, {"parent_profile": "281"}, {"new_migrations": []}, {"new_migrations": [MIGRATION, MIGRATION]}):
                with self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)

    @unittest.skipUnless(JQ, "jq unavailable; set SUB2API_TEST_JQ for real filter validation")
    def test_all_ancestor_jq_filters_accept_exact_283_and_historical_282(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "283", "parent_profile": "282", "new_migrations": [MIGRATION],
                    "deployment_mode": "downtime", "migration_catalog": catalog}
        historical = {**manifest, "profile": "282", "parent_profile": "281", "new_migrations": get_profile("282")["new_migrations"]}
        for number in (297, 298, 299, 301):
            source = (ROOT / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            invalid = [{**manifest, key: value} for key, value in (("profile", "284"), ("parent_profile", "281"), ("new_migrations", []), ("new_migrations", [MIGRATION, MIGRATION]), ("schema", 1))]
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            wrong_checksum = copy.deepcopy(manifest)
            next(r for r in wrong_checksum["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid += [wrong_checksum, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}]
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), (historical, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression],
                                        input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, document.get("profile"), document.get("parent_profile"), result.stderr))


class Profile283BashContractTest(unittest.TestCase):
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
    '.new_migrations == ["308_mcp_oauth.sql"] and .deployment_mode == "downtime"') [[ $migrations == new_migrations && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        cases = (("283", "0.2.20-baiyu", "282", "new_migrations", "downtime", True),
                 ("283", "0.2.22-baiyu", "282", "new_migrations", "downtime", False),
                 ("283", "0.2.20-baiyu", "281", "new_migrations", "downtime", False),
                 ("283", "0.2.20-baiyu", "282", "none", "downtime", False),
                 ("283", "0.2.20-baiyu", "282", "new_migrations", "blue-green", False),
                 ("284", "0.2.20-baiyu", "282", "new_migrations", "downtime", False))
        for profile, version, parent, migrations, mode, allowed in cases:
            result = self.execute(stub + checks, profile, version, parent, migrations, mode)
            self.assertEqual(result.returncode == 0, allowed, (profile, result.stderr))

    def test_current_only_guard_rejects_history_unknown_and_malformed(self):
        validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        guard = next(line.strip() for line in validator.splitlines() if line.strip() == '[[ "$profile" == 283 ]]')
        prepare = (ROOT / "maintenance/release/prepare.sh").read_text(encoding="utf-8")
        self.assertIn('[[ $profile == 283 ]]', prepare)
        for value, allowed in (("283", True), ("282", False), ("284", False), ("0283", False), ("283;true", False)):
            result = self.execute('profile=$1\n' + guard, value)
            self.assertEqual(result.returncode == 0, allowed, (value, result.stderr))
