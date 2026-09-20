from __future__ import annotations

import importlib
import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))


def load_plugin_api():
    try:
        return importlib.import_module("release.plugin_api")
    except ModuleNotFoundError as error:
        raise AssertionError("release.plugin_api must implement package installation decisions") from error


class PluginPackageDecisionTest(unittest.TestCase):
    def test_missing_plugin_selects_disabled_first_install(self) -> None:
        module = load_plugin_api()
        self.assertEqual(module.select_operation(None, "0.1.0", "a" * 64), "install")
        self.assertIn('if operation == "install" and after.get("state") != "disabled"', module._REMOTE_HELPER)

    def test_matching_version_and_package_selects_noop(self) -> None:
        module = load_plugin_api()
        existing = {
            "plugin_key": "baiyu.codex-state",
            "version": "0.1.0",
            "binary_sha256": "a" * 64,
        }
        self.assertEqual(module.select_operation(existing, "0.1.0", "a" * 64), "no-op")

    def test_changed_package_selects_upgrade_and_preserves_runtime_contract(self) -> None:
        module = load_plugin_api()
        existing = {
            "plugin_key": "baiyu.codex-state",
            "version": "0.0.9",
            "binary_sha256": "b" * 64,
        }
        self.assertEqual(module.select_operation(existing, "0.1.0", "a" * 64), "upgrade")
        self.assertIn("config_revision_preserved=", module._REMOTE_HELPER)
        self.assertIn("managed_scope_digest_preserved=", module._REMOTE_HELPER)
        self.assertIn('if operation == "upgrade" and after.get("state") != before_state', module._REMOTE_HELPER)

    def test_same_version_with_different_package_is_not_a_noop(self) -> None:
        module = load_plugin_api()
        existing = {
            "plugin_key": "baiyu.codex-state",
            "version": "0.1.0",
            "binary_sha256": "c" * 64,
        }
        self.assertEqual(module.select_operation(existing, "0.1.0", "a" * 64), "upgrade")

    def test_mismatched_plugin_identity_fails_closed(self) -> None:
        module = load_plugin_api()
        with self.assertRaisesRegex(RuntimeError, "identity"):
            module.select_operation(
                {"plugin_key": "other.plugin", "version": "0.1.0", "binary_sha256": "a" * 64},
                "0.1.0",
                "a" * 64,
            )


if __name__ == "__main__":
    unittest.main()
