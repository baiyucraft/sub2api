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

    def test_freeze_marks_state_only_after_remote_success(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.run_remote = mock.Mock(side_effect=RuntimeError("freeze failed"))
        with self.assertRaisesRegex(RuntimeError, "freeze failed"):
            release.freeze()
        self.assertFalse(release.frozen)
        self.assertFalse(release.units_masked)

    def test_recovery_detects_committed_remote_freeze(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.remote_writes_frozen = mock.Mock(return_value=True)
        release.run_remote.side_effect = [
            {"old_application_resumed": "true", "running_image_id": "old"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ]
        release.recover()
        self.assertIn("resume-old.sh", release.run_remote.call_args_list[0].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_remote_freeze_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_writes_frozen())

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
        release.remote_writes_frozen = mock.Mock(return_value=False)
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

    def test_context_reads_release_id_in_prepared_format(self) -> None:
        context = self.script("context.sh")
        self.assertIn('grep -Fxq "release_id=$release_id" "$active_claim/release_id"', context)

    def test_cleanup_supports_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("if [[ -d $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn("[[ ! -e $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn('pre-migrations.tsv', cleanup)
        self.assertIn('SELECT filename,checksum FROM schema_migrations ORDER BY filename', cleanup)
        self.assertIn("systemctl is-enabled sub2api-backup.timer", cleanup)

    def test_preflight_accepts_absent_or_matching_migration_only(self) -> None:
        preflight = self.script("preflight.sh")
        self.assertIn("migration_status=absent", preflight)
        self.assertIn("migration_status=verified", preflight)
        self.assertIn('[[ $migration_state == "$migration|$migration_checksum" ]]', preflight)

    def test_freeze_creates_release_state_root(self) -> None:
        freeze = self.script("freeze-backup.sh")
        self.assertIn("install -d -m 700 /opt/sub2api/backups/release-state", freeze)
        self.assertIn("docker compose stop -t 30 sub2api >/dev/null 2>&1", self.script("freeze.sh"))

    def test_backup_reads_redis_requirepass_without_cli_secret(self) -> None:
        backup = self.script("backup.sh")
        self.assertIn('index("--requirepass")', backup)
        self.assertIn('printf \'%s\\n\' "$redis_password" | docker exec -i', backup)
        self.assertNotIn("redis-cli -a", backup)
        self.assertIn("docker compose stop -t 30 redis >/dev/null 2>&1", backup)
        self.assertIn("docker compose start redis >/dev/null 2>&1", backup)

    def test_racknerd_verifier_does_not_hairpin_through_dmit(self) -> None:
        verify = self.script("verify.sh")
        finalize = self.script("finalize.sh")
        self.assertNotIn("DMIT_IP", verify)
        self.assertNotIn("DMIT_IP", finalize)
        self.assertNotIn("dmit_health", verify)

    def test_route_canary_reads_secret_from_stdin(self) -> None:
        script = self.script("route-canary.sh")
        self.assertIn("IFS= read -r api_key", script)
        self.assertNotIn("CANARY_KEY_FILE", script)
        self.assertIn("ROUTE_IP", script)

    def test_cleanup_handles_backup_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("sha256sum -c SHA256SUMS", cleanup)
        self.assertIn('rm -rf -- "$state_dir"', cleanup)
        self.assertIn("restored.committed", cleanup)

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
