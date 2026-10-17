"""Profile 267 local migration/admission contracts; no VM or production calls."""
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
from release.production_cleanup import RELEASE_ID
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
import test_profile263_release_contract as bash_helpers

BASELINE = "bbe1e7a2b9c8551a73066c4cda1e0d7bddb1171f"
UPSTREAM = "4d9ac3e482106d2da3a39fe3e546d8d6801f1cc1"
MIGRATIONS = [
    "292_group_i18n.sql", "293_gemini_cached_contents.sql",
    "294_gemini_cached_contents_channel_usage.sql", "295_gemini_cached_contents_mutation_lease.sql",
]
OFFICIAL = [
    "242_group_i18n.sql", "243_gemini_cached_contents.sql",
    "244_gemini_cached_contents_channel_usage.sql", "245_gemini_cached_contents_mutation_lease.sql",
]
RAW_SHA256 = [
    "9df295380bd4349bb510a64a3f3d829d7ee28904eab2bdf60ec50599b2128f28",
    "48d05b5795e22f45458fdd56e32e3af89ebc68002ddb9320bff4d19bacc71dc3",
    "282f55ed98d623f0a07cf4e7975f3ba0de09777ec664de612817a0ed2698577d",
    "d9e2fdc9e974cf35870cf0d46dba34399fde760a08e97632d6d20abae412ea25",
]


def git_blob(commit: str, path: str) -> bytes:
    return subprocess.run(
        ["git", "cat-file", "blob", f"{commit}:{path}"], cwd=WORKSPACE,
        check=True, capture_output=True, timeout=10,
    ).stdout


