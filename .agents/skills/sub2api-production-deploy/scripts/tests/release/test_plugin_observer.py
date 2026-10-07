from __future__ import annotations

import argparse
import importlib
import sys
import unittest
from pathlib import Path
from unittest import mock


sys.path.insert(0, str(Path(__file__).resolve().parents[2]))


class PluginObserverTest(unittest.TestCase):
    def setUp(self) -> None:
        self.module = importlib.import_module("release.plugin_observer")
        settings = mock.patch("release.vm_lifecycle.load_settings", return_value=None)
        settings.start()
        self.addCleanup(settings.stop)
        self.args = argparse.Namespace(release_id="plugin-release", heartbeat=1)

    def test_follow_waits_for_vm_shutdown_after_production_verified(self) -> None:
        views = [
            {"stage": "verified", "status": "verified", "runner_status": "running", "vm_power_status": "stopping", "vm_cleanup_status": "pending"},
            {"stage": "verified", "status": "verified", "runner_status": "verified", "runner_exit": 0, "vm_power_status": "stopped", "vm_cleanup_status": "verified"},
        ]
        with mock.patch.object(self.module, "status_view", side_effect=views) as status, mock.patch.object(self.module.time, "sleep"), mock.patch("builtins.print"):
            result = self.module.follow(self.args)
        self.assertEqual(result, 0)
        self.assertEqual(status.call_count, 2)

    def test_follow_reports_cleanup_failure_and_retains_production_success(self) -> None:
        view = {"stage": "verified", "status": "verified", "runner_status": "failed", "runner_exit": 1, "vm_power_status": "running", "vm_cleanup_status": "failed"}
        with mock.patch.object(self.module, "status_view", return_value=view), mock.patch("builtins.print") as output:
            result = self.module.follow(self.args)
        self.assertEqual(result, 2)
        self.assertTrue(any("生产插件验证证据已保留" in str(call) for call in output.call_args_list))

    def test_follow_legacy_verified_status_remains_compatible(self) -> None:
        with mock.patch.object(self.module, "status_view", return_value={"stage": "verified", "status": "verified"}), mock.patch("builtins.print"):
            self.assertEqual(self.module.follow(self.args), 0)

    def test_follow_blocked_release_returns_failure(self) -> None:
        with mock.patch.object(self.module, "status_view", return_value={"stage": "install_or_upgrade_started", "status": "blocked_reconciliation"}), mock.patch("builtins.print"):
            self.assertEqual(self.module.follow(self.args), 2)


if __name__ == "__main__":
    unittest.main()
