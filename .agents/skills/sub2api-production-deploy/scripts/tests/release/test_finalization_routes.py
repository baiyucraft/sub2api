from __future__ import annotations

import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.finalization import REMOTE_REQUIRED, _remote_consumed
from release.supervisor import _prove_pre_state_runtime


class FinalizationRoutesTest(unittest.TestCase):
    def test_consumed_check_uses_configured_target_and_health_observers(self) -> None:
        environment = SimpleNamespace(
            public_domain="sub2api.example.test",
            direct_route={"address": "198.51.100.10", "observer": "direct-observer", "target_node": "app-node"},
            relay_route={"address": "198.51.100.20", "observer": "relay-observer", "target_node": "app-node"},
            assert_current=mock.Mock(),
        )
        candidate = "sha256:" + "a" * 64
        target_values = {key: value for key, value in REMOTE_REQUIRED.items() if key not in {"direct_health", "dmit_route_health"}}
        target_values["running_image_id"] = candidate
        ssh = SimpleNamespace(
            deployment_environment=environment,
            run=mock.Mock(side_effect=[
                SimpleNamespace(values=target_values),
                SimpleNamespace(values={"direct_health": "pass"}),
                SimpleNamespace(values={"dmit_route_health": "pass"}),
            ]),
        )
        manifest = {
            "release_id": "182-aaaaaaaaaaaa-1-deadbeef",
            "profile": "182",
            "release_asset_sha256": {
                ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/freeze.sh": "b" * 64,
            },
        }
        originals = {"gate/gate.json": "c" * 64, "gate/gate.sig": "d" * 64, "gate/candidate.tar.gz": "e" * 64}
        with mock.patch("release.finalization._retained_recovery_script", return_value=""):
            result = _remote_consumed(ssh, manifest, candidate, originals, backup_evidence={})

        self.assertEqual(result["direct_health"], "pass")
        self.assertEqual(result["dmit_route_health"], "pass")
        calls = ssh.run.call_args_list
        self.assertEqual([call.args[0] for call in calls], ["app-node", "direct-observer", "relay-observer"])
        self.assertIn("198.51.100.10", calls[1].args[1])
        self.assertIn("198.51.100.20", calls[2].args[1])
        self.assertTrue(all(call.args[2] for call in calls))

    def test_pre_state_recovery_checks_both_configured_route_observers(self) -> None:
        environment = SimpleNamespace(
            public_domain="sub2api.example.test",
            direct_route={"address": "198.51.100.10", "observer": "direct-observer", "target_node": "app-node"},
            relay_route={"address": "198.51.100.20", "observer": "relay-observer", "target_node": "app-node"},
            assert_current=mock.Mock(),
        )
        ssh = mock.Mock()
        ssh.deployment_environment = environment
        ssh.run.side_effect = [
            mock.Mock(values={"public_health": "pass"}),
            mock.Mock(values={"public_health": "pass"}),
        ]
        doctor = mock.Mock()
        doctor.run.return_value = {"racknerd_ready": "true", "dmit_ready": "true", "production_current_image_id": "image"}
        document = {
            "manifest": {"profile": "182", "commit_sha": "a" * 40},
            "evidence": {"production_current_image_id": "image"},
        }
        with (
            mock.patch("release.supervisor.SSHRunner", return_value=ssh),
            mock.patch("release.supervisor.ReleaseDoctor", return_value=doctor),
        ):
            result = _prove_pre_state_runtime(document)

        self.assertEqual(result["direct_route_health"], "pass")
        self.assertEqual(result["dmit_route_health"], "pass")
        self.assertEqual([call.args[0] for call in ssh.run.call_args_list], ["direct-observer", "relay-observer"])
        self.assertIn("198.51.100.10", ssh.run.call_args_list[0].args[1])
        self.assertIn("198.51.100.20", ssh.run.call_args_list[1].args[1])


if __name__ == "__main__":
    unittest.main()
