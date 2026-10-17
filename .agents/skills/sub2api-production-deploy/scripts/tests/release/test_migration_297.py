from __future__ import annotations

import hashlib
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

DEPLOY = Path(__file__).resolve().parents[2]
WORKSPACE = DEPLOY.parents[3]
HELPER = DEPLOY / "maintenance/release/migration-297-assert.sh"
MIGRATION = WORKSPACE / "backend/migrations/297_upstream_cache_guard_account_scope.sql"
BASH = shutil.which("bash") or ("C:/Program Files/Git/bin/bash.exe" if os.name == "nt" else None)
sys.path.insert(0, str(DEPLOY))
from release.migration_planner import HOOK_REGISTRY
from release.production import ProductionRelease
from release.manifest import release_asset_paths
from release.gate import _validate_v2_pending

FILENAME = MIGRATION.name


class Migration297ContractTest(unittest.TestCase):
    def test_registered_asset_and_readonly_production_replay(self):
        self.assertIn(HELPER, release_asset_paths())
        self.assertTrue(HOOK_REGISTRY[FILENAME]["replay"])
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
            calls = release.run_remote.call_args_list
            self.assertEqual(len(calls), 3)
            for call, phase in zip(calls, ("preflight", "postflight", "verified_replay")):
                self.assertIn(f"migration-297-assert.sh {phase} >/dev/null", call.args[1])
                self.assertIn("ASSERT_MANIFEST_FILE=/opt/sub2api/releases/test/gate.json", call.args[1])
                self.assertNotIn("vm_semantics", call.args[1])
            self.assertIn(f"MIGRATION_STATUS={'absent' if pending else 'verified'}", calls[0].args[1])

    def test_vm_checks_semantics_and_verified_replay_before_signing(self):
        source = (DEPLOY / "release/vm-validate.sh").read_text(encoding="utf-8")
        semantics = source.index('run_hook_v2 "$account_scope_filename" migration-297-assert.sh vm_semantics verified')
        self.assertLess(source.index("mark_v2_stage migration_apply"), semantics)
        self.assertLess(semantics, source.index('/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json"'))
        self.assertIn('ASSERT_MANIFEST_FILE="$manifest"', source)
        self.assertIn('ASSERT_MIGRATION_FILE="$source_dir/backend/migrations/$filename"', source)
        self.assertIn('.evidence.migration_evidence.migration_297', source)

    def test_existing_and_pending_gate_evidence_is_bound_and_complete(self):
        checksum = "a" * 64
        manifest = {"profile": "269", "migration_catalog": [{"filename": FILENAME, "checksum": checksum, "non_transactional": False}]}
        # Profile 269 also requires the immutable 285 contract.
        lifecycle = {"filename": "285_upstream_null_rate_lifecycle.sql", "checksum": "b" * 64, "non_transactional": False}
        manifest["migration_catalog"].insert(0, lifecycle)
        semantic = {"checksum": checksum, "preflight": True, "postflight": True, "vm_semantics": True, "verified_replay": True}
        for pending in (False, True):
            migration = {"database_high_watermark": None, "pending": [], "existing_checksums_verified": True,
                         "isolated_upgrade_verified": True, "final_schema_verified": True,
                         "migration_285": {**semantic, "checksum": "b" * 64}, "migration_297": semantic.copy()}
            if pending:
                migration["pending"] = [{"filename": FILENAME, "checksum": checksum, "preflight": True, "postflight": True,
                                         "hook_results": {key: True for key in semantic if key != "checksum"}, "rollback_policy": "coordinated_restore"}]
            _validate_v2_pending(manifest, {"migration_evidence": migration})
            for key in semantic:
                with self.subTest(pending=pending, key=key):
                    changed = {**migration, "migration_297": {**semantic, key: "wrong"}}
                    with self.assertRaisesRegex(RuntimeError, "migration 297"):
                        _validate_v2_pending(manifest, {"migration_evidence": changed})
            del migration["migration_297"]
            with self.assertRaises(RuntimeError):
                _validate_v2_pending(manifest, {"migration_evidence": migration})

    def test_checksum_context_and_readonly_contract(self):
        source = HELPER.read_text(encoding="utf-8")
        raw = MIGRATION.read_bytes()
        catalog = hashlib.sha256(raw.decode("utf-8").strip().encode("utf-8")).hexdigest()
        self.assertIn("catalog_checksum=" + catalog, source)
        self.assertNotEqual(catalog, hashlib.sha256(raw).hexdigest())
        for clause in (
            '[[ $profile == 269 || $profile == 270 || $profile == 271 || $profile == 272 || $profile == 273 ]]', '$m.parent_profile == "268"',
            '$m.new_migrations == [$filename]', 'COUNT(*)=1 AND bool_and(checksum=',
            'BEGIN READ ONLY;', 'SET LOCAL search_path = public, pg_catalog;',
            "'upstream_cache_guard_facts','upstream_key_id'",
            "'upstream_cache_guard_accounts','account_id'",
            "'upstream_cache_guard_accounts','upstream_key_id'",
            "'upstream_cache_guard_accounts','version'",
            "'upstream_cache_guard_accounts','state_json'",
            "'upstream_cache_guard_accounts','updated_at'",
            'idx_upstream_cache_guard_account_history',
            'idx_upstream_cache_guard_legacy_identity',
            'upstream_cache_guard_account_positive',
            'upstream_cache_guard_account_object',
            'AND NOT condeferrable AND NOT condeferred',
            "COALESCE((to_jsonb(pg_constraint)->>'conenforced')::boolean,TRUE)",
            'COUNT(*) FILTER (WHERE x.indisunique)=1',
            'x.indisprimary AND x.indisunique AND x.indisvalid AND x.indisready',
            "x.indoption::text='0 0'",
            "'9223372036854775807'", 'IS DISTINCT FROM expected_key',
        ):
            with self.subTest(clause=clause):
                self.assertIn(clause, source)
        readonly_source = source.split("verify_vm_semantics()", 1)[0]
        self.assertNotRegex(readonly_source, r"(?m)^\s*(?:INSERT|UPDATE|DELETE|ALTER|CREATE|DROP)\b")


