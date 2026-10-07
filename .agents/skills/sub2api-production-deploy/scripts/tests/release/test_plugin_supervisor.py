from __future__ import annotations

import argparse
import importlib
import json
import sys
import tempfile
import unittest
from contextlib import ExitStack, contextmanager
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
        self.settings_patch = mock.patch("release.vm_lifecycle.load_settings", return_value=None)
        self.settings_patch.start()
        self.real_lease = self.module.VMLease
        self.real_guard = self.module.vm_guard
        self.root_patch = mock.patch.object(self.module, "PLUGIN_RUN_ROOT", self.root)
        self.run_root_patch = mock.patch.object(self.module, "RUN_ROOT", self.root / "host-releases")
        self.root_patch.start()
        self.run_root_patch.start()
        self.lease = mock.MagicMock()
        self.lease.enabled = True
        self.lease.__enter__.return_value = self.lease
        self.lease_patch = mock.patch.object(self.module, "VMLease", return_value=self.lease)
        self.lifecycle_patch = mock.patch.object(self.module, "lifecycle_view", return_value={})
        self.guard_patch = mock.patch.object(self.module, "vm_guard")
        self.environment_patch = mock.patch.dict(self.module.os.environ)
        self.lease_factory = self.lease_patch.start()
        self.lifecycle_patch.start()
        self.guard_factory = self.guard_patch.start()
        self.environment_patch.start()

    def tearDown(self) -> None:
        self.environment_patch.stop()
        self.lifecycle_patch.stop()
        self.guard_patch.stop()
        self.lease_patch.stop()
        self.settings_patch.stop()
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
        self.write_manifest()
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
                "arches": {"amd64": {"binary_sha256": "b" * 64}},
            },
        )
        self.write("runner.json", {"status": "awaiting_authorization", "exit_code": 0})
        return state

    def write_manifest(self) -> None:
        self.write(
            "manifest.json",
            {
                "schema": 1,
                "release_id": self.release_id,
                "plugin_id": "baiyu.codex-state",
                "source_commit": "a" * 40,
            },
        )

    def initialized_state(self) -> None:
        self.write_manifest()
        self.write("runner.json", {"status": "starting", "exit_code": None})
        self.module.PluginRunState.create(
            self.run_dir() / "state.json", self.release_id, "baiyu.codex-state", "a" * 40,
        )

    def awaiting_vm_state(self):
        self.initialized_state()
        state = self.module.PluginRunState.load(self.run_dir() / "state.json")
        for stage in ("package_built", "local_verified", "awaiting_vm_authorization"):
            state.transition(stage)
        return state

    @contextmanager
    def worker_context(self, api):
        package = {
            "plugin_id": "baiyu.codex-state",
            "version": "0.1.0",
            "signature_key_id": "production-key",
            "arches": {
                arch: {"package_sha256": "a" * 64, "binary_sha256": "b" * 64}
                for arch in ("amd64", "arm64")
            },
        }
        with ExitStack() as stack:
            stack.enter_context(mock.patch.object(self.module, "build_packages", return_value={"amd64": self.identity(), "arm64": self.identity()}))
            stack.enter_context(mock.patch.object(self.module, "package_manifest", return_value=package))
            stack.enter_context(mock.patch.object(self.module, "_identity", return_value=self.identity()))
            stack.enter_context(mock.patch.object(self.module, "_previous_packages", return_value=[]))
            stack.enter_context(mock.patch.object(self.module, "PluginAPIClient", return_value=api))
            stack.enter_context(mock.patch.object(self.module, "load_plugin_admin_credentials", side_effect=lambda _node: bytearray(b"credentials")))
            stack.enter_context(mock.patch("builtins.print"))
            yield

    def run_worker(self) -> None:
        self.module.worker(argparse.Namespace(release_id=self.release_id, commit="a" * 40))

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
        self.initialized_state()
        api = mock.Mock()
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result()]
        with self.worker_context(api):
            self.run_worker()

        self.assertEqual([call.kwargs["node"] for call in api.apply.call_args_list], ["local_vm", "racknerd"])
        self.lease.ensure_ready.assert_called_once()
        self.lease.complete.assert_called_once()
        self.lease_factory.assert_called_once_with(
            self.run_dir(), self.release_id, api.runner, process_token=self.module._process_token(self.module.os.getpid()),
            commit="a" * 40,
        )
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        runner = json.loads((self.run_dir() / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual((state["stage"], state["status"]), ("verified", "verified"))
        self.assertEqual((runner["status"], runner["exit_code"]), ("verified", 0))

    def test_worker_without_vm_configuration_performs_no_power_or_ssh_operations(self) -> None:
        self.initialized_state()
        api = mock.Mock()
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result()]
        with (
            self.worker_context(api),
            mock.patch.object(self.module, "VMLease", self.real_lease),
            mock.patch("release.vm_lifecycle.run_hidden") as vmrun,
            mock.patch.object(self.module, "_verified_result") as extra_verification,
        ):
            self.run_worker()
        vmrun.assert_not_called()
        api.runner.run.assert_not_called()
        extra_verification.assert_not_called()
        self.assertEqual(api.apply.call_count, 2)
        runner = json.loads((self.run_dir() / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual((runner["status"], runner["exit_code"]), ("verified", 0))

    def test_worker_uses_ssh_runner_when_api_has_no_runner_attribute(self) -> None:
        self.initialized_state()
        api = mock.Mock(spec=["apply"])
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result()]
        ssh = mock.Mock()
        with self.worker_context(api), mock.patch.object(self.module, "SSHRunner", return_value=ssh) as factory:
            self.run_worker()
        factory.assert_called_once_with()
        self.assertIs(self.lease_factory.call_args.args[2], ssh)

    def test_worker_holds_release_lock_and_same_lease_through_shutdown(self) -> None:
        self.initialized_state()
        real_lock = self.module.RunLock
        events = []

        def assert_locked():
            with self.assertRaisesRegex(RuntimeError, "another release process"):
                with real_lock(self.module.RUN_ROOT / ".release.lock"):
                    self.fail("worker released the lock")

        def apply(**kwargs):
            assert_locked()
            events.append(kwargs["node"])
            self.lease.complete.assert_not_called()
            if kwargs["node"] == "local_vm":
                self.lease.ensure_ready.assert_called_once()
                return self.apply_result(restoration_status="restored")
            return self.apply_result()

        def complete():
            assert_locked()
            self.module._verified_result(self.run_dir())
            events.append("shutdown")

        api = mock.Mock()
        api.apply.side_effect = apply
        self.lease.complete.side_effect = complete
        with self.worker_context(api), mock.patch.object(self.module, "RunLock", wraps=real_lock) as lock_factory:
            self.run_worker()
        lock_factory.assert_called_once()
        self.lease_factory.assert_called_once()
        self.guard_factory.assert_not_called()
        self.assertEqual(events, ["local_vm", "racknerd", "shutdown"])

    def test_worker_keeps_vm_for_failures_and_blocked_results(self) -> None:
        for failure in ("vm_exception", "vm_gate", "production_exception", "production_uncertain", "production_verification"):
            with self.subTest(failure=failure):
                self.initialized_state()
                self.lease.reset_mock()
                api = mock.Mock()
                vm = self.apply_result(restoration_status="restored")
                results = {
                    "vm_exception": [RuntimeError("VM transport failed")],
                    "vm_gate": [self.apply_result(restoration_status="restored", instances_verified="1")],
                    "production_exception": [vm, RuntimeError("production transport failed")],
                    "production_uncertain": [vm, self.apply_result(write_uncertain="true")],
                    "production_verification": [vm, self.apply_result(managed_scope_digest_preserved="false")],
                }
                api.apply.side_effect = results[failure]
                with self.worker_context(api), self.assertRaises(RuntimeError):
                    self.run_worker()
                self.lease.complete.assert_not_called()
                self.assertFalse((self.run_dir() / "production-result.json").exists())
                state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
                self.assertIn(state["status"], {"failed", "blocked_reconciliation"})
                runner = json.loads((self.run_dir() / "runner.json").read_text(encoding="utf-8"))
                self.assertEqual((runner["status"], runner["exit_code"]), ("failed", 1))

    def test_worker_shutdown_failure_retains_verified_production_without_retry(self) -> None:
        self.initialized_state()
        api = mock.Mock()
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result()]
        self.lease.complete.side_effect = RuntimeError("VM shutdown failed")
        with self.worker_context(api), self.assertRaisesRegex(RuntimeError, "shutdown failed"):
            self.run_worker()
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        production = json.loads((self.run_dir() / "production-result.json").read_text(encoding="utf-8"))
        runner = json.loads((self.run_dir() / "runner.json").read_text(encoding="utf-8"))
        self.assertEqual((state["stage"], state["status"], production["status"]), ("verified", "verified", "verified"))
        self.assertEqual((runner["status"], runner["exit_code"]), ("failed", 1))
        with mock.patch.object(self.module, "PluginAPIClient") as factory, self.assertRaisesRegex(RuntimeError, "terminal"):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        factory.assert_not_called()
        self.assertEqual(api.apply.call_count, 2)
        with mock.patch("builtins.print"):
            self.module.verify_result(argparse.Namespace(release_id=self.release_id))

    def test_worker_verifies_production_evidence_before_shutdown(self) -> None:
        self.initialized_state()
        api = mock.Mock()
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result(target_version="wrong-version")]
        with self.worker_context(api), self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.run_worker()
        self.lease.complete.assert_not_called()

    def test_manual_authorization_uses_guard_without_vm_power_operations(self) -> None:
        self.awaiting_vm_state()
        api = mock.Mock()
        api.apply.side_effect = [self.apply_result(restoration_status="restored"), self.apply_result()]
        with self.worker_context(api):
            self.write("package.json", self.module.package_manifest({}))
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
            self.lease.ensure_ready.assert_not_called()
            self.lease.complete.assert_not_called()
            state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
            self.assertEqual(state["stage"], "awaiting_production_authorization")
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        self.lease.complete.assert_not_called()
        self.lease_factory.assert_not_called()
        self.assertEqual(self.guard_factory.call_count, 2)

    def test_manual_guard_and_release_lock_cover_apply_and_verification(self) -> None:
        self.awaiting_production_state()
        real_lock = self.module.RunLock
        guard_active = False

        @contextmanager
        def guard():
            nonlocal guard_active
            guard_active = True
            try:
                yield
            finally:
                guard_active = False

        def assert_locked():
            self.assertTrue(guard_active)
            with self.assertRaisesRegex(RuntimeError, "another release process"):
                with real_lock(self.module.RUN_ROOT / ".release.lock"):
                    self.fail("manual authorization released the lock")

        api = mock.Mock()

        def apply(**_kwargs):
            assert_locked()
            return self.apply_result()

        real_verify = self.module._verified_result

        def verify(run_dir):
            assert_locked()
            return real_verify(run_dir)

        api.apply.side_effect = apply
        with (
            self.worker_context(api),
            mock.patch.object(self.module, "vm_guard", guard),
            mock.patch.object(self.module, "_verified_result", side_effect=verify),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        self.assertFalse(guard_active)
        self.lease_factory.assert_not_called()

    def test_manual_authorization_without_vm_configuration_keeps_guard_noop(self) -> None:
        self.awaiting_production_state()
        api = mock.Mock()
        api.apply.return_value = self.apply_result()
        with (
            self.worker_context(api),
            mock.patch.object(self.module, "vm_guard", self.real_guard),
            mock.patch("release.vm_lifecycle.run_hidden") as vmrun,
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        vmrun.assert_not_called()
        self.lease_factory.assert_not_called()
        api.runner.run.assert_not_called()

    def test_manual_release_lock_failure_creates_no_guard_or_credentials(self) -> None:
        self.awaiting_production_state()
        lock = mock.MagicMock()
        lock.__enter__.side_effect = RuntimeError("another release process is running")
        with (
            mock.patch.object(self.module, "RunLock", return_value=lock),
            mock.patch.object(self.module, "load_plugin_admin_credentials") as credentials,
            self.assertRaisesRegex(RuntimeError, "another release process"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        self.guard_factory.assert_not_called()
        credentials.assert_not_called()

    def test_internal_authorization_requires_existing_worker_lease(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "requires the worker VM lease"):
            self.module.authorize(argparse.Namespace(release_id=self.release_id), acquire_lock=False)

    def test_vm_credentials_are_cleared_when_previous_packages_lookup_fails(self) -> None:
        self.awaiting_vm_state()
        api = mock.Mock()
        credentials = bytearray(b"test-secret")
        with (
            self.worker_context(api),
            mock.patch.object(self.module, "load_plugin_admin_credentials", return_value=credentials),
            mock.patch.object(self.module, "_previous_packages", side_effect=RuntimeError("previous package unavailable")),
            self.assertRaisesRegex(RuntimeError, "previous package unavailable"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        self.assertEqual(credentials, bytearray(len(credentials)))
        api.apply.assert_not_called()

    def test_manual_guard_rejects_retained_owner_before_any_plugin_write(self) -> None:
        self.awaiting_vm_state()
        self.guard_factory.return_value.__enter__.side_effect = RuntimeError("VM ownership requires manual audit")
        with mock.patch.object(self.module, "PluginAPIClient") as factory, self.assertRaisesRegex(RuntimeError, "manual audit"):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        factory.assert_not_called()
        self.lease_factory.assert_not_called()
        state = self.module.PluginRunState.load(self.run_dir() / "state.json").value
        self.assertEqual((state["stage"], state["status"]), ("awaiting_vm_authorization", "running"))

    def test_manual_authorization_rereads_state_after_lock_acquisition(self) -> None:
        state = self.awaiting_production_state()
        lock = mock.MagicMock()
        lock.__enter__.side_effect = lambda: state.fail("awaiting_production_authorization", blocked=True)
        with (
            mock.patch.object(self.module, "RunLock", return_value=lock),
            mock.patch.object(self.module, "PluginAPIClient") as factory,
            self.assertRaisesRegex(RuntimeError, "terminal"),
        ):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        factory.assert_not_called()
        self.lease_factory.assert_not_called()

    def test_failed_vm_phase_cannot_be_authorized_again(self) -> None:
        state = self.awaiting_vm_state()
        state.fail("awaiting_vm_authorization")
        with mock.patch.object(self.module, "PluginAPIClient") as factory, self.assertRaisesRegex(RuntimeError, "terminal"):
            self.module.authorize(argparse.Namespace(release_id=self.release_id))
        factory.assert_not_called()

    def test_worker_ready_failure_keeps_vm_and_performs_no_plugin_writes(self) -> None:
        self.initialized_state()
        api = mock.Mock()
        self.lease.ensure_ready.side_effect = RuntimeError("VM SSH readiness failed")
        with self.worker_context(api), self.assertRaisesRegex(RuntimeError, "readiness failed"):
            self.run_worker()
        api.apply.assert_not_called()
        self.lease.complete.assert_not_called()

    def test_status_has_only_vm_lifecycle_allowlisted_fields(self) -> None:
        self.initialized_state()
        safe = {"vm_power_status": "running", "vm_started_by_release": True, "vm_cleanup_status": "pending"}
        with mock.patch.object(self.module, "lifecycle_view", return_value={**safe, "vmx_path": "private", "password": "secret"}):
            view = self.module.status_view(self.release_id)
        self.assertEqual({field: view[field] for field in safe}, safe)
        self.assertNotIn("vmx_path", view)
        self.assertNotIn("password", view)

    def test_wait_does_not_succeed_until_runner_finishes_cleanup(self) -> None:
        views = [
            {"status": "verified", "runner_status": "running", "runner_exit": None},
            {"status": "verified", "runner_status": "verified", "runner_exit": 0},
        ]
        with mock.patch.object(self.module, "status_view", side_effect=views) as status, mock.patch.object(self.module.time, "sleep"), mock.patch("builtins.print"):
            result = self.module.wait(argparse.Namespace(release_id=self.release_id, timeout=0))
        self.assertEqual(result, 0)
        self.assertEqual(status.call_count, 2)

    def test_wait_reports_runner_cleanup_failure_despite_verified_production(self) -> None:
        view = {"status": "verified", "runner_status": "failed", "runner_exit": 1}
        with mock.patch.object(self.module, "status_view", return_value=view), mock.patch("builtins.print"):
            result = self.module.wait(argparse.Namespace(release_id=self.release_id, timeout=0))
        self.assertEqual(result, 2)

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
