from __future__ import annotations

import argparse
import importlib
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))


def load_plugin_supervisor():
    try:
        return importlib.import_module("release.plugin_supervisor")
    except (ImportError, ModuleNotFoundError) as error:
        raise AssertionError("release.plugin_supervisor must be importable for plugin release verification") from error


class PluginSupervisorTest(unittest.TestCase):
    release_id = "codex-state-aaaaaaaaaaaa-1-deadbeef"

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "plugin-releases"
        self.root.mkdir()
        self.module = load_plugin_supervisor()
        self.root_patch = mock.patch.object(self.module, "PLUGIN_RUN_ROOT", self.root)
        self.run_root_patch = mock.patch.object(self.module, "RUN_ROOT", self.root / "host-releases")
        self.root_patch.start()
        self.run_root_patch.start()

    def tearDown(self) -> None:
        self.run_root_patch.stop()
        self.root_patch.stop()
        self.temporary.cleanup()

    def run_dir(self) -> Path:
        path = self.root / self.release_id
        path.mkdir(parents=True, exist_ok=True)
        return path

    def write(self, name: str, value: dict[str, object]) -> None:
        path = self.run_dir() / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value), encoding="utf-8")

    def awaiting_production_state(self):
        state = self.module.PluginRunState.create(
            self.run_dir() / "state.json",
            self.release_id,
            "baiyu.codex-state",
            "a" * 40,
        )
        for stage in (
            "package_built",
            "local_verified",
            "awaiting_vm_authorization",
            "vm_gate_verified",
            "awaiting_production_authorization",
        ):
            state.transition(stage)
        self.write(
            "package.json",
            {
                "plugin_id": "baiyu.codex-state",
                "version": "0.1.0",
                "signature_key_id": "production-key",
                "arches": {},
            },
        )
        self.write("runner.json", {"status": "awaiting_authorization", "exit_code": 0})
        return state

    def apply_result(self, **changes: str):
        values = {
            "operation": "upgrade",
            "installation_id": "7",
            "previous_version": "0.0.9",
            "previous_binary_sha256": "c" * 64,
            "target_version": "0.1.0",
            "previous_state": "enabled",
            "final_state": "enabled",
            "binary_sha256": "b" * 64,
            "signature_status": "trusted",
            "config_revision_preserved": "true",
            "managed_scope_digest_preserved": "true",
            "runtime_healthy": "true",
            "instances_expected": "2",
            "instances_verified": "2",
            "restoration_status": "not_required",
            "write_uncertain": "false",
        }
        values.update(changes)
        plugin_api = importlib.import_module("release.plugin_api")
        return plugin_api.PluginApplyResult(values)

    def identity(self):
        return self.module.PackageIdentity(
            path=self.run_dir() / "artifacts" / "plugin.s2plugin",
            arch="amd64",
            version="0.1.0",
            package_sha256="a" * 64,
            binary_sha256="b" * 64,
            key_id="production-key",
            signature_status="trusted",
        )

    def test_uncertain_write_enters_blocked_reconciliation_without_retry(self) -> None:
        self.awaiting_production_state()
        api = mock.Mock()
        api.apply.return_value = self.apply_result(write_uncertain="true", instances_verified="0")
        with (
            mock.patch.object(self.module, "_identity", return_value=self.identity()),
            mock.patch.object(self.module, "PluginAPIClient", return_value=api),
            mock.patch.object(self.module, "load_plugin_admin_credentials", return_value=bytearray(b"credentials")),
            self.assertRaisesRegex(RuntimeError, "reconciliation"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        self.assertEqual(state["status"], "blocked_reconciliation")
        self.assertTrue(state["history"][-1]["evidence"]["write_uncertain"])
        api.apply.assert_called_once()

    def test_upgrade_reaches_verified_only_when_preservation_and_instances_pass(self) -> None:
        self.awaiting_production_state()
        api = mock.Mock()
        api.apply.return_value = self.apply_result()
        with (
            mock.patch.object(self.module, "_identity", return_value=self.identity()),
            mock.patch.object(self.module, "PluginAPIClient", return_value=api),
            mock.patch.object(self.module, "load_plugin_admin_credentials", return_value=bytearray(b"credentials")),
            mock.patch("builtins.print"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        self.assertEqual((state["stage"], state["status"]), ("verified", "verified"))
        production = json.loads((self.run_dir() / "production-result.json").read_text(encoding="utf-8"))
        self.assertEqual(production["instances_expected"], production["instances_verified"])
        self.assertEqual(production["final_state"], "enabled")

    def test_missing_instance_prevents_verify_result(self) -> None:
        state = self.module.PluginRunState.create(
            self.run_dir() / "state.json",
            self.release_id,
            "baiyu.codex-state",
            "a" * 40,
        )
        for stage in (
            "package_built",
            "local_verified",
            "awaiting_vm_authorization",
            "vm_gate_verified",
            "awaiting_production_authorization",
            "production_preflight_verified",
            "install_or_upgrade_started",
            "installation_committed",
            "instances_verified",
        ):
            state.transition(stage)
        state.transition("verified", "verified")
        self.write(
            "manifest.json",
            {
                "schema": 1,
                "release_id": self.release_id,
                "plugin_id": "baiyu.codex-state",
                "source_commit": "a" * 40,
            },
        )
        self.write("package.json", {"version": "0.1.0", "arches": {"amd64": {"binary_sha256": "b" * 64}}})
        self.write(
            "production-result.json",
            {
                "status": "verified",
                "target_version": "0.1.0",
                "binary_sha256": "b" * 64,
                "signature_status": "trusted",
                "instances_expected": "2",
                "instances_verified": "1",
            },
        )
        with self.assertRaisesRegex(RuntimeError, "instance verification is incomplete"):
            self.module.verify_result(argparse.Namespace(release_id=self.release_id))

    def test_preservation_failure_blocks_after_committed_write(self) -> None:
        self.awaiting_production_state()
        api = mock.Mock()
        api.apply.return_value = self.apply_result(config_revision_preserved="false")
        with (
            mock.patch.object(self.module, "_identity", return_value=self.identity()),
            mock.patch.object(self.module, "PluginAPIClient", return_value=api),
            mock.patch.object(self.module, "load_plugin_admin_credentials", return_value=bytearray(b"credentials")),
            self.assertRaisesRegex(RuntimeError, "verification failed"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        self.assertEqual(state["status"], "blocked_reconciliation")
        api.apply.assert_called_once()

    def test_worker_runs_vm_and_production_writes_automatically(self) -> None:
        run_dir = self.run_dir()
        self.write(
            "manifest.json",
            {
                "schema": 1,
                "release_id": self.release_id,
                "plugin_id": "baiyu.codex-state",
                "source_commit": "a" * 40,
            },
        )
        self.write("runner.json", {"status": "starting", "exit_code": None})
        self.module.PluginRunState.create(
            run_dir / "state.json",
            self.release_id,
            "baiyu.codex-state",
            "a" * 40,
        )
        identities = {"amd64": self.identity(), "arm64": self.identity()}

        def advance(_args) -> None:
            state = self.module.PluginRunState.load(run_dir / "state.json")
            if state.value["stage"] == "awaiting_vm_authorization":
                state.transition("vm_gate_verified")
                state.transition("awaiting_production_authorization")
                return
            for stage in (
                "production_preflight_verified",
                "install_or_upgrade_started",
                "installation_committed",
                "instances_verified",
            ):
                state.transition(stage)
            state.transition("verified", "verified")

        package = {
            "plugin_id": "baiyu.codex-state",
            "version": "0.1.0",
            "signature_key_id": "production-key",
            "arches": {
                "amd64": {"package_sha256": "a" * 64},
                "arm64": {"package_sha256": "a" * 64},
            },
        }
        with (
            mock.patch.object(self.module, "build_packages", return_value=identities),
            mock.patch.object(self.module, "package_manifest", return_value=package),
            mock.patch.object(self.module, "authorize", side_effect=advance) as authorize,
        ):
            self.module.worker(argparse.Namespace(release_id=self.release_id, commit="a" * 40))

        self.assertEqual(authorize.call_count, 2)
        state = self.module.PluginRunState.load(run_dir / "state.json").value
        runner = json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual((state["stage"], state["status"]), ("verified", "verified"))
        self.assertEqual((runner["status"], runner["exit_code"]), ("verified", 0))

    def test_start_accepts_worker_that_finishes_before_handshake_poll(self) -> None:
        process = mock.Mock(pid=123)

        def complete_immediately(*_args, **_kwargs):
            run_dir = next(self.root.iterdir())
            runner = json.loads((run_dir / "runner.json").read_text(encoding="utf-8"))
            runner.update({"status": "verified", "exit_code": 0})
            (run_dir / "runner.json").write_text(json.dumps(runner), encoding="utf-8")
            return process

        with (
            mock.patch.object(self.module, "validate_source_commit", return_value="a" * 40),
            mock.patch.object(self.module, "popen_detached_worker", side_effect=complete_immediately),
            mock.patch.object(self.module, "_process_token", return_value="token"),
            mock.patch("builtins.print"),
        ):
            release_id = self.module.start(argparse.Namespace(commit="a" * 40))

        self.assertTrue(release_id.startswith("codex-state-aaaaaaaaaaaa-"))


if __name__ == "__main__":
    unittest.main()