@unittest.skipUnless(BASH and Path(BASH).exists(), "Bash unavailable")
class Migration297HelperTest(unittest.TestCase):
    def test_bash_syntax(self):
        result = subprocess.run([BASH, "-n", str(HELPER)], capture_output=True, text=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)

    def run_helper(self, phase="preflight", status="absent", *, profile="269", false_query="", bad_manifest=False,
                   db="sub2api", isolated="", fixture_result="verified"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            context = root / "context.sh"
            context.write_text(f"profile={profile}\n", encoding="utf-8")
            manifest = root / "manifest.json"
            manifest.write_text("{}\n", encoding="utf-8")
            log = root / "query.log"
            env = os.environ.copy()
            env.update({
                "ASSERT_CONTEXT_FILE": context.as_posix(),
                "ASSERT_MANIFEST_FILE": manifest.as_posix(),
                "MIGRATION_STATUS": status,
                "TASK_297_QUERY_LOG": log.as_posix(),
                "TASK_297_FALSE_QUERY": false_query,
                "TASK_297_BAD_MANIFEST": "1" if bad_manifest else "0",
                "ASSERT_DB_NAME": db,
                "ASSERT_VM_ISOLATED_DB": isolated,
                "ASSERT_MIGRATION_FILE": MIGRATION.as_posix(),
                "TASK_297_FIXTURE_RESULT": fixture_result,
            })
            script = r'''
jq() {
  [[ $TASK_297_BAD_MANIFEST == 0 ]]
}
docker() {
  if [[ " $* " != *" -c "* ]]; then
    cat >/dev/null
    printf '%s\n' "$TASK_297_FIXTURE_RESULT"
    return
  fi
  local statement=${!#}
  printf '%s\n' "$statement" >> "$TASK_297_QUERY_LOG"
  if [[ -n $TASK_297_FALSE_QUERY && $statement == *"$TASK_297_FALSE_QUERY"* ]]; then
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

    def test_pending_preflight_is_readonly_and_rejects_partial_schema(self):
        result, queries = self.run_helper()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout.strip(), "migration_297_preflight=pass")
        self.assertEqual(queries.count("BEGIN READ ONLY"), 2)
        self.assertIn("to_regclass('public.upstream_cache_guard_accounts') IS NULL", queries)
        self.assertNotIn("FROM public.schema_migrations", queries)
        result, _ = self.run_helper(false_query="to_regclass('public.upstream_cache_guard_accounts')")
        self.assertEqual(result.stdout.strip(), "migration_297_failure_code=preflight_contract")

    def test_verified_phases_check_record_columns_constraints_indexes_backfill(self):
        for phase in ("preflight", "postflight", "verified_replay"):
            with self.subTest(phase=phase):
                result, queries = self.run_helper(phase, "verified")
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(queries.count("BEGIN READ ONLY"), 8)
                for marker in ("FROM public.schema_migrations", "pg_attrdef", "pg_constraint",
                               "pg_index", "upstream_cache_guard_facts)"):
                    self.assertIn(marker, queries)
        for marker, code in (
            ("FROM public.schema_migrations", "record_contract"),
            ("WITH expected(rel,col,typ,required,default_sql)", "column_contract"),
            ("FROM pg_constraint", "constraint_contract"),
            ("WITH expected(name,table_name,keys,predicate)", "index_contract"),
            ("COUNT(*) FILTER (WHERE x.indisunique)", "index_contract"),
            ("WITH facts AS", "backfill_contract"),
        ):
            with self.subTest(marker=marker):
                result, _ = self.run_helper("verified_replay", "verified", false_query=marker)
                self.assertEqual(result.stdout.strip(), "migration_297_failure_code=" + code)
                self.assertEqual(result.stderr, "")

    def test_invalid_status_profile_or_manifest_fails_before_query(self):
        for kwargs in ({"profile": "268"}, {"phase": "unknown"},
                       {"phase": "postflight"}, {"status": "unknown"},
                       {"bad_manifest": True}):
            with self.subTest(kwargs=kwargs):
                result, queries = self.run_helper(**kwargs)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(queries, "")
                self.assertNotIn("=pass", result.stdout)

    def test_semantics_refuses_production_and_requires_isolation(self):
        for db, isolated in (("sub2api", "sub2api"), ("sub2api_v2_test", ""), ("sub2api_v2_test", "sub2api_v2_other")):
            result, _ = self.run_helper("vm_semantics", "verified", db=db, isolated=isolated)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("isolation_boundary", result.stdout)
        result, _ = self.run_helper("vm_semantics", "verified", db="sub2api_v2_test", isolated="sub2api_v2_test")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result, _ = self.run_helper("vm_semantics", "verified", db="sub2api_v2_test", isolated="sub2api_v2_test", fixture_result="")
        self.assertEqual(result.stdout.strip(), "migration_297_failure_code=vm_semantics")


if __name__ == "__main__":
    unittest.main()
