from __future__ import annotations

import argparse
import json
import sys
import tempfile
import time
import unittest
from datetime import timedelta
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import cli


class DeployCommandTest(unittest.TestCase):
    def test_progress_output_failure_is_non_fatal(self) -> None:
        with mock.patch("builtins.print", side_effect=BrokenPipeError):
            cli.emit_progress("stage=doctor")

    def test_since_parser_accepts_release_log_durations(self) -> None:
        self.assertEqual(cli._parse_since("30m"), timedelta(minutes=30))
        self.assertEqual(cli._parse_since("7d"), timedelta(days=7))
        with self.assertRaises(argparse.ArgumentTypeError):
            cli._parse_since("yesterday")

    def test_retention_unreadable_metadata_is_fail_closed(self) -> None:
        unreadable = mock.Mock()
        unreadable.is_symlink.side_effect = PermissionError("denied")
        self.assertEqual(cli._retention_read_json(unreadable), {})

    def test_retention_dry_run_persists_plan_and_apply_requires_checksum(self) -> None:
        identifier = "235-aaaaaaaaaaaa-1-deadbeef"
        current_identifier = "235-bbbbbbbbbbbb-2-cafebabe"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            logs = root / identifier / "logs"
            logs.mkdir(parents=True)
            (root / identifier / "manifest.json").write_text(
                json.dumps({"release_id": identifier, "created_at": int(time.time()) - 120 * 86400}),
                encoding="utf-8",
            )
            (root / identifier / "runner.json").write_text(json.dumps({"status": "verified"}), encoding="utf-8")
            (logs / "events.jsonl").write_text("{}\n", encoding="utf-8")
            current_dir = root / current_identifier
            current_dir.mkdir(parents=True)
            (current_dir / "manifest.json").write_text(
                json.dumps({"release_id": current_identifier, "created_at": int(time.time()) - 86400}),
                encoding="utf-8",
            )
            (current_dir / "runner.json").write_text(json.dumps({"status": "verified"}), encoding="utf-8")
            args = argparse.Namespace(mode="dry-run", plan_sha256=None, success_retention_days=90, keep_recent=0)
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch("builtins.print") as output:
                cli.retention(args)
            document = json.loads(output.call_args.args[0])
            self.assertEqual(document["delete"][0]["release_id"], identifier)
            self.assertRegex(document["plan_sha256"], r"^[0-9a-f]{64}$")
            with mock.patch.object(cli, "RUN_ROOT", root), self.assertRaisesRegex(RuntimeError, "requires --plan-sha256"):
                cli.retention(argparse.Namespace(mode="apply", plan_sha256=None, success_retention_days=90, keep_recent=0))
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch("builtins.print") as applied:
                cli.retention(argparse.Namespace(mode="apply", plan_sha256=document["plan_sha256"], success_retention_days=90, keep_recent=0))
            self.assertFalse(logs.exists())
            result = json.loads(applied.call_args.args[0])
            self.assertEqual(result["retention_status"], "completed")

    def test_retention_apply_rejects_plan_drift(self) -> None:
        identifier = "235-aaaaaaaaaaaa-1-deadbeef"
        current_identifier = "235-bbbbbbbbbbbb-2-cafebabe"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            logs = root / identifier / "logs"
            logs.mkdir(parents=True)
            (root / identifier / "manifest.json").write_text(
                json.dumps({"release_id": identifier, "created_at": int(time.time()) - 120 * 86400}),
                encoding="utf-8",
            )
            runner = root / identifier / "runner.json"
            runner.write_text(json.dumps({"status": "verified"}), encoding="utf-8")
            (logs / "events.jsonl").write_text("{}\n", encoding="utf-8")
            current_dir = root / current_identifier
            current_dir.mkdir(parents=True)
            (current_dir / "manifest.json").write_text(
                json.dumps({"release_id": current_identifier, "created_at": int(time.time()) - 86400}),
                encoding="utf-8",
            )
            (current_dir / "runner.json").write_text(json.dumps({"status": "verified"}), encoding="utf-8")
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch("builtins.print") as output:
                cli.retention(argparse.Namespace(mode="dry-run", plan_sha256=None, success_retention_days=90, keep_recent=0))
            checksum = json.loads(output.call_args.args[0])["plan_sha256"]
            runner.write_text(json.dumps({"status": "running"}), encoding="utf-8")
            with mock.patch.object(cli, "RUN_ROOT", root), self.assertRaisesRegex(RuntimeError, "drift"):
                cli.retention(argparse.Namespace(mode="apply", plan_sha256=checksum, success_retention_days=90, keep_recent=0))
            self.assertTrue(logs.exists())

    def test_vm_failure_never_calls_production_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40, deployment_mode="blue-green")
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "install_vm_validator") as install_unit, mock.patch.object(cli, "bootstrap_production"), mock.patch.object(cli, "create_vm_gate", side_effect=RuntimeError("vm failed")), mock.patch.object(cli, "release") as production:
            with self.assertRaisesRegex(RuntimeError, "vm failed"):
                cli.deploy(args)
        production.assert_not_called()
        self.assertEqual(doctor.return_value.run.call_args_list[0].args[0], ("local",))
        self.assertEqual(doctor.return_value.run.call_args_list[1].args[0], ("vm", "dmit", "backup"))
        self.assertEqual(doctor.return_value.run.call_args_list[2].args[0], ("racknerd",))
        install_unit.assert_called_once_with(doctor.return_value._ssh.return_value)

    def test_verified_vm_gate_is_passed_to_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40, deployment_mode="blue-green")
        gate = Path("gate")
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "install_vm_validator") as install_unit, mock.patch.object(cli, "bootstrap_production") as bootstrap, mock.patch.object(cli, "create_vm_gate", return_value=gate), mock.patch.object(cli, "release") as production, mock.patch("builtins.print"):
            cli.deploy(args)
        self.assertEqual(production.call_args.args[0].gate, str(gate))
        install_unit.assert_called_once_with(doctor.return_value._ssh.return_value)
        bootstrap.assert_called_once_with("182", doctor.return_value._ssh.return_value)
        self.assertEqual(doctor.return_value.run.call_count, 3)

    def test_doctor_failure_stops_before_vm(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40, deployment_mode="blue-green")
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "install_vm_validator") as install_unit, mock.patch.object(cli, "bootstrap_production") as bootstrap, mock.patch.object(cli, "create_vm_gate") as vm:
            doctor.return_value.run.side_effect = RuntimeError("not ready")
            with self.assertRaisesRegex(RuntimeError, "not ready"):
                cli.deploy(args)
        vm.assert_not_called()
        bootstrap.assert_not_called()
        install_unit.assert_not_called()

    def test_vm_release_unit_failure_stops_before_remote_doctor_and_gate(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40, deployment_mode="blue-green")
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "install_vm_validator", side_effect=RuntimeError("unit update failed")) as install_unit, mock.patch.object(cli, "bootstrap_production") as bootstrap, mock.patch.object(cli, "create_vm_gate") as vm:
            with self.assertRaisesRegex(RuntimeError, "unit update failed"):
                cli.deploy(args)
        self.assertEqual(doctor.return_value.run.call_args_list[0].args[0], ("local",))
        self.assertEqual(doctor.return_value.run.call_count, 1)
        install_unit.assert_called_once_with(doctor.return_value._ssh.return_value)
        bootstrap.assert_not_called()
        vm.assert_not_called()

    def test_bootstrap_failure_stops_before_vm(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40, deployment_mode="blue-green")
        with mock.patch.object(cli, "ReleaseDoctor"), mock.patch.object(cli, "install_vm_validator"), mock.patch.object(cli, "bootstrap_production", side_effect=RuntimeError("bootstrap failed")), mock.patch.object(cli, "create_vm_gate") as vm:
            with self.assertRaisesRegex(RuntimeError, "bootstrap failed"):
                cli.deploy(args)
        vm.assert_not_called()

    def test_bootstrap_production_uses_profile(self) -> None:
        args = argparse.Namespace(profile="182")
        with mock.patch.object(cli, "bootstrap_production", return_value={"production_bootstrap": "true"}) as bootstrap, mock.patch("builtins.print"):
            cli.production_bootstrap(args)
        bootstrap.assert_called_once_with("182")

    def test_production_cleanup_forwards_plan_checksum(self) -> None:
        args = argparse.Namespace(
            release_id="202-aaaaaaaaaaaa-1-deadbeef",
            mode="apply",
            plan_sha256="d" * 64,
        )
        with mock.patch("release.production_cleanup.cleanup_production", return_value={"cleanup_status": "completed"}) as cleanup, mock.patch(
            "builtins.print"
        ):
            cli.production_cleanup(args)
        cleanup.assert_called_once_with(args.release_id, "apply", "d" * 64)

    def test_vm_gate_accepts_matching_supervisor_preallocation(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            run_dir = root / identifier
            run_dir.mkdir(parents=True)
            cli.write_manifest_once(
                run_dir / "manifest.json",
                {"release_id": identifier, "profile": "199", "commit_sha": commit, "deployment_mode": "blue-green"},
            )
            cli.RunState.create(run_dir / "state.json", identifier)
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}), mock.patch.object(cli.subprocess, "run") as child, mock.patch.object(cli, "verify_gate"):
                gate = cli.create_vm_gate("199", commit, "blue-green", identifier=identifier, acquire_lock=False)
        self.assertEqual(gate, run_dir / "gate")
        child.assert_called_once()
        child_env = child.call_args.kwargs["env"]
        self.assertEqual(child_env["SUB2API_RELEASE_ID"], identifier)
        self.assertEqual(child_env["SUB2API_DEPLOYMENT_MODE"], "blue-green")
        self.assertEqual(child_env["SUB2API_EVENT_LOG"], str(run_dir / "logs" / "events.jsonl"))

    def test_vm_gate_rejects_incomplete_preallocation(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            (root / identifier).mkdir(parents=True)
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "preallocated release state"):
                    cli.create_vm_gate("199", "a" * 40, "blue-green", identifier=identifier, acquire_lock=False)

    def test_vm_gate_rejects_mismatched_preallocated_state(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            run_dir = root / identifier
            run_dir.mkdir(parents=True)
            cli.write_manifest_once(
                run_dir / "manifest.json",
                {"release_id": identifier, "profile": "199", "commit_sha": commit, "deployment_mode": "blue-green"},
            )
            cli.RunState.create(run_dir / "state.json", "199-bbbbbbbbbbbb-2-feedface")
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "release state identity"):
                    cli.create_vm_gate("199", commit, "blue-green", identifier=identifier, acquire_lock=False)

    def test_vm_gate_rejects_existing_output_path(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            run_dir = root / identifier
            run_dir.mkdir(parents=True)
            cli.write_manifest_once(
                run_dir / "manifest.json",
                {"release_id": identifier, "profile": "199", "commit_sha": commit, "deployment_mode": "blue-green"},
            )
            cli.RunState.create(run_dir / "state.json", identifier)
            (run_dir / "gate").write_text("unsafe", encoding="utf-8")
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "Gate output path"):
                    cli.create_vm_gate("199", commit, "blue-green", identifier=identifier, acquire_lock=False)

    def test_noninteractive_deploy_requires_explicit_mode_before_creating_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        with mock.patch.object(cli.sys.stdin, "isatty", return_value=False), mock.patch.object(cli, "ReleaseDoctor") as doctor:
            with self.assertRaisesRegex(RuntimeError, "requires --mode"):
                cli.deploy(args)
        doctor.assert_not_called()


if __name__ == "__main__":
    unittest.main()
