"""Cache protection profile contracts. No VM, database or production execution."""
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
from release.migration_planner import (
    CHECKSUM_COMPATIBILITY_RULES, catalog_sha256, checksum_policy_sha256,
    discover_migration_catalog, migration_checksum, pending_hooks, plan_migrations,
)
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

BASELINE = "50804a7835cfc2e443a255d30e454b060b000c37"
MIGRATION = "296_upstream_cache_guard.sql"


def git_blob(path: str) -> bytes:
    return subprocess.run(
        ["git", "cat-file", "blob", f"{BASELINE}:{path}"], cwd=WORKSPACE,
        check=True, capture_output=True, timeout=10,
    ).stdout


class Profile268ReleaseContractTest(unittest.TestCase):
    def test_historical_profiles_migrations_and_checksums_are_immutable(self):
        baseline = {}
        exec(compile(git_blob(".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), baseline)
        self.assertEqual(baseline["CURRENT_RELEASE_PROFILE"], "267")
        self.assertEqual(set(PROFILES), set(baseline["PROFILES"]) | {"268", "269", "270", "271"})
        for name, profile in baseline["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
            with self.assertRaisesRegex(ValueError, "historical"):
                get_release_profile(name)
        tree = subprocess.run(
            ["git", "ls-tree", "-r", "--name-only", BASELINE, "backend/migrations"],
            cwd=WORKSPACE, check=True, capture_output=True, text=True, timeout=10,
        ).stdout
        historical = {path for path in tree.splitlines() if path.endswith(".sql")}
        for path in historical:
            self.assertEqual((WORKSPACE / path).read_bytes(), git_blob(path), path)
        current = {path.relative_to(WORKSPACE).as_posix() for path in (WORKSPACE / "backend/migrations").glob("*.sql")}
        self.assertEqual(current, historical | {f"backend/migrations/{MIGRATION}", "backend/migrations/297_upstream_cache_guard_account_scope.sql", "backend/migrations/298_group_scheduling_protection_policies.sql", "backend/migrations/299_upstream_cache_guard_revisions.sql"})
        prior = json.loads(git_blob(".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"))
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        for name, checksum in prior["migration_contracts"].items():
            self.assertEqual(registration["migration_contracts"][name], checksum, name)
        for name, profile in prior["historical_profiles"].items():
            self.assertEqual(registration["historical_profiles"][name], profile, name)
        self.assertEqual(registration["historical_profiles"]["267"], {key: value for key, value in prior["current_profile"].items() if key not in ("id", "status")})

    def test_profile_registration_and_raw_vs_runner_checksum(self):
        profile = get_profile("268")
        self.assertEqual(CURRENT_RELEASE_PROFILE, "272")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.16-baiyu", "267", 2))
        self.assertEqual(profile["new_migrations"], [MIGRATION])
        self.assertEqual(profile["release_policy"], get_profile("267")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 273"):
            get_profile("273")
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registration["current_profile"]["id"], "272")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["268"][field], profile[field])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registration["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        catalog = {entry["filename"]: entry for entry in discover_migration_catalog(WORKSPACE)}
        self.assertEqual(catalog[MIGRATION]["checksum"], migration_checksum(raw))
        self.assertFalse(catalog[MIGRATION]["non_transactional"])
        self.assertNotIn(MIGRATION, CHECKSUM_COMPATIBILITY_RULES)
        for marker in registration["migration_assertions"][f"backend/migrations/{MIGRATION}"]:
            self.assertIn(marker, raw.decode())
        extension = next(item for item in registration["extensions"] if item["id"] == "upstream-cache-affinity-guard")
        self.assertEqual(extension["migration_files"], [MIGRATION, "297_upstream_cache_guard_account_scope.sql", "298_group_scheduling_protection_policies.sql"])
        for path in extension["required_tests"]:
            self.assertTrue((WORKSPACE / path).is_file(), path)

    def test_upgrade_replay_checksum_conflict_and_unknown_successor(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {entry["filename"]: entry["checksum"] for entry in catalog if entry["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([entry["filename"] for entry in plan["pending"]], [MIGRATION])
        self.assertEqual(pending_hooks(plan["pending"]), [])
        snapshot[MIGRATION] = plan["pending"][0]["checksum"]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        bad = plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})
        self.assertFalse(bad["existing_checksums_verified"])
        self.assertEqual([entry["filename"] for entry in bad["conflicts"]], [MIGRATION])
        self.assertIsNotNone(RELEASE_ID.fullmatch("268-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("272-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_manifest_identity_and_ancestor_evidence_remain_required(self):
        profile = get_profile("268")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "268-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "268", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "267",
            "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"parent_profile": "266"}, {"parent_profile": "268"}, {"profile": "267"}, {"profile": "269"}, {"new_migrations": []}, {"new_migrations": get_profile("267")["new_migrations"]}, {"version": "0.2.15-baiyu"}):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
        evidence = {"migration_evidence": {
            "database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
            "isolated_upgrade_verified": True, "final_schema_verified": True,
        }}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "268", "migration_catalog": []}, evidence)


class Profile268BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute
    assert_guard_cases = bash_helpers.Profile263BashContractTest.assert_guard_cases
    test_release_id_guards = bash_helpers.Profile263BashContractTest.test_release_id_guards_retain_history_and_reject_unknown
    test_ancestor_assertion_guards = bash_helpers.Profile263BashContractTest.test_ancestor_assertion_guards_admit_263_through_270_and_reject_271

    def test_vm_admits_exact_268_and_retains_267(self):
        source = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["296_upstream_cache_guard.sql"]') [[ $migrations == cache268 ]] ;;
    '.new_migrations == ["292_group_i18n.sql", "293_gemini_cached_contents.sql", "294_gemini_cached_contents_channel_usage.sql", "295_gemini_cached_contents_mutation_lease.sql"]') [[ $migrations == upstream267 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("267", "0.2.16-baiyu", "266", "upstream267", True),
            ("268", "0.2.16-baiyu", "267", "cache268", True),
            ("268", "0.2.15-baiyu", "267", "cache268", False),
            ("268", "0.2.16-baiyu", "266", "cache268", False),
            ("268", "0.2.16-baiyu", "268", "cache268", False),
            ("268", "0.2.16-baiyu", "267", "upstream267", False),
            ("268", "0.2.16-baiyu", "267", "empty", False),
            ("269", "0.2.16-baiyu", "268", "cache268", False),
        ):
            with self.subTest(profile=profile, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
