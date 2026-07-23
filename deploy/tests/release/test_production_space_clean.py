from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.production_cleanup import (
    PRODUCTION_CLEAN_FIELDS,
    CleanupIdentity,
    cleanup_production,
    load_cleanup_identity,
    validate_cleanup_evidence,
)


class Result:
    def __init__(self, values: dict[str, str]):
        self.values = values


def cleanup_report(release_id: str = "199-aaaaaaaaaaaa-1-deadbeef") -> dict[str, str]:
    return {
        "cleanup_mode": "dry-run",
        "cleanup_status": "ready",
        "plan_sha256": "d" * 64,
        "release_id": release_id,
        "current_image_id": "sha256:" + "a" * 64,
        "pre_switch_image_id": "sha256:" + "b" * 64,
        "root_free_before_bytes": "100",
        "root_free_after_bytes": "100",
        "root_free_delta_bytes": "0",
        "containerd_free_before_bytes": "100",
        "containerd_free_after_bytes": "100",
        "containerd_free_delta_bytes": "0",
        "migration_evidence_containers": "5",
        "image_candidates": "30",
        "image_candidates_after": "30",
        "image_candidate_logical_bytes": "123",
        "removed_images": "0",
        "build_cache_records_before": "10",
        "build_cache_records_after": "10",
        "build_cache_policy": "all_lru_maxused_2gb_reserved_2gb",
        "build_cache_gc_attempted": "false",
    }


class FakeRunner:
    def __init__(self, cleanup_values: dict[str, str]):
        self.cleanup_values = cleanup_values
        self.calls: list[tuple[str, str, set[str], int]] = []
        self.uploads: list[tuple[str, Path, str, int]] = []

    def create_temp_dir(self, host: str, base: str, prefix: str) -> str:
        self.calls.append((host, f"create:{base}:{prefix}", set(), 0))
        return "/tmp/production-clean.abcdefgh"

    def upload_file(self, host: str, local_path: Path, remote_path: str, mode: int) -> None:
        self.uploads.append((host, local_path, remote_path, mode))

    def run(self, host: str, command: str, fields: set[str], timeout: int = 120) -> Result:
        self.calls.append((host, command, fields, timeout))
        if fields == {"cleaner_verified"}:
            return Result({"cleaner_verified": "true"})
        if fields == {"cleanup"}:
            return Result({"cleanup": "true"})
        if fields == PRODUCTION_CLEAN_FIELDS:
            return Result(self.cleanup_values)
        raise AssertionError(f"unexpected fields: {fields}")


