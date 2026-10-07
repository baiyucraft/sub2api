"""Profile 263 lineage and local entrypoint checks; no VM or remote execution."""
from __future__ import annotations

import json
import os
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
from release.migration_planner import catalog_sha256, checksum_policy_sha256, discover_migration_catalog, plan_migrations
from release.paths import WORKSPACE
from release.production_cleanup import RELEASE_ID as CLEANUP_RELEASE_ID
from release.profiles import CURRENT_RELEASE_PROFILE, PROFILES, get_profile, get_release_profile

BASELINE = "8a59856bfa15e71810307e905d4a001cb907e41c"


class Profile263ReleaseContractTest(unittest.TestCase):
    def test_successor_preserves_every_historical_contract(self) -> None:
        source = subprocess.run(
            ["git", "cat-file", "blob", f"{BASELINE}:.agents/skills/sub2api-production-deploy/scripts/release/profiles.py"],
            cwd=WORKSPACE, check=True, capture_output=True, timeout=10,
        ).stdout
        previous = {}
        exec(compile(source, "historical-profiles", "exec"), previous)
        self.assertEqual(previous["CURRENT_RELEASE_PROFILE"], "262")
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"263", "264"})
        for name, contract in previous["PROFILES"].items():
            with self.subTest(profile=name):
                self.assertEqual(get_profile(name), contract)
                with self.assertRaisesRegex(ValueError, "historical"):
                    get_release_profile(name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "264")
        current = get_profile("263")
        self.assertEqual(current["version"], "0.2.14-baiyu")
        self.assertEqual(current["parent"], "262")
        self.assertEqual(current["new_migrations"], [])
        self.assertEqual(current["gate_schema"], 2)
        self.assertEqual(current["release_policy"], get_profile("262")["release_policy"])
        with self.assertRaisesRegex(ValueError, "unknown release profile: 265"):
            get_profile("265")
        with self.assertRaises(ValueError):
            get_release_profile("265")

    def test_registration_and_existing_database_replay(self) -> None:
        registration = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registration["current_profile"]["id"], "264")
        for field in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registration["historical_profiles"]["263"][field], get_profile("263")[field])
            self.assertEqual(registration["historical_profiles"]["262"][field], get_profile("262")[field])
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {entry["filename"]: entry["checksum"] for entry in catalog}
        plan = plan_migrations(catalog, snapshot)
        self.assertTrue(plan["existing_checksums_verified"])
        self.assertEqual(plan["pending"], [])
        self.assertEqual(plan["conflicts"], [])

    def test_manifest_rejects_wrong_version_parent_or_nonempty_migrations(self) -> None:
        profile = get_profile("263")
        catalog = discover_migration_catalog(WORKSPACE)
        manifest = {
            "schema": 2, "release_asset_layout": "skill-v1", "deployment_mode": "blue-green",
            "release_id": "263-aaaaaaaaaaaa-1-aaaaaaaa", "profile": "263", "version": profile["version"],
            "commit_sha": "a" * 40, "origin": profile["origin"], "vm_identity": profile["vm_identity"],
            "migration_catalog": catalog, "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(), "parent_profile": "262",
            "new_migrations": [], "release_policy": profile["release_policy"],
        }
        with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
            validate_manifest_profile_contract(manifest, profile)
            for mutation in (
                {"version": "0.2.13-baiyu"}, {"parent_profile": "261"}, {"parent_profile": "263"},
                {"profile": "264"}, {"release_id": "262-aaaaaaaaaaaa-1-aaaaaaaa"},
                {"new_migrations": get_profile("262")["new_migrations"]},
            ):
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    validate_manifest_profile_contract({**manifest, **mutation}, profile)

    def test_no_new_migration_does_not_skip_ancestor_lifecycle_evidence(self) -> None:
        evidence = {"migration_evidence": {
            "database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
            "isolated_upgrade_verified": True, "final_schema_verified": True,
        }}
        with self.assertRaisesRegex(RuntimeError, "migration 285 catalog"):
            _validate_v2_pending({"profile": "263", "migration_catalog": []}, evidence)

    def test_cleanup_release_identity_accepts_263_and_rejects_265(self) -> None:
        self.assertIsNotNone(CLEANUP_RELEASE_ID.fullmatch("263-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNone(CLEANUP_RELEASE_ID.fullmatch("265-aaaaaaaaaaaa-1-aaaaaaaa"))


class Profile263BashContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.bash = Path(r"C:\Program Files\Git\bin\bash.exe") if os.name == "nt" else Path("/bin/bash")
        if not cls.bash.is_file():
            raise unittest.SkipTest("Bash unavailable; these checks do not replace VM validation")
        cls.environment = os.environ.copy()
        cls.environment.pop("BASH_ENV", None)

    def execute(self, script: str, *arguments: str) -> subprocess.CompletedProcess:
        return subprocess.run(
            [str(self.bash), "--noprofile", "--norc", "-e", "-u", "-c", script, "contract", *arguments],
            env=self.environment, capture_output=True, text=True, timeout=10,
        )

    def assert_guard_cases(self, guard: str, variable: str, cases: tuple[tuple[str, bool], ...]) -> None:
        script = (
            'while (( $# )); do\n'
            f'{variable}=$1\nexpected=$2\nshift 2\nactual=false\n'
            f'if {guard}; then actual=true; fi\n'
            f'[[ $actual == "$expected" ]] || {{ printf "guard mismatch: %s\\n" "${variable}" >&2; exit 1; }}\n'
            'done\n'
        )
        arguments = tuple(item for value, allowed in cases for item in (value, str(allowed).lower()))
        result = self.execute(script, *arguments)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_vm_validator_exact_contract_and_unknown_successor(self) -> None:
        validator = (DEPLOY_ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        begin = validator.index('  if [[ "$profile" == 254 ]]; then')
        checks = validator[begin:validator.index("  recovery_gate_mode=", begin)]
        stub = '''
profile=$1; version=$2; parent=$3; new_migrations=$4; manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == ["289_activity_reward_costs.sql"]') [[ $new_migrations == reward264 ]] ;;
    '.new_migrations == []') [[ $new_migrations == empty ]] ;;
    '.new_migrations == ["288_upstream_confidence_distribution.sql"]') [[ $new_migrations == distribution262 ]] ;;
    *) return 1 ;;
  esac
}
'''
        for profile, version, parent, migrations, allowed in (
            ("262", "0.2.13-baiyu", "261", "distribution262", True),
            ("263", "0.2.14-baiyu", "262", "empty", True),
            ("263", "0.2.13-baiyu", "262", "empty", False),
            ("263", "0.2.14-baiyu", "261", "empty", False),
            ("263", "0.2.14-baiyu", "263", "empty", False),
            ("263", "0.2.14-baiyu", "262", "distribution262", False),
            ("264", "0.2.14-baiyu", "263", "empty", False),
            ("264", "0.2.14-baiyu", "263", "reward264", True),
            ("264", "0.2.13-baiyu", "263", "reward264", False),
            ("264", "0.2.14-baiyu", "262", "reward264", False),
            ("264", "0.2.14-baiyu", "264", "reward264", False),
            ("265", "0.2.14-baiyu", "264", "reward264", False),
        ):
            with self.subTest(profile=profile, version=version, parent=parent, migrations=migrations):
                result = self.execute(stub + checks, profile, version, parent, migrations)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_release_id_guards_retain_history_and_reject_unknown(self) -> None:
        files = (
            "release/sign-gate.sh", "release/sign-dr-evidence.sh", "release/production-space-clean.sh",
            "release/production-recovery-retention-clean.sh", "release/promote-dr-baseline.sh",
            "maintenance/release/context.sh", "maintenance/release/prepare.sh", "maintenance/release/promote-backup.sh",
            "maintenance/181/mask-backup-units.sh", "maintenance/181/restore-backup-units.sh",
        )
        for relative in files:
            source = "\n".join(line for line in (DEPLOY_ROOT / relative).read_text(encoding="utf-8").splitlines() if not line.lstrip().startswith("#"))
            groups = re.findall(r"\((?:182\||195\|)[0-9|]+\)", source)
            self.assertTrue(groups, relative)
            for group in groups:
                with self.subTest(file=relative):
                    cases = tuple((f"{profile}-aaaaaaaaaaaa-1-aaaaaaaa", allowed) for profile, allowed in (("261", True), ("262", True), ("263", True), ("264", True), ("265", False)))
                    self.assert_guard_cases(f'[[ $release_id =~ ^{group}-[0-9a-f]{{12}}-[0-9]+-[0-9a-f]{{8}}$ ]]', "release_id", cases)

    def test_ancestor_assertion_guards_admit_263_and_264_and_reject_265(self) -> None:
        for path in sorted((DEPLOY_ROOT / "maintenance/release").glob("migration-*-assert.sh")):
            source = path.read_text(encoding="utf-8")
            guards = [re.search(r"\[\[.*?\]\]", line)[0] for line in source.splitlines() if line.startswith("[[ $profile ==")]
            self.assertTrue(guards, path.name)
            for guard in guards:
                with self.subTest(file=path.name):
                    self.assert_guard_cases(guard, "profile", (("262", True), ("263", True), ("264", True), ("265", False)))


if __name__ == "__main__":
    unittest.main()
