from __future__ import annotations

import argparse
import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import cli


class DeployCommandTest(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