class ProductionSpaceCleanTest(unittest.TestCase):
    def write_release(self, root: Path, *, pre_switch: str = "sha256:" + "b" * 64, consumed: bool = True) -> str:
        release_id = "199-aaaaaaaaaaaa-1-deadbeef"
        gate_dir = root / release_id / "gate"
        gate_dir.mkdir(parents=True)
        (gate_dir / "gate.json").write_text(
            json.dumps(
                {
                    "manifest": {"release_id": release_id},
                    "evidence": {"candidate_image_id": "sha256:" + "a" * 64},
                }
            ),
            encoding="utf-8",
        )
        (gate_dir / "gate.sig").write_bytes(b"signature")
        (gate_dir / "production-result.json").write_text(
            json.dumps(
                {
                    "release_id": release_id,
                    "status": "verified",
                    "stage": "production_verified",
                    "history": [
                        {"stage": "production_preflight_verified", "evidence": {"pre_switch_image_id": pre_switch}},
                        {
                            "stage": "production_verified",
                            "evidence": {"gate_consumed": "true" if consumed else "false"},
                        },
                    ],
                }
            ),
            encoding="utf-8",
        )
        return release_id

    def test_identity_requires_signed_terminal_consumed_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release_id = self.write_release(root)
            with mock.patch("release.production_cleanup.subprocess.run") as verify:
                identity = load_cleanup_identity(release_id, root)
        verify.assert_called_once()
        self.assertEqual(identity.current_image_id, "sha256:" + "a" * 64)
        self.assertEqual(identity.pre_switch_image_id, "sha256:" + "b" * 64)

    def test_identity_rejects_unconsumed_release(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release_id = self.write_release(root, consumed=False)
            with mock.patch("release.production_cleanup.subprocess.run"):
                with self.assertRaisesRegex(RuntimeError, "Gate was consumed"):
                    load_cleanup_identity(release_id, root)

    def test_identity_rejects_ambiguous_pre_switch_image(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release_id = self.write_release(root)
            result_path = root / release_id / "gate" / "production-result.json"
            result = json.loads(result_path.read_text(encoding="utf-8"))
            result["history"].append(
                {
                    "stage": "production_verified",
                    "evidence": {"pre_switch_image_id": "sha256:" + "c" * 64},
                }
            )
            result_path.write_text(json.dumps(result), encoding="utf-8")
            with mock.patch("release.production_cleanup.subprocess.run"):
                with self.assertRaisesRegex(RuntimeError, "unambiguous pre-switch"):
                    load_cleanup_identity(release_id, root)

    def test_cleanup_passes_signed_release_identity_to_remote_script(self) -> None:
        identity = CleanupIdentity(
            "199-aaaaaaaaaaaa-1-deadbeef",
            "sha256:" + "a" * 64,
            "sha256:" + "b" * 64,
        )
        runner = FakeRunner(cleanup_report())
        with mock.patch("release.production_cleanup.load_cleanup_identity", return_value=identity), mock.patch(
            "release.production_cleanup.RunLock"
        ):
            values = cleanup_production(identity.release_id, "dry-run", runner=runner)
        cleanup_calls = [call for call in runner.calls if call[2] == PRODUCTION_CLEAN_FIELDS]
        self.assertEqual(len(cleanup_calls), 1)
        self.assertIn(f" dry-run {identity.release_id} {identity.current_image_id} {identity.pre_switch_image_id}", cleanup_calls[0][1])
        self.assertEqual(cleanup_calls[0][3], 1200)
        self.assertEqual(values["image_candidates"], "30")
        self.assertEqual(len(runner.uploads), 1)

    def test_cleanup_rejects_remote_identity_mismatch(self) -> None:
        identity = CleanupIdentity(
            "199-aaaaaaaaaaaa-1-deadbeef",
            "sha256:" + "a" * 64,
            "sha256:" + "b" * 64,
        )
        runner = FakeRunner(cleanup_report("199-bbbbbbbbbbbb-2-feedface"))
        with mock.patch("release.production_cleanup.load_cleanup_identity", return_value=identity), mock.patch(
            "release.production_cleanup.RunLock"
        ):
            with self.assertRaisesRegex(RuntimeError, "different protected identity"):
                cleanup_production(identity.release_id, "dry-run", runner=runner)

    def test_cleanup_evidence_rejects_inconsistent_filesystem_delta(self) -> None:
        identity = CleanupIdentity(
            "199-aaaaaaaaaaaa-1-deadbeef",
            "sha256:" + "a" * 64,
            "sha256:" + "b" * 64,
        )
        values = cleanup_report()
        values["root_free_after_bytes"] = "101"
        with self.assertRaisesRegex(RuntimeError, "root filesystem delta"):
            validate_cleanup_evidence(values, identity, "dry-run", None)

    def test_cleanup_evidence_requires_apply_to_remove_all_candidates(self) -> None:
        identity = CleanupIdentity(
            "199-aaaaaaaaaaaa-1-deadbeef",
            "sha256:" + "a" * 64,
            "sha256:" + "b" * 64,
        )
        values = cleanup_report()
        values.update(
            {
                "cleanup_mode": "apply",
                "cleanup_status": "completed",
                "removed_images": "29",
                "image_candidates_after": "1",
                "build_cache_gc_attempted": "true",
            }
        )
        with self.assertRaisesRegex(RuntimeError, "empty image candidate set"):
            validate_cleanup_evidence(values, identity, "apply", values["plan_sha256"])

    def test_apply_requires_exact_dry_run_plan(self) -> None:
        with self.assertRaisesRegex(ValueError, "exact dry-run plan"):
            cleanup_production("199-aaaaaaaaaaaa-1-deadbeef", "apply")

    def test_shell_has_narrow_destructive_allowlist(self) -> None:
        script = (DEPLOY_ROOT / "release" / "production-space-clean.sh").read_text(encoding="utf-8")
        prepare = (DEPLOY_ROOT / "maintenance" / "release" / "prepare.sh").read_text(encoding="utf-8")
        self.assertIn("/opt/sub2api/releases/.active-release", script)
        self.assertIn("assert_release_identity", script)
        self.assertIn("assert_release_marker", script)
        self.assertIn('grep -Fxq "candidate_image_id=$expected_current_image" "$consumed_dir/marker"', script)
        self.assertIn('[[ $(tr -d \'\\r\\n\' < "$release_state_dir/pre-image-id") == "$pre_switch_image" ]]', script)
        self.assertIn("write_container_images", script)
        self.assertIn("write_protected_images", script)
        self.assertIn("pre-image-id", script)
        self.assertIn('[[ ! -L $release_state ]]', script)
        self.assertIn('[[ -f $path && ! -L $path ]]', script)
        self.assertNotIn('-name pre-image-id -type f', script)
        self.assertIn('docker image rm "${tags[@]}"', script)
        self.assertIn("migration_evidence_containers", script)
        self.assertIn("image_candidate_logical_bytes", script)
        self.assertIn("expected_plan_sha256", script)
        self.assertIn("plan_sha256", script)
        self.assertNotIn("image_reclaimable_bytes", script)
        self.assertIn("root_free_delta_bytes", script)
        self.assertIn("containerd_free_delta_bytes", script)
        self.assertIn("/run/lock/sub2api-production-release.lock", script)
        self.assertIn("/run/lock/sub2api-production-release.lock", prepare)
        self.assertIn("/run/lock/sub2api-backup-global.lock", script)
        self.assertIn("sub2api-backup.service", script)
        self.assertIn("^sub2api:.+-[0-9a-f]{40}$", script)
        self.assertNotIn("docker rm", script)
        self.assertNotIn("docker image prune", script)
        self.assertNotIn("docker system prune", script)
        self.assertNotIn("docker volume", script)
        self.assertNotIn("rm -rf /var/lib/containerd", script)
        self.assertIn("docker buildx prune --all --force", script)
        self.assertIn('--max-used-space "$build_cache_max_used_space"', script)
        self.assertIn('--reserved-space "$build_cache_reserved_space"', script)


if __name__ == "__main__":
    unittest.main()
