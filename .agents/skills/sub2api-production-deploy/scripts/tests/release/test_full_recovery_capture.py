"""Execute PostgreSQL snapshot capture with bounded fake store processes."""
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.process import run_hidden

SCRIPT = Path(__file__).resolve().parents[2] / "maintenance/release/full-recovery-capture.sh"
BASH = shutil.which("bash") or (r"C:\Program Files\Git\bin\bash.exe" if os.name == "nt" else None)


@unittest.skipUnless(BASH and Path(BASH).is_file(), "Git Bash or bash required")
class FullRecoveryCaptureTests(unittest.TestCase):
    def run_bash(self, script):
        return run_hidden([BASH, "--noprofile", "--norc", "-c", script], capture_output=True, text=True, timeout=20)

    def test_shell_syntax(self):
        result = run_hidden([BASH, "-n", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_exact_snapshot_is_shared_by_dump_and_all_metadata_readers(self):
        text = SCRIPT.read_text()
        begin = text.index("coproc PG_SNAPSHOT")
        end = text.index("redis_password=", begin)
        code = text[begin:end]
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary).resolve().as_posix()
            # The exporter responds once then stays alive until ROLLBACK + \q.
            # Every importer must request that same live snapshot; stale or
            # independently sampled metadata is an actual test failure.
            harness = r'''
set -Eeuo pipefail
work=WORK
mkdir -p "$work/database" "$work/metadata"
docker() {
  printf '%s\n' "$*" >> "$work/calls"
  if [[ $* == 'exec -i sub2api-postgres psql '* ]]; then
    IFS= read -r statement
    [[ $statement == *'pg_export_snapshot();' ]]
    printf '00000003-00000123-1\n'
    while IFS= read -r statement; do [[ $statement == '\q' ]] && break; done
    return 0
  fi
  if [[ $* == 'exec sub2api-postgres pg_dump '* ]]; then
    [[ $* == *'--snapshot=00000003-00000123-1'* ]]
    printf 'PGDMPfixture'
    return 0
  fi
  if [[ $* == 'exec sub2api-postgres pg_dumpall '* ]]; then printf 'globals'; return 0; fi
  [[ $* == *"SET TRANSACTION SNAPSHOT '00000003-00000123-1';"* ]]
  printf 'fixture metadata\n'
}
timeout() { shift; "$@"; }
CODE
test -s "$work/database/sub2api.dump"
test -s "$work/metadata/core-counts.txt"
test -s "$work/metadata/core-content-digests.txt"
test -s "$work/metadata/migrations.txt"
test -s "$work/metadata/postgres.txt"
test "$(grep -c 'SET TRANSACTION SNAPSHOT' "$work/calls")" = 4
'''
            # Single quote is absent from a TemporaryDirectory generated path.
            result = self.run_bash(harness.replace("WORK", "'" + work + "'").replace("CODE", code))
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_failed_snapshot_export_prevents_dump_and_metadata(self):
        text = SCRIPT.read_text()
        begin = text.index("coproc PG_SNAPSHOT")
        end = text.index("redis_password=", begin)
        code = text[begin:end]
        with tempfile.TemporaryDirectory() as temporary:
            work = Path(temporary).resolve().as_posix()
            harness = r'''
set -Eeuo pipefail
work=WORK
mkdir -p "$work/database" "$work/metadata"
docker() {
  if [[ $* == 'exec -i sub2api-postgres psql '* ]]; then IFS= read -r statement; printf 'bad_snapshot\n'; return 0; fi
  touch "$work/unexpected_dump"
  return 1
}
CODE
'''
            result = self.run_bash(harness.replace("WORK", "'" + work + "'").replace("CODE", code))
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((Path(temporary).resolve() / "unexpected_dump").exists())

    def test_error_receipt_contains_only_stage_line_and_exit_code(self):
        text = SCRIPT.read_text()
        begin = text.index("capture_stage=preflight")
        end = text.index("cleanup()", begin)
        code = text[begin:end]
        result = self.run_bash("set -Eeuo pipefail\n" + code + "capture_stage=redis_snapshot\nprivate_fixture=never-echo-this-fixture\nfalse \"$private_fixture\"\n")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("capture_stage=redis_snapshot", result.stderr)
        self.assertIn("failed_line=", result.stderr)
        self.assertIn("exit_code=1", result.stderr)
        self.assertNotIn("never-echo", result.stderr + result.stdout)
        self.assertNotIn("false", result.stderr)

    def test_copied_data_root_permissions_do_not_touch_live_data(self):
        text = SCRIPT.read_text()
        begin = text.index('cp -a "$deploy_dir/data"')
        end = text.index('(cd "$work/config/app/data"', begin)
        code = text[begin:end]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve().as_posix()
            harness = r'''
set -Eeuo pipefail
root=ROOT
deploy_dir="$root/live"
work="$root/work"
mkdir -p "$deploy_dir/data" "$work/config/app"
printf 'same bytes' > "$deploy_dir/data/file"
chmod() { printf '%s\n' "$*" >> "$root/chmod-calls"; }
CODE
cmp "$deploy_dir/data/file" "$work/config/app/data/file"
test "$(cat "$root/chmod-calls")" = "700 $work/config/app/data"
'''
            result = self.run_bash(harness.replace("ROOT", "'" + root + "'").replace("CODE", code))
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
