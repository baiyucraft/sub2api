from __future__ import annotations

import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
import sys

sys.path.insert(0, str(DEPLOY_ROOT))

from release.environment import load_deployment_environment, pin_deployment_environment
from release.profiles import get_profile


def fixture_config() -> dict:
    return {
        "servers": {
            "app-node": {"host": "app.example.test", "user": "root", "proxy_via": "relay-node"},
            "relay-node": {"host": "relay.example.test", "user": "root"},
            "backup-node": {"host": "backup.example.test", "user": "root"},
            "upload-node": {"host": "upload.example.test", "user": "root"},
            "vm-node": {"host": "vm.example.test", "user": "root"},
        },
        "deployment": {
            "schema": 1,
            "public_domain": "sub2api.example.test",
            "health_routes": {
                "direct": {"address": "198.51.100.10", "observer": "app-node", "target_node": "app-node"},
                "relay": {"address": "198.51.100.20", "observer": "backup-node", "target_node": "app-node"},
            },
            "backup_upload": {"node": "upload-node", "user": "fixture", "key_path": "/tmp/fixture-key", "port": 2202},
            "relay_probe": {
                "allowed_sources": ["192.0.2.0/24"], "connect_port": 1030, "legacy_port": 8888,
                "listen_address": "192.0.2.10", "listen_port": 1080, "node": "relay-node",
                "service": "sub2api-relay.service",
            },
            "vm_health": {"address": "127.0.0.1", "port": 18080},
        },
    }


class DeploymentEnvironmentTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.config_path = Path(self.temporary.name) / ".ssh.local"
        self.write(fixture_config())

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def write(self, document: dict) -> None:
        self.config_path.write_text(yaml.safe_dump(document, sort_keys=False), encoding="utf-8")

    def test_loads_roles_routes_and_keeps_profiles_environment_free(self) -> None:
        environment = load_deployment_environment(self.config_path)
        self.assertEqual(environment.node_for_role("racknerd"), "app-node")
        self.assertEqual(environment.node_for_role("dmit"), "relay-node")
        self.assertEqual(environment.node_for_role("backup"), "upload-node")
        self.assertEqual(environment.direct_route["observer"], "app-node")
        self.assertEqual(environment.relay_route["observer"], "backup-node")
        profile = get_profile("182")
        self.assertNotIn("public_domain", profile)
        self.assertNotIn("direct_public_ip", profile)
        self.assertNotIn("relay_public_ip", profile)

    def test_invalid_node_reference_and_proxy_loop_fail_closed(self) -> None:
        document = fixture_config()
        document["deployment"]["health_routes"]["relay"]["observer"] = "missing-node"
        self.write(document)
        with self.assertRaisesRegex(RuntimeError, "node reference"):
            load_deployment_environment(self.config_path)

        document = fixture_config()
        document["servers"]["relay-node"]["proxy_via"] = "app-node"
        self.write(document)
        with self.assertRaisesRegex(RuntimeError, "loop"):
            load_deployment_environment(self.config_path)

    def test_configured_role_target_wins_over_legacy_alias_entry(self) -> None:
        document = fixture_config()
        document["servers"]["racknerd"] = {"host": "old.example.test", "user": "root"}
        self.write(document)
        environment = load_deployment_environment(self.config_path)
        self.assertEqual(environment.node_for_role("racknerd"), "app-node")

    def test_pinned_configuration_drift_is_rejected(self) -> None:
        with mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("SUB2API_DEPLOYMENT_CONFIG_SHA256", None)
            pinned = pin_deployment_environment(self.config_path)
            self.assertEqual(os.environ["SUB2API_DEPLOYMENT_CONFIG_SHA256"], pinned.sha256)
            document = fixture_config()
            document["deployment"]["vm_health"]["port"] = 18081
            self.write(document)
            with self.assertRaisesRegex(RuntimeError, "drifted"):
                pin_deployment_environment(self.config_path)

    def test_missing_or_symlinked_configuration_is_rejected(self) -> None:
        with self.assertRaises(RuntimeError):
            load_deployment_environment(Path(self.temporary.name) / "missing.local")
        link = Path(self.temporary.name) / "linked.local"
        try:
            link.symlink_to(self.config_path)
        except (OSError, NotImplementedError):
            self.skipTest("symlink creation is not available on this host")
        with self.assertRaisesRegex(RuntimeError, "unsafe"):
            load_deployment_environment(link)


if __name__ == "__main__":
    unittest.main()
