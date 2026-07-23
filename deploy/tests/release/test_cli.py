from __future__ import annotations

import argparse
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import cli


class DeployCommandTest(unittest.TestCase):
    def test_progress_output_failure_is_non_fatal(self) -> None:
        with mock.patch("builtins.print", side_effect=BrokenPipeError):
            cli.emit_progress("stage=doctor")

    def test_vm_failure_never_calls_production_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "bootstrap_production"), mock.patch.object(cli, "create_vm_gate", side_effect=RuntimeError("vm failed")), mock.patch.object(cli, "release") as production:
            with self.assertRaisesRegex(RuntimeError, "vm failed"):
                cli.deploy(args)
        production.assert_not_called()
        self.assertEqual(doctor.return_value.run.call_args_list[0].args[0], ("local", "vm", "dmit", "backup"))
        self.assertEqual(doctor.return_value.run.call_args_list[1].args[0], ("racknerd",))

    def test_verified_vm_gate_is_passed_to_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        gate = Path("gate")
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "bootstrap_production") as bootstrap, mock.patch.object(cli, "create_vm_gate", return_value=gate), mock.patch.object(cli, "release") as production, mock.patch("builtins.print"):
            cli.deploy(args)
        self.assertEqual(production.call_args.args[0].gate, str(gate))
        bootstrap.assert_called_once_with("182", doctor.return_value.runner)
        self.assertEqual(doctor.return_value.run.call_count, 2)

    def test_doctor_failure_stops_before_vm(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        with mock.patch.object(cli, "ReleaseDoctor") as doctor, mock.patch.object(cli, "bootstrap_production") as bootstrap, mock.patch.object(cli, "create_vm_gate") as vm:
            doctor.return_value.run.side_effect = RuntimeError("not ready")
            with self.assertRaisesRegex(RuntimeError, "not ready"):
                cli.deploy(args)
        vm.assert_not_called()
        bootstrap.assert_not_called()

    def test_bootstrap_failure_stops_before_vm(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        with mock.patch.object(cli, "ReleaseDoctor"), mock.patch.object(cli, "bootstrap_production", side_effect=RuntimeError("bootstrap failed")), mock.patch.object(cli, "create_vm_gate") as vm:
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
            release_id="199-aaaaaaaaaaaa-1-deadbeef",
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
                {"release_id": identifier, "profile": "199", "commit_sha": commit},
            )
            cli.RunState.create(run_dir / "state.json", identifier)
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}), mock.patch.object(cli.subprocess, "run") as child, mock.patch.object(cli, "verify_gate"):
                gate = cli.create_vm_gate("199", commit, identifier=identifier, acquire_lock=False)
        self.assertEqual(gate, run_dir / "gate")
        child.assert_called_once()

    def test_vm_gate_rejects_incomplete_preallocation(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            (root / identifier).mkdir(parents=True)
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "preallocated release state"):
                    cli.create_vm_gate("199", "a" * 40, identifier=identifier, acquire_lock=False)

    def test_vm_gate_rejects_mismatched_preallocated_state(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            run_dir = root / identifier
            run_dir.mkdir(parents=True)
            cli.write_manifest_once(
                run_dir / "manifest.json",
                {"release_id": identifier, "profile": "199", "commit_sha": commit},
            )
            cli.RunState.create(run_dir / "state.json", "199-bbbbbbbbbbbb-2-feedface")
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "release state identity"):
                    cli.create_vm_gate("199", commit, identifier=identifier, acquire_lock=False)

    def test_vm_gate_rejects_existing_output_path(self) -> None:
        identifier = "199-aaaaaaaaaaaa-1-deadbeef"
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "releases"
            run_dir = root / identifier
            run_dir.mkdir(parents=True)
            cli.write_manifest_once(
                run_dir / "manifest.json",
                {"release_id": identifier, "profile": "199", "commit_sha": commit},
            )
            cli.RunState.create(run_dir / "state.json", identifier)
            (run_dir / "gate").write_text("unsafe", encoding="utf-8")
            with mock.patch.object(cli, "RUN_ROOT", root), mock.patch.object(cli, "get_profile", return_value={"name": "199"}):
                with self.assertRaisesRegex(RuntimeError, "Gate output path"):
                    cli.create_vm_gate("199", commit, identifier=identifier, acquire_lock=False)


if __name__ == "__main__":
    unittest.main()
