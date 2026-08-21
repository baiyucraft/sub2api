from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import supervisor
from release.atomic import canonical_json
from release_logging import EventContext, JSONLEventLogger


class SupervisorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "path with spaces!" / "releases"
        self.root.mkdir(parents=True)
        self.root_patch = mock.patch.object(supervisor, "RUN_ROOT", self.root)
        self.root_patch.start()

    def tearDown(self) -> None:
        self.root_patch.stop()
        self.temporary.cleanup()

    def write(self, identifier: str, name: str, value: dict) -> Path:
        path = self.root / identifier / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(canonical_json(value) + b"\n")
        return path

    def minimum_release(self, identifier: str = "198-aaaaaaaaaaaa-1-deadbeef") -> Path:
        path = self.root / identifier
        self.write(identifier, "manifest.json", {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40, "deployment_mode": "blue-green"})
        self.write(identifier, "runner.json", {"status": "running", "pid": 123, "process_token": "token", "exit_code": None})
        self.write(identifier, "state.json", {"stage": "vm_validate", "status": "verified"})
        return path

    def test_release_id_rejects_path_traversal(self) -> None:
        with self.assertRaisesRegex(ValueError, "release ID"):
            supervisor.status_view("../outside")

    def test_malformed_state_fails_closed(self) -> None:
        run_dir = self.minimum_release()
        (run_dir / "state.json").write_text("not-json", encoding="utf-8")
        with self.assertRaisesRegex(RuntimeError, "malformed"):
            supervisor.status_view(run_dir.name)

    @unittest.skipIf(os.name == "nt", "creating symlinks normally requires Windows developer mode")
    def test_symlinked_parent_is_rejected(self) -> None:
        run_dir = self.minimum_release()
        outside = Path(self.temporary.name) / "outside"
        outside.mkdir()
        (outside / "production-result.json").write_text("{}", encoding="utf-8")
        (run_dir / "gate").symlink_to(outside, target_is_directory=True)
        with self.assertRaisesRegex(RuntimeError, "unsafe state"):
            supervisor.status_view(run_dir.name)

    def test_status_is_strict_allowlist_and_does_not_echo_unknown_fields(self) -> None:
        run_dir = self.minimum_release()
        runner = json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))
        runner["argv"] = ["--secret", "do-not-echo"]
        self.write(run_dir.name, "runner.json", runner)
        with mock.patch.object(supervisor, "_runner_alive", return_value=True):
            value = supervisor.status_view(run_dir.name)
        self.assertEqual(set(value), set(supervisor.STATUS_FIELDS))
        self.assertNotIn("do-not-echo", json.dumps(value))

    def test_pid_reuse_token_mismatch_is_not_alive(self) -> None:
        with mock.patch.object(supervisor, "_process_token", return_value="new-token"):
            self.assertFalse(supervisor._runner_alive({"status": "running", "pid": 123, "process_token": "old-token"}))

    def test_wait_timeout_never_terminates_worker(self) -> None:
        value = {field: None for field in supervisor.STATUS_FIELDS}
        value.update({"release_id": "release", "runner_alive": True, "runner_status": "running"})
        with mock.patch.object(supervisor, "status_view", return_value=value), mock.patch.object(supervisor.time, "monotonic", side_effect=[0, 2]), mock.patch("builtins.print") as output:
            supervisor.wait(argparse.Namespace(release_id="release", timeout=1))
        self.assertIn("still_running", output.call_args.args[0])

    def test_detached_start_records_identity_and_returns_after_handshake(self) -> None:
        process = mock.Mock(pid=456)
        captured: dict[str, object] = {}

        def launch(command, **kwargs):
            captured["command"] = command
            captured["kwargs"] = kwargs
            runner_path = next(self.root.iterdir()) / "runner.json"
            value = json.loads(runner_path.read_text(encoding="utf-8"))
            value["status"] = "running"
            runner_path.write_bytes(canonical_json(value) + b"\n")
            return process

        manifest = {"release_id": "placeholder", "profile": "198", "commit_sha": "a" * 40, "deployment_mode": "blue-green"}
        with mock.patch.object(supervisor, "get_profile", return_value={"name": "198"}), mock.patch.object(supervisor, "create_manifest", side_effect=lambda commit, profile, identifier, mode: {**manifest, "release_id": identifier, "deployment_mode": mode}), mock.patch.object(supervisor, "runner_checksum", return_value="c" * 64), mock.patch.object(supervisor, "popen_detached_worker", side_effect=launch), mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch("builtins.print") as output:
            supervisor.start(argparse.Namespace(profile="198", commit="a" * 40, deployment_mode="blue-green"))
        run_dir = next(self.root.iterdir())
        runner = json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual(runner["pid"], 456)
        self.assertEqual(runner["process_token"], "token")
        self.assertEqual(runner["runner_sha256"], "c" * 64)
        self.assertEqual(runner["stdout"], "logs/runner.stdout.log")
        self.assertEqual(runner["stderr"], "logs/runner.stderr.log")
        self.assertNotIn("secret", json.dumps(runner))
        self.assertIn(str(supervisor.DEPLOY_ROOT / "release.py"), captured["command"])
        self.assertIn("runner=started", output.call_args.args[0])
        events = [json.loads(line) for line in (run_dir / "logs" / "events.jsonl").read_text(encoding="utf-8").splitlines()]
        self.assertEqual(events[0]["event"], "runner_starting")
        self.assertEqual(events[-1]["event"], "startup_handshake_verified")
        self.assertTrue((run_dir / "logs" / "runner.stdout.log").is_file())
        self.assertTrue((run_dir / "logs" / "runner.stderr.log").is_file())

    def test_new_commands_expose_help(self) -> None:
        for command in ("deploy-start", "deploy-follow", "follow", "status", "logs", "wait", "verify-result", "reconcile-inspect", "reconcile"):
            result = subprocess.run([sys.executable, str(DEPLOY_ROOT / "release.py"), command, "--help"], cwd=DEPLOY_ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.assertEqual(result.returncode, 0, command)

    def test_logs_view_filters_local_events_and_redacts_again(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        logger = JSONLEventLogger(
            run_dir / "logs" / "events.jsonl",
            EventContext(identifier, "blue-green", "local"),
        )
        logger.emit(
            stage="doctor", script="release.py", event="old", message="old",
            level="info", timestamp=datetime(2026, 8, 14, 1, tzinfo=timezone.utc),
        )
        logger.emit(
            stage="gate", script="release.py", event="failed", message="token=hidden",
            level="error", timestamp=datetime(2026, 8, 14, 2, tzinfo=timezone.utc),
        )
        args = argparse.Namespace(
            release_id=identifier, node="local", stage="gate", level="error",
            tail=10, since=timedelta(days=3650),
        )
        value = supervisor.logs_view(args)
        self.assertEqual(value["log_status"], "ok")
        self.assertEqual([item["event"] for item in value["events"]], ["failed"])
        self.assertNotIn("hidden", json.dumps(value))

    def test_logs_view_reports_remote_unavailability_without_write_retry(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        ssh = mock.Mock()
        ssh.read_release_events.side_effect = OSError("offline")
        args = argparse.Namespace(
            release_id=identifier, node="racknerd", stage=None, level=None,
            tail=100, since=None,
        )
        with mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            value = supervisor.logs_view(args)
        self.assertEqual(value["log_status"], "unknown")
        self.assertEqual(value["issues"][0]["node"], "racknerd")
        ssh.read_release_events.assert_called_once_with("racknerd", identifier)

    def test_logs_view_maps_vm_display_node_to_local_vm_ssh_config(self) -> None:
        identifier = "239-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        local_log = run_dir / "logs" / "events.jsonl"
        logger = JSONLEventLogger(local_log, EventContext(identifier, "downtime", "vm"))
        logger.emit(stage="vm_validate", script="vm-validate.sh", event="failed", message="VM validation failed", level="error")
        ssh = mock.Mock()
        ssh.read_release_events.return_value = local_log.read_bytes()
        args = argparse.Namespace(
            release_id=identifier, node="vm", stage=None, level="error",
            tail=100, since=None,
        )
        with mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            value = supervisor.logs_view(args)
        self.assertEqual(value["log_status"], "ok")
        self.assertEqual([item["node"] for item in value["events"]], ["vm"])
        ssh.read_release_events.assert_called_once_with("local_vm", identifier)

    def test_logs_view_uses_vm_failure_markers_when_remote_jsonl_is_missing(self) -> None:
        identifier = "239-bbbbbbbbbbbb-2-deadbeef"
        self.minimum_release(identifier)
        ssh = mock.Mock()
        ssh.read_release_events.side_effect = FileNotFoundError("events missing")
        ssh.run.return_value.values = {
            "gate_stage": "migration_assertion_profile_232_channel_monitor_media",
            "gate_failure_category": "migration_assertion_profile_232_channel_monitor_media",
            "gate_failure_line": "1024",
            "raw_log_status": "ok",
            "raw_log_bytes": "0",
            "validator_stderr_bytes": "138",
        }
        args = argparse.Namespace(
            release_id=identifier, node="vm", stage=None, level="error",
            tail=100, since=None,
        )
        with mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            value = supervisor.logs_view(args)
        self.assertEqual(value["log_status"], "partial")
        self.assertEqual([item["event"] for item in value["events"]], ["gate_failure_evidence"])
        self.assertEqual(value["events"][0]["details"]["failure_line"], 1024)
        ssh.read_release_events.assert_called_once_with("local_vm", identifier)
        self.assertEqual(ssh.run.call_args.args[0], "local_vm")

    def test_worker_holds_one_lock_across_deploy(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        runner = {"schema": 1, "release_id": identifier, "profile": "198", "commit": "a" * 40, "pid": os.getpid(), "process_token": "token", "status": "starting", "exit_code": None}
        self.write(identifier, "runner.json", runner)
        lock = mock.MagicMock()
        with mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch.object(supervisor, "RunLock", return_value=lock), mock.patch("release.cli.deploy") as deploy:
            supervisor.worker(argparse.Namespace(release_id=identifier, profile="198", commit="a" * 40, deployment_mode="blue-green"))
        deploy.assert_called_once()
        self.assertFalse(deploy.call_args.kwargs["acquire_lock"])
        lock.__enter__.assert_called_once()
        lock.__exit__.assert_called_once()
        self.assertEqual(json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))["status"], "verified")

    def test_verify_result_rejects_missing_core_evidence(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "verified", "pid": 123, "process_token": "token", "exit_code": 0})
        self.write(identifier, "release-state.json", {"stage": "production_release", "status": "verified"})
        self.write(identifier, "gate/production-result.json", {"stage": "production_verified", "status": "verified", "history": [{"stage": "production_verified", "evidence": {"running_image_id": "sha256:" + "b" * 64}}]})
        document = {"manifest": {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40, "deployment_mode": "blue-green"}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(supervisor, "verify_gate", return_value=document):
            with self.assertRaisesRegex(RuntimeError, "evidence is incomplete"):
                supervisor.verify_result(argparse.Namespace(release_id=identifier))

    def test_verify_result_accepts_health_only_release_evidence(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "verified", "pid": 123, "process_token": "token", "exit_code": 0})
        self.write(identifier, "release-state.json", {"stage": "production_release", "status": "verified"})
        image_id = "sha256:" + "b" * 64
        evidence = {
            "direct_health": "pass", "direct_route_health": "pass", "direct_streaming": "not_checked",
            "dmit_route_health": "pass", "dmit_streaming": "not_checked",
            "canary_usage_recorded": "not_checked", "real_client_ip": "not_checked", "final_health": "pass",
            "dmit_final_health": "pass", "gate_consumed": "true", "plaintext_state_removed": "true",
            "backup_units_restored": "true", "running_image_id": image_id,
        }
        self.write(identifier, "gate/production-result.json", {
            "stage": "production_verified", "status": "verified",
            "history": [{"stage": "production_verified", "evidence": evidence}],
        })
        document = {
            "manifest": {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40, "deployment_mode": "blue-green"},
            "evidence": {"candidate_image_id": image_id},
        }
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(supervisor, "verify_gate", return_value=document):
            result = supervisor.verified_result_view(identifier)
        self.assertEqual(result["status"], "verified")

    def test_claim_only_interruption_decision(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "gate/production-result.json", {"stage": "stage_assets_verified", "status": "blocked_reconciliation", "history": [{"stage": "stage_assets"}, {"stage": "stage_assets_verified"}]})
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "false", "app_health": "healthy", "nginx_active": "true", "backup_timer_enabled": "true", "running_image_id": "sha256:" + "a" * 64, "candidate_exists": "false", "candidate_health": "absent"}
        ssh = mock.Mock()
        ssh.run.return_value.values = remote
        with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch.object(supervisor, "_runner_alive", return_value=False):
            value = supervisor._inspect_reconciliation(identifier)
        self.assertEqual(value["decision"], "claim_only_recover")
        self.assertEqual(value["failure_code"], "caller_interrupted_after_claim")

    def test_claim_only_recovery_rejects_unknown_running_image(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "gate/production-result.json", {"stage": "stage_assets_verified", "status": "blocked_reconciliation", "history": [{"stage": "stage_assets_verified"}]})
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "false", "app_health": "healthy", "nginx_active": "true", "backup_timer_enabled": "true", "running_image_id": "unknown", "candidate_exists": "false", "candidate_health": "absent"}
        ssh = mock.Mock()
        ssh.run.return_value.values = remote
        with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch.object(supervisor, "_runner_alive", return_value=False):
            value = supervisor._inspect_reconciliation(identifier)
        self.assertEqual(value["decision"], "blocked")

    def test_reconciliation_probe_handles_missing_active_container(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "gate/production-result.json", {"stage": "blocked_reconciliation", "status": "blocked_reconciliation", "history": []})
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "true", "plaintext_cleaned": "false", "route_started": "false", "migration_started": "true", "app_health": "unknown", "nginx_active": "false", "backup_timer_enabled": "false", "running_image_id": "unknown"}
        ssh = mock.Mock()
        ssh.run.return_value.values = remote

        with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch.object(supervisor, "_runner_alive", return_value=False):
            value = supervisor._inspect_reconciliation(identifier)

        probe = ssh.run.call_args.args[1]
        self.assertIn('app_health=unknown', probe)
        self.assertIn('if test -n "$active_container" && docker inspect "$active_container"', probe)
        self.assertEqual(value["decision"], "coordinated_restore_required")

    def test_foreground_release_without_runner_metadata_only_allows_coordinated_restore(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        (run_dir / "runner.json").unlink()
        self.write(identifier, "gate/production-result.json", {"stage": "route_restored", "status": "blocked_reconciliation", "history": []})
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "true", "plaintext_cleaned": "false", "route_started": "false", "migration_started": "true", "app_health": "healthy", "nginx_active": "true", "backup_timer_enabled": "false", "running_image_id": "sha256:" + "a" * 64, "candidate_exists": "true", "candidate_health": "healthy"}
        ssh = mock.Mock()
        ssh.run.return_value.values = remote

        with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            value = supervisor._inspect_reconciliation(identifier)

        self.assertEqual(value["decision"], "coordinated_restore_required")

    def test_coordinated_recovery_uses_versioned_restore_and_reconciles_claim(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "release-state.json", {"schema": 1, "release_id": identifier, "stage": "production_release", "status": "blocked_reconciliation", "history": []})
        self.write(identifier, "gate/production-result.json", {"release_id": identifier, "stage": "blocked_reconciliation", "status": "blocked_reconciliation", "history": []})
        inspection = {"decision": "coordinated_restore_required", "runner_alive": False}
        ssh = mock.Mock()
        ssh.create_temp_dir.return_value = "/opt/sub2api/releases/coordinated-restore.abcdefgh"
        ssh.run.side_effect = [
            mock.Mock(values={"coordinated_restore": "verified", "restored_image_id": "old", "application_health": "pass"}),
            mock.Mock(values={"backup_units_restored": "true", "release_claim_reconciled": "true", "plaintext_state_removed": "true", "state_cleanup": "recovery_point_preserved"}),
            mock.Mock(values={"cleanup": "true"}),
        ]

        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))

        ssh.upload_file.assert_called_once_with(
            "racknerd",
            supervisor.COORDINATED_RESTORE,
            "/opt/sub2api/releases/coordinated-restore.abcdefgh/restore.sh",
            0o500,
        )
        restore_command = ssh.run.call_args_list[0].args[1]
        self.assertIn(f"RELEASE_DIR=/opt/sub2api/releases/{identifier}", restore_command)
        self.assertIn("/coordinated-restore.abcdefgh/restore.sh", restore_command)
        finish_command = ssh.run.call_args_list[1].args[1]
        self.assertIn("restore-backup-units.sh", finish_command)
        self.assertIn("cleanup-state.sh", finish_command)
        self.assertIn("reconcile.sh", finish_command)
        self.assertIn("reconcile.stderr", finish_command)
        self.assertEqual(finish_command.count('2>>"$stderr_file"'), 3)
        production = json.loads((self.root / identifier / "gate" / "production-result.json").read_text(encoding="utf-8"))
        self.assertEqual(production["status"], "recovered")
        self.assertEqual(production["stage"], "recovered_after_coordinated_restore")

    def test_coordinated_recovery_records_already_recovered_remote_state(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "release-state.json", {"schema": 1, "release_id": identifier, "stage": "production_release", "status": "blocked_reconciliation", "history": []})
        self.write(identifier, "gate/production-result.json", {"release_id": identifier, "stage": "blocked_reconciliation", "status": "blocked_reconciliation", "history": []})
        inspection = {"decision": "already_recovered", "runner_alive": False}
        ssh = mock.Mock()
        ssh.run.return_value.values = {"backup_units_restored": "true", "release_claim_reconciled": "true", "plaintext_state_removed": "true"}

        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))

        self.assertIn(f"{identifier}/.recovered/marker", ssh.run.call_args.args[1])
        self.assertNotIn("restore.sh", ssh.run.call_args.args[1])
        production = json.loads((self.root / identifier / "gate" / "production-result.json").read_text(encoding="utf-8"))
        self.assertEqual(production["status"], "recovered")

    def test_coordinated_recovery_rejects_active_runner(self) -> None:
        inspection = {"decision": "coordinated_restore_required", "runner_alive": True}
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection):
            with self.assertRaisesRegex(RuntimeError, "coordinated recovery is not allowed"):
                supervisor.reconcile(argparse.Namespace(release_id="198-aaaaaaaaaaaa-1-deadbeef", mode="coordinated-recover"))


if __name__ == "__main__":
    unittest.main()
