from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import supervisor
from release.atomic import canonical_json


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
        self.write(identifier, "manifest.json", {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40})
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

        manifest = {"release_id": "placeholder", "profile": "198", "commit_sha": "a" * 40}
        with mock.patch.object(supervisor, "get_profile", return_value={"name": "198"}), mock.patch.object(supervisor, "create_manifest", side_effect=lambda commit, profile, identifier: {**manifest, "release_id": identifier}), mock.patch.object(supervisor, "runner_checksum", return_value="c" * 64), mock.patch.object(supervisor.subprocess, "Popen", side_effect=launch), mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch("builtins.print") as output:
            supervisor.start(argparse.Namespace(profile="198", commit="a" * 40))
        run_dir = next(self.root.iterdir())
        runner = json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual(runner["pid"], 456)
        self.assertEqual(runner["process_token"], "token")
        self.assertEqual(runner["runner_sha256"], "c" * 64)
        self.assertNotIn("secret", json.dumps(runner))
        self.assertIn(str(supervisor.DEPLOY_ROOT / "release.py"), captured["command"])
        self.assertIn("runner=started", output.call_args.args[0])

    def test_new_commands_expose_help(self) -> None:
        for command in ("deploy-start", "status", "wait", "verify-result", "reconcile-inspect", "reconcile"):
            result = subprocess.run([sys.executable, str(DEPLOY_ROOT / "release.py"), command, "--help"], cwd=DEPLOY_ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            self.assertEqual(result.returncode, 0, command)

    def test_worker_holds_one_lock_across_deploy(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        runner = {"schema": 1, "release_id": identifier, "profile": "198", "commit": "a" * 40, "pid": os.getpid(), "process_token": "token", "status": "starting", "exit_code": None}
        self.write(identifier, "runner.json", runner)
        lock = mock.MagicMock()
        with mock.patch.object(supervisor, "_process_token", return_value="token"), mock.patch.object(supervisor, "RunLock", return_value=lock), mock.patch("release.cli.deploy") as deploy:
            supervisor.worker(argparse.Namespace(release_id=identifier, profile="198", commit="a" * 40))
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
        document = {"manifest": {"release_id": identifier, "profile": "198", "commit_sha": "a" * 40}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        with mock.patch.object(supervisor, "_runner_alive", return_value=False), mock.patch.object(supervisor, "verify_gate", return_value=document):
            with self.assertRaisesRegex(RuntimeError, "evidence is incomplete"):
                supervisor.verify_result(argparse.Namespace(release_id=identifier))

    def test_claim_only_interruption_decision(self) -> None:
        identifier = "198-aaaaaaaaaaaa-1-deadbeef"
        run_dir = self.minimum_release(identifier)
        self.write(identifier, "runner.json", {"status": "blocked_reconciliation", "pid": 123, "process_token": "token", "exit_code": 1})
        self.write(identifier, "gate/production-result.json", {"stage": "stage_assets_verified", "status": "blocked_reconciliation", "history": [{"stage": "stage_assets"}, {"stage": "stage_assets_verified"}]})
        document = {"manifest": {"release_id": identifier}, "evidence": {"candidate_image_id": "sha256:" + "b" * 64}}
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "false", "app_health": "healthy", "nginx_active": "true", "backup_timer_enabled": "true", "running_image_id": "sha256:" + "a" * 64}
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
        remote = {"active_claim": "matching", "consumed": "false", "recovered": "false", "state_present": "false", "app_health": "healthy", "nginx_active": "true", "backup_timer_enabled": "true", "running_image_id": "unknown"}
        ssh = mock.Mock()
        ssh.run.return_value.values = remote
        with mock.patch.object(supervisor, "verify_gate", return_value=document), mock.patch.object(supervisor, "SSHRunner", return_value=ssh), mock.patch.object(supervisor, "_runner_alive", return_value=False):
            value = supervisor._inspect_reconciliation(identifier)
        self.assertEqual(value["decision"], "blocked")


if __name__ == "__main__":
    unittest.main()
