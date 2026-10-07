"""Exercise the opt-in catch-up shell boundary with a local psql double."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class ActivityRewardCostCatchupTest(unittest.TestCase):
    def setUp(self):
        self.bash = Path(r"C:\Program Files\Git\bin\bash.exe") if os.name == "nt" else Path("/bin/bash")
        if not self.bash.is_file():
            self.skipTest("Bash unavailable")
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.sql = self.directory / "captured.sql"
        # Bash -c source avoids any real database connection or CLI credentials.
        self.wrapper = '''
psql() { cat > "$SQL_CAPTURE"; printf '{"verified":true,"inserted_rows":0}\\n'; }
export -f psql
source "$SCRIPT_PATH" "$@"
'''
        self.environment = os.environ.copy()
        self.environment.pop("BASH_ENV", None)
        self.environment.update(SQL_CAPTURE=self.sql.as_posix(), SCRIPT_PATH=(ROOT / "release/backfill-activity-reward-costs.sh").as_posix())

    def run_script(self, *args):
        return subprocess.run([str(self.bash), "--noprofile", "--norc", "-c", self.wrapper, "catchup", *args], env=self.environment, text=True, capture_output=True, timeout=10)

    def test_check_is_read_only_and_audits_zero_amount_and_original_date(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        sql = self.sql.read_text()
        self.assertIn("REPEATABLE READ READ ONLY", sql)
        self.assertNotIn("SELECT public.backfill_activity_reward_costs()", sql)
        self.assertNotIn("amount > 0", sql)
        for field in ("Asia/Shanghai", "missing_count", "orphan_count", "mismatch_count", "related_user_id", "activity_type", "created_at", "reversal_of"):
            self.assertIn(field, sql)

    def test_apply_requires_explicit_drain_assertion_before_database_connection(self):
        result = self.run_script("--mode", "apply")
        self.assertEqual(result.returncode, 2)
        self.assertIn("old_instances_not_drained", result.stderr)
        self.assertFalse(self.sql.exists())

    def test_apply_calls_function_once_and_commits_with_daily_audit(self):
        result = self.run_script("--mode", "apply", "--old-instances-drained")
        self.assertEqual(result.returncode, 0, result.stderr)
        sql = self.sql.read_text()
        self.assertEqual(sql.count("SELECT public.backfill_activity_reward_costs()"), 1)
        self.assertIn("BEGIN;", sql)
        self.assertIn("COMMIT;", sql)
        self.assertIn("'daily'", sql)
        self.assertNotIn("UPDATE users", sql)

    def test_unknown_mode_and_argument_are_rejected(self):
        for args in (("--mode", "force"), ("--force",)):
            with self.subTest(args=args):
                result = self.run_script(*args)
                self.assertEqual(result.returncode, 2)
                self.assertFalse(self.sql.exists())

    def test_database_errors_are_redacted_and_fail_closed(self):
        self.wrapper = self.wrapper.replace("cat >", "printf 'password=private' >&2; return 1; cat >")
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertIn("database_check_or_apply_failed", result.stderr)
        self.assertNotIn("private", result.stderr)

    def test_nonmatching_audit_cannot_report_success(self):
        self.wrapper = self.wrapper.replace('"verified":true', '"verified":false')
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertIn("audit_mismatch", result.stderr)
        self.assertIn('"verified":false', result.stdout)


if __name__ == "__main__":
    unittest.main()
