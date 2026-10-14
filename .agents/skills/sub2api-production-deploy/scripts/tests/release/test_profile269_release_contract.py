"""Account-scoped cache protection profile; no VM or database side effects."""
from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.migration_planner import discover_migration_catalog, migration_checksum, plan_migrations
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile
from release.production_cleanup import RELEASE_ID
import test_profile263_release_contract as bash_helpers

BASELINE = "f6636bbda74c6333c161be862385a78ad265d00a"
MIGRATION = "297_upstream_cache_guard_account_scope.sql"


def git_blob(path: str) -> bytes:
    return subprocess.run(
        ["git", "cat-file", "blob", f"{BASELINE}:{path}"], cwd=WORKSPACE,
        check=True, capture_output=True, timeout=10,
    ).stdout


class Profile269ReleaseContractTest(unittest.TestCase):
    def test_preserves_historical_profile_migrations_and_checksums(self) -> None:
        baseline = {}
        exec(compile(git_blob(".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "baseline", "exec"), baseline)
        self.assertEqual(baseline["CURRENT_RELEASE_PROFILE"], "268")
        self.assertEqual(set(PROFILES), set(baseline["PROFILES"]) | {"269", "270"})
        for name, contract in baseline["PROFILES"].items():
            self.assertEqual(get_profile(name), contract, name)
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
        self.assertEqual(current, historical | {f"backend/migrations/{MIGRATION}", "backend/migrations/298_group_scheduling_protection_policies.sql"})
        prior = json.loads(git_blob(".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"))
        registered = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        for name, checksum in prior["migration_contracts"].items():
            self.assertEqual(registered["migration_contracts"][name], checksum, name)
        for name, contract in prior["historical_profiles"].items():
            self.assertEqual(registered["historical_profiles"][name], contract, name)
        self.assertEqual(registered["historical_profiles"]["268"], {
            key: value for key, value in prior["current_profile"].items() if key not in ("id", "status")
        })

    def test_registration_catalog_and_unknown_successor(self) -> None:
        profile = get_profile("269")
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("269")
        self.assertEqual(CURRENT_RELEASE_PROFILE, "270")
        self.assertEqual((profile["version"], profile["parent"], profile["gate_schema"]), ("0.2.16-baiyu", "268", 2))
        self.assertEqual(profile["new_migrations"], [MIGRATION])
        self.assertEqual(profile["release_policy"], get_profile("268")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 271"):
            get_profile("271")
        registered = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registered["current_profile"]["id"], "270")
        self.assertEqual(registered["current_profile"]["status"], "pending")
        for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
            self.assertEqual(registered["historical_profiles"]["269"][field], profile[field])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registered["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        for marker in registered["migration_assertions"][f"backend/migrations/{MIGRATION}"]:
            self.assertIn(marker, raw.decode("utf-8"))
        catalog = discover_migration_catalog(WORKSPACE)
        entry = next(row for row in catalog if row["filename"] == MIGRATION)
        self.assertEqual(entry["checksum"], migration_checksum(raw))
        self.assertFalse(entry["non_transactional"])
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION])
        self.assertEqual(plan_migrations(catalog, {**snapshot, MIGRATION: entry["checksum"]})["pending"], [])
        self.assertFalse(plan_migrations(catalog, {**snapshot, MIGRATION: "0" * 64})["existing_checksums_verified"])
        self.assertIsNotNone(RELEASE_ID.fullmatch("269-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(RELEASE_ID.fullmatch("271-aaaaaaaaaaaa-1-aaaaaaaa"))


class Profile269BashContractTest(unittest.TestCase):
    setUpClass = classmethod(bash_helpers.Profile263BashContractTest.setUpClass.__func__)
    execute = bash_helpers.Profile263BashContractTest.execute

    def test_validator_requires_exact_version_parent_and_migration(self) -> None:
        source = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = source.index('  if [[ "$profile" == 254 ]]; then')
        checks = source[begin:source.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["296_upstream_cache_guard.sql"]') [[ $migrations == cache268 ]] ;;
    '.new_migrations == ["297_upstream_cache_guard_account_scope.sql"]') [[ $migrations == account269 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("268", "0.2.16-baiyu", "267", "cache268", True),
            ("269", "0.2.16-baiyu", "268", "account269", True),
            ("269", "0.2.15-baiyu", "268", "account269", False),
            ("269", "0.2.16-baiyu", "267", "account269", False),
            ("269", "0.2.16-baiyu", "269", "account269", False),
            ("269", "0.2.16-baiyu", "268", "cache268", False),
            ("270", "0.2.16-baiyu", "269", "account269", False),
        ):
            with self.subTest(profile=profile, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)


if __name__ == "__main__":
    unittest.main()
