"""Official 0.2.22 is additive; historical identities and recovery reviews are frozen."""
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

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
from release.manifest import create_manifest, validate_manifest_profile_contract
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum, plan_migrations
from test_profile281_release_contract import git_blob
import test_profile263_release_contract as bash_helpers

BASELINE = "78521acd35470348d8e264668499d3bea079ee6e"
UPSTREAM = "15871f904056f548dd874a1d069ffc6cdf777b78"
MIGRATIONS = ["311_group_force_openai_ultrafast.sql", "312_group_usage_rollup_nonblocking_build.sql"]
ORIGINALS = ["251_group_force_openai_ultrafast.sql", "251_group_usage_rollup_nonblocking_build.sql"]
RAW_CHECKSUMS = ["f91352f2d3b1bffbbea5f6b7fc17e37c5ad64f04c3426cb7168778d10135a7bf",
                 "da504646104cec14aa7bcf5a0af6f06f5cd40883459379ae915db27c1d4cec8b"]
CHECKSUMS = ["d78e605d4fc2038c57f620dff398022823a850ea8994b129b50774ac49e20af1",
             "5b6e9b57f9b354a96c6870185c034db8adc7def944efcddf654e39cd97770718"]
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


class Profile285ReleaseContractTest(unittest.TestCase):
    def test_every_previous_profile_sql_and_recovery_review_remains_frozen(self):
        previous = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), previous)
        self.assertEqual(len(previous["PROFILES"]), 71)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"285", "286"})
        for name, profile in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
        paths = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"], cwd=WORKSPACE, text=True).splitlines()
        paths = [p for p in paths if p.endswith(".sql")]
        self.assertEqual(len(paths), 361)
        self.assertEqual({p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")}, {Path(p).name for p in paths} | set(MIGRATIONS) | set(get_profile("286")["new_migrations"]))
        for path in paths:
            self.assertEqual((WORKSPACE / path).read_bytes(), git_blob(BASELINE, path), path)
        protected = ".agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py"
        self.assertEqual((WORKSPACE / protected).read_bytes(), git_blob(BASELINE, protected))

    def test_current_identity_registry_and_original_sql_bytes(self):
        self.assertEqual(CURRENT_RELEASE_PROFILE, "286")
        profile = get_profile("285")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]), ("0.2.22-baiyu", "284", 2, MIGRATIONS))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("284")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 287"):
            get_profile("287")
        self.assertTrue(RELEASE_ID.fullmatch("285-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertFalse(RELEASE_ID.fullmatch("287-aaaaaaaaaaaa-1-aaaaaaaa"))
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "286")
        for key in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registry["historical_profiles"]["285"][key], profile[key])
            self.assertEqual(registry["historical_profiles"]["284"][key], get_profile("284")[key])
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text().strip(), get_release_profile("286")["version"])
        for name, original, raw_sha, checksum in zip(MIGRATIONS, ORIGINALS, RAW_CHECKSUMS, CHECKSUMS):
            raw = (WORKSPACE / "backend/migrations" / name).read_bytes()
            self.assertEqual(raw, git_blob(UPSTREAM, "backend/migrations/" + original))
            self.assertEqual(hashlib.sha256(raw).hexdigest(), raw_sha)
            self.assertEqual(migration_checksum(raw), checksum)
            self.assertEqual(registry["migration_contracts"][name], raw_sha)

    def test_planner_missing_partial_complete_and_conflict(self):
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {r["filename"]: r["checksum"] for r in catalog if r["filename"] not in MIGRATIONS}
        self.assertEqual([r["filename"] for r in plan_migrations(catalog, snapshot)["pending"]], MIGRATIONS)
        rows = {r["filename"]: r for r in catalog}
        self.assertFalse(rows[MIGRATIONS[0]]["non_transactional"])
        self.assertFalse(rows[MIGRATIONS[1]]["non_transactional"])
        snapshot[MIGRATIONS[0]] = CHECKSUMS[0]
        self.assertEqual([r["filename"] for r in plan_migrations(catalog, snapshot)["pending"]], MIGRATIONS[1:])
        snapshot[MIGRATIONS[1]] = CHECKSUMS[1]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATIONS[0]: "0" * 64})["existing_checksums_verified"])

    def test_downtime_and_exact_manifest_require_all_ancestor_assertions(self):
        profile = get_profile("285")
        with self.assertRaisesRegex(ValueError, "requires downtime"):
            create_manifest("a" * 40, profile, "285-aaaaaaaaaaaa-1-aaaaaaaa", "blue-green")
        catalog = discover_migration_catalog(WORKSPACE)
        assets = {f".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-{n}-assert.sh": str(n % 10) * 64 for n in (297, 298, 299, 301)}
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "downtime",
            "release_id": "285-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "285", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "parent_profile": "284", "new_migrations": MIGRATIONS, "release_policy": profile["release_policy"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "release_asset_sha256": assets,
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in ({"version": "0.2.21-baiyu"}, {"parent_profile": "283"}, {"new_migrations": []}, {"new_migrations": MIGRATIONS[::-1]}, {"new_migrations": MIGRATIONS + MIGRATIONS}):
                with self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)
            for missing in assets:
                with self.assertRaisesRegex(RuntimeError, "assertion asset is missing"):
                    validate_manifest_profile_contract({**manifest, "release_asset_sha256": {k: v for k, v in assets.items() if k != missing}}, profile)

    def test_go_toolchain_pin_is_consistent(self):
        self.assertIn("go 1.27.4", (WORKSPACE / "backend/go.mod").read_text(encoding="utf-8"))
        self.assertIn("golang:1.27.4-alpine", (WORKSPACE / "Dockerfile").read_text(encoding="utf-8"))
        for path in ("release/vm-build-common.sh", "release/vm_build.py"):
            source = (SCRIPTS / path).read_text(encoding="utf-8")
            self.assertIn("docker.io/library/golang:1.27.4-alpine", source)
            self.assertNotIn("golang:1.27.3-alpine", source)

    @unittest.skipUnless(JQ, "jq unavailable; real jq validation required separately")
    def test_real_ancestor_filters_exact_contract_and_invalid_mutations(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "285", "parent_profile": "284", "new_migrations": MIGRATIONS, "deployment_mode": "downtime", "migration_catalog": catalog}
        historical = {**manifest, "profile": "284", "parent_profile": "283", "new_migrations": get_profile("284")["new_migrations"]}
        for number in (297, 298, 299, 301):
            source = (SCRIPTS / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            invalid = [{**manifest, key: value} for key, value in (("profile", "286"), ("parent_profile", "282"), ("new_migrations", []), ("new_migrations", MIGRATIONS[::-1]), ("new_migrations", MIGRATIONS + MIGRATIONS), ("schema", 1))]
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            wrong = copy.deepcopy(manifest)
            next(r for r in wrong["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid += [wrong, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}]
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), (historical, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression], input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, result.stderr))


