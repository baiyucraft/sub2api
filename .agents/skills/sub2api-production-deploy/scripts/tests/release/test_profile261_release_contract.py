"""Local catalog/planner and isolated Bash contracts; no PostgreSQL upgrade runs."""
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

UPSTREAM = "b8dece9000c68815a5b867ca5a1e6f236e173905"
BASELINE = "f670e8051529776fa681b628eb05ce073d73c17c"
MIGRATIONS = ["286_add_payment_order_bonus_amount.sql", "287_add_typesafe_platform.sql"]
OFFICIAL = ["241_add_payment_order_bonus_amount.sql", "241_add_typesafe_platform.sql"]
RAW_SHA256 = [
    "ddd45f14154ea6d6b5b612bee7eb49436e1f2a1665f9a71eb8304e48475ed12d",
    "419fceaa3e6f1bd089a454fda30b9d343a83fff75ad3b6dffdc44287bf99d171",
]


def git_blob(ref: str, path: str) -> bytes:
    return subprocess.run(
        ["git", "cat-file", "blob", f"{ref}:{path}"], cwd=WORKSPACE,
        check=True, capture_output=True, timeout=10,
    ).stdout


class Profile261ReleaseContractTest(unittest.TestCase):
    def test_current_successor_and_all_historical_profiles_are_immutable(self) -> None:
        previous = {}
        source = git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py")
        exec(compile(source, "baseline-profiles", "exec"), previous)
        self.assertEqual(previous["CURRENT_RELEASE_PROFILE"], "260")
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"261", "262", "263", "264", "265"})
        for name, contract in previous["PROFILES"].items():
            with self.subTest(profile=name):
                self.assertEqual(get_profile(name), contract)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "265")
        current = get_profile("261")
        self.assertEqual(current["version"], "0.2.13-baiyu")
        self.assertEqual(current["parent"], "260")
        self.assertEqual(current["new_migrations"], MIGRATIONS)
        self.assertEqual(current["gate_schema"], 2)
        self.assertEqual(current["release_policy"], get_profile("260")["release_policy"])
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("260")
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("261")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 266"):
            get_profile("266")
        with self.assertRaises(ValueError):
            get_release_profile("266")

    def test_official_raw_bytes_and_catalog_checksums_match(self) -> None:
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        catalog = {item["filename"]: item for item in discover_migration_catalog(WORKSPACE)}
        self.assertEqual(git_blob(UPSTREAM, "backend/cmd/server/VERSION").decode().strip(), "0.2.13")
        self.assertEqual(registration["version_contract"]["official_release_versions"][UPSTREAM], "0.2.13")
        self.assertEqual(registration["current_profile"]["id"], "265")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        for field in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["261"][field], get_profile("261")[field])
            self.assertEqual(registration["historical_profiles"]["260"][field], get_profile("260")[field])
        for filename, official, digest in zip(MIGRATIONS, OFFICIAL, RAW_SHA256):
            with self.subTest(migration=filename):
                content = (WORKSPACE / "backend/migrations" / filename).read_bytes()
                self.assertEqual(content, git_blob(UPSTREAM, f"backend/migrations/{official}"))
                self.assertEqual(hashlib.sha256(content).hexdigest(), digest)
                self.assertEqual(registration["migration_contracts"][filename], digest)
                self.assertEqual(catalog[filename]["checksum"], migration_checksum(content))
                self.assertFalse(catalog[filename]["non_transactional"])
                self.assertNotIn(filename, CHECKSUM_COMPATIBILITY_RULES)
                self.assertNotIn(official, catalog)
        for filename in ("241_precise_upstream_effective_rate.sql", "279_proxy_ip_groups.sql", "284_unified_proxy_bindings.sql", "285_upstream_null_rate_lifecycle.sql"):
            with self.subTest(history=filename):
                self.assertEqual((WORKSPACE / "backend/migrations" / filename).read_bytes(), git_blob(BASELINE, f"backend/migrations/{filename}"))

    def test_260_upgrade_partial_replay_and_unknown_records_remain_closed(self) -> None:
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {item["filename"]: item["checksum"] for item in catalog if item["filename"] not in MIGRATIONS}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([item["filename"] for item in plan["pending"]], MIGRATIONS)
        self.assertEqual(plan["unknown"], [])
        self.assertEqual(plan["conflicts"], [])
        self.assertEqual(pending_hooks(plan["pending"]), [])
        snapshot[MIGRATIONS[0]] = plan["pending"][0]["checksum"]
        partial = plan_migrations(catalog, snapshot)
        self.assertEqual([item["filename"] for item in partial["pending"]], [MIGRATIONS[1]])
        snapshot[MIGRATIONS[1]] = partial["pending"][0]["checksum"]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        for filename in MIGRATIONS:
            conflict = plan_migrations(catalog, {**snapshot, filename: "0" * 64})
            self.assertFalse(conflict["existing_checksums_verified"])
            self.assertEqual([item["filename"] for item in conflict["conflicts"]], [filename])
        for filename in OFFICIAL:
            unknown = plan_migrations(catalog, {**snapshot, filename: "a" * 64})
            self.assertFalse(unknown["existing_checksums_verified"])
            self.assertEqual(unknown["unknown"], [{"filename": filename, "checksum": "a" * 64}])

    def test_manifest_rejects_wrong_version_parent_identity_or_exact_migrations(self) -> None:
        profile = get_profile("261")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "261-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "261", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "260",
            "new_migrations": MIGRATIONS, "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for fields in (
                {"version": "0.2.11-baiyu"}, {"parent_profile": "259"}, {"parent_profile": "261"},
                {"profile": "262"}, {"release_id": "260-aaaaaaaaaaaa-1-aaaaaaaa"},
                {"new_migrations": []}, {"new_migrations": MIGRATIONS[:1]},
                {"new_migrations": list(reversed(MIGRATIONS))}, {"new_migrations": OFFICIAL},
                {"new_migrations": ["285_upstream_null_rate_lifecycle.sql", *MIGRATIONS]},
            ):
                with self.subTest(fields=fields), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **fields}, profile)

    def test_current_gate_still_requires_ancestor_285_semantics(self) -> None:
        evidence = {"migration_evidence": {
            "database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
            "isolated_upgrade_verified": True, "final_schema_verified": True,
        }}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "261", "migration_catalog": []}, evidence)


