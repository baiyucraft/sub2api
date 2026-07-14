from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.production import ProductionRelease
from release.production import quoted_env


class ProductionRecoveryTest(unittest.TestCase):
    def test_quoted_env_quotes_shell_metacharacters(self) -> None:
        self.assertEqual(quoted_env({"VALUE": "a b;$(x)"}), "VALUE='a b;$(x)'")

    def release(self) -> ProductionRelease:
        instance = object.__new__(ProductionRelease)
        instance.frozen = True
        instance.units_masked = True
        instance.mask_intent = False
        instance.public_exposed = False
        instance.migration_started = False
        instance.state_dir = "/state"
        instance.release_dir = "/release"
        instance.active_assets = "/active/assets"
        instance.result = {"status": "running", "history": []}
        instance.stage = mock.Mock()
        instance.run_remote = mock.Mock(side_effect=[
            {"old_application_resumed": "true", "running_image_id": "old"},
            {"backup_units_restored": "true"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ])
        return instance

    def test_pre_migration_failure_resumes_old_application(self) -> None:
        release = self.release()
        release.recover()
        first_script = release.run_remote.call_args_list[0].args[1]
        self.assertIn("resume-old.sh", first_script)
        self.assertNotIn("restore.sh", first_script)
        self.assertEqual(release.result["status"], "recovered")

    def test_post_migration_failure_runs_coordinated_restore(self) -> None:
        release = self.release()
        release.migration_started = True
        release.run_remote.side_effect = [
            {"coordinated_restore": "verified", "restored_image_id": "old", "application_health": "pass"},
            {"backup_units_restored": "true"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ]
        release.recover()
        first_script = release.run_remote.call_args_list[0].args[1]
        self.assertIn("/restore.sh", first_script)
        self.assertNotIn("resume-old.sh", first_script)
        self.assertEqual(release.result["status"], "recovered")

    def test_reconcile_lost_reply_checks_committed_recovery(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.run_remote = mock.Mock(side_effect=[{"plaintext_state_removed": "true"}, RuntimeError("reply lost"), {"release_claim_reconciled": "true"}])
        release.recover()
        self.assertIn(".recovered/marker", release.run_remote.call_args_list[2].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_public_exposure_failure_never_calls_snapshot_recovery(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.emergency_close = mock.Mock()
        release.recover = mock.Mock()
        release.upload_assets = mock.Mock(side_effect=RuntimeError("canary failed"))
        with self.assertRaisesRegex(RuntimeError, "canary failed"):
            release.execute()
        release.emergency_close.assert_called_once()
        release.recover.assert_not_called()
        self.assertEqual(release.result["status"], "blocked_reconciliation")

    def test_remote_claim_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_claimed": "true"})
        self.assertTrue(release.remote_gate_claimed())
        script = release.run_remote.call_args.args[1]
        self.assertIn(".active-release/release_id", script)
        self.assertNotIn(".claimed", script)

    def test_remote_claim_probe_failure_does_not_guess(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_gate_claimed())

    def test_remote_claim_probe_reports_explicit_absence(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_claimed": "false"})
        self.assertFalse(release.remote_gate_claimed())
        self.assertIn("gate_claimed=false", release.run_remote.call_args.args[1])

    def test_active_claim_probe_detects_incomplete_claim(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"active_claim": "true"})
        self.assertTrue(release.remote_active_claim_exists())

    def test_active_claim_probe_failure_does_not_guess(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_active_claim_exists())

    def test_active_claim_probe_reports_explicit_absence(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"active_claim": "false"})
        self.assertFalse(release.remote_active_claim_exists())
        self.assertIn("active_claim=false", release.run_remote.call_args.args[1])

    def test_consumed_probe_requires_healthy_candidate(self) -> None:
        release = self.release()
        release.image_id = "sha256:" + "a" * 64
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_consumed": "true"})
        self.assertTrue(release.remote_gate_consumed())
        script = release.run_remote.call_args.args[1]
        self.assertIn(".State.Health.Status", script)
        self.assertIn("= healthy", script)

    def test_mask_probe_detects_committed_remote_mask(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"units_masked": "true"})
        self.assertTrue(release.remote_units_masked())

    def test_mask_probe_failure_is_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("reply lost"))
        self.assertIsNone(release.remote_units_masked())


class ReleaseClaimScriptTest(unittest.TestCase):
    def script(self, name: str) -> str:
        return (DEPLOY_ROOT / "maintenance" / "release" / name).read_text(encoding="utf-8")

    def test_prepare_rejects_linked_candidate_and_copies_assets(self) -> None:
        script = self.script("prepare.sh")
        self.assertIn("! -L $release_dir/candidate.tar.gz", script)
        self.assertIn("stat -c '%h' \"$release_dir/candidate.tar.gz\"", script)
        self.assertIn("install -m 500 \"$path\"", script)
        self.assertNotIn("$release_dir/.claimed", script)

    def test_consume_atomically_commits_active_claim(self) -> None:
        script = self.script("consume.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.consumed"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)

    def test_reconcile_atomically_commits_active_claim(self) -> None:
        script = self.script("reconcile.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.recovered"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)


if __name__ == "__main__":
    unittest.main()