class Profile285BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_validator_checks_exact_version_parent_migrations_and_mode(self):
        source = (SCRIPTS / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = r'''
profile=$1; version=$2; parent=$3; migrations=$4; mode=$5; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\n' "$parent" ;;
    '.new_migrations == ["311_group_force_openai_ultrafast.sql", "312_group_usage_rollup_nonblocking_build.sql"] and .deployment_mode == "downtime"') [[ $migrations == exact && $mode == downtime ]] ;;
    *) return 1 ;;
  esac
}
'''
        cases = (
            ("285", "0.2.22-baiyu", "284", "exact", "downtime", True),
            ("285", "0.2.21-baiyu", "284", "exact", "downtime", False),
            ("285", "0.2.22-baiyu", "283", "exact", "downtime", False),
            ("285", "0.2.22-baiyu", "284", "wrong", "downtime", False),
            ("285", "0.2.22-baiyu", "284", "exact", "blue-green", False),
            ("286", "0.2.22-baiyu", "284", "exact", "downtime", False),
        )
        for *values, allowed in cases:
            result = self.execute(stub + checks, *values)
            self.assertEqual(result.returncode == 0, allowed, (values, result.stderr))

    def test_current_only_guards_reject_history_unknown_and_malformed(self):
        source = (SCRIPTS / "release/vm-validate.sh").read_text(encoding="utf-8")
        guard = next(line.strip() for line in source.splitlines() if line.strip() == '[[ "$profile" == 286 ]]')
        self.assertIn('[[ $profile == 286 ]]', (SCRIPTS / "maintenance/release/prepare.sh").read_text(encoding="utf-8"))
        for value, allowed in (("286", True), ("285", False), ("284", False), ("287", False), ("0285", False), ("285;true", False)):
            result = self.execute('profile=$1\n' + guard, value)
            self.assertEqual(result.returncode == 0, allowed)

if __name__ == '__main__': unittest.main()
