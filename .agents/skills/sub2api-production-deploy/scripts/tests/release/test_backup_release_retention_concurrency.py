"""Exercise release retention against tiny local bundles and real Linux locks.

The dynamic cases never use a network connection or a real backup directory.
Windows skips them explicitly: Git Bash does not establish Linux flock coverage.
"""
from __future__ import annotations

import hashlib
import os
import shlex
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[2] / "release" / "backup-release-retention-clean.sh"
BASH = shutil.which("bash")
FLOCK = shutil.which("flock")
LINUX_FIXTURE = os.name == "posix" and BASH is not None and FLOCK is not None
RELEASE_IDS = (
    "283-aaaaaaaaaaaa-1700000000-11111111",
    "283-bbbbbbbbbbbb-1700000100-22222222",
    "283-cccccccccccc-1700000200-33333333",
)


class ReleaseRetentionLockOrderTest(unittest.TestCase):
    def test_apply_locks_precede_inventory_and_protection_reads(self) -> None:
        source = SCRIPT.read_text(encoding="utf-8")
        start = source.index("if [[ $mode == apply ]]; then")
        retention = source.index("flock -n 9", start)
        promotion = source.index("flock -n 8", retention)
        inventory = source.index('find "$release_root" -mindepth 2')
        protection = source.index('find "$release_root" -mindepth 1')
        digest_check = source.index('[[ $plan_arg =~ ^[0-9a-f]{64}$ && $plan_arg == "$plan_sha" ]]')
        delete = source.index('rm -- "$file"')
        self.assertLess(start, retention)
        self.assertLess(retention, promotion)
        self.assertLess(promotion, inventory)
        self.assertLess(inventory, protection)
        self.assertLess(protection, digest_check)
        self.assertLess(digest_check, delete)
        self.assertEqual(source.count("flock -n 9"), 1)
        self.assertEqual(source.count("flock -n 8"), 1)


