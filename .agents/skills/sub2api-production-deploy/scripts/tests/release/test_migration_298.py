"""Production hooks stay read-only; mutation fixture requires isolated VM DB."""
from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.paths import WORKSPACE
from release.migration_planner import HOOK_REGISTRY
from release.manifest import release_asset_paths
from release.production import ProductionRelease
from release.gate import _validate_v2_pending

HELPER = ROOT / "maintenance/release/migration-298-assert.sh"
MIGRATION = WORKSPACE / "backend/migrations/298_group_scheduling_protection_policies.sql"
FILENAME = MIGRATION.name
BASH = shutil.which("bash") or ("C:/Program Files/Git/bin/bash.exe" if os.name == "nt" else None)


class Migration298ContractTest(unittest.TestCase):
    def test_hooks_assets_and_production_replay(self):
        self.assertIn(HELPER, release_asset_paths())
        self.assertEqual(HOOK_REGISTRY[FILENAME]["vm_postflight"], ("vm_semantics",))
        for pending in (True, False):
            release = ProductionRelease.__new__(ProductionRelease)
            item = {"filename": FILENAME, "checksum": "a" * 64}
            release.migration_plan = {"existing": [] if pending else [{**item, "status": "verified"}]}
            release.evidence = {"migration_evidence": {"pending": [item] if pending else []}}
            release.release_dir = "/opt/sub2api/releases/test"
            release.active_assets = "/opt/sub2api/releases/.active-release/assets"
            release.stage = mock.Mock()
            release.run_remote = mock.Mock(return_value={"hook_verified": "true"})
            release.migration_preflight_v2()
            release.postflight_migration_hooks_v2()
            self.assertEqual(len(release.run_remote.call_args_list), 3)
            for call, phase in zip(release.run_remote.call_args_list, ("preflight", "postflight", "verified_replay")):
                self.assertIn(f"migration-298-assert.sh {phase} >/dev/null", call.args[1])
                self.assertNotIn("vm_semantics", call.args[1])

    def test_gate_requires_checksum_and_all_four_semantic_phases(self):
        contracts = [("285_upstream_null_rate_lifecycle.sql", "b" * 64)]
        # A shared dirty checkout may also have the independent 297 hook patch.
        # Keep this test valid with both the committed chain and that local patch.
        if "297_upstream_cache_guard_account_scope.sql" in HOOK_REGISTRY:
            contracts.append(("297_upstream_cache_guard_account_scope.sql", "c" * 64))
        contracts.append((FILENAME, "d" * 64))
        catalog = [{"filename": name, "checksum": checksum, "non_transactional": False} for name, checksum in contracts]
        manifest = {"profile": "270", "migration_catalog": catalog}
        semantic = {"preflight": True, "postflight": True, "vm_semantics": True, "verified_replay": True}
        migration = {"database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
                     "isolated_upgrade_verified": True, "final_schema_verified": True}
        for row in catalog:
            migration["migration_" + row["filename"][:3]] = {**semantic, "checksum": row["checksum"]}
        _validate_v2_pending(manifest, {"migration_evidence": migration})
        for key in ("checksum", *semantic):
            with self.subTest(key=key):
                bad = {**migration, "migration_298": {**migration["migration_298"], key: "wrong"}}
                with self.assertRaisesRegex(RuntimeError, "migration 298"):
                    _validate_v2_pending(manifest, {"migration_evidence": bad})

    def test_immutable_checksum_and_vm_order(self):
        source = HELPER.read_text(encoding="utf-8")
        raw = MIGRATION.read_bytes()
        self.assertIn("catalog_checksum=" + hashlib.sha256(raw.decode().strip().encode()).hexdigest(), source)
        self.assertIn(hashlib.sha256(raw).hexdigest(), source)
        vm = (ROOT / "release/vm-validate.sh").read_text(encoding="utf-8")
        self.assertLess(vm.index("mark_v2_stage migration_apply"), vm.index('run_hook_v2 "$protection_filename" migration-298-assert.sh vm_semantics verified'))
        self.assertIn('.evidence.migration_evidence.migration_298', vm)
        self.assertIn('BEGIN READ ONLY;', source)
        self.assertIn("ROLLBACK;", source)


