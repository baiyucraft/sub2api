from __future__ import annotations

import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.process import run_hidden


class RecoveryFileContractTest(unittest.TestCase):
    def setUp(self) -> None:
        self.bash = shutil.which("bash")
        if os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git/bin/bash.exe"
            if candidate.is_file():
                self.bash = str(candidate)
        if not self.bash:
            self.skipTest("bash unavailable")
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        self.source = self.base / "source"
        self.target = self.base / "target"
        self.source.write_text("snapshot\n")
        self.target.write_text("original\n")
        self.source.chmod(0o600)
        self.target.chmod(0o600)
        source = (ROOT / "maintenance/release/restore.sh").read_text(encoding="utf-8")
        self.functions = self.base / "file-functions.sh"
        self.functions.write_text(source[source.index("cleanup_recovery() {"):
                                         source.index("restore_nginx_recovery() {")], encoding="utf-8")
        self.environment = os.environ.copy()
        self.environment.pop("BASH_ENV", None)
        self.environment.pop("ENV", None)
        self.environment.update(RF_BASE=str(self.base), RF_FUNCTIONS=str(self.functions))

    def execute(self, setup: str, command: str, mode: str):
        invocation = {
            "plain": command,
            "if": f"if {command}; then exit 0; else exit 17; fi",
            "or": f"{command} || exit 17",
        }[mode]
        script = r'''
set -Eeuo pipefail
if [[ ${OS:-} == Windows_NT ]]; then
  RF_BASE=$(cygpath -u "$RF_BASE"); RF_FUNCTIONS=$(cygpath -u "$RF_FUNCTIONS")
  # Preserve real filesystem, symlink and link-count behavior. Git Bash cannot
  # model Linux root ownership/permissions, so adapt only those metadata fields.
  stat() {
    local result; result=$(command stat "$@") || return 1
    if [[ $2 == '%U:%G:%a:%h' ]]; then printf 'root:root:600:%s\n' "${result##*:}"
    else printf '%s\n' "$result"; fi
  }
fi
source "$RF_FUNCTIONS"
''' + setup + "\n" + invocation + "\n"
        return run_hidden([self.bash, "--noprofile", "--norc", "-c", script], env=self.environment,
                          text=True, capture_output=True, check=False)

    def rejects(self, setup: str, command: str) -> None:
        for mode in ("plain", "if", "or"):
            with self.subTest(mode=mode):
                result = self.execute(setup, command, mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def symlink(self, source: Path, destination: Path, *, directory: bool = False) -> None:
        try:
            destination.symlink_to(source, target_is_directory=directory)
        except OSError:
            self.skipTest("native symlinks unavailable")

    def test_stat_partial_output_failure_is_rejected(self) -> None:
        self.rejects("stat() { printf 'root:root:600:1\\n'; return 29; }",
                     'assert_root_file "$RF_BASE/source" nginx')

    def test_symlink_file_rejected_even_when_stat_reports_allowed_metadata(self) -> None:
        self.symlink(self.source, self.base / "link")
        self.rejects("stat() { printf 'root:root:600:1\\n'; }", 'assert_root_file "$RF_BASE/link" nginx')

    def test_missing_parent_cannot_authorize_new_target(self) -> None:
        self.rejects("", 'assert_safe_file_target "$RF_BASE/missing/new" 600')

    def test_symlink_parent_cannot_authorize_new_target(self) -> None:
        self.symlink(self.base, self.base / "parent-link", directory=True)
        self.rejects("", 'assert_safe_file_target "$RF_BASE/parent-link/new" 600')

    def test_replacement_aborts_before_copy_when_source_assertion_fails(self) -> None:
        self.rejects(r'''assert_root_file() { return 29; }
cp() { printf 'copy called\n' > "$RF_BASE/copy-called"; }
mv() { printf 'move called\n' > "$RF_BASE/move-called"; }
''', 'replace_from_snapshot "$RF_BASE/source" "$RF_BASE/target" 600')
        self.assertFalse((self.base / "copy-called").exists())
        self.assertFalse((self.base / "move-called").exists())
        self.assertEqual(self.target.read_text(), "original\n")

    def test_copy_failure_never_moves_partial_copy(self) -> None:
        self.rejects(r'''cp() { printf 'partial\n' > "$4"; return 29; }
mv() { printf 'move called\n' > "$RF_BASE/move-called"; }
''', 'replace_from_snapshot "$RF_BASE/source" "$RF_BASE/target" 600')
        self.assertFalse((self.base / "move-called").exists())
        self.assertEqual(self.target.read_text(), "original\n")

    def test_existing_temporary_target_is_never_overwritten(self) -> None:
        self.rejects('printf "unrelated\\n" > "$RF_BASE/target.restore.$$"',
                     'replace_from_snapshot "$RF_BASE/source" "$RF_BASE/target" 600')
        self.assertEqual(self.target.read_text(), "original\n")
        self.assertTrue(all(path.read_text() == "unrelated\n" for path in self.base.glob("target.restore.*")))

    def test_partial_find_failure_is_rejected(self) -> None:
        self.rejects("find() { printf 'source\\0'; return 29; }", 'snapshot_entries "$RF_BASE"')
        self.rejects("find() { printf 'observability.conf\\0'; return 29; }",
                     'assert_observability_snapshot "$RF_BASE"')

    def test_extra_snapshot_entry_cannot_be_hidden_by_final_file_success(self) -> None:
        directory = self.base / "snapshot"
        directory.mkdir()
        (directory / "source").write_text("snapshot\n")
        (directory / "source").chmod(0o600)
        (directory / "unexpected").write_text("extra\n")
        self.rejects("", 'assert_single_snapshot "$RF_BASE/snapshot" source 600')
        self.rejects("find() { printf 'observability.conf\\0observability.absent\\0'; }; assert_root_file() { return 0; }",
                     'assert_observability_snapshot "$RF_BASE"')

    def test_cleanup_refuses_symlink_without_removing_it(self) -> None:
        self.symlink(self.base, self.base / "cleanup-link", directory=True)
        self.rejects('recovery="$RF_BASE/cleanup-link"', "cleanup_recovery")
        self.assertTrue((self.base / "cleanup-link").is_symlink())

    def test_valid_replacement_preserves_snapshot_bytes(self) -> None:
        result = self.execute("", 'replace_from_snapshot "$RF_BASE/source" "$RF_BASE/target" 600', "if")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.target.read_text(), "snapshot\n")


if __name__ == "__main__":
    unittest.main()