class Profile267ReleaseContractTest(unittest.TestCase):
    def test_all_historical_profiles_and_sql_remain_immutable(self):
        before = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), before)
        self.assertEqual(before["CURRENT_RELEASE_PROFILE"], "266")
        self.assertEqual(set(PROFILES), set(before["PROFILES"]) | {"267", "268", "269", "270", "271", "272", "273"})
        for name, contract in before["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
            with self.assertRaisesRegex(ValueError, "historical"):
                get_release_profile(name)
        tree = subprocess.run(
            ["git", "ls-tree", "-r", BASELINE, "backend/migrations"], cwd=WORKSPACE,
            check=True, capture_output=True, text=True, timeout=10,
        ).stdout
        historical = {}
        for line in tree.splitlines():
            metadata, path = line.split("\t", 1)
            if path.endswith(".sql"):
                historical[path] = metadata.split()[2]
        for path, blob in historical.items():
            raw = (WORKSPACE / path).read_bytes()
            self.assertEqual(hashlib.sha1(f"blob {len(raw)}\0".encode() + raw).hexdigest(), blob, path)
        current = {p.relative_to(WORKSPACE).as_posix() for p in (WORKSPACE / "backend/migrations").glob("*.sql")}
        self.assertEqual(current, set(historical) | {f"backend/migrations/{name}" for name in [*MIGRATIONS, *get_profile("268")["new_migrations"], *get_profile("269")["new_migrations"], *get_profile("270")["new_migrations"], *get_profile("271")["new_migrations"], *get_profile("273")["new_migrations"]]})
        self.assertEqual(CURRENT_RELEASE_PROFILE, "273")
        profile = get_profile("267")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.16-baiyu", "266", 2))
        self.assertEqual(profile["new_migrations"], MIGRATIONS)
        self.assertEqual(profile["release_policy"], get_profile("266")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 274"):
            get_profile("274")

    def test_raw_upstream_blobs_registration_and_separate_checksums(self):
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        prior = json.loads(git_blob(BASELINE, ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"))
        for name, historical in prior["historical_profiles"].items():
            self.assertEqual(registration["historical_profiles"][name], historical)
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registration["current_profile"][field], get_profile("273")[field])
            self.assertEqual(registration["historical_profiles"]["266"][field], get_profile("266")[field])
        self.assertEqual(registration["current_profile"]["id"], "273")
        self.assertEqual(registration["current_profile"]["status"], "pending")
        self.assertEqual(git_blob(UPSTREAM, "backend/cmd/server/VERSION").decode().strip(), "0.2.16")
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text().strip(), get_profile(CURRENT_RELEASE_PROFILE)["version"])
        catalog = {item["filename"]: item for item in discover_migration_catalog(WORKSPACE)}
        for name, official, checksum in zip(MIGRATIONS, OFFICIAL, RAW_SHA256):
            raw = (WORKSPACE / "backend/migrations" / name).read_bytes()
            self.assertEqual(raw, git_blob(UPSTREAM, f"backend/migrations/{official}"))
            self.assertEqual(hashlib.sha256(raw).hexdigest(), checksum)
            self.assertEqual(registration["migration_contracts"][name], checksum)
            self.assertEqual(registration["migration_renames"]["official_" + official[:-4]], name)
            self.assertEqual(catalog[name]["checksum"], migration_checksum(raw))
            self.assertNotEqual(checksum, catalog[name]["checksum"])
            self.assertFalse(catalog[name]["non_transactional"])
            self.assertNotIn(name, CHECKSUM_COMPATIBILITY_RULES)
            self.assertNotIn(official, catalog)
            for marker in registration["migration_assertions"][f"backend/migrations/{name}"]:
                self.assertIn(marker, raw.decode())
        extension = next(item for item in registration["extensions"] if item["id"] == "migration-profile-contract")
        self.assertTrue(set(MIGRATIONS).issubset(extension["migration_files"]))
        for path in extension["required_tests"]:
            self.assertTrue((WORKSPACE / path).is_file(), path)

    def test_upgrade_order_partial_application_replay_and_conflicts(self):
        catalog = discover_migration_catalog(WORKSPACE)
        checksums = {item["filename"]: item["checksum"] for item in catalog}
        snapshot = {name: checksum for name, checksum in checksums.items() if name not in MIGRATIONS}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual([item["filename"] for item in plan["pending"]], MIGRATIONS)
        self.assertEqual(pending_hooks(plan["pending"]), [])
        # A profile-265 snapshot also needs the preserved invoice migration.
        from_265 = plan_migrations(catalog, {name: checksum for name, checksum in snapshot.items() if name != "291_user_invoice_annotations.sql"})
        self.assertEqual([item["filename"] for item in from_265["pending"]], ["291_user_invoice_annotations.sql", *MIGRATIONS])
        for index, name in enumerate(MIGRATIONS):
            snapshot[name] = checksums[name]
            partial = plan_migrations(catalog, snapshot)
            self.assertEqual([item["filename"] for item in partial["pending"]], MIGRATIONS[index + 1:])
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        for name in MIGRATIONS:
            conflict = plan_migrations(catalog, {**snapshot, name: "0" * 64})
            self.assertFalse(conflict["existing_checksums_verified"])
            self.assertEqual([item["filename"] for item in conflict["conflicts"]], [name])
        for official in OFFICIAL:
            unknown = plan_migrations(catalog, {**snapshot, official: "a" * 64})
            self.assertEqual(unknown["unknown"], [{"filename": official, "checksum": "a" * 64}])
        self.assertIsNotNone(RELEASE_ID.fullmatch("267-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("272-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("274-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_manifest_binds_exact_profile_and_preserves_ancestor_evidence(self):
        profile = get_profile("267")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "267-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "267", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "266",
            "new_migrations": MIGRATIONS, "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for change in (
                {"version": "0.2.15-baiyu"}, {"parent_profile": "265"}, {"parent_profile": "267"},
                {"new_migrations": []}, {"new_migrations": MIGRATIONS[::-1]},
                {"new_migrations": get_profile("266")["new_migrations"]},
                {"profile": "266"}, {"profile": "268"}, {"release_id": "266-aaaaaaaaaaaa-1-aaaaaaaa"},
            ):
                with self.subTest(change=change), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **change}, profile)
        evidence = {"migration_evidence": {
            "database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
            "isolated_upgrade_verified": True, "final_schema_verified": True,
        }}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "267", "migration_catalog": []}, evidence)


class Profile267BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute
    assert_guard_cases = bash_helpers.Profile263BashContractTest.assert_guard_cases
    test_release_id_guards = bash_helpers.Profile263BashContractTest.test_release_id_guards_retain_history_and_reject_unknown
    test_ancestor_assertion_guards = bash_helpers.Profile263BashContractTest.test_ancestor_assertion_guards_admit_263_through_270_and_reject_271

    def test_vm_exact_267_contract_retains_266_and_rejects_268(self):
        source = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        migration_check = '.new_migrations == ' + json.dumps(MIGRATIONS)
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["291_user_invoice_annotations.sql"]') [[ $migrations == invoice266 ]] ;;
    '__NEW_MIGRATIONS__') [[ $migrations == upstream267 ]] ;;
    *) return 1 ;;
  esac
}
'''.replace("__NEW_MIGRATIONS__", migration_check)
        for profile, version, parent, migrations, allowed in (
            ("266", "0.2.15-baiyu", "265", "invoice266", True),
            ("267", "0.2.16-baiyu", "266", "upstream267", True),
            ("267", "0.2.15-baiyu", "266", "upstream267", False),
            ("267", "0.2.16-baiyu", "265", "upstream267", False),
            ("267", "0.2.16-baiyu", "267", "upstream267", False),
            ("267", "0.2.16-baiyu", "266", "invoice266", False),
            ("267", "0.2.16-baiyu", "266", "empty", False),
            ("268", "0.2.16-baiyu", "267", "upstream267", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
