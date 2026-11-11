"""Official 0.2.21 is additive; historical identities and recovery reviews are frozen."""
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

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
from release.migration_planner import discover_migration_catalog, migration_checksum, plan_migrations
from test_profile281_release_contract import git_blob
import test_profile263_release_contract as bash_helpers

BASELINE = "a92c84a3f0d9d23a305044a67341374fc8b8b253"
UPSTREAM = "530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484"
MIGRATIONS = ["309_add_usage_logs_api_key_cost_index_notx.sql", "310_openai_cache_write_ttl.sql"]
ORIGINALS = ["249_add_usage_logs_api_key_cost_index_notx.sql", "250_openai_cache_write_ttl.sql"]
RAW_CHECKSUMS = ["789d4d5a5c186531cb6edd65ea2ecc07f5e488272f7876c6f46240e2fed0c30b",
                 "c470116c49042425f5663c421f0aea2a6aa6eae75b7e489dc95c12fc303562b2"]
CHECKSUMS = ["94f124c3e50405f7b75746885111a4f662c331985a697de46fe946a17ff1f7d9",
             "532551ce7aee86b2ee0a78d397d422424ceed4051e31d5f542277c80c21b8b98"]
JQ = os.environ.get("SUB2API_TEST_JQ") or shutil.which("jq")


class Profile284ReleaseContractTest(unittest.TestCase):
    def test_every_previous_profile_sql_and_recovery_review_remains_frozen(self):
        previous = {}
        exec(compile(git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), previous)
        self.assertEqual(len(previous["PROFILES"]), 70)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"284"})
        for name, profile in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
        paths = subprocess.check_output(["git", "ls-tree", "-r", "--name-only", BASELINE, "--", "backend/migrations"], cwd=WORKSPACE, text=True).splitlines()
        paths = [p for p in paths if p.endswith(".sql")]
        self.assertEqual(len(paths), 359)
        self.assertEqual({p.name for p in (WORKSPACE / "backend/migrations").glob("*.sql")}, {Path(p).name for p in paths} | set(MIGRATIONS))
        for path in paths:
            self.assertEqual((WORKSPACE / path).read_bytes(), git_blob(BASELINE, path), path)
        protected = ".agents/skills/sub2api-production-deploy/scripts/release/recovery_gate.py"
        self.assertEqual((WORKSPACE / protected).read_bytes(), git_blob(BASELINE, protected))

    def test_current_identity_registry_and_original_sql_bytes(self):
        self.assertEqual(CURRENT_RELEASE_PROFILE, "284")
        profile = get_release_profile("284")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"], profile["new_migrations"]), ("0.2.21-baiyu", "283", 2, MIGRATIONS))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("283")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 285"):
            get_profile("285")
        self.assertTrue(RELEASE_ID.fullmatch("284-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertFalse(RELEASE_ID.fullmatch("285-aaaaaaaaaaaa-1-aaaaaaaa"))
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "284")
        for key in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registry["current_profile"][key], profile[key])
            self.assertEqual(registry["historical_profiles"]["283"][key], get_profile("283")[key])
        self.assertEqual((WORKSPACE / "backend/cmd/server/VERSION").read_text().strip(), profile["version"])
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
        self.assertTrue(rows[MIGRATIONS[0]]["non_transactional"])
        self.assertFalse(rows[MIGRATIONS[1]]["non_transactional"])
        snapshot[MIGRATIONS[0]] = CHECKSUMS[0]
        self.assertEqual([r["filename"] for r in plan_migrations(catalog, snapshot)["pending"]], MIGRATIONS[1:])
        snapshot[MIGRATIONS[1]] = CHECKSUMS[1]
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATIONS[0]: "0" * 64})["existing_checksums_verified"])

    @unittest.skipUnless(JQ, "jq unavailable; real jq validation required separately")
    def test_real_ancestor_filters_exact_contract_and_invalid_mutations(self):
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {"schema": 2, "profile": "284", "parent_profile": "283", "new_migrations": MIGRATIONS, "deployment_mode": "downtime", "migration_catalog": catalog}
        historical = {**manifest, "profile": "283", "parent_profile": "282", "new_migrations": get_profile("283")["new_migrations"]}
        for number in (297, 298, 299, 301):
            source = (SCRIPTS / f"maintenance/release/migration-{number}-assert.sh").read_text(encoding="utf-8")
            expression = re.search(r"jq -e --arg filename \"\$filename\" --arg checksum \"\$catalog_checksum\" '(.*?)' \"\$document\"", source, re.S)[1]
            row = next(r for r in catalog if r["filename"].startswith(f"{number}_"))
            invalid = [{**manifest, key: value} for key, value in (("profile", "285"), ("parent_profile", "282"), ("new_migrations", []), ("new_migrations", MIGRATIONS[::-1]), ("new_migrations", MIGRATIONS + MIGRATIONS), ("schema", 1))]
            if number == 301:
                invalid.append({**manifest, "deployment_mode": "blue-green"})
            wrong = copy.deepcopy(manifest)
            next(r for r in wrong["migration_catalog"] if r["filename"] == row["filename"])["checksum"] = "0" * 64
            invalid += [wrong, {**manifest, "migration_catalog": []}, {**manifest, "migration_catalog": catalog + [row]}]
            for document, allowed in ((manifest, True), ({"manifest": manifest}, True), (historical, True), *((m, False) for m in invalid)):
                result = subprocess.run([JQ, "-e", "--arg", "filename", row["filename"], "--arg", "checksum", row["checksum"], expression], input=json.dumps(document), text=True, capture_output=True, timeout=10)
                self.assertEqual(result.returncode == 0, allowed, (number, result.stderr))


class Profile284BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_current_only_guards_reject_history_unknown_and_malformed(self):
        source = (SCRIPTS / "release/vm-validate.sh").read_text(encoding="utf-8")
        guard = next(line.strip() for line in source.splitlines() if line.strip() == '[[ "$profile" == 284 ]]')
        self.assertIn('[[ $profile == 284 ]]', (SCRIPTS / "maintenance/release/prepare.sh").read_text(encoding="utf-8"))
        for value, allowed in (("284", True), ("283", False), ("285", False), ("0284", False), ("284;true", False)):
            result = self.execute('profile=$1\n' + guard, value)
            self.assertEqual(result.returncode == 0, allowed)

if __name__ == '__main__': unittest.main()
