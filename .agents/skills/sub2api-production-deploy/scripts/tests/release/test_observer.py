from __future__ import annotations

import argparse
import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import observer


def status(**changes):
    value = {
        "release_id": "240-aaaaaaaaaaaa-1-deadbeef",
        "runner_status": "running",
        "runner_alive": True,
        "vm_stage": "vm_validate",
        "vm_status": "running",
        "production_stage": "not_started",
        "production_status": "not_started",
    }
    value.update(changes)
    return value


class ReleaseObserverTest(unittest.TestCase):
    def args(self):
        return argparse.Namespace(release_id="240-aaaaaaaaaaaa-1-deadbeef", heartbeat=60, lang="zh-CN")

    def test_follow_renders_chinese_and_verifies_terminal_result(self) -> None:
        terminal = status(
            runner_status="verified",
            runner_alive=False,
            vm_status="verified",
            production_stage="production_verified",
            production_status="verified",
        )
        result = {"running_image_id": "sha256:" + "a" * 64}
        with mock.patch.object(observer, "status_view", side_effect=[status(), terminal]), mock.patch.object(
            observer, "verified_result_view", return_value=result
        ), mock.patch.object(observer.time, "sleep"), mock.patch("builtins.print") as output:
            code = observer.follow(self.args())
        self.assertEqual(code, 0)
        rendered = "\n".join(call.args[0] for call in output.call_args_list)
        self.assertIn("正在执行 VM 验证", rendered)
        self.assertIn("发布已验真", rendered)
        self.assertNotIn("runner_status", rendered)

    def test_follow_interrupt_does_not_start_or_stop_runner(self) -> None:
        with mock.patch.object(observer, "status_view", side_effect=KeyboardInterrupt), mock.patch("builtins.print") as output:
            code = observer.follow(self.args())
        self.assertEqual(code, 130)
        self.assertIn("后台 runner 未被终止", output.call_args.args[0])

    def test_failed_release_outputs_only_stable_failure_code(self) -> None:
        failed = status(runner_status="failed", runner_alive=False, production_status="failed")
        with mock.patch.object(observer, "status_view", return_value=failed), mock.patch.object(
            observer, "_failure_hint", return_value="migration_241_preflight"
        ), mock.patch("builtins.print") as output:
            code = observer.follow(self.args())
        self.assertEqual(code, 1)
        rendered = "\n".join(call.args[0] for call in output.call_args_list)
        self.assertIn("migration_241_preflight", rendered)

    def test_deploy_follow_starts_once_then_attaches_same_release(self) -> None:
        args = argparse.Namespace(profile="240", commit="a" * 40, deployment_mode="downtime", heartbeat=60, lang="zh-CN")
        with mock.patch.object(observer, "start", return_value="240-aaaaaaaaaaaa-1-deadbeef") as start, mock.patch.object(
            observer, "follow", return_value=0
        ) as follow, mock.patch("builtins.print"):
            code = observer.deploy_follow(args)
        self.assertEqual(code, 0)
        start.assert_called_once_with(args, announce=False)
        follow.assert_called_once_with(args)
        self.assertEqual(args.release_id, "240-aaaaaaaaaaaa-1-deadbeef")


if __name__ == "__main__":
    unittest.main()
