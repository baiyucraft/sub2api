"""Local successor contracts; these checks do not execute a PostgreSQL upgrade."""
from __future__ import annotations

import hashlib
import json
import os
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
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile

BASELINE = "a541a5c4bb6ad72e7198d2802fc6dfac22b305ee"
MIGRATION = "288_upstream_confidence_distribution.sql"
RAW_SHA256 = "18d33b7a9d64abab88d4cda73fded0e2ac3619daa11467704a4f2565b2c46835"


class Profile262ReleaseContractTest(unittest.TestCase):
    def test_successor_preserves_every_historical_profile(self) -> None:
        source = subprocess.run(
            ["git", "cat-file", "blob", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"],
            cwd=WORKSPACE, check=True, capture_output=True, timeout=10,
        ).stdout
        previous = {}
        exec(compile(source, "historical-profiles", "exec"), previous)
        self.assertEqual(previous["CURRENT_RELEASE_PROFILE"], "261")
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"262", "263", "264"})
        for profile, contract in previous["PROFILES"].items():
            with self.subTest(profile=profile):
                self.assertEqual(get_profile(profile), contract)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "264")
        current = get_profile("262")
        self.assertEqual(current["version"], "0.2.13-baiyu")
        self.assertEqual(current["parent"], "261")
        self.assertEqual(current["new_migrations"], [MIGRATION])
        self.assertEqual(current["gate_schema"], 2)
        self.assertEqual(current["release_policy"], get_profile("261")["release_policy"])
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("261")
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("262")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 265"):
            get_profile("265")

    def test_registration_and_both_checksum_contracts(self) -> None:
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registration["current_profile"]["id"], "264")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        for field in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["262"][field], get_profile("262")[field])
            self.assertEqual(registration["historical_profiles"]["261"][field], get_profile("261")[field])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(hashlib.sha256(raw).hexdigest(), RAW_SHA256)
        self.assertEqual(registration["migration_contracts"][MIGRATION], RAW_SHA256)
        catalog = {entry["filename"]: entry for entry in discover_migration_catalog(WORKSPACE)}
        self.assertEqual(catalog[MIGRATION]["checksum"], migration_checksum(raw))
        self.assertFalse(catalog[MIGRATION]["non_transactional"])
        self.assertNotIn(MIGRATION, CHECKSUM_COMPATIBILITY_RULES)
        self.assertTrue(registration["migration_assertions"]["backend/migrations/" + MIGRATION])

    def test_upgrade_replay_and_checksum_conflict(self) -> None:
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {entry["filename"]: entry["checksum"] for entry in catalog if entry["filename"] != MIGRATION}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([entry["filename"] for entry in plan["pending"]], [MIGRATION])
        self.assertEqual(pending_hooks(plan["pending"]), [])
        snapshot[MIGRATION] = plan["pending"][0]["checksum"]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        conflict = plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})
        self.assertFalse(conflict["existing_checksums_verified"])
        self.assertEqual([entry["filename"] for entry in conflict["conflicts"]], [MIGRATION])

    def test_manifest_contract_rejects_wrong_version_parent_or_migrations(self) -> None:
        profile = get_profile("262")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "262-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "262", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "261",
            "new_migrations": [MIGRATION], "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in (
                {"version": "0.2.11-baiyu"}, {"parent_profile": "260"}, {"parent_profile": "262"},
                {"profile": "263"}, {"release_id": "261-aaaaaaaaaaaa-1-aaaaaaaa"},
                {"new_migrations": []}, {"new_migrations": get_profile("261")["new_migrations"]},
                {"new_migrations": [MIGRATION, *get_profile("261")["new_migrations"]]},
            ):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)

    def test_ancestor_lifecycle_semantics_remain_required(self) -> None:
        evidence = {"migration_evidence": {
            "database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
            "isolated_upgrade_verified": True, "final_schema_verified": True,
        }}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "262", "migration_catalog": []}, evidence)

    def test_vm_validator_exact_profile_contract(self) -> None:
        bash = Path(r"C:\Program Files\Git\bin\bash.exe") if os.name == "nt" else Path("/bin/bash")
        if not bash.is_file():
            self.skipTest("Bash unavailable; this test is not VM validation")
        validator = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = validator.index('  if [[ "$profile" == 254 ]]; then')
        checks = validator[begin:validator.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; new_migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["286_add_payment_order_bonus_amount.sql", "287_add_typesafe_platform.sql"]') [[ $new_migrations == release261 ]] ;;
    '.new_migrations == ["288_upstream_confidence_distribution.sql"]') [[ $new_migrations == distribution262 ]] ;;
    *) return 1 ;;
  esac
}
'''
        environment = os.environ.copy()
        environment.pop("BASH_ENV", None)
        for profile, version, parent, migrations, allowed in (
            ("261", "0.2.13-baiyu", "260", "release261", True),
            ("262", "0.2.13-baiyu", "261", "distribution262", True),
            ("262", "0.2.11-baiyu", "261", "distribution262", False),
            ("262", "0.2.13-baiyu", "260", "distribution262", False),
            ("262", "0.2.13-baiyu", "262", "distribution262", False),
            ("262", "0.2.13-baiyu", "261", "release261", False),
            ("262", "0.2.13-baiyu", "261", "empty", False),
            ("263", "0.2.13-baiyu", "262", "distribution262", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = subprocess.run(
                    [str(bash), "--noprofile", "--norc", "-e", "-u", "-c", stub + checks, "contract", profile, version, parent, migrations],
                    env=environment, capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
