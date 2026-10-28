"""Local profile 266 admission contracts; no VM or production execution."""
from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))
from release.gate import _validate_v2_pending
from release.manifest import validate_manifest_profile_contract
from release.migration_planner import CHECKSUM_COMPATIBILITY_RULES, catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum, pending_hooks, plan_migrations
from release.paths import WORKSPACE
from release.production_cleanup import RELEASE_ID
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
import test_profile263_release_contract as bash_helpers

BASELINE = "fc46d6e332f4a2dffdad4fceb53f5205b2f9ce75"
MIGRATION = "291_user_invoice_annotations.sql"
RAW_SHA256 = "18c1af60bc42f747314ddd7c51d97a613c533c374d4235f8208d1f0dad7e843a"


class Profile266ReleaseContractTest(unittest.TestCase):
    def test_additive_profile_preserves_all_historical_profiles_and_sql(self):
        source = subprocess.run(["git", "cat-file", "blob", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"], cwd=WORKSPACE, check=True, capture_output=True, timeout=10).stdout
        baseline = {}
        exec(compile(source, "historical-profiles", "exec"), baseline)
        self.assertEqual(baseline["CURRENT_RELEASE_PROFILE"], "265")
        self.assertEqual(set(PROFILES), set(baseline["PROFILES"]) | {"266", "267", "268", "269", "270", "271", "272", "273", "274", "275", "276", "277", "278", "279", "280"})
        for name, contract in baseline["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
            with self.assertRaisesRegex(ValueError, "historical"):
                get_release_profile(name)
        historical = subprocess.run(["git", "ls-tree", "-r", BASELINE, "backend/migrations"], cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=10).stdout
        old_sql = {}
        for line in historical.splitlines():
            metadata, path = line.split("\t", 1)
            if path.endswith(".sql"):
                old_sql[path] = metadata.split()[2]
        for path, blob in old_sql.items():
            content = (WORKSPACE / path).read_bytes()
            self.assertEqual(hashlib.sha1(f"blob {len(content)}\0".encode() + content).hexdigest(), blob, path)
        current_sql = {p.relative_to(WORKSPACE).as_posix() for p in (WORKSPACE / "backend/migrations").glob("*.sql")}
        self.assertEqual(current_sql, set(old_sql) | {f"backend/migrations/{name}" for name in [MIGRATION, *get_profile("267")["new_migrations"], *get_profile("268")["new_migrations"], *get_profile("269")["new_migrations"], *get_profile("270")["new_migrations"], *get_profile("271")["new_migrations"], *get_profile("273")["new_migrations"], *get_profile("274")["new_migrations"], *get_profile("275")["new_migrations"], *get_profile("276")["new_migrations"], *get_profile("277")["new_migrations"], *get_profile("278")["new_migrations"]]})
        self.assertEqual(CURRENT_RELEASE_PROFILE, "280")
        current = get_profile("266")
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("266")
        self.assertEqual((current["version"], current["parent"], current["gate_schema"]), ("0.2.15-baiyu", "265", 2))
        self.assertEqual(current["new_migrations"], [MIGRATION])
        self.assertEqual(current["release_policy"], get_profile("265")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 281"):
            get_profile("281")

    def test_registration_and_distinct_checksum_contracts(self):
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["266"][field], get_profile("266")[field])
            self.assertEqual(registration["historical_profiles"]["265"][field], get_profile("265")[field])
        self.assertEqual(registration["current_profile"]["id"], "280")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), RAW_SHA256)
        self.assertEqual(registration["migration_contracts"][MIGRATION], RAW_SHA256)
        catalog = {entry["filename"]: entry for entry in discover_migration_catalog(WORKSPACE)}
        self.assertEqual(catalog[MIGRATION]["checksum"], migration_checksum(raw))
        self.assertFalse(catalog[MIGRATION]["non_transactional"])
        self.assertNotIn(MIGRATION, CHECKSUM_COMPATIBILITY_RULES)
        for marker in registration["migration_assertions"][f"backend/migrations/{MIGRATION}"]:
            self.assertIn(marker, raw.decode("utf-8"))
        extension = next(item for item in registration["extensions"] if item["id"] == "user-funds-summary-and-invoice-annotations")
        self.assertEqual(extension["migration_files"], [MIGRATION])
        for test in extension["required_tests"]:
            self.assertTrue((WORKSPACE / test).is_file(), test)

    def test_pending_upgrade_verified_replay_and_checksum_conflict(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {entry["filename"]: entry["checksum"] for entry in catalog if entry["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertEqual([entry["filename"] for entry in plan["pending"]], [MIGRATION])
        self.assertEqual(pending_hooks(plan["pending"]), [])
        snapshot[MIGRATION] = plan["pending"][0]["checksum"]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        snapshot[MIGRATION] = "0" * 64
        self.assertTrue(plan_migrations(catalog, snapshot)["conflicts"])
        self.assertIsNotNone(RELEASE_ID.fullmatch("266-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("272-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("281-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_manifest_binds_identity_and_retains_ancestor_evidence(self):
        profile = get_profile("266")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "266-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "266", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "265",
            "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.14-baiyu"}, {"parent_profile": "264"}, {"parent_profile": "266"}, {"new_migrations": []}, {"new_migrations": get_profile("265")["new_migrations"]}, {"profile": "267"}):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
        evidence = {"migration_evidence": {"database_high_watermark": None, "pending": [], "existing_checksums_verified": True, "isolated_upgrade_verified": True, "final_schema_verified": True}}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "266", "migration_catalog": []}, evidence)


class Profile266BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute
    assert_guard_cases = bash_helpers.Profile263BashContractTest.assert_guard_cases
    test_release_id_guards = bash_helpers.Profile263BashContractTest.test_release_id_guards_retain_history_and_reject_unknown
    test_ancestor_assertion_guards = bash_helpers.Profile263BashContractTest.test_ancestor_assertion_guards_admit_263_through_270_and_reject_271

    def test_vm_exact_new_contract_retains_265_and_rejects_unknown_268(self):
        validator = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = validator.index('  if [[ "$profile" == 254 ]]; then')
        checks = validator[begin:validator.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["290_drop_platform_check_constraints.sql"]') [[ $migrations == platform265 ]] ;;
    '.new_migrations == ["291_user_invoice_annotations.sql"]') [[ $migrations == invoice266 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("265", "0.2.15-baiyu", "264", "platform265", True),
            ("266", "0.2.15-baiyu", "265", "invoice266", True),
            ("266", "0.2.14-baiyu", "265", "invoice266", False),
            ("266", "0.2.15-baiyu", "264", "invoice266", False),
            ("266", "0.2.15-baiyu", "266", "invoice266", False),
            ("266", "0.2.15-baiyu", "265", "platform265", False),
            ("266", "0.2.15-baiyu", "265", "empty", False),
            ("268", "0.2.15-baiyu", "266", "invoice266", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
