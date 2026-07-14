from __future__ import annotations

import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.vm_validate import SPACE_FIELDS, ensure_vm_space


class Result:
    def __init__(self, values: dict[str, str]):
        self.values = values


def report(status: str, mode: str = "dry-run") -> Result:
    return Result(
        {
            "cleanup_mode": mode,
            "space_status": status,
            "free_bytes": "1",
            "required_bytes": "2",
            "container_candidates": "0",
            "container_reclaimable_bytes": "0",
            "image_candidates": "0",
            "image_reclaimable_bytes": "0",
            "removed_containers": "0",
            "removed_images": "0",
        }
    )


class FakeRunner:
    def __init__(self, results: list[Result]):
        self.results = results
        self.calls: list[tuple[str, str, set[str], int]] = []

    def run(self, host: str, command: str, fields: set[str], timeout: int = 120) -> Result:
        self.calls.append((host, command, fields, timeout))
        return self.results.pop(0)


class VMSpaceCleanTest(unittest.TestCase):
    def test_sufficient_space_only_runs_dry_run(self) -> None:
        runner = FakeRunner([report("sufficient")])
        ensure_vm_space(runner, "/tmp/vm-space-clean.sh", "a" * 40)
        self.assertEqual(len(runner.calls), 1)
        self.assertIn(" dry-run ", runner.calls[0][1])
        self.assertEqual(runner.calls[0][2], SPACE_FIELDS)

    def test_insufficient_space_applies_once_then_rechecks_once(self) -> None:
        runner = FakeRunner([report("insufficient"), report("insufficient", "apply"), report("sufficient")])
        ensure_vm_space(runner, "/tmp/vm-space-clean.sh", "a" * 40)
        self.assertEqual(len(runner.calls), 3)
        self.assertIn(" dry-run ", runner.calls[0][1])
        self.assertIn(" apply ", runner.calls[1][1])
        self.assertIn(" dry-run ", runner.calls[2][1])

    def test_cleanup_does_not_loop_when_space_remains_insufficient(self) -> None:
        runner = FakeRunner([report("insufficient"), report("insufficient", "apply"), report("insufficient")])
        with self.assertRaisesRegex(RuntimeError, "remains insufficient"):
            ensure_vm_space(runner, "/tmp/vm-space-clean.sh", "a" * 40)
        self.assertEqual(len(runner.calls), 3)

    def test_shell_limits_destructive_operations_to_allowlist(self) -> None:
        script = (DEPLOY_ROOT / "release" / "vm-space-clean.sh").read_text(encoding="utf-8")
        self.assertIn("--filter status=exited", script)
        self.assertIn("^sub2api-dev-pre", script)
        self.assertIn("^sub2api:.+-([0-9a-f]{40})$", script)
        self.assertIn("grep -Fxq \"$image_id\" \"$work_dir/referenced-images-now\"", script)
        self.assertIn('docker rm "$container_id"', script)
        self.assertNotIn('docker rm -v', script)
        self.assertNotIn('docker rm --volumes', script)
        self.assertNotIn('docker image prune', script)
        self.assertNotIn('docker system prune', script)
        self.assertNotIn('docker volume', script)
        self.assertNotIn('docker image rm -f', script)
        self.assertNotIn('dropdb', script)
        self.assertNotIn('DROP DATABASE', script)
        self.assertIn("database_size * 2 + current_image_size * 2 + 1073741824", script)

    def test_vm_entry_verifies_cleaner_checksum_before_execution(self) -> None:
        entry = (DEPLOY_ROOT / "release" / "vm_validate.py").read_text(encoding="utf-8")
        checksum_check = entry.index("space_cleaner_verified=true")
        cleanup_call = entry.index("ensure_vm_space(runner", checksum_check)
        self.assertLess(checksum_check, cleanup_call)


if __name__ == "__main__":
    unittest.main()
