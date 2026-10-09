"""Profile 265 local admission/planner contracts; no VM or production execution."""
from __future__ import annotations

import hashlib
import json
import re
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

BASELINE = "293864a601e829dbf5f344883a872dfdec03ec55"
UPSTREAM = "3a6fd1c9db07203ca308aaba69e502bc1f35b307"
MIGRATION = "290_drop_platform_check_constraints.sql"
OFFICIAL = "242_drop_platform_check_constraints.sql"
RAW_SHA256 = "89b9ca37806f5725fd01cd3b45d006c119d34820e29e1b8a90c0a8ba461db5bb"


def git_blob(commit: str, path: str) -> bytes:
    return subprocess.run(["git", "cat-file", "blob", f"{commit}:{path}"], cwd=WORKSPACE, check=True, capture_output=True, timeout=10).stdout


class Profile265ReleaseContractTest(unittest.TestCase):
    def test_all_historical_profiles_and_sql_remain_immutable(self):
        baseline = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), baseline)
        self.assertEqual(baseline["CURRENT_RELEASE_PROFILE"], "264")
        self.assertEqual(set(PROFILES), set(baseline["PROFILES"]) | {"265"})
        for name, profile in baseline["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
            with self.assertRaisesRegex(ValueError, "historical"):
                get_release_profile(name)
        for path in sorted((WORKSPACE / "backend/migrations").glob("*.sql")):
            if path.name != MIGRATION:
                self.assertEqual(path.read_bytes(), git_blob(BASELINE, f"backend/migrations/{path.name}"), path.name)
        current = get_release_profile("265")
        self.assertEqual(CURRENT_RELEASE_PROFILE, "265")
        self.assertEqual((current["version"], current["parent"], current["gate_schema"]), ("0.2.15-baiyu", "264", 2))
        self.assertEqual(current["new_migrations"], [MIGRATION])
        self.assertEqual(current["release_policy"], get_profile("264")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 266"):
            get_profile("266")

    def test_official_raw_bytes_registration_and_checksum_contracts(self):
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registration["current_profile"][field], get_profile("265")[field])
            self.assertEqual(registration["historical_profiles"]["264"][field], get_profile("264")[field])
        self.assertEqual(registration["current_profile"]["id"], "265")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        self.assertEqual(registration["version_contract"]["official_release_versions"][UPSTREAM], "0.2.15")
        self.assertEqual(git_blob(UPSTREAM, "backend/cmd/server/VERSION").decode().strip(), "0.2.15")
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(raw, git_blob(UPSTREAM, "backend/migrations/" + OFFICIAL))
        self.assertEqual(hashlib.sha256(raw).hexdigest(), RAW_SHA256)
        self.assertEqual(registration["migration_contracts"][MIGRATION], RAW_SHA256)
        catalog = {entry["filename"]: entry for entry in discover_migration_catalog(WORKSPACE)}
        self.assertNotIn(OFFICIAL, catalog)
        self.assertEqual(catalog[MIGRATION]["checksum"], migration_checksum(raw))
        self.assertFalse(catalog[MIGRATION]["non_transactional"])
        self.assertNotIn(MIGRATION, CHECKSUM_COMPATIBILITY_RULES)

    def test_partial_upgrade_verified_replay_and_conflict(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {entry["filename"]: entry["checksum"] for entry in catalog if entry["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertEqual([entry["filename"] for entry in plan["pending"]], [MIGRATION])
        self.assertEqual(pending_hooks(plan["pending"]), [])
        snapshot[MIGRATION] = plan["pending"][0]["checksum"]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        snapshot[MIGRATION] = "0" * 64
        self.assertTrue(plan_migrations(catalog, snapshot)["conflicts"])
        self.assertIsNotNone(RELEASE_ID.fullmatch("265-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("266-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_manifest_binds_new_profile_exactly_and_requires_ancestor_evidence(self):
        profile = get_profile("265")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "265-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "265", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "264",
            "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.14-baiyu"}, {"parent_profile": "263"}, {"parent_profile": "265"}, {"new_migrations": []}, {"new_migrations": get_profile("264")["new_migrations"]}, {"profile": "266"}):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
        evidence = {"migration_evidence": {"database_high_watermark": None, "pending": [], "existing_checksums_verified": True, "isolated_upgrade_verified": True, "final_schema_verified": True}}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "265", "migration_catalog": []}, evidence)


class Profile265BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute
    assert_guard_cases = bash_helpers.Profile263BashContractTest.assert_guard_cases
    test_release_id_guards_retain_history_and_reject_unknown = bash_helpers.Profile263BashContractTest.test_release_id_guards_retain_history_and_reject_unknown
    test_ancestor_assertion_guards = bash_helpers.Profile263BashContractTest.test_ancestor_assertion_guards_admit_263_through_265_and_reject_266

    def test_vm_exact_version_parent_and_new_migration_contract(self):
        validator = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = validator.index('  if [[ "$profile" == 254 ]]; then')
        checks = validator[begin:validator.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["290_drop_platform_check_constraints.sql"]') [[ $migrations == platform265 ]] ;;
    '.new_migrations == ["289_activity_reward_costs.sql"]') [[ $migrations == reward264 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("264", "0.2.14-baiyu", "263", "reward264", True),
            ("265", "0.2.15-baiyu", "264", "platform265", True),
            ("265", "0.2.14-baiyu", "264", "platform265", False),
            ("265", "0.2.15-baiyu", "263", "platform265", False),
            ("265", "0.2.15-baiyu", "265", "platform265", False),
            ("265", "0.2.15-baiyu", "264", "reward264", False),
            ("265", "0.2.15-baiyu", "264", "empty", False),
            ("266", "0.2.15-baiyu", "265", "platform265", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_reward_postflight_is_inherited_only_for_downtime(self):
        for relative in ("maintenance/release/switch.sh", "maintenance/release/activity-reward-cost-postflight.sh"):
            source = (DEPLOY_ROOT / relative).read_text(encoding="utf-8")
            guard = next(re.search(r"\[\[.*?\]\]", line)[0] for line in source.splitlines() if "$release_profile == 264" in line)
            for profile, mode, allowed in (("264", "downtime", True), ("265", "downtime", True), ("265", "blue-green", False), ("266", "downtime", False)):
                result = self.execute('release_profile=$1; deployment_mode=$2\n' + guard, profile, mode)
                self.assertEqual(result.returncode == 0, allowed, (relative, result.stderr))
        source = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn('if [[ "$profile" == 264 || "$profile" == 265 ]]; then\n    # Exercise', source)


if __name__ == "__main__":
    unittest.main()
