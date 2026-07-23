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
            "container_candidate_logical_bytes": "0",
            "image_candidates": "0",
            "image_candidate_logical_bytes": "0",
            "removed_containers": "0",
            "removed_images": "0",
            "build_cache_policy": "all_lru_maxused_1gb_reserved_1gb",
            "build_cache_records": "0",
            "build_cache_gc_attempted": "false",
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
        self.assertNotIn('docker builder prune', script)
        self.assertIn('docker buildx prune --all --force', script)
        self.assertIn('--max-used-space "$build_cache_max_used_space"', script)
        self.assertIn('--reserved-space "$build_cache_reserved_space"', script)
        self.assertIn('build_cache_max_used_space=1gb', script)
        self.assertIn('build_cache_reserved_space=1gb', script)
        self.assertNotIn('docker buildx du --filter', script)
        self.assertIn("build_cache_records=$(docker buildx du --format", script)
        self.assertNotIn('docker volume', script)
        self.assertNotIn('docker image rm -f', script)
        self.assertNotIn('dropdb', script)
        self.assertNotIn('DROP DATABASE', script)
        self.assertIn("database_size * 2 + current_image_size * 2 + 1073741824", script)
        self.assertIn("minimum_free_bytes=$((8 * 1024 * 1024 * 1024))", script)
        self.assertIn("required_bytes=$minimum_free_bytes", script)
        self.assertIn('/opt/sub2api-deploy/release-gates', script)
        self.assertIn('flock -n 9', script)
        self.assertIn('assert_no_active_build', script)
        self.assertIn('container_candidate_logical_bytes', script)
        self.assertIn('image_candidate_logical_bytes', script)
        self.assertNotIn('container_reclaimable_bytes', script)
        self.assertNotIn('image_reclaimable_bytes', script)

    def test_validator_installs_build_failure_cleanup_before_build(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        trap = validator.index("trap on_build_failure ERR INT TERM")
        build = validator.index("docker build --network=host")
        post_build_space_check = validator.index("[[ $free_after_build -gt $required_free ]]")
        self.assertLess(trap, build)
        self.assertLess(trap, post_build_space_check)
        self.assertIn("cleanup_candidate_tag", validator)

    def test_vm_entry_verifies_cleaner_checksum_before_execution(self) -> None:
        entry = (DEPLOY_ROOT / "release" / "vm_validate.py").read_text(encoding="utf-8")
        checksum_check = entry.index("space_cleaner_verified=true")
        cleanup_call = entry.index("ensure_vm_space(runner", checksum_check)
        self.assertLess(checksum_check, cleanup_call)


if __name__ == "__main__":
    unittest.main()
