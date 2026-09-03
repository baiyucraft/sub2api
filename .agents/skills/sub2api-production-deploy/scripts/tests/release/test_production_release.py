from __future__ import annotations

import os
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.paths import WORKSPACE

from release.production import ProductionRelease, write_stage_bundle
from release.production import emit_progress
from release.production import gate_consumption_probe_script
from release.production import quoted_env


class ProductionRecoveryTest(unittest.TestCase):
    def test_stage_bundle_is_deterministic_and_contains_checksum_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "asset.sh"
            source.write_text("#!/usr/bin/env bash\nprintf 'ok=true\n'\n", encoding="utf-8")
            first = root / "first.tar"
            second = root / "second.tar"
            first_sha = write_stage_bundle(first, {"assets/asset.sh": source})
            second_sha = write_stage_bundle(second, {"assets/asset.sh": source})
            self.assertEqual(first_sha, second_sha)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first, "r") as archive:
                self.assertEqual(archive.getnames(), ["assets/asset.sh", "ASSET_SHA256SUMS"])
                self.assertIn(b"assets/asset.sh", archive.extractfile("ASSET_SHA256SUMS").read())
    def test_container_healthcheck_uses_ipv4_loopback_for_release_bind(self) -> None:
        dockerfile = (WORKSPACE / "Dockerfile").read_text(encoding="utf-8")

        self.assertIn("http://127.0.0.1:${SERVER_PORT:-8080}/health", dockerfile)
        self.assertNotIn("http://localhost:${SERVER_PORT:-8080}/health", dockerfile)

    def test_frontend_bootstrap_pins_pnpm_and_corepack_registry(self) -> None:
        dockerfile = (WORKSPACE / "Dockerfile").read_text(encoding="utf-8")

        self.assertIn("ARG COREPACK_NPM_REGISTRY=https://registry.npmmirror.com", dockerfile)
        self.assertIn("ENV COREPACK_NPM_REGISTRY=${COREPACK_NPM_REGISTRY}", dockerfile)
        self.assertIn("corepack prepare pnpm@9.15.9 --activate", dockerfile)
        self.assertNotIn("corepack prepare pnpm@9 --activate", dockerfile)
        self.assertEqual(dockerfile.count("    -p 1 \\\n"), 3)
        self.assertEqual(dockerfile.count("GOMAXPROCS=1 GOMEMLIMIT=1GiB GOGC=50"), 3)

    def test_migration_232_postflight_hashes_backup_for_new_migration(self) -> None:
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-232-assert.sh").read_text(encoding="utf-8")

        self.assertIn(
            "if [[ $migration_status == verified || $phase == postflight ]]; then",
            assertion,
        )
        self.assertIn("source_relation=groups_video_price_backup_232", assertion)
        self.assertIn('[[ $backup_hash == "$expected_plan" ]]', assertion)

    def test_migration_233_blocks_duplicates_and_verifies_partial_unique_index(self) -> None:
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-233-assert.sh").read_text(encoding="utf-8")
        self.assertIn("HAVING COUNT(*) > 1", assertion)
        self.assertIn("idx_accounts_upstream_key_id_active", assertion)
        self.assertIn("regexp_replace(pg_get_expr(i.indpred,i.indrelid),'[()[:space:]]','','g')", assertion)
        self.assertIn("upstream_key_idISNOTNULLANDdeleted_atISNULL", assertion)
        self.assertNotIn("pg_get_expr(i.indpred,i.indrelid)='(upstream_key_id IS NOT NULL) AND (deleted_at IS NULL)'", assertion)
        self.assertIn("migration_233_preflight=pass", assertion)
        self.assertIn("migration_233_postflight=pass", assertion)

    def test_migration_233_verifies_health_observation_table_columns_index_and_trigger(self) -> None:
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-233-assert.sh").read_text(encoding="utf-8")
        self.assertIn("upstream_health_observations", assertion)
        for column in (
            "upstream_config_id", "upstream_key_id", "account_id", "platform", "model", "protocol",
            "source", "state", "result", "reason", "http_status", "ttft_ms", "duration_ms",
            "input_tokens", "output_tokens", "output_tps", "observed_at", "created_at",
        ):
            self.assertIn(column, assertion)
        self.assertIn("idx_upstream_health_observations_key_observed", assertion)
        self.assertIn("indisvalid", assertion)
        self.assertIn("indisready", assertion)
        self.assertIn("upstream_key_id, observed_at", assertion)
        self.assertIn("migration_233_table_state=absent", assertion)
        self.assertIn("migration_233_trigger_verified=true", assertion)
        self.assertIn("NEW.load_factor :=", assertion)
        self.assertIn("migration_233_preflight=pass", assertion)
        self.assertIn("migration_233_postflight=pass", assertion)

    def test_migration_227_resets_the_version_two_factory_row(self) -> None:
        migration = (WORKSPACE / "backend" / "migrations" / "227_channel_monitor_v2_reset_factory_cache_thresholds.sql").read_text(
            encoding="utf-8"
        )

        self.assertIn("AND version IN (1, 2)", migration)
        self.assertNotIn("AND version = 1", migration)

    def test_progress_output_failure_is_non_fatal(self) -> None:
        with mock.patch("builtins.print", side_effect=BrokenPipeError):
            emit_progress("stage=freeze")

    def test_quoted_env_quotes_shell_metacharacters(self) -> None:
        self.assertEqual(quoted_env({"VALUE": "a b;$(x)"}), "VALUE='a b;$(x)'")

    def test_remote_raw_log_capture_is_root_only_and_keeps_stderr_out_of_protocol(self) -> None:
        release = object.__new__(ProductionRelease)
        release.release_dir = "/opt/sub2api/releases/235-aaaaaaaaaaaa-1-aaaaaaaa"
        release.result = {"stage": "backup"}
        release._remote_log_sequence = 0

        wrapped = release._wrap_remote_logging("printf 'ok=true\\n'")

        self.assertIn("logs/production.raw.log", wrapped)
        self.assertIn("install -d -m 700", wrapped)
        self.assertIn("root:root:700", wrapped)
        self.assertIn("root:root:600:1", wrapped)
        self.assertIn("stderr is diagnostic output, not part of the structured machine protocol", wrapped)
        self.assertNotIn("code=97", wrapped)
        self.assertIn('cat "$stderr_tmp"', wrapped)
        self.assertIn("stage=backup", wrapped)

    def test_remote_raw_log_capture_rejects_unsafe_stage_name(self) -> None:
        release = object.__new__(ProductionRelease)
        release.release_dir = "/opt/sub2api/releases/235-aaaaaaaaaaaa-1-aaaaaaaa"
        release.result = {"stage": "backup;touch"}
        release._remote_log_sequence = 0

        with self.assertRaisesRegex(RuntimeError, "not safe"):
            release._wrap_remote_logging("true")

    def release(self) -> ProductionRelease:
        instance = object.__new__(ProductionRelease)
        instance.frozen = True
        instance.units_masked = True
        instance.mask_intent = False
        instance.public_exposed = False
        instance.route_switch_attempted = False
        instance.route_switched = False
        instance.migration_started = False
        instance.state_dir = "/state"
        instance.profile = {"public_domain": "example.test", "rack_public_ip": "192.0.2.1"}
        instance.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        instance.release_dir = "/release"
        instance.image_id = "sha256:" + "a" * 64
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
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=True)
        release.recover()
        first_script = next(call.args[1] for call in release.run_remote.call_args_list if "resume-old.sh" in call.args[1])
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

    def test_freeze_checkpoints_scheduler_outbox_without_stopping_traffic(self) -> None:
        freeze = (DEPLOY_ROOT / "maintenance" / "release" / "freeze.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")

        self.assertIn("sched:v2:outbox:watermark", freeze)
        self.assertNotIn("systemctl stop nginx", freeze)
        self.assertNotIn("docker compose stop -t 30 sub2api", freeze)
        self.assertIn("traffic_preserved=true", freeze)
        self.assertIn("outbox_checkpoint=true", freeze)
        self.assertIn("drain_deadline=$((SECONDS + 30))", freeze)
        self.assertIn('"outbox_checkpoint"', production)

    def test_backup_rejects_unready_local_restore_point(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.frozen = False
        release.units_masked = False
        release.run_remote = mock.Mock(return_value={
            "backup_units_masked": "true",
            "writes_frozen": "true",
            "state_dir": "/state",
            "pre_switch_image_id": "old",
            "compose_sha256": "digest",
            "artifact": "artifact",
            "transport_artifact": "transport",
            "artifact_size": "1",
            "artifact_sha256": "digest",
            "no_restart_path_proven": "true",
            "local_restore_point_ready": "false",
        })

        with self.assertRaisesRegex(RuntimeError, "local coordinated restore point is not ready"):
            release.backup()

        self.assertFalse(release.frozen)
        self.assertFalse(release.units_masked)

    def test_backup_promotion_retries_with_bounded_backoff(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file = mock.Mock()
        release.run_remote = mock.Mock(side_effect=[
            {
                "artifact": "artifact",
                "transport_artifact": "transport",
                "artifact_size": "1",
                "artifact_sha256": "digest",
                "traffic_preserved": "true",
                "redis_backup_mode": "rdb",
                "no_restart_path_proven": "true",
                "local_restore_point_ready": "true",
            },
            RuntimeError("artifact visibility pending"),
            RuntimeError("artifact visibility pending"),
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])
        with mock.patch.object(time, "sleep") as sleep:
            release.backup()
        self.assertEqual(sleep.call_args_list, [mock.call(5), mock.call(15)])
        self.assertTrue(release.stage.called)
        self.assertEqual(release.stage.call_args_list[-1].args[0], "backup_verified")

    def test_backup_promotion_retries_temp_directory_creation(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.side_effect = [
            RuntimeError("temporary SSH failure"),
            "/tmp/release-promote.test",
        ]
        release.run_remote = mock.Mock(side_effect=[
            {
                "artifact": "artifact", "transport_artifact": "transport", "artifact_size": "1",
                "artifact_sha256": "digest", "traffic_preserved": "true", "redis_backup_mode": "rdb",
                "no_restart_path_proven": "true", "local_restore_point_ready": "true",
            },
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])

        with mock.patch.object(time, "sleep") as sleep:
            release.backup()

        self.assertEqual(sleep.call_args_list, [mock.call(5)])
        self.assertEqual(release.runner.create_temp_dir.call_count, 2)
        release.runner.upload_file.assert_called_once()
        self.assertEqual(release.stage.call_args_list[-1].args[0], "backup_verified")

    def test_backup_promotion_retries_script_upload_in_same_temp_directory(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file.side_effect = [RuntimeError("temporary SFTP failure"), None]
        release.run_remote = mock.Mock(side_effect=[
            {
                "artifact": "artifact", "transport_artifact": "transport", "artifact_size": "1",
                "artifact_sha256": "digest", "traffic_preserved": "true", "redis_backup_mode": "rdb",
                "no_restart_path_proven": "true", "local_restore_point_ready": "true",
            },
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])

        with mock.patch.object(time, "sleep") as sleep:
            release.backup()

        self.assertEqual(sleep.call_args_list, [mock.call(5)])
        release.runner.create_temp_dir.assert_called_once()
        self.assertEqual(release.runner.upload_file.call_count, 2)
        self.assertEqual(release.stage.call_args_list[-1].args[0], "backup_verified")

    def test_stage_bundle_upload_retries_before_remote_claim(self) -> None:
        release = self.release()
        with tempfile.TemporaryDirectory() as directory:
            release.gate_dir = Path(directory)
            (release.gate_dir / "gate.json").write_text("{}", encoding="utf-8")
            (release.gate_dir / "gate.sig").write_bytes(b"sig")
            (release.gate_dir / "candidate.tar.gz").write_bytes(b"candidate")
            release.evidence = {"candidate_archive_sha256": "digest"}
            release.manifest = {}
            release.runner = mock.Mock()
            release.runner.create_temp_dir.return_value = "/opt/sub2api/releases/.stage-test"
            release.runner.upload_file.side_effect = [RuntimeError("temporary SFTP failure"), None]
            release.run_remote = mock.Mock(side_effect=[
                {"trust_key_verified": "true"},
                {"release_directory_created": "true"},
                {
                    "prepared": "true",
                    "candidate_image_id": release.image_id,
                    "candidate_archive_sha256": "digest",
                    "trust_key_sha256": "trust",
                },
            ])

            with mock.patch.object(time, "sleep") as sleep:
                release.upload_assets()

            self.assertEqual(sleep.call_args_list, [mock.call(5)])
            self.assertEqual(release.runner.upload_file.call_count, 2)
            self.assertTrue(release.claimed)

    def test_backup_recovers_committed_result_after_lost_remote_reply(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file = mock.Mock()
        complete = {
            "artifact": "artifact", "transport_artifact": "transport", "artifact_size": "1",
            "artifact_sha256": "digest", "traffic_preserved": "true", "redis_backup_mode": "rdb",
            "no_restart_path_proven": "true", "local_restore_point_ready": "true",
        }
        release.run_remote = mock.Mock(side_effect=[
            RuntimeError("remote reply lost"),
            complete,
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])

        release.backup()

        self.assertEqual(release.stage.call_args_list[1].args[0], "backup_result_reconciled")
        reconcile_script = release.run_remote.call_args_list[1].args[1]
        self.assertIn("sha256sum -c backup-result.sha256", reconcile_script)
        self.assertIn("grep -c '^[a-z_][a-z0-9_]*='", reconcile_script)

    def test_backup_waits_for_committed_result_after_lost_remote_reply(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file = mock.Mock()
        complete = {
            "artifact": "artifact", "transport_artifact": "transport", "artifact_size": "1",
            "artifact_sha256": "digest", "traffic_preserved": "true", "redis_backup_mode": "rdb",
            "no_restart_path_proven": "true", "local_restore_point_ready": "true",
        }
        release.run_remote = mock.Mock(side_effect=[
            RuntimeError("remote reply lost"),
            RuntimeError("result not visible yet"),
            {"backup_failure_stage": "absent", "backup_failure_exit_code": "absent"},
            complete,
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])

        with mock.patch.object(time, "sleep") as sleep:
            release.backup()

        self.assertEqual(sleep.call_args_list, [mock.call(2)])
        self.assertEqual(release.stage.call_args_list[1].args[0], "backup_result_reconciled")

    def test_backup_does_not_reconcile_uncommitted_result(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.run_remote = mock.Mock(side_effect=[RuntimeError("backup failed"), RuntimeError("result absent")])

        with mock.patch.object(time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "backup failed"):
                release.backup()

    def test_backup_reports_preserved_failure_stage_when_result_missing(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.run_remote = mock.Mock(side_effect=[
            RuntimeError("backup failed"), RuntimeError("result absent"),
            {"backup_failure_stage": "upload", "backup_failure_exit_code": "1"},
        ] * 3)

        with mock.patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "stage=upload exit_code=1"):
                release.backup()

        self.assertEqual(sleep.call_args_list, [mock.call(5), mock.call(15)])
        failure_stages = [call for call in release.stage.call_args_list if call.args[0] == "backup_generation_failed"]
        self.assertEqual(len(failure_stages), 3)
        self.assertEqual(failure_stages[0].args[1]["backup_failure_stage"], "upload")

    def test_backup_retries_only_explicit_upload_failure_with_new_generation(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file = mock.Mock()
        complete = {
            "artifact": "artifact", "transport_artifact": "transport", "artifact_size": "1",
            "artifact_sha256": "digest", "traffic_preserved": "true", "redis_backup_mode": "rdb",
            "no_restart_path_proven": "true", "local_restore_point_ready": "true",
        }
        release.run_remote = mock.Mock(side_effect=[
            RuntimeError("backup failed"), RuntimeError("result absent"),
            {"backup_failure_stage": "upload", "backup_failure_exit_code": "255"},
            complete,
            {"backup_promotion": "verified", "release_artifact": release.release_id, "release_sha256": "digest", "release_free_bytes": "2"},
            {"cleanup": "true"},
        ])

        with mock.patch.object(time, "sleep") as sleep:
            release.backup()

        self.assertEqual(sleep.call_args_list, [mock.call(5)])
        generation_calls = [call for call in release.run_remote.call_args_list if call.args[1].endswith("/backup.sh")]
        self.assertEqual(len(generation_calls), 2)
        self.assertIn("BACKUP_ATTEMPT_ID=", generation_calls[0].args[1])
        self.assertNotEqual(generation_calls[0].args[1], generation_calls[1].args[1])
        failure_stage = [call for call in release.stage.call_args_list if call.args[0] == "backup_generation_failed"]
        self.assertEqual(failure_stage[0].args[1]["backup_generation_attempt"], "1")

    def test_backup_does_not_retry_non_upload_generation_failure(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.run_remote = mock.Mock(side_effect=[
            RuntimeError("backup failed"), RuntimeError("result absent"),
            {"backup_failure_stage": "redis", "backup_failure_exit_code": "1"},
        ])

        with mock.patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(RuntimeError, "stage=redis exit_code=1"):
                release.backup()

        sleep.assert_not_called()

    def test_backup_script_binds_failure_marker_to_generation_attempt(self) -> None:
        script = (DEPLOY_ROOT / "maintenance" / "release" / "backup.sh").read_text(encoding="utf-8")
        self.assertIn("BACKUP_ATTEMPT_ID", script)
        self.assertIn("attempt_id=%s", script)
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn("s/^attempt_id=//p", production)
        self.assertIn("BackupGenerationFailure", production)

    def test_backup_promotion_retry_window_is_bounded(self) -> None:
        release = self.release()
        release.profile = {"minimum_backup_free_bytes": 1}
        release.runner = mock.Mock()
        release.runner.create_temp_dir.return_value = "/tmp/release-promote.test"
        release.runner.upload_file = mock.Mock()
        responses: list[object] = [
            {
                "artifact": "artifact",
                "transport_artifact": "transport",
                "artifact_size": "1",
                "artifact_sha256": "digest",
                "traffic_preserved": "true",
                "redis_backup_mode": "rdb",
                "no_restart_path_proven": "true",
                "local_restore_point_ready": "true",
            }
        ]
        responses.extend(RuntimeError("artifact visibility pending") for _ in range(5))
        responses.extend(
            [
                {
                    "backup_promotion": "verified",
                    "release_artifact": release.release_id,
                    "release_sha256": "digest",
                    "release_free_bytes": "2",
                },
                {"cleanup": "true"},
            ]
        )
        release.run_remote = mock.Mock(side_effect=responses)
        with mock.patch.object(time, "sleep") as sleep:
            release.backup()
        self.assertEqual(
            sleep.call_args_list,
            [mock.call(5), mock.call(15), mock.call(30), mock.call(60), mock.call(120)],
        )

    def test_recovery_detects_committed_remote_freeze(self) -> None:
        release = self.release()
        release.frozen = False
        release.units_masked = False
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=True)
        release.run_remote.side_effect = [
            {"old_application_resumed": "true", "running_image_id": "old"},
            {"plaintext_state_removed": "true"},
            {"release_claim_reconciled": "true"},
        ]
        release.recover()
        self.assertIn("resume-old.sh", release.run_remote.call_args_list[0].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_partial_freeze_probe_requires_recovery(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"recovery_needed": "true"})
        self.assertTrue(release.remote_pre_switch_recovery_needed())
        script = release.run_remote.call_args.args[1]
        self.assertIn('test "$app_status" != running', script)
        self.assertIn('test "$active_image" != "$pre_image"', script)
        self.assertIn('test "$nginx_status" != active', script)
        self.assertIn('test "$upstream_valid" != true', script)
        self.assertIn('test "$route_marker_valid" != true', script)
        self.assertIn("route-switch-intent", script)
        self.assertIn("route-switched", script)
        self.assertIn('test "$upstream_port" = "$active_port"', script)

    def test_partial_route_switch_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"recovery_needed": "true"})

        self.assertTrue(release.remote_pre_switch_recovery_needed())

        script = release.run_remote.call_args.args[1]
        self.assertIn('route_marker_valid=false', script)
        self.assertIn('switched_port=$(sed -n', script)
        self.assertIn('test "$switched_port" = "$active_port"', script)
        self.assertIn("127[.]0[.]0[.]1:(18080|18081)", script)

    def test_remote_freeze_probe_is_fail_closed(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("ssh interrupted"))
        self.assertIsNone(release.remote_pre_switch_recovery_needed())

    def test_post_migration_failure_runs_coordinated_restore(self) -> None:
        release = self.release()
        release.migration_started = True
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=True)
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
        release.remote_pre_switch_recovery_needed = mock.Mock(return_value=False)
        release.run_remote = mock.Mock(side_effect=[{"plaintext_state_removed": "true"}, RuntimeError("reply lost"), {"release_claim_reconciled": "true"}])
        release.recover()
        self.assertIn(".recovered/marker", release.run_remote.call_args_list[2].args[1])
        self.assertEqual(release.result["status"], "recovered")

    def test_public_exposure_failure_never_calls_snapshot_recovery(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.rollback_route = mock.Mock(return_value={"route_rollback": "true"})
        release.recover = mock.Mock()
        release.upload_assets = mock.Mock(side_effect=RuntimeError("canary failed"))
        with self.assertRaisesRegex(RuntimeError, "canary failed"):
            release.execute()
        release.rollback_route.assert_called_once()
        release.recover.assert_not_called()
        self.assertEqual(release.result["status"], "blocked_reconciliation")

    def test_downtime_switch_failure_runs_recovery_instead_of_route_rollback(self) -> None:
        release = self.release()
        release.deployment_mode = "downtime"
        release.claimed = True
        release.frozen = False
        release.migration_started = False
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.upload_assets = mock.Mock()
        release.preflight = mock.Mock()
        release.verify_public_health_routes = mock.Mock()
        release.freeze = mock.Mock(side_effect=lambda: setattr(release, "frozen", True))
        release.migration_preflight = mock.Mock()
        release.backup = mock.Mock()
        release.bind_migration_plan = mock.Mock()
        release.switch = mock.Mock(side_effect=RuntimeError("switch failed"))
        release.recover = mock.Mock()
        release.rollback_route = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "switch failed"):
            release.execute()

        release.recover.assert_called_once()
        release.rollback_route.assert_not_called()

    def test_downtime_nginx_or_public_verification_failure_runs_coordinated_recovery(self) -> None:
        release = self.release()
        release.deployment_mode = "downtime"
        release.claimed = True
        release.migration_started = True
        release.route_switch_attempted = True
        release.public_exposed = True
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.upload_assets = mock.Mock()
        release.preflight = mock.Mock()
        release.verify_public_health_routes = mock.Mock()
        release.freeze = mock.Mock()
        release.migration_preflight = mock.Mock()
        release.backup = mock.Mock()
        release.bind_migration_plan = mock.Mock()
        release.switch = mock.Mock()
        release.verify_and_finalize = mock.Mock(side_effect=RuntimeError("nginx start failed"))
        release.recover = mock.Mock()
        release.rollback_route = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "nginx start failed"):
            release.execute()

        release.recover.assert_called_once()
        release.rollback_route.assert_not_called()

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

    def test_consumed_probe_failure_is_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("reply lost"))
        self.assertIsNone(release.remote_gate_consumed())

    def test_consumed_probe_reports_valid_unconsumed_claim(self) -> None:
        release = self.release()
        release.release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.image_id = "sha256:" + "a" * 64
        release.release_dir = "/opt/sub2api/releases/182-aaaaaaaaaaaa-1-aaaaaaaa"
        release.run_remote = mock.Mock(return_value={"gate_consumed": "false"})

        self.assertFalse(release.remote_gate_consumed())
        script = release.run_remote.call_args.args[1]
        self.assertIn("gate_consumed=false", script)
        self.assertIn("sha256sum -c CLAIM_SHA256SUMS", script)
        self.assertIn("gate_consumed=unknown", script)

    def test_consumed_probe_reports_inconsistent_state_as_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"gate_consumed": "unknown"})
        self.assertIsNone(release.remote_gate_consumed())

    def test_consumption_probe_shell_covers_true_false_and_unknown(self) -> None:
        bash = shutil.which("bash")
        if bash is None and os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git" / "bin" / "bash.exe"
            bash = str(candidate) if candidate.is_file() else None
        if bash is None:
            self.skipTest("bash is unavailable")

        release_id = "182-aaaaaaaaaaaa-1-aaaaaaaa"
        image_id = "sha256:" + "a" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            release_dir = root / "release"
            active = root / "active"
            fake_bin = root / "bin"
            release_dir.mkdir()
            active.mkdir()
            fake_bin.mkdir()
            release_id_file = active / "release_id"
            gate_file = active / "gate.json"
            release_id_file.write_bytes(f"release_id={release_id}\n".encode())
            gate_file.write_bytes(b"{}\n")

            import hashlib

            checksums = []
            for path in (release_id_file, gate_file):
                checksums.append(f"{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}")
            checksum_file = active / "CLAIM_SHA256SUMS"
            checksum_file.write_bytes(("\n".join(checksums) + "\n").encode())
            jq = fake_bin / "jq"
            jq.write_bytes(
                (
                    "#!/usr/bin/env bash\ncase \"$2\" in\n"
                    "  .manifest.release_id) printf '%s\\n' \"$FAKE_RELEASE_ID\" ;;\n"
                    "  .evidence.candidate_image_id) printf '%s\\n' \"$FAKE_IMAGE_ID\" ;;\n"
                    "  *) exit 1 ;;\nesac\n"
                ).encode(),
            )
            docker = fake_bin / "docker"
            docker.write_bytes(
                (
                    "#!/usr/bin/env bash\ncase \"$*\" in\n"
                    "  *Health.Status*) printf 'healthy\\n' ;;\n"
                    "  *) printf '%s\\n' \"$FAKE_IMAGE_ID\" ;;\nesac\n"
                ).encode(),
            )
            systemctl = fake_bin / "systemctl"
            systemctl.write_bytes(b"#!/usr/bin/env bash\nif [[ $* == *is-active* ]]; then printf 'active\\n'; else printf 'enabled\\n'; fi\n")
            curl = fake_bin / "curl"
            curl.write_bytes(
                b"#!/usr/bin/env bash\n"
                b"for ((i=1;i<=$#;i++)); do if [[ ${!i} == -D ]]; then j=$((i+1)); printf 'x-sub2api-instance: 182-aaaaaaaaaaaa-1-aaaaaaaa-active\\nx-sub2api-background-ready: true\\n' > \"${!j}\"; fi; done\n"
                b"for ((i=1;i<=$#;i++)); do if [[ ${!i} == -w ]]; then printf '200'; fi; done\n"
            )
            nginx_path = root / "sub2api-release-upstream-test.conf"
            nginx_path.write_text("server 127.0.0.1:18081;\n", encoding="utf-8")
            for executable in (jq, docker, systemctl, curl):
                executable.chmod(0o755)

            environment = os.environ.copy()
            bash_fake_bin = fake_bin.as_posix()
            environment.update({
                "FAKE_RELEASE_ID": release_id,
                "FAKE_IMAGE_ID": image_id,
                "NGINX_MANAGED_UPSTREAM": nginx_path.as_posix(),
            })
            if os.name == "nt":
                converted = subprocess.run(
                    [bash, "-lc", f"cygpath -u {shlex.quote(str(fake_bin))}; cygpath -u {shlex.quote(str(nginx_path))}"],
                    check=True, capture_output=True, text=True,
                ).stdout.splitlines()
                bash_fake_bin, environment["NGINX_MANAGED_UPSTREAM"] = converted
            environment["PATH"] = f"{bash_fake_bin}:/usr/bin:/bin"
            slot = root / "active-app"
            script = gate_consumption_probe_script(
                release_dir.as_posix(), release_id, image_id, active.as_posix(), slot.as_posix()
            )

            valid = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(valid.stdout, "gate_consumed=false\n")

            checksum_file.write_bytes(("0" * 64 + "  gate.json\n").encode())
            corrupt = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(corrupt.stdout, "gate_consumed=unknown\n")

            checksum_file.write_bytes(("\n".join(checksums) + "\n").encode())
            environment["FAKE_IMAGE_ID"] = "sha256:" + "b" * 64
            mismatched = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(mismatched.stdout, "gate_consumed=unknown\n")

            environment["FAKE_IMAGE_ID"] = image_id
            (release_dir / ".recovered").mkdir()
            contradictory = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(contradictory.stdout, "gate_consumed=unknown\n")
            (release_dir / ".recovered").rmdir()

            shutil.rmtree(active)
            consumed = release_dir / ".consumed"
            consumed.mkdir()
            marker = consumed / "marker"
            marker.write_bytes(f"release_id=182-bbbbbbbbbbbb-2-bbbbbbbb\ncandidate_image_id={image_id}\n".encode())
            (consumed / "plaintext-cleaned").write_bytes(b"true\n")
            wrong_release = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(wrong_release.stdout, "gate_consumed=unknown\n")

            marker.write_bytes(f"release_id={release_id}\ncandidate_image_id={image_id}\n".encode())
            slot.write_text(f"container=sub2api\nport=18081\nimage_id={image_id}\nrelease_id={release_id}\ninstance_id={release_id}-active\n", encoding="utf-8")
            # This fixture intentionally lacks a real Compose closure; the
            # hardened probe must refuse to consume it rather than trusting the
            # marker and health headers alone.
            completed = subprocess.run([bash, "-c", script], check=True, capture_output=True, text=True, env=environment)
            self.assertEqual(completed.stdout, "gate_consumed=unknown\n")
            slot.unlink()

    def test_unconsumed_claim_before_public_exposure_runs_recovery(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = False
        release.upload_assets = mock.Mock(side_effect=RuntimeError("preflight failed"))
        release.remote_gate_consumed = mock.Mock(return_value=False)
        release.recover = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "preflight failed"):
            release.execute()

        release.recover.assert_called_once()

    def test_unknown_consumption_status_defers_route_rollback(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.rollback_route = mock.Mock(return_value={"route_rollback": "true"})

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        release.rollback_route.assert_not_called()
        self.assertEqual(release.result["status"], "blocked_reconciliation")
        evidence = release.stage.call_args.args[1]
        self.assertEqual(evidence["route_rollback"], "deferred_reconciliation")

    def test_unknown_consumption_status_without_public_exposure_does_not_close(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = False
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.rollback_route = mock.Mock()

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        release.rollback_route.assert_not_called()

    def test_unknown_consumption_status_never_attempts_an_unconfirmed_rollback(self) -> None:
        release = self.release()
        release.claimed = True
        release.public_exposed = True
        release.upload_assets = mock.Mock(side_effect=RuntimeError("reply lost"))
        release.remote_gate_consumed = mock.Mock(return_value=None)
        release.rollback_route = mock.Mock(side_effect=RuntimeError("rollback reply lost"))

        with self.assertRaisesRegex(RuntimeError, "reply lost"):
            release.execute()

        release.rollback_route.assert_not_called()
        self.assertEqual(release.result["status"], "blocked_reconciliation")
        evidence = release.stage.call_args.args[1]
        self.assertEqual(evidence["route_rollback"], "deferred_reconciliation")

    def test_mask_probe_detects_committed_remote_mask(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(return_value={"units_masked": "true"})
        self.assertTrue(release.remote_units_masked())

    def test_mask_probe_failure_is_unknown(self) -> None:
        release = self.release()
        release.run_remote = mock.Mock(side_effect=RuntimeError("reply lost"))
        self.assertIsNone(release.remote_units_masked())


class PublicHealthOnlyTest(unittest.TestCase):
    def test_public_route_check_never_reads_credentials_or_sends_model_request(self) -> None:
        release = object.__new__(ProductionRelease)
        release.profile = {
            "public_domain": "example.test",
            "rack_public_ip": "192.0.2.1",
            "dmit_public_ip": "192.0.2.2",
        }
        release.stage = mock.Mock()
        release.run_remote = mock.Mock(side_effect=[
            {"route_health": "pass", "http_code": "200", "curl_exit": "0", "attempts": "1", "streaming": "not_checked"},
            {"route_health": "pass", "http_code": "200", "curl_exit": "0", "attempts": "11", "streaming": "not_checked"},
        ])
        release.run_remote_with_input = mock.Mock(side_effect=AssertionError("model request must not run"))

        direct, dmit = release.verify_public_health_routes("pre_switch")

        self.assertEqual(direct["streaming"], "not_checked")
        self.assertEqual(dmit["streaming"], "not_checked")
        release.run_remote_with_input.assert_not_called()
        self.assertEqual(release.run_remote.call_count, 2)
        direct_script = release.run_remote.call_args_list[0].args[1]
        dmit_script = release.run_remote.call_args_list[1].args[1]
        for script in (direct_script, dmit_script):
            self.assertIn("for attempt in $(seq 1 30)", script)
            self.assertIn("--connect-timeout 3 --max-time 5", script)
            self.assertIn("curl_exit=$?", script)
            self.assertIn("[[ $attempt == 30 ]] || sleep 1", script)
        verified = release.stage.call_args_list[-1]
        self.assertEqual(verified.args[0], "pre_switch_public_health_verified")
        self.assertEqual(verified.args[1]["direct_attempts"], "1")
        self.assertEqual(verified.args[1]["dmit_attempts"], "11")

    def test_public_route_failure_is_recorded_after_bounded_retries(self) -> None:
        release = object.__new__(ProductionRelease)
        release.profile = {
            "public_domain": "example.test",
            "rack_public_ip": "192.0.2.1",
            "dmit_public_ip": "192.0.2.2",
        }
        release.stage = mock.Mock()
        release.run_remote = mock.Mock(side_effect=[
            {"route_health": "pass", "http_code": "200", "curl_exit": "0", "attempts": "1", "streaming": "not_checked"},
            {"route_health": "fail", "http_code": "000", "curl_exit": "35", "attempts": "30", "streaming": "not_checked"},
        ])

        with self.assertRaisesRegex(RuntimeError, "post_switch public health route verification failed"):
            release.verify_public_health_routes("post_switch")

        failed = release.stage.call_args_list[-1]
        self.assertEqual(failed.args[0], "post_switch_public_health_failed")
        self.assertEqual(failed.args[1]["direct_route_health"], "pass")
        self.assertEqual(failed.args[1]["dmit_route_health"], "fail")
        self.assertEqual(failed.args[1]["dmit_http_code"], "000")
        self.assertEqual(failed.args[1]["dmit_curl_exit"], "35")
        self.assertEqual(failed.args[1]["dmit_attempts"], "30")


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

    def test_switch_canonicalizes_pending_migration_order_before_gate_compare(self) -> None:
        switch = self.script("switch.sh")
        self.assertIn(
            "map({filename,checksum}) | sort_by(.filename, .checksum)",
            switch,
        )

    def test_activation_marker_uses_the_runtime_process_identity(self) -> None:
        compose = self.script("compose-contract.sh")
        switch = self.script("switch.sh")
        finalize = self.script("finalize.sh")
        rollback = self.script("rollback-route.sh")
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")

        self.assertIn("write_release_activation_marker()", compose)
        self.assertIn("/proc/1/status", compose)
        self.assertIn("chown \"$runtime_uid:$runtime_gid\"", compose)
        self.assertIn("$runtime_uid:$runtime_gid:600:1", compose)
        self.assertIn("name=userns", compose)
        self.assertIn("name=rootless", compose)
        self.assertIn('printf "%s\\n" "$5"', compose)
        self.assertIn('docker inspect "$container" >/dev/null 2>&1', compose)
        self.assertIn('RELEASE_ACTIVATION_MARKER_FAILURE_REASON=container_inspect', compose)
        self.assertIn('! -L $activation_host_dir', compose)
        self.assertIn('RELEASE_ACTIVATION_MARKER_FAILURE_REASON=data_mount_invalid', compose)
        self.assertIn("run_release_logged_command()", compose)
        self.assertIn('>> "$SUB2API_RELEASE_RAW_LOG" 2>&1', compose)
        self.assertIn('write_release_activation_marker "$candidate_container" "$candidate_instance_id"', switch)
        self.assertIn('write_release_activation_marker sub2api "$final_instance_id"', finalize)
        self.assertIn('write_release_activation_marker "$old_container" "$old_instance_id"', rollback)
        self.assertNotIn('marker_source=$candidate_container', rollback)
        self.assertIn('X-Sub2API-Background-Ready true', rollback)
        self.assertLess(
            rollback.index('[[ $rollback_background_ready == true ]]'),
            rollback.index('route_to_port "$old_port"'),
        )
        self.assertIn('source "$migration_assertion_dir/compose-contract.sh"', validator)
        self.assertIn('write_release_activation_marker "$probe_app" "$activation_instance"', validator)
        self.assertIn('X-Sub2API-Background-Ready false', validator)
        self.assertIn('X-Sub2API-Background-Ready true', validator)
        for script in (switch, finalize, rollback):
            self.assertNotIn('.sub2api-active-instance.tmp"', script)

    def test_cleanup_supports_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("if [[ -d $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn("[[ ! -e $state_dir && ! -L $state_dir ]]", cleanup)
        self.assertIn('pre-migrations.tsv', cleanup)
        self.assertIn('SELECT filename,checksum FROM schema_migrations ORDER BY filename', cleanup)
        self.assertIn("systemctl is-enabled sub2api-backup.timer", cleanup)

    def test_vm_gate_signing_rejects_empty_or_partial_payloads(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("snapshot_digest=$(jq -cS '{current_image_id, schema_migrations}'", validator)
        self.assertIn('gate_payload_tmp="$output_dir/gate.payload.tmp"', validator)
        self.assertIn('[[ -s $gate_payload_tmp ]] || fail_gate_signing gate_signing_payload', validator)
        self.assertIn('[[ -s $gate_json_tmp ]] || fail_gate_signing gate_signing_canonicalize', validator)
        self.assertIn('fail_gate_signing gate_signing_signature', validator)
        self.assertNotIn('| jq -cS . > "$output_dir/gate.json.tmp"', validator)
        for field in (
            "migration_240_preflight_verified",
            "migration_240_schema_verified",
            "migration_241_preflight_verified",
            "migration_241_schema_verified",
        ):
            self.assertIn(f'--argjson {field} "${field}"', validator)

    def test_preflight_accepts_absent_or_matching_migration_only(self) -> None:
        preflight = self.script("preflight.sh")
        self.assertIn("migration_status=absent", preflight)
        self.assertIn("migration_status=verified", preflight)
        self.assertIn("migration_195_status=verified", preflight)
        self.assertIn("migration_195_status=absent", preflight)
        for migration in ("196", "197", "198", "199", "200", "201", "202", "203", "204", "205", "206", "208", "209", "233"):
            self.assertIn(f"migration_{migration}_status=not_applicable", preflight)
            self.assertIn(f"migration_{migration}_status=verified", preflight)
            self.assertIn(f"migration_{migration}_status=absent", preflight)
        self.assertIn("printf 'migration_195_status=%s", preflight)
        self.assertIn('[[ $migration_state == "$migration|$migration_checksum" ]]', preflight)

    def test_profile_197_uses_the_independent_migration_195_status(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn('self.migration_195_status = values["migration_195_status"]', production)
        self.assertIn('env = assertion_env(self.migration_195_status)', production)
        self.assertNotIn('"MIGRATION_STATUS": self.migration_status', production)

    def test_profile_198_verifies_managed_monitor_key_names(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        self.assertIn('allowed.add("managed_monitor_key_names_verified")', production)
        self.assertIn("managed_monitor_key_name_state", switch)
        self.assertIn("character_maximum_length", switch)
        self.assertIn("managed_monitor_key_names_verified=true", switch)

    def test_profile_199_verifies_reasoning_effort_policy(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        self.assertIn('allowed.add("reasoning_effort_policy_verified")', production)
        self.assertIn("reasoning_effort_policy_state", switch)
        self.assertIn("max_reasoning_effort", switch)
        self.assertIn("reasoning_effort_mappings", switch)
        self.assertIn("reasoning_effort_policy_verified=true", switch)

    def test_profile_202_verifies_all_upstream_migration_semantics(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        expected = {
            "alipay_mobile_precreate_migration_verified": "ALIPAY_MOBILE_PRECREATE_DEEP_LINK",
            "group_auth_cache_image_generation_verified": "allow_image_generation",
            "composite_model_routes_verified": "composite_model_routes",
        }
        for evidence, semantic_probe in expected.items():
            self.assertIn(f'"{evidence}",', production)
            self.assertIn(f"{evidence}=true", switch)
            self.assertIn(semantic_probe, switch)

    def test_profile_202_tracks_each_migration_status_for_reconciliation(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for migration, filename in (
            ("200", "200_alipay_mobile_precreate_deep_link.sql"),
            ("201", "201_group_auth_cache_image_generation.sql"),
            ("202", "202_composite_model_routes.sql"),
        ):
            self.assertIn(f'self.migration_{migration}_status = values["migration_{migration}_status"]', production)
            self.assertIn(f'"{filename}": self.migration_{migration}_status', production)

    def test_profile_206_verifies_all_upstream_migration_semantics(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        expected = {
            "session_id_columns_verified": "session_id_columns_state",
            "live_request_type_verified": "usage_logs_request_type_check",
            "group_allow_live_verified": "group_allow_live_state",
            "email_alias_index_verified": "idx_users_email_dot_stripped",
        }
        for evidence, semantic_probe in expected.items():
            self.assertIn(f'"{evidence}",', production)
            self.assertIn(f"{evidence}=true", switch)
            self.assertIn(semantic_probe, switch)

    def test_profile_206_tracks_each_migration_status_for_reconciliation(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for migration, filename in (
            ("203", "203_add_usage_log_session_id.sql"),
            ("204", "204_allow_live_usage_request_type.sql"),
            ("205", "205_add_group_allow_live.sql"),
            ("206", "206_add_users_email_alias_dedup_index_notx.sql"),
        ):
            self.assertIn(f'self.migration_{migration}_status = values["migration_{migration}_status"]', production)
            self.assertIn(f'"{filename}": self.migration_{migration}_status', production)

    def test_profile_207_reuses_profile_206_reconciliation_without_a_new_status(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        preflight = self.script("preflight.sh")
        switch = self.script("switch.sh")
        self.assertIn('self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241", "242", "243", "244", "245"}', production)
        self.assertIn("$profile == 206 || $profile == 207 || $profile == 208 || $profile == 209 || $profile == 210 || $profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237", switch)
        self.assertIn("migration_206_status", production)
        self.assertIn("migration_206_status", preflight)
        self.assertNotIn("migration_207_status", production)
        self.assertNotIn("migration_207_status", preflight)

    def test_profile_208_tracks_passkey_migration_and_schema_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        preflight = self.script("preflight.sh")
        switch = self.script("switch.sh")
        self.assertIn('self.migration_208_status = values["migration_208_status"]', production)
        self.assertIn('"208_passkey_credentials.sql": self.migration_208_status', production)
        self.assertIn("migration_208_status=not_applicable", preflight)
        self.assertIn("208_passkey_credentials.sql) migration_208_status=verified", preflight)
        self.assertIn("208_passkey_credentials.sql) migration_208_status=absent", preflight)
        self.assertIn("printf 'migration_208_status=%s", preflight)
        self.assertIn('allowed.add("passkey_schema_verified")', production)
        self.assertIn("passkey_schema_state", switch)
        self.assertIn("passkey_schema_verified=true", switch)

    def test_profile_209_tracks_user_usage_aggregation_migration_and_schema_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        preflight = self.script("preflight.sh")
        switch = self.script("switch.sh")
        self.assertIn('self.migration_209_status = values["migration_209_status"]', production)
        self.assertIn('"209_user_usage_aggregation.sql": self.migration_209_status', production)
        self.assertIn("migration_209_status=not_applicable", preflight)
        self.assertIn("209_user_usage_aggregation.sql) migration_209_status=verified", preflight)
        self.assertIn("209_user_usage_aggregation.sql) migration_209_status=absent", preflight)
        self.assertIn("printf 'migration_209_status=%s", preflight)
        self.assertIn('allowed.add("user_usage_aggregation_schema_verified")', production)
        self.assertIn("user_usage_aggregation_schema_state", switch)
        self.assertIn("usage_dashboard_user_hourly", switch)
        self.assertIn("usage_dashboard_user_daily", switch)
        self.assertIn("usage_dashboard_user_backfill_state", switch)
        self.assertIn("user_usage_aggregation_schema_verified=true", switch)

    def test_profile_212_tracks_profit_control_migrations_and_schema_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        preflight = self.script("preflight.sh")
        switch = self.script("switch.sh")
        for migration, filename in (
            ("211", "211_group_profit_control.sql"),
            ("212", "212_group_profit_control_auth_cache_invalidation.sql"),
        ):
            self.assertIn(f'self.migration_{migration}_status = values["migration_{migration}_status"]', production)
            self.assertIn(f'"{filename}": self.migration_{migration}_status', production)
            self.assertIn(f"migration_{migration}_status=not_applicable", preflight)
            self.assertIn(f"{filename}) migration_{migration}_status=verified", preflight)
            self.assertIn(f"{filename}) migration_{migration}_status=absent", preflight)
            self.assertIn(f"printf 'migration_{migration}_status=%s", preflight)
        self.assertIn('allowed.add("group_profit_control_schema_verified")', production)
        self.assertIn('allowed.add("group_profit_auth_cache_trigger_verified")', production)
        self.assertIn("group_profit_control_schema_state", switch)
        self.assertIn("group_profit_auth_cache_trigger_state", switch)

    def test_profile_213_reuses_profile_212_migration_and_schema_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        self.assertIn('{"212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241", "242", "243", "244", "245"}', production)
        self.assertIn("$profile == 212 || $profile == 213 || $profile == 215 || $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237", switch)
        self.assertNotIn("migration_213_status", production)
        self.assertNotIn("migration_213_status", self.script("preflight.sh"))

    def test_profile_215_tracks_usage_model_migrations_and_schema_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        preflight = self.script("preflight.sh")
        for field in ("migration_214_status", "migration_215_status", "usage_log_upstream_model_columns_verified", "usage_log_upstream_model_mismatch_index_verified"):
            self.assertIn(field, production)
            self.assertIn(field, validator)
            self.assertIn(field, gate)
        self.assertIn("214_add_usage_log_upstream_response_model.sql", preflight)
        self.assertIn("215_add_usage_log_upstream_model_mismatch_index_notx.sql", preflight)
        self.assertIn("upstream_response_model", validator)
        self.assertIn("upstream_model_mismatch", validator)
        self.assertIn("idx_usage_logs_upstream_model_mismatch_created_at", validator)
        self.assertIn("old_image_compatibility_version", validator)
        self.assertIn("Sub2API 0.1.171-baiyu", validator)
        self.assertIn('compat_merge_commit=$(git rev-list --first-parent --merges -n 1 "$commit")', validator)
        self.assertIn('compat_commit=$(git rev-parse "$compat_merge_commit^1")', validator)
        self.assertIn('compat_tag="sub2api:baiyu-0.1.171-baiyu-$compat_commit"', validator)
        self.assertIn('docker image inspect -f \'{{.Id}}\' "$compat_tag"', validator)
        self.assertIn('vm_old_image_id "$compat_image_id"', validator)

    def test_migration_195_assertion_is_summary_only_and_fail_closed(self) -> None:
        assertion = self.script("migration-195-assert.sh")
        switch = self.script("switch.sh")
        self.assertIn("information_schema.columns", assertion)
        self.assertIn("source_rate_column_exists", assertion)
        self.assertIn("CEIL((k.rate_multiplier*c.recharge_rate)*100)/100", assertion)
        self.assertIn("migration-195-source-rate-column-existed", assertion)
        self.assertIn("migration-195-timezone.name", assertion)
        self.assertIn("ASSERT_CONFIG_FILE", (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8"))
        self.assertNotIn("s.timezone=COALESCE(NULLIF(current_setting('TIMEZONE'", assertion)
        self.assertIn("matching_outbox.count=1", assertion)
        self.assertIn("matching_outbox.count>=1", assertion)
        self.assertIn("migration-195-account-ids-mismatch.count", assertion)
        self.assertIn("migration-195-trigger-missing.count", assertion)
        self.assertIn("migration_195_data_plan_sha256", assertion)
        self.assertIn("migration_195_account_mismatch", assertion)
        self.assertIn("migration_195_snapshot_missing", assertion)
        self.assertIn("migration_195_outbox_missing", assertion)
        self.assertIn("sched:v2:outbox:watermark", assertion)
        self.assertIn("migration-195-outbox-already-consumed", assertion)
        self.assertIn("migration_195_constraint_missing", assertion)
        self.assertIn("migration_195_trigger_missing", assertion)
        self.assertIn("[[ $recompute_mismatch == 0", assertion)
        self.assertIn("precise_data_plan_query", assertion)
        self.assertIn("release_profile=${release_profile:-$profile}", assertion)
        self.assertIn("$profile == 240 || $profile == 241 || $profile == 242 || $profile == 243", assertion)
        self.assertIn("if [[ $release_profile == 240 || $release_profile == 241 || $release_profile == 242 || $release_profile == 243 || $release_profile == 244 || $release_profile == 245 ]]; then", assertion)
        self.assertNotIn("if [[ $profile == 240 ]]; then", assertion)
        self.assertIn("ROUND(k.source_rate_multiplier * COALESCE(c.recharge_rate, 1), 10)", assertion)
        self.assertGreater(switch.index('migration-195-assert.sh" postflight_db'), switch.index('docker compose "${candidate_compose_args[@]}"'))
        self.assertGreater(switch.index('migration-195-assert.sh" postflight_runtime'), switch.index('docker compose "${candidate_compose_args[@]}"'))

    def test_migration_195_assertion_ignores_soft_deleted_accounts(self) -> None:
        assertion = self.script("migration-195-assert.sh")
        self.assertGreaterEqual(
            assertion.count(
                "FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id "
                "WHERE a.deleted_at IS NULL AND ("
            ),
            2,
        )
        self.assertIn(
            "FROM accounts a LEFT JOIN upstream_keys k ON k.id=a.upstream_key_id "
            "WHERE a.deleted_at IS NULL AND a.upstream_key_id IS NOT NULL",
            assertion,
        )
        self.assertIn(
            "FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL "
            "AND concurrency > 1073741823",
            assertion,
        )

    def test_coordinated_restore_reads_redis_password_from_startup_arguments(self) -> None:
        restore = self.script("restore.sh")
        self.assertIn('index("--requirepass")', restore)
        self.assertIn('startswith("--requirepass=")', restore)
        self.assertIn("IFS= read -r REDISCLI_AUTH", restore)
        self.assertNotIn('export REDISCLI_AUTH="${REDIS_PASSWORD:-}"', restore)
        self.assertIn("redis_backup_expiring", restore)
        self.assertIn("redis_restored_expiring", restore)
        self.assertIn("redis_backup_dbsize - redis_dbsize", restore)
        self.assertIn("redis-check-rdb /tmp/sub2api-restore.rdb", restore)
        self.assertIn("redis_already_expired", restore)
        self.assertNotIn("core-counts-restored.txt", restore)
        self.assertNotIn("core-content-digests-restored.txt", restore)
        self.assertIn('docker stop -t 30 "$candidate_container"', restore)
        self.assertLess(restore.index('docker stop -t 30 "$candidate_container"'), restore.index('docker rm -f "$active_container"'))

    def test_coordinated_restore_seeds_redis_7_multipart_aof_from_verified_rdb(self) -> None:
        restore = self.script("restore.sh")
        checksum_check = 'diff -u "$recovery/metadata/redis-files.sha256"'
        aof_seed = 'install -o "$redis_data_uid" -g "$redis_data_gid" -m 600 "$redis_source/dump.rdb" "$redis_source/appendonlydir/appendonly.aof.1.base.rdb"'
        redis_start = "docker start sub2api-redis"

        self.assertIn('index("--appendonly")', restore)
        self.assertIn('startswith("--appendonly=")', restore)
        self.assertIn("appendonly.aof.manifest", restore)
        self.assertIn("startoffset 0", restore)
        self.assertIn('redis_data_uid=$(stat -c %u "$redis_source/dump.rdb")', restore)
        self.assertIn('chown "$redis_data_uid:$redis_data_gid"', restore)
        self.assertLess(restore.index(checksum_check), restore.index(aof_seed))
        self.assertLess(restore.index(aof_seed), restore.index(redis_start))

    def test_migration_195_preflight_precedes_switch_and_commit_is_reconciled(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        execute = production[production.index("def execute(self)"):]
        self.assertLess(execute.index("self.freeze()"), execute.index("self.migration_preflight()"))
        self.assertLess(execute.index("self.migration_preflight()"), execute.index("self.backup()"))
        self.assertLess(execute.index("self.backup()"), execute.index("self.bind_migration_plan()"))
        self.assertLess(execute.index("self.bind_migration_plan()"), execute.index("self.switch()"))
        self.assertLess(switch.index('migration-195-assert.sh" postflight_db'), switch.index("candidate_run_args=(run -d"))
        self.assertIn("migration-committed", switch)
        self.assertIn("migration_manifest_sha256", switch)
        self.assertIn("printf 'migration=%s checksum=%s", switch)
        self.assertIn("remote_migration_committed", production)
        self.assertIn("migration 195 committed state is unknown", production)

    def test_migration_preflight_uses_minimal_frozen_context_and_reports_195_failure(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")

        self.assertIn('assertion_context = f"{self.state_dir}/migration-preflight-context.sh"', production)
        self.assertIn("migration_preflight_context_verified", production)
        self.assertIn('"ASSERT_CONTEXT_FILE": assertion_context', production)
        self.assertIn('self.stage("migration_195_preflight_failed", failure)', production)
        self.assertIn("code=context_or_state", production)

    def test_switch_failure_stage_is_preserved_before_recovery(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")
        stages = (
            "initialized", "migration_started", "migration_completed", "schema_verified",
            "migration_committed", "candidate_started", "candidate_healthy", "candidate_network_verified", "candidate_port_verified", "candidate_probe_started", "candidate_http_verified",
            "candidate_headers_verified", "active_health_verified", "prompt_audit_verified", "runtime_verified",
        )
        for stage in stages:
            self.assertIn(f"mark_switch_stage {stage}", switch)
        self.assertIn("switch_failure_stage", production)
        self.assertIn("switch_failure_code", production)
        self.assertIn("migration_record_verification:migration_record_checksum", production)
        self.assertIn("schema_contract_assertion:schema_assertion", production)
        self.assertIn("migration_completed|schema_verified", production)
        self.assertIn("mark_migration_failure_context migration_record_verification migration_record_checksum", switch)
        self.assertIn("mark_migration_failure_context schema_contract_assertion schema_assertion", switch)
        self.assertIn("switch_failure_code=%s", switch)
        self.assertIn("switch_init_failure_file=\"$pre_state_dir/switch-init-failure\"", switch)
        self.assertIn("record_switch_init_failure context_source", switch)
        self.assertIn("record_switch_init_failure initial_contract", switch)
        self.assertIn("init_failure_substage", production)
        self.assertIn("init_failure_code", production)
        self.assertLess(
            switch.index("trap 'record_migration_failure"),
            switch.index("while IFS=$'\\t' read -r migration migration_checksum"),
        )
        self.assertIn('self.stage("migration_switch_failed"', production)
        self.assertLess(production.index('self.stage("migration_switch_failed"'), production.index("def verify_and_finalize"))

    def test_candidate_start_failure_preserves_root_only_logs_and_structured_evidence(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch = self.script("switch.sh")

        self.assertIn('candidate_failure_file="$state_dir/candidate-failure"', switch)
        self.assertIn("capture_candidate_failure", switch)
        self.assertIn("docker logs --since 15m", switch)
        self.assertIn('root:root:600:1', switch)
        self.assertIn('candidate_failure_kind=health_unhealthy', switch)
        self.assertIn('candidate_failure_kind=health_timeout', switch)
        self.assertIn('candidate_failure_kind=container_exited', switch)
        self.assertIn('candidate_failure_kind=post_start_contract_failure', switch)
        self.assertIn('runtime_contract_mismatch', switch)
        self.assertIn('activation_marker_write_failed', switch)
        self.assertIn('background_activation_timeout', switch)
        self.assertIn('runtime_contract_mismatch', production)
        self.assertIn('activation_marker_write_failed', production)
        self.assertIn('background_activation_timeout', production)
        self.assertIn('candidate_oom_killed', switch)
        self.assertIn('candidate_health_log_entries', switch)
        self.assertIn('if [[ $candidate_ready != true ]]', switch)
        self.assertIn('candidate_runtime_image=$(docker inspect', switch)
        self.assertIn('candidate_runtime_health=$(docker inspect', switch)
        self.assertIn('candidate_runtime_image != "$candidate_image_id"', switch)
        self.assertIn('capture_candidate_failure_on_exit', switch)
        self.assertIn('candidate-failure-capture-started', switch)
        self.assertIn('candidate_failure_capture_started=true', switch)
        self.assertIn('candidate_failure_committed=true', switch)
        self.assertIn("trap 'remember_candidate_failure_line", switch)
        self.assertIn('trap capture_candidate_failure_on_exit EXIT', switch)
        self.assertIn('trap - ERR EXIT', switch)
        self.assertIn('stage=candidate_failure_capture status=failed', switch)
        self.assertIn('exit_code=98', switch)
        self.assertIn('candidate_start_exit=0', switch)
        self.assertIn('candidate_original_exit_code=unknown', switch)
        self.assertIn('candidate_start_failure_line=$((LINENO + 1))', switch)
        self.assertIn('capture_candidate_failure_once "$candidate_start_failure_line"', switch)
        self.assertIn('[[ $(grep -c \'^candidate_\' "$candidate_failure_file") == 10 ]] || return 1', switch)
        self.assertIn('candidate_original_exit_code=$candidate_start_exit', switch)
        self.assertIn('original_exit_code=%s', switch)
        self.assertLess(
            switch.index('trap capture_candidate_failure_on_exit EXIT'),
            switch.index('mark_switch_stage candidate_started'),
        )
        candidate_started = switch.index("mark_switch_stage candidate_started")
        self.assertLess(
            switch.index('capture_candidate_failure_once "$LINENO"', candidate_started),
            switch.index("mark_switch_stage candidate_healthy", candidate_started),
        )
        for field in (
            "candidate_failure_kind", "candidate_state", "candidate_health", "candidate_exit_code",
            "candidate_original_exit_code",
            "candidate_oom_killed", "candidate_restart_count", "candidate_health_log_entries",
            "candidate_log_capture", "candidate_failure_line",
        ):
            self.assertIn(field, production)

    def test_switch_records_candidate_runtime_assertion_stages(self) -> None:
        switch = self.script("switch.sh")
        expected = (
            "candidate_healthy",
            "candidate_network_verified",
            "candidate_port_verified",
            "candidate_probe_started",
            "candidate_http_verified",
            "candidate_headers_verified",
            "active_health_verified",
            "prompt_audit_verified",
            "runtime_verified",
        )
        offsets = [switch.index(f"mark_switch_stage {stage}") for stage in expected]
        self.assertEqual(offsets, sorted(offsets))

    def test_switch_failure_records_only_a_sanitized_reason_category(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for reason in ("undeclared_field", "missing_field", "unexpected_stderr", "remote_exit", "unknown"):
            self.assertIn(f'"{reason}"', production)
        self.assertIn('failure["switch_failure_reason"] = failure_reason', production)
        self.assertNotIn('failure["switch_failure_error"]', production)

    def test_candidate_http_probe_has_bounded_retry_and_failure_evidence(self) -> None:
        switch = self.script("switch.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn("for _ in $(seq 1 30)", switch)
        self.assertIn('candidate_http_code == 200', switch)
        self.assertIn('candidate-http.code', switch)
        self.assertIn('candidate-curl.exit', switch)
        self.assertIn('candidate_http_code', production)
        self.assertIn('candidate_curl_exit', production)

    def test_switch_outputs_all_verified_profile_235_migration_evidence(self) -> None:
        switch = self.script("switch.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for field in (
            "migration_233_duplicate_keys=0",
            "migration_233_index_verified=true",
            "migration_233_table_state=verified",
            "migration_233_columns_verified=true",
            "migration_233_health_index_verified=true",
            "migration_233_privileges_verified=true",
            "migration_233_trigger_verified=true",
            "migration_233_postflight=pass",
            "migration_234_schema_state=verified",
            "migration_234_schema_verified=true",
            "migration_234_postflight=pass",
        ):
            self.assertIn(field, switch)
            self.assertIn(field.split("=")[0], production)

    def test_migration_233_preflight_accepts_all_schema_evidence_fields(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for field in (
            '"migration_233_duplicate_keys"',
            '"migration_233_index_verified"',
            '"migration_233_table_state"',
            '"migration_233_columns_verified"',
            '"migration_233_health_index_verified"',
            '"migration_233_privileges_verified"',
            '"migration_233_trigger_verified"',
            '"migration_233_preflight"',
        ):
            self.assertIn(field, production)

    def test_migration_234_preflight_accepts_schema_verified_field(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn('"migration_234_schema_state", "migration_234_schema_verified", "migration_234_preflight"', production)

    def test_profile_238_switch_accepts_all_migration_237_postflight_output_fields(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        condition = 'if getattr(self, "profile", {}).get("name") in {"238", "239", "240", "241", "242", "243", "244", "245"}:'
        switch_allowlist = production[
            production.index(condition):
            production.index("try:", production.index(condition))
        ]
        for field in (
            "migration_237_schema_state",
            "migration_237_schema_verified",
            "migration_237_preflight",
            "migration_237_postflight",
        ):
            self.assertIn(f'"{field}"', switch_allowlist)

    def test_profile_239_switch_accepts_current_postflight_output_fields(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        condition = 'if getattr(self, "profile", {}).get("name") in {"239", "240", "241", "242", "243", "244", "245"}:'
        switch_allowlist = production[
            production.index(condition):
            production.index("try:", production.index(condition))
        ]
        for field in (
            "migration_238_schema_state",
            "migration_238_schema_verified",
            "migration_238_preflight",
            "migration_238_postflight",
            "migration_239_backup_rows",
            "migration_239_remaining_rows",
            "migration_239_constraint_verified",
            "migration_239_postflight",
        ):
            self.assertIn(f'"{field}"', switch_allowlist)

    def test_profile_241_switch_emits_inherited_237_and_238_postflight_fields(self) -> None:
        switch = self.script("switch.sh")
        output_section = switch[switch.index("printf 'migration_verified=true\\n'"):]
        self.assertIn(
            "if [[ $release_profile == 238 || $release_profile == 239 || $release_profile == 240 || $release_profile == 241 ]]; then",
            output_section,
        )
        self.assertIn(
            "if [[ $release_profile == 239 || $release_profile == 240 || $release_profile == 241 ]]; then",
            output_section,
        )
        self.assertIn('"$assets_dir/migration-237-assert.sh" postflight', output_section)
        self.assertIn('"$assets_dir/migration-238-assert.sh" postflight', output_section)

    def test_profile_240_switch_accepts_new_observation_and_precise_rate_fields(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        condition = 'if getattr(self, "profile", {}).get("name") in {"240", "241", "242", "243", "244", "245"}:'
        switch_allowlist = production[
            production.index(condition):
            production.index("try:", production.index(condition))
        ]
        for field in (
            "migration_240_schema_state",
            "migration_240_schema_verified",
            "migration_240_preflight",
            "migration_240_postflight",
            "migration_241_schema_state",
            "migration_241_schema_verified",
            "migration_241_preflight",
            "migration_241_postflight",
        ):
            self.assertIn(f'"{field}"', switch_allowlist)

    def test_profile_240_migration_status_is_persisted_in_protected_state_dir(self) -> None:
        observation = self.script("migration-240-assert.sh")
        precise_rate = self.script("migration-241-assert.sh")
        for script in (observation, precise_rate):
            self.assertIn('source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"', script)
            self.assertIn('> "$state_dir/migration-', script)
            self.assertNotIn('> "$release_dir/migration-', script)
        self.assertIn('migration-240-status', observation)
        self.assertIn('migration-241-status', precise_rate)

    def test_profile_245_is_health_only_and_does_not_use_canary_credentials(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        preflight = (DEPLOY_ROOT / "maintenance" / "release" / "preflight.sh").read_text(encoding="utf-8")
        profiles = (DEPLOY_ROOT / "release" / "profiles.py").read_text(encoding="utf-8")
        self.assertIn('[[ "$profile" == 245 ]]', validator)
        self.assertIn('canary_verified:"not_checked"', validator)
        self.assertNotIn("candidate-canary.json", validator)
        self.assertNotIn("key='admin_api_key'", validator)
        self.assertNotIn("/api/v1/admin/users", validator)
        v2 = validator[:validator.index("\nfi\n[[ $release_id =~", validator.index('if [[ "$manifest_schema" == 2 ]]; then'))]
        self.assertNotIn("/api/v1/", v2)
        self.assertNotIn("/v1/", v2)
        self.assertNotIn("Authorization: Bearer", v2)
        self.assertNotIn("Canary request", v2)
        self.assertNotIn("canary checks", v2)
        self.assertNotIn("canary-api-key", v2)
        self.assertNotIn("CANARY_KEY_FILE", preflight)
        self.assertNotIn("canary-api-key", preflight)
        self.assertNotIn('    "canary_api_key_id",', profiles[profiles.index('PROFILES["245"]'):])

    def test_profile_245_version_contract_matches_vm_validator(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        profiles = (DEPLOY_ROOT / "release" / "profiles.py").read_text(encoding="utf-8")
        profile_block = profiles[profiles.index('PROFILES["245"]'):]
        self.assertIn('"version": "0.2.0-baiyu"', profile_block)
        self.assertIn('[[ "$version" == 0.2.0-baiyu ]]', validator)
        self.assertIn('[[ $(jq -er \'.parent_profile\' "$manifest") == 244 ]]', validator)
        self.assertIn('[[ $(jq -er \'.new_migrations | length\' "$manifest") == 6 ]]', validator)

    def test_profile_242_switch_uses_gate_v2_without_legacy_state_files(self) -> None:
        switch = self.script("switch.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertNotIn("$release_profile == 241 || $release_profile == 242", switch)
        self.assertNotIn("$profile == 241 || $profile == 242", switch)
        self.assertIn("if [[ $manifest_schema == 2 ]]; then", switch)
        self.assertIn(".evidence.migration_evidence.pending", switch)
        self.assertIn('getattr(self, "profile", {}).get("name") in {"241"}:', production)
        self.assertIn('if self.manifest.get("schema") == 2:', production)
        self.assertIn('Gate v2 emits only these core runtime fields', production)

    def test_profile_242_switch_allowlist_matches_gate_v2_stdout_contract(self) -> None:
        switch = self.script("switch.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        switch_method = production[production.index("    def switch(self)"):production.index("    def verify_and_finalize(self)")]
        # Gate v2 switch.sh emits this stable core plus the prompt-audit
        # not-applicable fields.  Keep the Python allowlist exactly aligned.
        for field in (
            "migration_verified", "running_image_id", "internal_health", "public_traffic_enabled",
            "candidate_container", "candidate_port", "active_container", "active_port",
            "background_activation", "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
        ):
            self.assertIn(f'"{field}"', switch_method)
        self.assertIn('if self.manifest.get("schema") == 2:', switch_method)
        self.assertIn('allowed = {', switch_method)
        # The shell must remain on the v2 path and must not grow legacy
        # migration-status output for profile 242.
        self.assertIn("if [[ $manifest_schema == 2 ]]; then", switch)
        self.assertNotIn("printf 'migration_242_", switch)

    def test_profile_242_preflight_validates_candidate_plan_before_switch(self) -> None:
        preflight = self.script("preflight.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn("candidate_plan_tmp=$(mktemp", preflight)
        self.assertIn("--migration-plan-json", preflight)
        self.assertIn("candidate_pending_names", preflight)
        self.assertIn("candidate_pending_checksums", preflight)
        self.assertIn('2> "$candidate_plan_stderr"', preflight)
        self.assertIn("candidate_plan_stderr=$(mktemp", preflight)
        self.assertIn('"candidate_pending_names"', production)
        self.assertIn("candidate pending migration checksums differ from signed Gate", production)

    def test_profile_242_switch_isolates_planner_stderr_from_gate_protocol(self) -> None:
        switch = self.script("switch.sh")
        self.assertIn('migration_plan_stderr="$state_dir/migration-plan.stderr"', switch)
        self.assertIn('chmod 600 "$migration_plan_stderr_tmp"', switch)
        self.assertIn('2>> "$migration_plan_stderr"', switch)
        self.assertIn("for plan_attempt in 1 2 3", switch)
        self.assertIn('if docker compose "${candidate_compose_args[@]}" run --rm --no-deps sub2api', switch)
        self.assertIn('planner_exit=$?', switch)
        self.assertIn('plan_valid=false', switch)
        self.assertIn('[[ $planner_exit -eq 0 ]]', switch)
        self.assertIn('[[ $current_snapshot == "$expected_snapshot" ]]', switch)
        self.assertIn('planner_exit=%s', switch)
        self.assertIn('actual_pending_sha256=', switch)
        self.assertIn('[[ $plan_verified == true ]]', switch)

    def test_profile_242_migration_plan_is_readable_by_non_root_runner(self) -> None:
        switch = self.script("switch.sh")
        self.assertIn('chmod 444 "$execution_plan_tmp"', switch)
        self.assertIn('-v "$execution_plan:/input/migration-plan.json:ro"', switch)
        self.assertNotIn('chmod 600 "$execution_plan_tmp"', switch)

    def test_backup_unit_restore_captures_stderr_without_widening_allowlist(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn("def restore_backup_units", production)
        self.assertIn("restore-backup-units.stderr", production)
        self.assertIn('2>>\"$err_file\"', production)
        self.assertIn('printf \'backup_units_restored=true\\\\n\'', production)

    def test_candidate_uses_network_aware_container_and_publish_ports(self) -> None:
        contract = self.script("compose-contract.sh")
        switch = self.script("switch.sh")
        candidate_start = switch[switch.index("candidate_run_args=(run -d"):]
        self.assertIn('[[ $candidate_network_mode == host ]] || candidate_run_args+=(--service-ports)', candidate_start)
        self.assertIn('SERVER_HOST: 127.0.0.1', switch)
        self.assertIn('SERVER_HOST: 0.0.0.0', switch)
        self.assertIn('SERVER_PORT: "8080"', switch)
        self.assertIn('export SERVER_PORT="$candidate_port"', switch)
        self.assertIn('candidate_health_url=$(sub2api_healthcheck_url', switch)
        self.assertIn('assert_sub2api_healthcheck_contract', switch)
        self.assertIn('assert_sub2api_runtime_contract', switch)
        self.assertIn('$container.HostConfig.NetworkMode == "host"', contract)
        self.assertIn('$container.NetworkSettings.Ports["8080/tcp"]', contract)
        self.assertIn('mark_switch_stage candidate_network_verified', candidate_start)
        self.assertIn('mark_switch_stage candidate_port_verified', candidate_start)
        self.assertIn('run_release_logged_command docker compose', candidate_start)
        self.assertNotIn('sub2api >/dev/null 2>&1', candidate_start)
        self.assertIn('capture_candidate_failure_once "$LINENO" runtime_contract_mismatch', candidate_start)

    def test_candidate_failure_captures_docker_healthcheck_output_in_root_only_raw_log(self) -> None:
        switch = self.script("switch.sh")

        self.assertIn('stage=candidate_failure stream=healthcheck', switch)
        self.assertIn('.State.Health.Log', switch)
        self.assertIn('exit_code=%d output=%q', switch)
        self.assertIn('>> "$SUB2API_RELEASE_RAW_LOG"', switch)

    def test_freeze_creates_release_state_root(self) -> None:
        freeze = self.script("freeze-backup.sh")
        self.assertIn("install -d -m 700 /opt/sub2api/backups/release-state", freeze)
        self.assertNotIn("docker compose stop -t 30 sub2api", self.script("freeze.sh"))
        self.assertIn("traffic_preserved=true", self.script("freeze.sh"))
        self.assertNotIn('"$assets_dir/backup.sh"', freeze)

    def test_blue_green_uses_active_slot_and_graceful_route_switch(self) -> None:
        preflight = self.script("preflight.sh")
        expose = self.script("expose.sh")
        finalize = self.script("finalize.sh")
        rollback = self.script("rollback-route.sh")
        emergency = self.script("emergency-close.sh")

        self.assertIn('active_container=$(sed -n \'s/^container=//p\'', preflight)
        self.assertNotIn("docker inspect -f '{{.State.Status}}' sub2api", preflight)
        self.assertIn("systemctl reload nginx >/dev/null 2>&1", expose)
        self.assertIn("for _ in $(seq 1 30)", expose)
        self.assertIn("X-Sub2API-Instance \"$candidate_instance_id\"", expose)
        self.assertIn("[[ $public_verified == true ]]", expose)
        self.assertIn("systemctl reload nginx >/dev/null 2>&1", finalize)
        self.assertIn("for _ in $(seq 1 30)", finalize)
        self.assertIn("X-Sub2API-Instance \"$final_instance_id\"", finalize)
        self.assertIn('final_network_mode=$(sub2api_compose_network_mode', finalize)
        self.assertIn('write_release_active_override', finalize)
        self.assertIn('assert_sub2api_compose_closure', finalize)
        self.assertIn('assert_sub2api_runtime_contract', finalize)
        self.assertIn("final_instance_ready=false", finalize)
        self.assertIn("[[ $final_instance_ready == true ]]", finalize)
        self.assertIn("background_ready=false", finalize)
        self.assertIn("[[ $background_ready == true ]]", finalize)
        self.assertIn("for _ in $(seq 1 120)", finalize)
        self.assertIn("finalize_failure_phase=%s", finalize)
        self.assertIn("finalize_phase=background_activation", finalize)
        self.assertIn("if [[ $public_verified != true ]]", finalize)
        self.assertIn("if [[ $deployment_mode == blue-green ]]", expose)
        self.assertIn("systemctl stop nginx", expose)
        self.assertIn("systemctl start nginx", expose)
        route_write = expose.index('mv -T -- "$upstream_tmp" "$managed_upstream"')
        switched = expose.index("switched=true", route_write)
        validation = expose.index("nginx -t", switched)
        self.assertLess(route_write, switched)
        self.assertLess(switched, validation)
        self.assertIn("DRAIN_TIMEOUT_SECONDS:-3600", finalize)
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn('self.stage("finalize_failed", failure)', production)
        self.assertIn('"finalize_failure_phase", "finalize_failure_line"', production)
        self.assertIn("production.raw.log", production)
        self.assertIn("chmod 600", production)
        self.assertIn("_remote_raw_logging_ready = True", production)
        self.assertIn("SUB2API_RELEASE_RAW_LOG", finalize)
        self.assertIn("trap - ERR", finalize)

    def test_downtime_uses_single_compose_managed_container_on_the_active_port(self) -> None:
        context = self.script("context.sh")
        switch = self.script("switch.sh")
        expose = self.script("expose.sh")
        finalize = self.script("finalize.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")

        self.assertIn("if [[ $deployment_mode == downtime ]]; then", context)
        self.assertIn("candidate_port=$active_port", context)
        self.assertIn("candidate_container=sub2api", context)
        self.assertIn("candidate_instance_id=$final_instance_id", context)
        downtime_context = context[context.index("if [[ $deployment_mode == downtime ]]; then"):]
        downtime_context = downtime_context[:downtime_context.index("\nelse\n")]
        self.assertNotIn("candidate_port=18081", downtime_context)
        self.assertNotIn("sub2api-candidate-", downtime_context)

        downtime = switch[switch.index("if [[ $deployment_mode == downtime ]]; then", switch.index("docker rm \"$migration_container\"")):]
        downtime = downtime[:downtime.index("\nelse\n")]
        self.assertIn('docker compose "${candidate_compose_args[@]}" up -d --no-deps --force-recreate sub2api', downtime)
        self.assertNotIn('docker compose "${candidate_compose_args[@]}" run -d', downtime)
        self.assertIn('SERVER_PORT=%s', downtime)
        self.assertIn('mark_switch_stage downtime_compose_prepared', downtime)
        self.assertIn('mark_switch_stage background_activated', switch)

        downtime_expose = expose[expose.index("if [[ $deployment_mode == downtime ]]; then"):]
        downtime_expose = downtime_expose[:downtime_expose.index("\nfi\n") + 4]
        self.assertIn('[[ $candidate_port == "$active_port" ]]', downtime_expose)
        self.assertIn("systemctl start nginx", downtime_expose)
        self.assertNotIn('mv -T -- "$upstream_tmp"', downtime_expose)

        downtime_finalize = finalize[finalize.index("if [[ $deployment_mode == downtime ]]; then"):]
        downtime_finalize = downtime_finalize[:downtime_finalize.index("\nfi\n") + 4]
        self.assertIn("drain_status=not_applicable", downtime_finalize)
        self.assertNotIn("wait_for_application_drain", downtime_finalize)
        self.assertIn('finalize_stage = "downtime_finalizing"', production)
        self.assertIn('completed_stage = "downtime_finalized"', production)

    def test_downtime_cleanup_and_recovery_preserve_the_recorded_active_slot(self) -> None:
        cleanup = self.script("cleanup-slots.sh")
        freeze = self.script("freeze.sh")
        restore = self.script("restore.sh")
        resume = self.script("resume-old.sh")

        self.assertIn('[[ $candidate_container != sub2api ]]', cleanup)
        self.assertIn("release_id=%s", freeze)
        for script in (restore, resume):
            self.assertIn("old_port=$(sed -n 's/^port=//p'", script)
            self.assertIn('port=%s\\nimage_id=%s\\nrelease_id=%s\\ninstance_id=%s', script)
            self.assertNotIn("port=18080\\nimage_id", script)
        self.assertIn('SERVER_PORT=%s', restore)
        self.assertIn('restore_network_mode=$(sub2api_compose_network_mode', restore)
        self.assertIn('write_release_active_override', restore)
        self.assertIn('assert_sub2api_runtime_contract', restore)
        self.assertIn('resume_network_mode=$(sub2api_compose_network_mode', resume)
        self.assertIn('assert_sub2api_compose_closure "$deploy_dir" "$old_port"', resume)

    def test_preflight_accepts_only_exact_legacy_active_healthcheck_and_records_failure_phase(self) -> None:
        contract = self.script("compose-contract.sh")
        preflight = self.script("preflight.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")

        self.assertIn('local policy=${4:-strict}', contract)
        self.assertIn('local policy=${5:-strict}', contract)
        self.assertIn('["CMD", "wget", "-q", "--spider", $expected]', contract)
        self.assertIn('assert_sub2api_healthcheck_contract "$compose_json" "$compose_network_mode" "$active_port" active_compat', preflight)
        self.assertIn('assert_sub2api_runtime_contract "$active_container" "$pre_image_id" "$compose_network_mode" "$active_port" active_compat', preflight)
        self.assertIn('preflight_failure_phase=%s', preflight)
        self.assertIn('production_preflight_failed', production)
        self.assertLess(
            preflight.index("trap record_preflight_result EXIT"),
            preflight.index('source /opt/sub2api/releases/.active-release/assets/context.sh'),
        )
        self.assertIn('preflight_phase=context', preflight)
        self.assertIn(
            "jq -cSn --arg image \"$active_image\" --argjson rows \"$snapshot_rows\"",
            preflight,
        )

    def test_resume_old_canonicalizes_legacy_healthcheck_before_start(self) -> None:
        resume = self.script("resume-old.sh")

        self.assertIn('resume_base_compose_json=$(docker compose', resume)
        self.assertIn('resume_network_mode=$(sub2api_compose_network_mode', resume)
        self.assertIn('write_release_active_override "$resume_override_tmp"', resume)
        self.assertIn('COMPOSE_FILE=%s', resume)
        self.assertIn('BIND_HOST=127.0.0.1', resume)
        self.assertLess(resume.index('write_release_active_override'), resume.index('docker compose "${release_compose_args[@]}" up -d'))

    def test_release_healthcheck_contracts_compare_the_exact_command_array(self) -> None:
        finalize = self.script("finalize.sh")
        rollback = self.script("rollback-route.sh")
        expose = self.script("expose.sh")
        emergency = self.script("emergency-close.sh")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        for name in ("compose-contract.sh", "context.sh", "preflight.sh", "switch.sh", "finalize.sh", "restore.sh", "resume-old.sh"):
            script = self.script(name)
            self.assertNotIn('join(" ") | contains(', script)
            self.assertNotIn('join(" ") | test(', script)
        context = self.script("context.sh")
        contract = self.script("compose-contract.sh")
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn('["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", $expected]', contract)
        self.assertIn('http://127.0.0.1:8080/health', contract)
        self.assertIn('http://localhost:8080/health', contract)
        self.assertIn('sub2api_compose_network_mode()', contract)
        self.assertIn('assert_sub2api_runtime_contract()', contract)
        self.assertIn('source "$assets_dir/compose-contract.sh"', context)
        self.assertIn('compose-contract-integration.sh', validator)
        self.assertIn("docker logs --since 15m", finalize)
        self.assertIn('finalize_stage = "downtime_finalizing" if deployment_mode == "downtime" else "old_slot_draining"', production)
        self.assertIn("timeout=7500", production)
        self.assertIn('wait_for_application_drain "$old_container"', finalize)
        self.assertIn('wait_for_application_drain "$candidate_container"', finalize)
        self.assertIn('$service.network_mode == "host"', contract)
        self.assertIn('($service.environment.SERVER_PORT | tostring) == $port', contract)
        self.assertIn('($service.environment.SERVER_PORT | tostring) == "8080"', contract)
        self.assertIn("if [[ $connections == 0 && $draining_workers == 0 ]]", context)
        self.assertIn("printf 'timeout\\n'", context)
        self.assertIn("printf 'unknown\\n'", context)
        self.assertNotIn("printf '%s\\n' \"$connections\"", context)
        self.assertIn('assert_http_header_equals "$final_headers" X-Sub2API-Background-Ready', finalize)
        self.assertIn('docker rm "$old_container"', finalize)
        self.assertIn("docker-compose.release-active.yml", finalize)
        self.assertIn("SUB2API_RELEASE_IMAGE", finalize)
        self.assertIn('container_name: sub2api', contract)
        self.assertIn("systemctl reload nginx", rollback)
        self.assertIn('install -m 600 "$managed_upstream" "$previous"', rollback)
        self.assertIn('install -m 600 "$previous" "$restore"', rollback)
        self.assertIn('mv -T -- "$restore" "$managed_upstream"', rollback)
        self.assertNotIn("systemctl stop nginx", rollback)
        self.assertNotIn('docker stop -t 30 "$candidate_container"', rollback)
        self.assertIn('candidate_preserved=', rollback)
        consume = self.script("consume.sh")
        reconcile = self.script("reconcile.sh")
        self.assertIn("route-switch-intent", expose)
        self.assertIn("route-switched", expose)
        self.assertIn("route-switched", consume)
        self.assertIn('server 127.0.0.1:$active_port;', reconcile)
        self.assertIn('docker compose "${release_compose_args[@]}" up -d --no-deps sub2api', rollback)
        self.assertIn('current_sub2api_image != "$pre_image_id"', rollback)
        self.assertIn('docker inspect -f \'{{.Image}}\' "$old_container") == "$pre_image_id"', rollback)
        self.assertIn('exec "$assets_dir/rollback-route.sh"', emergency)
        self.assertNotIn("systemctl stop nginx", emergency)

    def test_bootstrap_restores_original_nginx_site_on_failure(self) -> None:
        bootstrap = (DEPLOY_ROOT / "release" / "production_bootstrap.py").read_text(encoding="utf-8")
        self.assertIn('site_backup="$site.sub2api-release-backup"', bootstrap)
        self.assertIn('install -m 600 "$site_backup" "$site"', bootstrap)
        self.assertIn('rm -f -- "$managed_upstream"', bootstrap)
        self.assertIn("nginx -t >/dev/null 2>&1 && systemctl reload nginx", bootstrap)
        self.assertIn("grep -c '^image_id='", bootstrap)
        self.assertIn("active_image=$(sed -n 's/^image_id=//p'", bootstrap)

    def test_candidate_preserves_sync_setting_but_waits_for_activation(self) -> None:
        switch = self.script("switch.sh")
        compose = (WORKSPACE / "deploy" / "docker-compose.yml").read_text(encoding="utf-8")
        self.assertIn("UPSTREAM_SYNC_AUTO_ENABLED: ${UPSTREAM_SYNC_AUTO_ENABLED:-true}", switch)
        self.assertIn('SUB2API_INSTANCE_ID=$candidate_instance_id', switch)
        self.assertIn("SUB2API_BACKGROUND_ACTIVATION_FILE=/app/data/.sub2api-active-instance", switch)
        self.assertIn("SUB2API_INSTANCE_ID", compose)
        self.assertIn("SUB2API_BACKGROUND_ACTIVATION_FILE", compose)

    def test_scheduler_outbox_starts_before_candidate_activation(self) -> None:
        wire = (WORKSPACE / "backend" / "internal" / "service" / "wire.go").read_text(encoding="utf-8")
        provider = wire[wire.index("func ProvideSchedulerSnapshotService("):]
        provider = provider[:provider.index("\n}\n", provider.index(") *SchedulerSnapshotService")) + 3]
        self.assertIn("startReleasePreactivatedCheckedTask", provider)
        self.assertNotIn("startReleaseActivatedCheckedTask", provider)
        self.assertIn("WaitInitialReady", provider)

        switch = self.script("switch.sh")
        candidate_start = switch.index("candidate_run_args=(run -d")
        runtime_assertion = switch.index('migration-195-assert.sh" postflight_runtime')
        self.assertLess(candidate_start, runtime_assertion)
        self.assertIn('X-Sub2API-Background-Ready false', switch[candidate_start:runtime_assertion])

    def test_release_health_headers_are_crlf_normalized(self) -> None:
        context = self.script("context.sh")
        compose = self.script("compose-contract.sh")
        self.assertIn("assert_http_header_equals()", context)
        self.assertIn("assert_http_header_equals()", compose)
        self.assertIn("tr -d '\\r'", context)
        self.assertIn("tr -d '\\r'", compose)
        self.assertIn('[[ $actual == "$expected" ]]', context)
        for name in (
            "switch.sh",
            "expose.sh",
            "finalize.sh",
            "consume.sh",
            "cleanup-slots.sh",
            "verify.sh",
            "rollback-route.sh",
        ):
            script = self.script(name)
            self.assertNotIn("\\r?$", script)
            self.assertIn("assert_http_header_equals", script)

    def test_backup_reads_redis_requirepass_without_cli_secret(self) -> None:
        backup = self.script("backup.sh")
        self.assertIn('index("--requirepass")', backup)
        self.assertIn('printf \'%s\\n\' "$redis_password" | docker exec -i', backup)
        self.assertNotIn("redis-cli -a", backup)
        self.assertIn("redis_command --rdb /tmp/sub2api-release.rdb", backup)
        self.assertIn("redis-check-rdb /tmp/sub2api-release.rdb", backup)
        self.assertIn("redis_rdb_keys", backup)
        self.assertIn("redis-rdb-counts.txt", backup)
        self.assertIn('docker compose "${release_compose_args[@]}" config --format json', backup)
        self.assertNotIn("docker compose stop", backup)

    def test_backup_keeps_temporary_local_restore_tar_until_release_finishes(self) -> None:
        backup = self.script("backup.sh")
        restore = self.script("restore.sh")
        cleanup = self.script("cleanup-state.sh")

        self.assertIn('install -m 600 "$plain" "$state_dir/recovery-point.tar"', backup)
        self.assertLess(backup.index("umask 077"), backup.index('tar -C "$work" -cf "$plain"'))
        self.assertIn("local_restore_point_ready=true", backup)
        self.assertIn("find . -type f ! -name SHA256SUMS", backup)
        self.assertIn('sha256sum -c recovery-point.tar.sha256', restore)
        self.assertIn('tar -C "$recovery" -xf "$state_dir/recovery-point.tar"', restore)
        self.assertIn('write_release_active_override "$restore_override_tmp" "$(<"$state_dir/pre-image-id")"', restore)
        self.assertIn("release_compose_value_with_active_override", restore)
        self.assertIn("load_release_compose_files", restore)
        self.assertIn("SUB2API_RELEASE_IMAGE=%s", restore)
        self.assertNotIn("age-identity", restore)
        self.assertIn("if ! docker info", restore)
        self.assertIn('elif docker inspect "$active_container"', restore)
        self.assertIn("if ! container_names=$(docker ps -a", restore)
        self.assertIn('case "$nginx_status" in', restore)
        self.assertIn("inactive|failed", restore)
        self.assertIn("(( failed == 0 )) || exit 125", restore)
        self.assertNotIn("! -name recovery-point.tar", cleanup)

    def test_racknerd_verifier_does_not_hairpin_through_dmit(self) -> None:
        verify = self.script("verify.sh")
        finalize = self.script("finalize.sh")
        self.assertNotIn("DMIT_IP", verify)
        self.assertNotIn("DMIT_IP", finalize)
        self.assertNotIn("dmit_health", verify)

    def test_model_canary_asset_is_absent(self) -> None:
        self.assertFalse((DEPLOY_ROOT / "maintenance" / "release" / "route-canary.sh").exists())

    def test_cleanup_handles_backup_failure_before_recovery_point(self) -> None:
        cleanup = self.script("cleanup-state.sh")
        self.assertIn("sha256sum -c SHA256SUMS", cleanup)
        self.assertIn('rm -rf -- "$state_dir"', cleanup)
        self.assertIn("restored.committed", cleanup)

    def test_consume_atomically_commits_active_claim(self) -> None:
        script = self.script("consume.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.consumed"', script)
        self.assertIn('[[ $active_container == sub2api ]]', script)
        self.assertIn('assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready', script)
        self.assertIn('server 127.0.0.1:$active_port;', script)
        self.assertIn('assert_final_compose_closure "$deploy_dir" "$active_port"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)

    def test_slot_cleanup_rebinds_final_route_before_removing_candidate(self) -> None:
        script = self.script("cleanup-slots.sh")
        self.assertIn('instance_id=//p', script)
        self.assertIn('assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready', script)
        self.assertIn('server 127.0.0.1:$active_port;', script)
        self.assertIn('assert_final_compose_closure "$deploy_dir" "$active_port"', script)
        self.assertLess(
            script.index('assert_http_header_equals "$health_headers" X-Sub2API-Background-Ready'),
            script.index('docker rm "$candidate_container"'),
        )

    def test_cleanup_state_reports_when_recovery_point_is_preserved(self) -> None:
        script = self.script("cleanup-state.sh")
        self.assertIn("state_cleanup=recovery_point_preserved", script)
        self.assertIn("state_cleanup=%s", script)

    def test_consumed_probe_rebinds_compose_before_accepting_commit(self) -> None:
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        self.assertIn("assert_final_compose_closure", production)
        self.assertIn('test \\"$compose_valid\\" = true', production)

    def test_reconcile_atomically_commits_active_claim(self) -> None:
        script = self.script("reconcile.sh")
        self.assertIn('mv -T -- "$active_claim" "$release_dir/.recovered"', script)
        self.assertNotIn('rm -rf "$active_claim"', script)
        self.assertNotIn(".claimed", script)


if __name__ == "__main__":
    unittest.main()
