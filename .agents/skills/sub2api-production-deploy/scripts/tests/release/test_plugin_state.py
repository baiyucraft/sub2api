from __future__ import annotations

import importlib
import json
import sys
import tempfile
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))


STAGE_CHAIN = (
    "initialized",
    "package_built",
    "local_verified",
    "awaiting_vm_authorization",
    "vm_gate_verified",
    "awaiting_production_authorization",
    "production_preflight_verified",
    "install_or_upgrade_started",
    "installation_committed",
    "instances_verified",
    "verified",
)


def load_plugin_state():
    try:
        return importlib.import_module("release.plugin_state")
    except ModuleNotFoundError as error:
        raise AssertionError("release.plugin_state must implement the plugin release state contract") from error


class PluginRunStateTest(unittest.TestCase):
    def create_state(self, directory: str):
        module = load_plugin_state()
        path = Path(directory) / "state.json"
        state = module.PluginRunState.create(
            path,
            "baiyu.codex-state-0.1.0-deadbeef",
            "baiyu.codex-state",
            "a" * 40,
        )
        return module, state, path

    def test_declared_stage_chain_is_explicit_and_ordered(self) -> None:
        module = load_plugin_state()
        self.assertEqual(tuple(module.PLUGIN_STAGES), STAGE_CHAIN)

    def test_valid_stage_chain_persists_each_transition(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            _module, state, path = self.create_state(temporary)
            self.assertEqual(state.value["stage"], "initialized")
            for stage in STAGE_CHAIN[1:]:
                status = "verified" if stage == "verified" else "running"
                state.transition(stage, status, evidence={"contract_marker": stage})
                persisted = json.loads(path.read_text(encoding="utf-8"))
                self.assertEqual(persisted["stage"], stage)
                self.assertEqual(persisted["history"][-1]["evidence"]["contract_marker"], stage)
            self.assertEqual(state.value["status"], "verified")

    def test_skipping_or_reversing_a_stage_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            _module, state, _path = self.create_state(temporary)
            with self.assertRaisesRegex(RuntimeError, "transition"):
                state.transition("vm_gate_verified")
            with self.assertRaisesRegex(RuntimeError, "transition|verified plugin release"):
                state.transition("verified", "verified")
            state.transition("package_built")
            with self.assertRaisesRegex(RuntimeError, "transition"):
                state.transition("initialized")

    def test_terminal_state_cannot_resume_without_reconciliation(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            _module, state, _path = self.create_state(temporary)
            state.fail(
                "initialized",
                blocked=True,
                evidence={"reason_code": "write_response_uncertain"},
            )
            self.assertEqual(state.value["status"], "blocked_reconciliation")
            with self.assertRaisesRegex(RuntimeError, "terminal|reconciliation"):
                state.transition("production_preflight_verified")

    def test_failure_must_retain_the_current_stage(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            _module, state, _path = self.create_state(temporary)
            with self.assertRaisesRegex(RuntimeError, "current stage"):
                state.fail("install_or_upgrade_started", blocked=True)
            self.assertEqual((state.value["stage"], state.value["status"]), ("initialized", "running"))

    def test_invalid_persisted_stage_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            module, state, path = self.create_state(temporary)
            raw = dict(state.value)
            raw["stage"] = "vm_gate"
            path.write_text(json.dumps(raw), encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "invalid plugin release state"):
                module.PluginRunState.load(path)

    def test_sensitive_evidence_is_rejected_and_never_persisted(self) -> None:
        sentinel = "plugin-secret-sentinel"
        with tempfile.TemporaryDirectory() as temporary:
            _module, state, path = self.create_state(temporary)
            for key in ("api_key", "password", "totp", "authorization", "cookie", "jwt"):
                with self.subTest(key=key), self.assertRaisesRegex(ValueError, "sensitive"):
                    state.transition("package_built", evidence={key: sentinel})
                self.assertNotIn(sentinel, path.read_text(encoding="utf-8"))
            self.assertEqual(state.value["stage"], "initialized")


if __name__ == "__main__":
    unittest.main()