class Profile261BashContractTest(unittest.TestCase):
    def test_vm_exact_profile_contract_in_isolated_bash(self) -> None:
        bash = Path(r"C:\Program Files\Git\bin\bash.exe") if os.name == "nt" else Path("/bin/bash")
        if not bash.is_file():
            self.skipTest("Bash unavailable; this is a local predicate test, not VM validation")
        validator = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = validator.index('  if [[ "$profile" == 254 ]]; then')
        checks = validator[begin:validator.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; new_migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["285_upstream_null_rate_lifecycle.sql"]') [[ $new_migrations == lifecycle ]] ;;
    '.new_migrations == ["286_add_payment_order_bonus_amount.sql", "287_add_typesafe_platform.sql"]') [[ $new_migrations == release261 ]] ;;
    '.new_migrations == ["288_upstream_confidence_distribution.sql"]') [[ $new_migrations == distribution262 ]] ;;
    *) return 1 ;;
  esac
}
'''
        environment = os.environ.copy()
        environment.pop("BASH_ENV", None)
        for profile, version, parent, migrations, accepted in (
            ("260", "0.2.11-baiyu", "259", "lifecycle", True),
            ("260", "0.2.13-baiyu", "260", "release261", False),
            ("261", "0.2.13-baiyu", "260", "release261", True),
            ("261", "0.2.11-baiyu", "260", "release261", False),
            ("261", "0.2.13-baiyu", "259", "release261", False),
            ("261", "0.2.13-baiyu", "261", "release261", False),
            ("261", "0.2.13-baiyu", "260", "lifecycle", False),
            ("261", "0.2.13-baiyu", "260", "empty", False),
            ("261", "0.2.13-baiyu", "260", "reversed", False),
            ("262", "0.2.13-baiyu", "261", "release261", False),
            ("262", "0.2.13-baiyu", "261", "distribution262", True),
            ("263", "0.2.13-baiyu", "262", "distribution262", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = subprocess.run(
                    [str(bash), "--noprofile", "--norc", "-e", "-u", "-c", stub + checks, "contract", profile, version, parent, migrations],
                    env=environment, capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode == 0, accepted, result.stderr)


if __name__ == "__main__":
    unittest.main()
