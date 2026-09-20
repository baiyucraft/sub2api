from __future__ import annotations

import importlib
import json
import sys
import tempfile
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))


def load_module():
    return importlib.import_module("release.plugin_credentials")


class PluginCredentialConfigTest(unittest.TestCase):
    def test_loads_target_admin_api_key_without_prompting(self) -> None:
        module = load_module()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / ".ssh.local"
            path.write_text(
                """plugin_admin:
  vm:
    api_key: vm-admin-key
  production:
    api_key: production-admin-key
""",
                encoding="utf-8",
            )
            encoded = module.load_plugin_admin_credentials("production", path=path)
        payload = json.loads(bytes(encoded))
        self.assertEqual(payload, {"admin_api_key": "production-admin-key"})

    def test_missing_admin_api_key_fails_before_remote_write(self) -> None:
        module = load_module()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / ".ssh.local"
            path.write_text(
                """plugin_admin:
  vm:
    email: unused@example.test
""",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "api_key"):
                module.load_plugin_admin_credentials("vm", path=path)

    def test_symlinked_config_is_rejected(self) -> None:
        module = load_module()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "real.yaml"
            target.write_text("plugin_admin: {}\n", encoding="utf-8")
            link = root / ".ssh.local"
            try:
                link.symlink_to(target)
            except OSError:
                self.skipTest("symlinks are unavailable")
            with self.assertRaisesRegex(RuntimeError, "unavailable"):
                module.load_plugin_admin_credentials("vm", path=link)


if __name__ == "__main__":
    unittest.main()
