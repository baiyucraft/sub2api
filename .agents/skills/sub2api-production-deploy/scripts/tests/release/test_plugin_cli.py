from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
ENTRYPOINT = DEPLOY_ROOT / "release.py"


PLUGIN_COMMANDS = (
    "plugin-deploy-start",
    "plugin-deploy-follow",
    "plugin-authorize",
    "plugin-status",
    "plugin-follow",
    "plugin-wait",
    "plugin-verify-result",
    "plugin-rollback-start",
    "_plugin-deploy-worker",
)


class PluginCLITest(unittest.TestCase):
    def help(self, command: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [sys.executable, str(ENTRYPOINT), command, "--help"],
            cwd=DEPLOY_ROOT,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            check=False,
        )

    def test_all_plugin_release_commands_are_registered(self) -> None:
        for command in PLUGIN_COMMANDS:
            with self.subTest(command=command):
                result = self.help(command)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("usage:", result.stdout.lower())

    def test_authorize_accepts_no_secret_command_line_flags(self) -> None:
        result = self.help("plugin-authorize")
        self.assertEqual(result.returncode, 0, result.stderr)
        combined = (result.stdout + result.stderr).lower()
        for forbidden in ("--password", "--totp", "--token", "--jwt", "--cookie", "--authorization"):
            self.assertNotIn(forbidden, combined)


if __name__ == "__main__":
    unittest.main()
