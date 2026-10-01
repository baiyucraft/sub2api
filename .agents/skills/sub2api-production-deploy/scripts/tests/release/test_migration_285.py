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

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.migration_planner import HOOK_REGISTRY
from release.paths import WORKSPACE
from release.production import ProductionRelease

FILENAME = "285_upstream_null_rate_lifecycle.sql"
HELPER = DEPLOY_ROOT / "maintenance" / "release" / "migration-285-assert.sh"
BASH = shutil.which("bash") or ("C:/Program Files/Git/bin/bash.exe" if os.name == "nt" else None)


class Migration285ProductionTest(unittest.TestCase):
    def release(self, *, pending: bool) -> ProductionRelease:
        release = ProductionRelease.__new__(ProductionRelease)
        item = {"filename": FILENAME, "checksum": "a" * 64}
        release.migration_plan = {"existing": [] if pending else [{**item, "status": "verified"}]}
        release.evidence = {"migration_evidence": {"pending": [item] if pending else []}}
        release.release_dir = "/opt/sub2api/releases/test"
        release.active_assets = "/opt/sub2api/releases/.active-release/assets"
        release.stage = mock.Mock()
        release.run_remote = mock.Mock(return_value={"hook_verified": "true"})
        return release

    def test_pending_and_verified_replay_use_only_readonly_production_phases(self) -> None:
        for pending in (True, False):
            with self.subTest(pending=pending):
                release = self.release(pending=pending)
                release.migration_preflight_v2()
                release.postflight_migration_hooks_v2()
                calls = release.run_remote.call_args_list
                self.assertEqual(len(calls), 2)
                for call, phase in zip(calls, ("preflight", "postflight")):
                    self.assertIn(f"migration-285-assert.sh {phase} >/dev/null", call.args[1])
                    self.assertEqual(call.args[2], {"hook_verified"})
                    self.assertNotIn("vm_semantics", call.args[1])
                    self.assertNotIn("ASSERT_VM_ISOLATED_DB", call.args[1])
                self.assertIn(f"MIGRATION_STATUS={'absent' if pending else 'verified'}", calls[0].args[1])
                self.assertIn("MIGRATION_STATUS=verified", calls[1].args[1])

    def test_replay_does_not_rerun_unregistered_ordinary_or_other_migration_hooks(self) -> None:
        release = self.release(pending=False)
        release.migration_plan["existing"].extend([
            {"filename": "243_backfill_codex_fingerprint_seed.sql", "status": "verified"},
            {"filename": "ordinary.sql", "status": "verified"},
        ])
        self.assertEqual([item["filename"] for item in release.migration_hook_items_v2()], [FILENAME])

    def test_missing_or_unknown_production_plan_fails_closed(self) -> None:
        release = self.release(pending=False)
        release.migration_plan = None
        with self.assertRaisesRegex(RuntimeError, "plan is missing"):
            release.migration_preflight_v2()
        release.run_remote.assert_not_called()
        release = self.release(pending=False)
        release.migration_plan["existing"][0]["status"] = "unknown"
        with self.assertRaisesRegex(RuntimeError, "status is unknown"):
            release.migration_preflight_v2()
        release.run_remote.assert_not_called()

    def test_immutable_function_body_matches_readonly_fingerprint(self) -> None:
        sql = (WORKSPACE / "backend" / "migrations" / FILENAME).read_text(encoding="utf-8")
        body = re.search(r"RETURNS TRIGGER AS \$\$(.*?)\$\$ LANGUAGE plpgsql;", sql, re.S).group(1)
        digest = hashlib.sha256(body.replace("\r\n", "\n").encode()).hexdigest()
        self.assertIn(f"='{digest}'", HELPER.read_text(encoding="utf-8"))
        self.assertNotEqual(digest, hashlib.sha256(body.replace("'stale'", "'s tale'").encode()).hexdigest())
        self.assertNotIn("regexp_replace(p.prosrc", HELPER.read_text(encoding="utf-8"))
        self.assertEqual(HOOK_REGISTRY[FILENAME]["vm_postflight"], ("vm_semantics", "verified_replay"))
        self.assertTrue(HOOK_REGISTRY[FILENAME]["replay"])

    def test_vm_runs_semantics_and_replay_after_apply_before_signing(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        apply = validator.index("mark_v2_stage migration_apply")
        semantics = validator.index('run_hook_v2 "$lifecycle_filename" migration-285-assert.sh vm_semantics verified')
        replay = validator.index('run_hook_v2 "$lifecycle_filename" migration-285-assert.sh verified_replay verified')
        sign = validator.index('/usr/local/libexec/sub2api-sign-gate "$output_dir/gate.json"')
        self.assertLess(apply, semantics)
        self.assertLess(semantics, replay)
        self.assertLess(replay, sign)
        self.assertIn('ASSERT_VM_ISOLATED_DB="$probe_db"', validator)
        self.assertIn('.evidence.migration_evidence.migration_285', validator)
        self.assertIn('.existing | any(.filename == $filename and .status == "existing")', validator)
        go_planner = (WORKSPACE / "backend" / "internal" / "repository" / "migration_plan.go").read_text(encoding="utf-8")
        self.assertIn('status := "existing"', go_planner)


@unittest.skipUnless(BASH and Path(BASH).exists(), "Bash is unavailable")
class Migration285HelperTest(unittest.TestCase):
    def test_helper_and_vm_validator_bash_syntax(self) -> None:
        for path in (HELPER, DEPLOY_ROOT / "release" / "vm-validate.sh"):
            with self.subTest(path=path.name):
                result = subprocess.run([BASH, "-n", str(path)], capture_output=True, text=True, timeout=15)
                self.assertEqual(result.returncode, 0, result.stderr)

    def run_helper(self, phase: str, status: str, *, db: str = "sub2api", isolated: str = "",
                   false_query: str = "", failed_query: str = "", fixture_result: str = "verified") -> tuple[subprocess.CompletedProcess, str, str]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            context = root / "context.sh"
            context.write_text("profile=260\n", encoding="utf-8")
            env = os.environ.copy()
            env.update({
                "ASSERT_CONTEXT_FILE": context.as_posix(), "MIGRATION_STATUS": status,
                "ASSERT_DB_NAME": db, "ASSERT_VM_ISOLATED_DB": isolated,
                "TASK_285_QUERY_LOG": (root / "queries.log").as_posix(),
                "TASK_285_FIXTURE_LOG": (root / "fixture.sql").as_posix(),
                "TASK_285_FALSE_QUERY": false_query, "TASK_285_FAILED_QUERY": failed_query,
                "TASK_285_FIXTURE_RESULT": fixture_result,
            })
            script = r'''
docker() {
  local statement=${!#}
  if [[ " $* " == *" -c "* ]]; then
    printf '%s\n' "$statement" >> "$TASK_285_QUERY_LOG"
    if [[ -n $TASK_285_FAILED_QUERY && $statement == *"$TASK_285_FAILED_QUERY"* ]]; then
      printf 'private-database-diagnostic\n' >&2
      return 1
    fi
    if [[ -n $TASK_285_FALSE_QUERY && $statement == *"$TASK_285_FALSE_QUERY"* ]]; then
      printf 'f\n'
    else
      printf 't\n'
    fi
  else
    cat > "$TASK_285_FIXTURE_LOG"
    printf '%s\n' "$TASK_285_FIXTURE_RESULT"
  fi
}
export -f docker
bash "$1" "$2"
'''
            result = subprocess.run([BASH, "-c", script, "test", HELPER.as_posix(), phase],
                                    env=env, capture_output=True, text=True, timeout=15)
            queries = (root / "queries.log").read_text(encoding="utf-8") if (root / "queries.log").exists() else ""
            fixture = (root / "fixture.sql").read_text(encoding="utf-8") if (root / "fixture.sql").exists() else ""
            return result, queries, fixture

    def test_absent_preflight_is_readonly_schema_only(self) -> None:
        result, queries, fixture = self.run_helper("preflight", "absent")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "migration_285_preflight=pass")
        self.assertIn("BEGIN READ ONLY", queries)
        self.assertIn("'preflight'='preflight' AND 'absent'='absent'", queries)
        self.assertIn("format_type(a.atttypid,a.atttypmod)='numeric(10,4)'", queries)
        self.assertNotIn("pg_proc", queries)
        self.assertEqual(fixture, "")

    def test_production_postflight_and_replay_are_readonly_metadata_assertions(self) -> None:
        for phase in ("preflight", "postflight", "verified_replay"):
            with self.subTest(phase=phase):
                result, queries, fixture = self.run_helper(phase, "verified")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(queries.count("BEGIN READ ONLY"), 3)
                self.assertIn("t.tgtype=23", queries)
                self.assertIn("upstream_source_rate_multiplier", queries)
                self.assertEqual(fixture, "")

    def test_schema_function_and_trigger_drift_fail_closed(self) -> None:
        for contract in ("WITH expected", "FROM pg_proc", "FROM pg_trigger"):
            with self.subTest(contract=contract):
                result, _, fixture = self.run_helper("verified_replay", "verified", false_query=contract)
                self.assertNotEqual(result.returncode, 0)
                self.assertRegex(result.stdout, r"migration_285_failure_code=(schema|function|trigger)_contract")
                self.assertNotIn("=pass", result.stdout)
                self.assertEqual(fixture, "")

    def test_database_diagnostics_never_escape_helper(self) -> None:
        result, _, _ = self.run_helper("postflight", "verified", failed_query="FROM pg_proc")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout.strip(), "migration_285_failure_code=function_query")
        self.assertEqual(result.stderr, "")

    def test_invalid_phase_or_migration_status_cannot_query_database(self) -> None:
        for phase, status in (("invalid", "verified"), ("postflight", "absent"), ("preflight", "unknown")):
            with self.subTest(phase=phase, status=status):
                result, queries, fixture = self.run_helper(phase, status)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(queries, "")
                self.assertEqual(fixture, "")

    def test_fixture_refuses_production_and_missing_or_unsafe_isolation_identity(self) -> None:
        for db, isolated in (("sub2api", "sub2api"), ("sub2api_v2_test", ""),
                             ("sub2api_v2_test", "sub2api_v2_other"), ("sub2api_v2_'", "sub2api_v2_'")):
            with self.subTest(db=db, isolated=isolated):
                result, _, fixture = self.run_helper("vm_semantics", "verified", db=db, isolated=isolated)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("isolation_boundary", result.stdout)
                self.assertEqual(fixture, "")

    def test_vm_fixture_uses_installed_function_temporary_tables_and_rollback(self) -> None:
        result, queries, fixture = self.run_helper("vm_semantics", "verified", db="sub2api_v2_test", isolated="sub2api_v2_test")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("current_database()", queries)
        self.assertIn("CREATE TEMP TABLE accounts", fixture)
        self.assertIn("CREATE TEMP TABLE upstream_keys", fixture)
        self.assertIn("EXECUTE FUNCTION public.validate_account_upstream_key_binding()", fixture)
        self.assertIn("EXCEPTION WHEN check_violation", fixture)
        self.assertIn("upstream_source_rate_multiplier=NULL WHERE id=1", fixture)
        self.assertTrue(fixture.strip().endswith("ROLLBACK;"))
        self.assertNotIn("CREATE OR REPLACE FUNCTION", fixture)

    def test_vm_fixture_failure_cannot_be_reported_as_pass(self) -> None:
        result, _, _ = self.run_helper("vm_semantics", "verified", db="sub2api_v2_test", isolated="sub2api_v2_test", fixture_result="")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout.strip(), "migration_285_failure_code=vm_semantics")


if __name__ == "__main__":
    unittest.main()