@unittest.skipUnless(BASH and Path(BASH).exists(), "Bash unavailable")
class Migration298HelperTest(unittest.TestCase):
    def run_helper(self, phase="preflight", status="absent", *, profile="270", false_query="", bad_manifest=False,
                   db="sub2api", isolated="", fixture_result="verified", manifest_override=True):
        # The docker/jq functions are stubs and never contact a host.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            context = root / "context.sh"
            context.write_text(f"profile={profile}\n", encoding="utf-8")
            manifest = root / "gate.json"
            manifest.write_text("{}\n", encoding="utf-8")
            log = root / "query.log"
            env = os.environ.copy()
            env.update({
                "ASSERT_CONTEXT_FILE": context.as_posix(),
                "ASSERT_MANIFEST_FILE": manifest.as_posix(),
                "RELEASE_DIR": root.as_posix(),
                "MIGRATION_STATUS": status,
                "TASK_298_QUERY_LOG": log.as_posix(),
                "TASK_298_FALSE_QUERY": false_query,
                "TASK_298_BAD_MANIFEST": "1" if bad_manifest else "0",
                "ASSERT_DB_NAME": db,
                "ASSERT_VM_ISOLATED_DB": isolated,
                "ASSERT_MIGRATION_FILE": MIGRATION.as_posix(),
                "TASK_298_FIXTURE_RESULT": fixture_result,
            })
            if not manifest_override:
                env.pop("ASSERT_MANIFEST_FILE", None)
            script = r'''
jq() {
  [[ $TASK_298_BAD_MANIFEST == 0 ]]
}
docker() {
  if [[ " $* " != *" -c "* ]]; then
    cat >/dev/null
    printf '%s\n' "$TASK_298_FIXTURE_RESULT"
    return
  fi
  local statement=${!#}
  printf '%s\n' "$statement" >> "$TASK_298_QUERY_LOG"
  if [[ -n $TASK_298_FALSE_QUERY && $statement == *"$TASK_298_FALSE_QUERY"* ]]; then
    printf 'f\n'
  else
    printf 't\n'
  fi
}
export -f jq docker
bash "$1" "$2"
'''
            result = subprocess.run([BASH, "-c", script, "test", HELPER.as_posix(), phase],
                                    env=env, capture_output=True, text=True, timeout=15)
            queries = log.read_text(encoding="utf-8") if log.exists() else ""
            return result, queries

    def test_production_release_dir_binds_candidate_without_manifest_override(self):
        result, queries = self.run_helper(manifest_override=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(queries.count("BEGIN READ ONLY"), 2)

    def test_syntax_readonly_checks_and_failures(self):
        result = subprocess.run([BASH, "-n", HELPER.as_posix()], capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        result, queries = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(queries.count("BEGIN READ ONLY"), 2)
        for marker, code in (("FROM public.schema_migrations", "migration_record"), ("WITH expected(col,typ)", "column_contract"), ("FROM pg_constraint", "constraint_contract")):
            result, queries = self.run_helper("verified_replay", "verified", false_query=marker)
            self.assertIn("migration_298_failure_code=" + code, result.stdout)
        for kwargs in ({"profile": "269"}, {"phase": "unknown"}, {"phase": "postflight"}, {"bad_manifest": True}):
            result, queries = self.run_helper(**kwargs)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(queries, "")

    def test_semantics_requires_exact_isolated_vm_database(self):
        for db, isolated in (("sub2api", "sub2api"), ("sub2api_v2_test", ""), ("sub2api_v2_test", "sub2api_v2_other")):
            result, _ = self.run_helper("vm_semantics", "verified", db=db, isolated=isolated)
            self.assertIn("isolation_boundary", result.stdout)
        result, _ = self.run_helper("vm_semantics", "verified", db="sub2api_v2_test", isolated="sub2api_v2_test")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