@unittest.skipUnless(LINUX_FIXTURE, "real Linux Bash/flock fixtures require the Linux integration platform")
class ReleaseRetentionConcurrencyTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory(prefix="sub2api-retention-test-")
        self.addCleanup(self.temporary.cleanup)
        self.work = Path(self.temporary.name)
        self.root = self.work / "backups"
        self.releases = self.root / "releases" / "283"
        self.releases.mkdir(parents=True)
        (self.root / ".release-promotion.lock").touch()
        for release_id in RELEASE_IDS:
            bundle = self.releases / release_id
            bundle.mkdir()
            artifact = ("synthetic-backup-" + release_id).encode("ascii")
            artifact_digest = hashlib.sha256(artifact).hexdigest()
            (bundle / "artifact.tar.age").write_bytes(artifact)
            (bundle / "artifact.tar.age.sha256").write_text(
                artifact_digest + "  artifact.tar.age\n", encoding="ascii"
            )
            (bundle / "manifest").write_text(
                "release_id=" + release_id + "\n"
                "transport_artifact=sub2api-20200101T000000Z.tar.age\n"
                "sha256=" + artifact_digest + "\n",
                encoding="ascii",
            )
            (bundle / "bundle.sha256").write_text(
                "".join(
                    hashlib.sha256((bundle / name).read_bytes()).hexdigest() + "  " + name + "\n"
                    for name in ("artifact.tar.age", "artifact.tar.age.sha256", "manifest")
                ),
                encoding="ascii",
            )
        self.source = SCRIPT.read_text(encoding="utf-8").replace("/srv/sub2api-backups", str(self.root))
        self.script = self.work / "cleaner.sh"
        self.script.write_text(self.source, encoding="utf-8")
        self.environment = {
            key: value for key, value in os.environ.items()
            if key not in {"BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS"}
            and not key.startswith("BASH_FUNC_")
        }
        # Do not inherit operator retention overrides into a self-contained fixture.
        for key in (
            "BACKUP_ROOT", "RELEASE_ROOT", "RELEASE_RETENTION_DAYS", "KEEP_RECENT_RELEASES",
            "KEEP_RECENT_PER_PROFILE", "KEEP_LATEST_PROFILE_ONLY", "FAILED_RELEASE_RETENTION_DAYS",
        ):
            self.environment.pop(key, None)

    def run_cleaner(self, *arguments: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(BASH), "--noprofile", "--norc", str(self.script), *arguments],
            capture_output=True, text=True, timeout=20, env=self.environment,
        )

    def plan(self) -> dict[str, str]:
        result = self.run_cleaner("dry-run")
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split("=", 1) for line in result.stdout.splitlines())
        self.assertEqual(values["candidate_count"], "2")
        self.assertEqual(set(values["candidate_release_ids"].split(",")), set(RELEASE_IDS[:2]))
        return values

    def apply(self, plan: dict[str, str]) -> subprocess.CompletedProcess[str]:
        return self.run_cleaner("apply", plan["plan_sha256"], *plan["candidate_release_ids"].split(","))

    def assert_all_bundles_present(self) -> None:
        for release_id in RELEASE_IDS:
            self.assertTrue((self.releases / release_id / "artifact.tar.age").is_file())

    def inject(self, functions: str) -> None:
        self.script.write_text(self.source.replace("umask 077\n", "umask 077\n" + functions, 1), encoding="utf-8")

    def test_dry_run_does_not_create_retention_lock_or_delete(self) -> None:
        self.plan()
        self.assertFalse((self.root / ".backup-release-retention.lock").exists())
        self.assert_all_bundles_present()

    def test_busy_retention_or_promotion_lock_blocks_before_deletion(self) -> None:
        import fcntl

        plan = self.plan()
        for lock_name in (".backup-release-retention.lock", ".release-promotion.lock"):
            with self.subTest(lock=lock_name), (self.root / lock_name).open("a+b") as held:
                fcntl.flock(held.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = self.apply(plan)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("cleanup_status=completed", result.stdout)
                self.assert_all_bundles_present()

    def test_pointer_created_at_lock_acquisition_invalidates_stale_plan(self) -> None:
        plan = self.plan()
        pointer = shlex.quote(str(self.releases / "current"))
        target = shlex.quote(str(self.releases / RELEASE_IDS[0]))
        # The hook still invokes real flock. With the former late-lock order the
        # pointer appeared after planning and its target could be deleted.
        self.inject(
            "flock() {\n"
            "  if [[ ${1:-} == -n && ${2:-} == 9 ]]; then\n"
            f"    ln -s -- {target} {pointer}\n"
            "  fi\n"
            '  command flock "$@"\n'
            "}\n"
        )
        result = self.apply(plan)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("cleanup_status=completed", result.stdout)
        self.assertTrue((self.releases / "current").is_symlink())
        self.assert_all_bundles_present()

    def test_file_delete_failure_never_reports_completion(self) -> None:
        plan = self.plan()
        root = shlex.quote(str(self.root / "releases") + "/")
        self.inject(
            "rm() {\n"
            f'  if [[ ${{1:-}} == -- && ${{2:-}} == {root}* ]]; then return 73; fi\n'
            '  command rm "$@"\n'
            "}\n"
        )
        result = self.apply(plan)
        self.assertEqual(result.returncode, 73)
        self.assertNotIn("cleanup_status=completed", result.stdout)
        self.assert_all_bundles_present()

    def test_directory_delete_failure_after_file_removal_never_reports_completion(self) -> None:
        plan = self.plan()
        root = shlex.quote(str(self.root / "releases") + "/")
        self.inject(
            "rmdir() {\n"
            f'  if [[ ${{1:-}} == -- && ${{2:-}} == {root}* ]]; then return 74; fi\n'
            '  command rmdir "$@"\n'
            "}\n"
        )
        result = self.apply(plan)
        self.assertEqual(result.returncode, 74)
        self.assertNotIn("cleanup_status=completed", result.stdout)
        emptied = [self.releases / release_id for release_id in RELEASE_IDS[:2]
                   if not (self.releases / release_id / "artifact.tar.age").exists()]
        self.assertEqual(len(emptied), 1)
        self.assertTrue(emptied[0].is_dir())
        self.assertEqual(list(emptied[0].iterdir()), [])
        self.assertTrue((self.releases / RELEASE_IDS[-1] / "artifact.tar.age").is_file())

    def test_success_deletes_only_planned_bundles_and_retains_newest(self) -> None:
        plan = self.plan()
        result = self.apply(plan)
        self.assertEqual(result.returncode, 0, result.stderr)
        values = dict(line.split("=", 1) for line in result.stdout.splitlines())
        self.assertEqual(values["cleanup_status"], "completed")
        self.assertEqual(values["plan_sha256"], plan["plan_sha256"])
        self.assertEqual(values["deleted_count"], "2")
        for release_id in RELEASE_IDS[:2]:
            self.assertFalse((self.releases / release_id).exists())
        self.assertTrue((self.releases / RELEASE_IDS[-1] / "artifact.tar.age").is_file())


if __name__ == "__main__":
    unittest.main(verbosity=2)
