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
        with mock.patch.object(cli, "create_vm_gate", side_effect=RuntimeError("vm failed")), mock.patch.object(cli, "release") as production:
            with self.assertRaisesRegex(RuntimeError, "vm failed"):
                cli.deploy(args)
        production.assert_not_called()

    def test_verified_vm_gate_is_passed_to_release(self) -> None:
        args = argparse.Namespace(profile="182", commit="a" * 40)
        gate = Path("gate")
        with mock.patch.object(cli, "create_vm_gate", return_value=gate), mock.patch.object(cli, "release") as production, mock.patch("builtins.print"):
            cli.deploy(args)
        self.assertEqual(production.call_args.args[0].gate, str(gate))


if __name__ == "__main__":
    unittest.main()
