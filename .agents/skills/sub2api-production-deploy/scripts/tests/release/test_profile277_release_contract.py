"""Cross-platform confidence fingerprint state migration profile contract."""
from __future__ import annotations

import hashlib
import json
import os
import shutil
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
import test_profile275_release_contract as predecessor

BASELINE = "1cbfdb66a6819335ae2109b3fbbcd702155e308d"
MIGRATION = "304_upstream_confidence_fingerprint.sql"


class Profile277ReleaseContractTest(unittest.TestCase):
    def test_profile_identity_and_immutable_history(self):
        previous = {}
        exec(compile(predecessor.git_blob(BASELINE, ".agents/skills/sub2api-production-deploy/scripts/release/profiles.py"), "profile275-baseline", "exec"), previous)
        self.assertEqual(set(PROFILES), set(previous["PROFILES"]) | {"276", "277", "278", "279", "280", "281"})
        for name, profile in previous["PROFILES"].items():
            self.assertEqual(get_profile(name), profile, name)
        self.assertEqual(CURRENT_RELEASE_PROFILE, "281")
        self.assertEqual((get_profile("276")["version"], get_profile("276")["parent"], get_profile("276")["new_migrations"]),
                         ("0.2.18-baiyu", "275", ["303_group_strip_optional_image_tools.sql"]))
        profile = get_profile("277")
        self.assertEqual((profile["version"], profile["parent"], profile["new_migrations"], profile["gate_schema"]),
                         ("0.2.19-baiyu", "276", [MIGRATION], 2))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("277")
        self.assertEqual((get_profile("278")["version"], get_profile("278")["parent"], get_profile("278")["new_migrations"]),
                         ("0.2.20-baiyu", "277", []))
        self.assertEqual((get_release_profile("280")["version"], get_release_profile("280")["parent"], get_release_profile("280")["new_migrations"]),
                         ("0.2.22-baiyu", "279", []))
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile("276")
        self.assertIsNotNone(RELEASE_ID.fullmatch("277-aaaaaaaaaaaa-1-aaaaaaaa"))
        self.assertIsNotNone(RELEASE_ID.fullmatch("278-aaaaaaaaaaaa-1-aaaaaaaa"))

    def test_catalog_checksum_and_pending_migration(self):
        registry = json.loads((WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml").read_text(encoding="utf-8"))
        self.assertEqual(registry["current_profile"]["id"], "281")
        for key in ("version", "parent", "new_migrations", "gate_schema", "release_policy"):
            self.assertEqual(registry["current_profile"][key], get_profile("281")[key])
        self.assertEqual(registry["current_profile"]["new_migrations"], [])
        raw = (WORKSPACE / "backend/migrations" / MIGRATION).read_bytes()
        self.assertEqual(registry["migration_contracts"][MIGRATION], hashlib.sha256(raw).hexdigest())
        catalog = discover_migration_catalog(WORKSPACE)
        snapshot = {row["filename"]: row["checksum"] for row in catalog if row["filename"] != MIGRATION}
        self.assertEqual([row["filename"] for row in plan_migrations(catalog, snapshot)["pending"]], [MIGRATION])
        snapshot[MIGRATION] = migration_checksum(raw)
        self.assertEqual(plan_migrations(catalog, snapshot)["pending"], [])

    def test_shell_context_and_vm_manifest_contract(self):
        context = (ROOT / "maintenance/release/context.sh").read_text(encoding="utf-8")
        validator = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("|277|278|279|280|281)", context)
        schema_two_profile_guard = next(
            line for line in validator.splitlines()
            if line.lstrip().startswith('[[ "$profile" == 281')
        )
        self.assertEqual(schema_two_profile_guard.strip(), '[[ "$profile" == 281 ]]')
        self.assertIn('elif [[ "$profile" == 277 ]]; then', validator)
        self.assertIn('0.2.19-baiyu', validator)
        self.assertIn('elif [[ "$profile" == 281 ]]; then', validator)
        self.assertIn('0.2.20-baiyu', validator)
        self.assertIn('0.2.21-baiyu', validator)
        self.assertIn(MIGRATION, validator)

    def test_release_id_parser_keeps_277_and_278_while_vm_admission_accepts_current_only(self):
        bundled = Path('C:/Program Files/Git/bin/bash.exe')
        bash = str(bundled) if bundled.is_file() else shutil.which('bash')
        if not bash:
            self.skipTest('Bash is required for inert profile admission predicates')
        context = (ROOT / 'maintenance/release/context.sh').read_text(encoding='utf-8')
        validator = (ROOT / 'release/vm-validate.sh').read_text(encoding='utf-8')
        prepare = (ROOT / 'maintenance/release/prepare.sh').read_text(encoding='utf-8')
        guards = [
            ('release_dir', next(line for line in context.splitlines() if '$release_dir =~' in line).strip().split(' || return')[0], 'history'),
            ('profile', next(line for line in validator.splitlines() if line.lstrip().startswith('[[ "$profile" == 281')).strip(), 'current'),
            ('profile', next(line for line in prepare.splitlines() if line.lstrip().startswith('[[ $profile ==')).strip(), 'current'),
        ]
        env = {key: value for key, value in os.environ.items() if key not in {'BASH_ENV', 'ENV', 'SHELLOPTS'}}
        for variable, guard, scope in guards:
            cases = (('278', True), ('277', True), ('279', True), ('280', True), ('281', False), ('0280', False), ('280;true', False)) if scope == 'history' else (
                ('280', True), ('279', False), ('278', False), ('277', False), ('0280', False), ('280;true', False)
            )
            for profile, accepted in cases:
                value = '/opt/sub2api/releases/' + profile + '-aaaaaaaaaaaa-1-aaaaaaaa' if variable == 'release_dir' else profile
                script = variable + '=$1\nif ' + guard + '; then exit 0; else exit 1; fi'
                result = subprocess.run([bash, '--noprofile', '--norc', '-c', script, 'profile-admission', value],
                                        env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, (variable, profile, result.stderr))
