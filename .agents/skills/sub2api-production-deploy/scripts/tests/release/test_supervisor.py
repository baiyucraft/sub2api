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

from release import supervisor, recovery_bundle
from release.atomic import canonical_json
from release_logging import EventContext, JSONLEventLogger
from release.ssh import UnexpectedRemoteStderr


class SupervisorTest(unittest.TestCase):
    def setUp(self) -> None:
        self.vm_config_patch = mock.patch("release.vm_lifecycle.load_settings", return_value=None)
        self.vm_config_patch.start()
        self.addCleanup(self.vm_config_patch.stop)
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
        self.write(identifier, "gate/gate.json", {"manifest": {"release_id": identifier}})
        (path / "gate" / "gate.sig").write_bytes(b"signed-gate-signature")
        return path

    @staticmethod
    def recovered_proof(candidate: str) -> dict[str, str]:
        return {
            "recovery_marker_verified": "true",
            "release_claim_reconciled": "true",
            "plaintext_state_removed": "true",
            "active_slot_verified": "true",
            "application_health": "pass",
            "nginx_active": "true",
            "nginx_route_verified": "true",
            "backup_service_inactive": "true",
            "backup_timer_enabled": "true",
            "backup_timer_active": "true",
            "candidate_absent": "true",
            "running_image_id": "sha256:" + "a" * 64 if candidate != "sha256:" + "a" * 64 else "sha256:" + "b" * 64,
        }

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
        for command in ("deploy-start", "deploy-follow", "follow", "status", "logs", "wait", "verify-result", "reconcile-inspect", "reconcile", "finalize-consumed"):
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

    def _owned_worker(self):
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        self.write(identifier, "runner.json", {
            "release_id": identifier, "status": "starting", "pid": os.getpid(), "process_token": "token", "exit_code": None,
        })
        args = argparse.Namespace(release_id=identifier, profile="198", commit="a" * 40, deployment_mode="blue-green")
        lease = mock.MagicMock()
        lease.__enter__.return_value = lease
        lease.enabled = True
        return run_dir, args, lease

    def test_owned_vm_starts_before_deploy_and_closes_after_evidence_and_postdoctor(self) -> None:
        run_dir, args, lease = self._owned_worker()
        calls = []
        lease.ensure_ready.side_effect = lambda: calls.append("vm_ready")
        lease.complete.side_effect = lambda: calls.append("vm_complete")
        doctor = mock.Mock()
        doctor.run.side_effect = lambda *a, **kw: calls.append("local" if a else "postdoctor")
        with mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch.object(supervisor, "RunLock"), mock.patch.object(supervisor, "VMLease", return_value=lease), mock.patch("release.doctor.ReleaseDoctor", return_value=doctor), mock.patch("release.cli.deploy", side_effect=lambda *a, **kw: calls.append("deploy")), mock.patch.object(supervisor, "_verified_evidence_view", side_effect=lambda *a: calls.append("verify")):
            supervisor.worker(args)
        self.assertEqual(calls, ["local", "vm_ready", "deploy", "verify", "postdoctor", "vm_complete"])
        self.assertEqual(json.loads((run_dir / "runner.json").read_text())["status"], "verified")

    def test_owned_vm_retained_when_deploy_or_final_checks_fail(self) -> None:
        for phase in ("deploy", "verify", "postdoctor"):
            with self.subTest(phase=phase):
                run_dir, args, lease = self._owned_worker()
                doctor = mock.Mock()
                doctor.run.side_effect = [None, RuntimeError("postdoctor failed")] if phase == "postdoctor" else None
                with mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch.object(supervisor, "RunLock"), mock.patch.object(supervisor, "VMLease", return_value=lease), mock.patch("release.doctor.ReleaseDoctor", return_value=doctor), mock.patch("release.cli.deploy", side_effect=RuntimeError("deploy failed") if phase == "deploy" else None), mock.patch.object(supervisor, "_verified_evidence_view", side_effect=RuntimeError("verify failed") if phase == "verify" else None):
                    with self.assertRaises(RuntimeError):
                        supervisor.worker(args)
                lease.complete.assert_not_called()
                self.assertEqual(json.loads((run_dir / "runner.json").read_text())["status"], "failed")

    def test_owned_vm_cleanup_failure_preserves_production_verified_without_runner_success(self) -> None:
        run_dir, args, lease = self._owned_worker()
        self.write(run_dir.name, "gate/production-result.json", {"status": "verified", "stage": "production_verified"})
        lease.complete.side_effect = RuntimeError("shutdown failed")
        with mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch.object(supervisor, "RunLock"), mock.patch.object(supervisor, "VMLease", return_value=lease), mock.patch("release.doctor.ReleaseDoctor"), mock.patch("release.cli.deploy") as deploy, mock.patch.object(supervisor, "_verified_evidence_view"):
            with self.assertRaisesRegex(RuntimeError, "shutdown failed"):
                supervisor.worker(args)
        deploy.assert_called_once()
        self.assertEqual(json.loads((run_dir / "runner.json").read_text())["status"], "failed")
        self.assertEqual(json.loads((run_dir / "gate/production-result.json").read_text())["status"], "verified")

    def test_public_verifier_keeps_terminal_runner_precondition(self) -> None:
        run_dir = self.minimum_release()
        with mock.patch.object(supervisor, "_runner_alive", return_value=True), mock.patch.object(supervisor, "_verified_evidence_view") as evidence:
            with self.assertRaisesRegex(RuntimeError, "not successfully terminal"):
                supervisor.verified_result_view(run_dir.name)
        evidence.assert_not_called()

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
            "backup_units_restored": "true", "nginx_ingress_policy": "pass", "running_image_id": image_id,
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

    def recovered_release(self, identifier: str, *, stage: str = "recovered_after_coordinated_restore") -> tuple[Path, dict, str, str]:
        run_dir = self.minimum_release(identifier)
        candidate = "sha256:" + "b" * 64
        restored = "sha256:" + "a" * 64
        self.write(identifier, "runner.json", {"status": "recovered", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "state.json", {"release_id": identifier, "stage": "vm_validate", "status": "verified"})
        self.write(identifier, "release-state.json", {"release_id": identifier, "stage": "production_release", "status": "recovered"})
        evidence = {
            "release_claim_reconciled": "true", "plaintext_state_removed": "true",
            "nginx_route_verified": "true", "backup_service_inactive": "true",
        }
        if stage == "recovered_after_coordinated_restore":
            evidence.update({
                "backup_units_restored": "true",
                "restored_image_id": restored,
                "coordinated_restore": "verified",
                "application_health": "pass",
            })
        self.write(identifier, "gate/production-result.json", {
            "release_id": identifier,
            "stage": stage,
            "status": "recovered",
            "history": [{"stage": stage, "evidence": evidence}],
        })
        document = {
            "manifest": {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40, "deployment_mode": "blue-green"},
            "evidence": {"candidate_image_id": candidate},
        }
        return run_dir, document, candidate, restored

    def test_verify_result_remains_candidate_only(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.recovered_release(identifier)
        with mock.patch.object(supervisor, "_runner_alive", return_value=False):
            with self.assertRaisesRegex(RuntimeError, "not successfully terminal"):
                supervisor.verified_result_view(identifier)

    def test_recovered_result_accepts_independently_verified_recovery(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        _, document, candidate, restored = self.recovered_release(identifier)
        ssh = mock.Mock()
        ssh.run.return_value.values = {
            "recovery_marker_verified": "true",
            "release_claim_reconciled": "true",
            "plaintext_state_removed": "true",
            "active_slot_verified": "true",
            "application_health": "pass",
            "nginx_active": "true",
            "nginx_route_verified": "true",
            "backup_service_inactive": "true",
            "backup_timer_enabled": "true",
            "backup_timer_active": "true",
            "candidate_absent": "true",
            "running_image_id": restored,
        }
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(
            supervisor, "verify_gate", return_value=document
        ), mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            result = supervisor.recovered_result_view(identifier)

        self.assertEqual(result, {
            "release_id": identifier,
            "status": "recovered",
            "recovery_stage": "recovered_after_coordinated_restore",
            "candidate_image_id": candidate,
            "running_image_id": restored,
            "claim_final_state": "recovered",
        })
        script = ssh.run.call_args.args[1]
        self.assertIn('grep -Fxq release_id=198-aaaaaaaaaaaa-1-deadbeef "$recovered/marker"', script)
        self.assertIn("sha256sum -c CLAIM_SHA256SUMS", script)
        self.assertIn('test "$active_slot_image_id" = "$running_image_id"', script)
        self.assertIn('test "$required_capture_running" = true', script)
        self.assertIn("systemctl is-active sub2api-backup.timer", script)
        self.assertIn('grep -Fq "server 127.0.0.1:$active_port;"', script)
        self.assertIn("systemctl is-active sub2api-backup.service", script)
        self.assertIn("running_image_id", ssh.run.call_args.args[2])

    def test_recovered_result_merges_prior_coordinated_restore_evidence(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        _, document, _, restored = self.recovered_release(identifier)
        production = json.loads((self.root / identifier / "gate" / "production-result.json").read_text(encoding="utf-8"))
        production["history"].append({
            "stage": "recovered_after_coordinated_restore",
            "evidence": {
                "backup_units_restored": "true",
                "release_claim_reconciled": "true",
                "plaintext_state_removed": "true",
            },
        })
        self.write(identifier, "gate/production-result.json", production)
        ssh = mock.Mock()
        ssh.run.return_value.values = {
            "recovery_marker_verified": "true",
            "release_claim_reconciled": "true",
            "plaintext_state_removed": "true",
            "active_slot_verified": "true",
            "application_health": "pass",
            "nginx_active": "true",
            "nginx_route_verified": "true",
            "backup_service_inactive": "true",
            "backup_timer_enabled": "true",
            "backup_timer_active": "true",
            "candidate_absent": "true",
            "running_image_id": restored,
        }
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(
            supervisor, "verify_gate", return_value=document
        ), mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            result = supervisor.recovered_result_view(identifier)

        self.assertEqual(result["running_image_id"], restored)

    def test_recovered_result_rejects_invalid_restored_image_before_ssh(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        _, document, _, _ = self.recovered_release(identifier)
        production = json.loads((self.root / identifier / "gate" / "production-result.json").read_text(encoding="utf-8"))
        production["history"][0]["evidence"]["restored_image_id"] = "invalid"
        self.write(identifier, "gate/production-result.json", production)
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(
            supervisor, "verify_gate", return_value=document
        ), mock.patch.object(supervisor, "SSHRunner") as ssh:
            with self.assertRaisesRegex(RuntimeError, "restored image is invalid"):
                supervisor.recovered_result_view(identifier)
        ssh.assert_not_called()

    def test_recovered_result_rejects_missing_local_recovery_evidence(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        _, document, _, _ = self.recovered_release(identifier)
        production = json.loads((self.root / identifier / "gate" / "production-result.json").read_text(encoding="utf-8"))
        production["history"][0]["evidence"].pop("plaintext_state_removed")
        self.write(identifier, "gate/production-result.json", production)
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(
            supervisor, "verify_gate", return_value=document
        ), mock.patch.object(supervisor, "SSHRunner") as ssh:
            with self.assertRaisesRegex(RuntimeError, "recovery evidence is incomplete"):
                supervisor.recovered_result_view(identifier)
        ssh.assert_not_called()

    def test_recovered_result_rejects_candidate_still_running(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        _, document, candidate, _ = self.recovered_release(identifier)
        ssh = mock.Mock()
        ssh.run.return_value.values = {
            "recovery_marker_verified": "true",
            "release_claim_reconciled": "true",
            "plaintext_state_removed": "true",
            "active_slot_verified": "true",
            "application_health": "pass",
            "nginx_active": "true",
            "nginx_route_verified": "true",
            "backup_service_inactive": "true",
            "backup_timer_enabled": "true",
            "backup_timer_active": "true",
            "candidate_absent": "false",
            "running_image_id": candidate,
        }
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(
            supervisor, "verify_gate", return_value=document
        ), mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            with self.assertRaisesRegex(RuntimeError, "remote recovery evidence is incomplete"):
                supervisor.recovered_result_view(identifier)

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

    def test_reconciliation_blocks_until_runner_exit_is_proven(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {
            "status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1,
        })
        self.write(identifier, "gate/production-result.json", {
            "stage": "stage_assets_verified", "status": "blocked_reconciliation",
            "history": [{"stage": "stage_assets_verified"}],
        })
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {
            "active_claim": "matching", "consumed": "false", "recovered": "false",
            "state_present": "false", "app_health": "healthy", "nginx_active": "true",
            "backup_timer_enabled": "true", "running_image_id": "sha256:" + "a" * 64,
            "candidate_exists": "false", "candidate_health": "absent",
        }
        ssh = mock.Mock()
        ssh.run.return_value.values = remote
        with mock.patch.object(supervisor, "verify_gate", return_value=document), \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), \
             mock.patch.object(supervisor, "_runner_alive", return_value=False), \
             mock.patch.object(supervisor, "_process_token", return_value="token"):
            value = supervisor._inspect_reconciliation(identifier)
        self.assertEqual(value["decision"], "blocked")
        self.assertEqual(value["failure_code"], "runner_exit_unproven")
        self.assertFalse(value["runner_exit_proven"])

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

    def test_reconcile_verifies_remote_state_after_stderr_without_repeating_mutation(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        candidate = "sha256:" + "b" * 64
        self.write(identifier, "runner.json", {
            "status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1,
        })
        self.write(identifier, "gate/production-result.json", {
            "stage": "blocked_reconciliation", "status": "blocked_reconciliation",
            "history": [{"stage": "stage_assets_verified"}],
        })
        inspection = {
            "decision": "claim_only_recover", "runner_alive": False,
            "runner_exit_proven": True, "candidate_image_id": candidate,
        }
        remote = self.recovered_proof(candidate)
        ssh = mock.Mock()
        ssh.run.side_effect = [
            UnexpectedRemoteStderr("racknerd returned unexpected stderr", {
                "release_claim_reconciled": "true", "plaintext_state_removed": "true",
            }),
            mock.Mock(values=remote),
        ]
        state = mock.Mock()

        def receipt(run_path, runner, raw):
            saved = json.loads((Path(run_path) / "gate" / "production-result.json").read_text(encoding="utf-8"))
            self.assertEqual(saved["status"], "recovered")
            self.assertEqual(runner["status"], "recovered")

        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), \
             mock.patch.object(supervisor.RunState, "create", return_value=state), \
             mock.patch.object(supervisor, "publish_terminal_receipt", side_effect=receipt) as publish, \
             mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="interrupted"))

        self.assertEqual(ssh.run.call_count, 2)
        self.assertIn("cleanup-state.sh", ssh.run.call_args_list[0].args[1])
        self.assertIn("CLAIM_SHA256SUMS", ssh.run.call_args_list[1].args[1])
        self.assertNotIn("cleanup-state.sh", ssh.run.call_args_list[1].args[1])
        saved = json.loads((run_dir / "gate" / "production-result.json").read_text(encoding="utf-8"))
        self.assertEqual(saved["status"], "recovered")
        self.assertEqual(saved["history"][-1]["evidence"]["release_claim_reconciled"], "true")
        state.transition.assert_called_once()
        publish.assert_called_once()

    def test_reconcile_does_not_close_out_on_incomplete_recovery_proof(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        candidate = "sha256:" + "b" * 64
        self.write(identifier, "runner.json", {
            "status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1,
        })
        original = {
            "stage": "blocked_reconciliation", "status": "blocked_reconciliation",
            "history": [{"stage": "stage_assets_verified"}],
        }
        self.write(identifier, "gate/production-result.json", original)
        inspection = {
            "decision": "already_recovered", "runner_alive": False,
            "runner_exit_proven": True, "candidate_image_id": candidate,
        }
        remote = self.recovered_proof(candidate)
        remote["candidate_absent"] = "false"
        ssh = mock.Mock()
        ssh.run.return_value.values = remote
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), \
             mock.patch.object(supervisor, "publish_terminal_receipt") as publish:
            with self.assertRaisesRegex(RuntimeError, "remote recovery evidence is incomplete"):
                supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="interrupted"))
        self.assertEqual(ssh.run.call_count, 1)
        self.assertNotIn("reconcile.sh", ssh.run.call_args.args[1])
        saved = json.loads((run_dir / "gate" / "production-result.json").read_text(encoding="utf-8"))
        self.assertEqual(saved, original)
        self.assertEqual(json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))["status"], "blocked_reconciliation")
        publish.assert_not_called()

    def test_reconcile_does_not_probe_after_non_stderr_remote_failure(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        candidate = "sha256:" + "b" * 64
        self.write(identifier, "runner.json", {
            "status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1,
        })
        self.write(identifier, "gate/production-result.json", {
            "stage": "blocked_reconciliation", "status": "blocked_reconciliation",
            "history": [{"stage": "stage_assets_verified"}],
        })
        inspection = {
            "decision": "claim_only_recover", "runner_alive": False,
            "runner_exit_proven": True, "candidate_image_id": candidate,
        }
        ssh = mock.Mock()
        ssh.run.side_effect = RuntimeError("racknerd stage failed with exit code 1; remote stderr withheld")
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            with self.assertRaisesRegex(RuntimeError, "exit code 1"):
                supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="interrupted"))
        self.assertEqual(ssh.run.call_count, 1)

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
        self.assertIn('if test -n "$active_container";', probe)
        self.assertIn('grep -Fxq -- "$active_container" <<<"$container_names"', probe)
        self.assertIn('container_names=$(docker ps -a', probe)
        self.assertEqual(value["decision"], "coordinated_restore_required")

    def test_crashed_worker_reconciliation_classifies_ingress_transaction_states(self) -> None:
        expected_decisions = {
            "applied": "coordinated_restore_required",
            "rolled_back": "cleanup_completed_recover",
            "rollback_failed": "coordinated_restore_required",
            "recovery_restored": "cleanup_completed_recover",
            "unsafe": "coordinated_restore_required",
        }

        for transaction, expected_decision in expected_decisions.items():
            with self.subTest(transaction=transaction):
                identifier = f"198-ingress-{transaction.replace('_', '-')}-deadbeef"
                self.minimum_release(identifier)
                self.write(identifier, "runner.json", {
                    "status": "blocked_reconciliation", "pid": 123,
                    "process_token": "token", "exit_code": 1,
                })
                self.write(identifier, "gate/production-result.json", {
                    "stage": "blocked_reconciliation", "status": "blocked_reconciliation",
                    "history": [{"stage": "stage_assets_verified"}],
                })
                document = {
                    "manifest": {"release_id": identifier},
                    "evidence": {"candidate_image_id": "sha256:" + "b" * 64},
                }
                remote = {
                    "active_claim": "matching", "consumed": "false", "recovered": "false",
                    "state_present": "true", "ingress_transaction": transaction,
                    "plaintext_cleaned": "true", "route_started": "false",
                    "migration_started": "false", "app_health": "healthy",
                    "nginx_active": "true", "backup_timer_enabled": "true",
                    "running_image_id": "sha256:" + "a" * 64,
                    "candidate_exists": "true", "candidate_health": "healthy",
                }
                ssh = mock.Mock()
                ssh.run.return_value.values = remote

                with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch.object(supervisor, "_runner_alive", return_value=False):
                    value = supervisor._inspect_reconciliation(identifier)

                self.assertEqual(value["ingress_transaction"], transaction)
                self.assertEqual(value["decision"], expected_decision)
                probe = ssh.run.call_args.args[1]
                allowed_fields = ssh.run.call_args.args[2]
                self.assertIn("ingress_transaction=unsafe", probe)
                self.assertIn('test -L "$ingress_txn"', probe)
                self.assertIn("SHA256SUMS.files", probe)
                self.assertIn("ingress_transaction", allowed_fields)

    def test_reconciliation_without_runner_metadata_cannot_prove_worker_exit(self) -> None:
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

        self.assertEqual(value["decision"], "blocked")
        self.assertEqual(value["failure_code"], "runner_exit_unproven")
        self.assertFalse(value["runner_exit_proven"])

    def coordinated_recovery_fixture(self, decision: str = "coordinated_restore_required"):
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "release-state.json", {"schema": 1, "release_id": identifier, "stage": "production_release", "status": "blocked_reconciliation", "history": []})
        self.write(identifier, "gate/production-result.json", {"release_id": identifier, "stage": "blocked_reconciliation", "status": "blocked_reconciliation", "history": []})
        files = {}
        for name in recovery_bundle.REQUIRED:
            category = "release" if name.endswith(".py") else "maintenance/181" if name == "restore-backup-units.sh" else "maintenance/release"
            files[recovery_bundle.PREFIX + category + "/" + name] = "a" * 64
        closure = recovery_bundle.recovery_bundle_document("a" * 40, files)
        bundle_sha = recovery_bundle.recovery_bundle_sha256(closure)
        document = {"manifest": {"profile": "198", "commit_sha": "a" * 40,
                                 "release_asset_sha256": files, "recovery_helper_bundle_sha256": bundle_sha},
                    "evidence": {"vm_restore_verified": True, "integration_verified": True}}
        self.write(identifier, "gate/gate.json", document)
        assets = f"/opt/sub2api/releases/{identifier}/recovery-helpers/{bundle_sha}"
        inspection = {"decision": decision, "runner_alive": False, "runner_exit_proven": True}
        ssh = mock.Mock()
        ssh.run.return_value.values = {"coordinated_restore": "verified", "restored_image_id": "sha256:" + "c" * 64,
                                       "application_health": "pass", "backup_units_restored": "true",
                                       "release_claim_reconciled": "true", "plaintext_state_removed": "true",
                                       "recovery_helper_bundle_sha256": bundle_sha}
        return identifier, inspection, ssh, document, assets, bundle_sha

    def test_coordinated_recovery_uses_signed_whole_bundle_and_one_formal_command(self) -> None:
        identifier, inspection, ssh, document, assets, checksum = self.coordinated_recovery_fixture()
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document) as verify, \
             mock.patch.object(supervisor, "stage_recovery_bundle", return_value=(assets, checksum)) as stage, \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))
        gate = self.root / identifier / "gate"
        verify.assert_called_once_with(gate, supervisor.TRUSTED_VM_PUBLIC_KEY, "198", allow_expired=True, allow_historical_runner=True)
        stage.assert_called_once_with(ssh, document["manifest"], f"/opt/sub2api/releases/{identifier}",
                                      self.root / identifier / "recovery-helper-bundles", supervisor.WORKSPACE, gate)
        ssh.run.assert_called_once()
        command = ssh.run.call_args.args[1]
        self.assertIn(f"RELEASE_DIR=/opt/sub2api/releases/{identifier}", command)
        self.assertIn(f"RECOVERY_ASSETS_DIR={assets}", command)
        self.assertIn(f"RECOVERY_HELPER_BUNDLE_SHA256={checksum}", command)
        self.assertIn(f"RECOVERY_HELPER_GATE={assets}.authorization/gate.json", command)
        self.assertIn(f"RECOVERY_HELPER_SIGNATURE={assets}.authorization/gate.sig", command)
        self.assertIn('bash "$RECOVERY_ASSETS_DIR/restore.sh"', command)
        self.assertIn('bash "$RECOVERY_ASSETS_DIR/recovery-finish.sh"', command)
        self.assertNotIn(".active-release/assets", command)
        self.assertNotIn("coordinated-restore.", command)
        self.assertIn('>>"$SUB2API_RELEASE_RAW_LOG" 2>&1', command)
        self.assertEqual(ssh.run.call_args.kwargs["timeout"], 3300)
        self.assertEqual(ssh.run.call_args.args[2], set(recovery_bundle.RECOVERY_RESULT_FIELDS))
        production = json.loads((gate / "production-result.json").read_text())
        self.assertEqual(production["status"], "recovered")
        self.assertEqual(production["stage"], "recovered_after_coordinated_restore")
        self.assertEqual(production["history"][-1]["evidence"]["recovery_helper_bundle_sha256"], checksum)

    def test_coordinated_recovery_already_recovered_still_verifies_and_stages_bundle(self) -> None:
        identifier, inspection, ssh, document, assets, checksum = self.coordinated_recovery_fixture("already_recovered")
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document) as verify, \
             mock.patch.object(supervisor, "stage_recovery_bundle", return_value=(assets, checksum)) as stage, \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))
        verify.assert_called_once()
        stage.assert_called_once()
        ssh.run.assert_called_once()
        command = ssh.run.call_args.args[1]
        self.assertIn('bash "$RECOVERY_ASSETS_DIR/recovery-finish.sh"', command)
        self.assertIn('test -f "$RELEASE_DIR/recovery-checkpoints/nginx_restored.done"', command)
        self.assertNotIn("already_restored", command)
        self.assertNotIn(".active-release/assets", command)

    def test_coordinated_recovery_cleanup_marker_alone_cannot_skip_checkpoint_validation(self) -> None:
        identifier, inspection, ssh, document, assets, checksum = self.coordinated_recovery_fixture()
        inspection.update(ingress_transaction="recovery_restored", app_health="healthy", nginx_active="true",
                          backup_timer_enabled="true", running_image_id="sha256:" + "a" * 64, candidate_image_id="sha256:" + "b" * 64)
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document), \
             mock.patch.object(supervisor, "stage_recovery_bundle", return_value=(assets, checksum)) as stage, \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))
        stage.assert_called_once()
        ssh.run.assert_called_once()
        command = ssh.run.call_args.args[1]
        self.assertIn('test -f "$state/recovery-point.tar"', command)
        self.assertIn('test -f "$RELEASE_DIR/recovery-checkpoints/nginx_restored.done"', command)
        self.assertIn('bash "$RECOVERY_ASSETS_DIR/recovery-finish.sh"', command)
        self.assertNotIn("already_restored", command)
        self.assertNotIn(".active-release/assets", command)

    def test_coordinated_recovery_rejects_historical_gate_without_bundle_before_ssh(self) -> None:
        identifier, inspection, _, document, _, _ = self.coordinated_recovery_fixture()
        document["manifest"].pop("recovery_helper_bundle_sha256")
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document), \
             mock.patch.object(supervisor, "stage_recovery_bundle") as stage, \
             mock.patch.object(supervisor, "SSHRunner") as ssh:
            with self.assertRaisesRegex(RuntimeError, "helper binding is absent"):
                supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))
        stage.assert_not_called()
        ssh.assert_not_called()
        production = json.loads((self.root / identifier / "gate/production-result.json").read_text())
        self.assertEqual(production["status"], "blocked_reconciliation")

    def test_coordinated_recovery_stage_failure_never_executes_restore_or_finish(self) -> None:
        identifier, inspection, ssh, document, _, _ = self.coordinated_recovery_fixture()
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document), \
             mock.patch.object(supervisor, "stage_recovery_bundle", side_effect=RuntimeError("bundle stage refused")), \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh):
            with self.assertRaisesRegex(RuntimeError, "bundle stage refused"):
                supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover"))
        ssh.run.assert_not_called()
        production = json.loads((self.root / identifier / "gate/production-result.json").read_text())
        self.assertEqual(production["status"], "blocked_reconciliation")

    def test_coordinated_recovery_explicit_helper_gate_is_verified_without_historical_relaxation(self) -> None:
        identifier, inspection, ssh, document, assets, checksum = self.coordinated_recovery_fixture()
        external = self.root / "270-bbbbbbbbbbbb-2-cafebabe" / "gate"
        external.mkdir(parents=True)
        document["manifest"]["profile"] = "270"
        (external / "gate.json").write_bytes(canonical_json(document))
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection), \
             mock.patch.object(supervisor, "verify_gate", return_value=document) as verify, \
             mock.patch.object(supervisor, "stage_recovery_bundle", return_value=(assets, checksum)) as stage, \
             mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=identifier, mode="coordinated-recover", recovery_helper_gate=str(external)))
        verify.assert_called_once_with(external.resolve(), supervisor.TRUSTED_VM_PUBLIC_KEY, "270", allow_expired=False, allow_historical_runner=False)
        self.assertEqual(stage.call_args.args[-1], external.resolve())
        self.assertIn('bash "$RECOVERY_ASSETS_DIR/recovery-finish.sh"', ssh.run.call_args.args[1])

    def test_coordinated_recovery_rejects_active_runner(self) -> None:
        inspection = {"decision": "coordinated_restore_required", "runner_alive": True}
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection):
            with self.assertRaisesRegex(RuntimeError, "coordinated recovery is not allowed"):
                supervisor.reconcile(argparse.Namespace(release_id="198-aaaaaaaaaaaa-1-deadbeef", mode="coordinated-recover"))


if __name__ == "__main__":
    unittest.main()
