from __future__ import annotations

import contextlib
from email.message import Message
import importlib
import io
import json
import sys
import tempfile
import unittest
import urllib.error
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))


def load_plugin_api():
    try:
        return importlib.import_module("release.plugin_api")
    except (ImportError, ModuleNotFoundError) as error:
        raise AssertionError("release.plugin_api must be importable for plugin release operations") from error


class FakeRunner:
    def __init__(self, values: dict[str, str]) -> None:
        self.values = values
        self.servers = {
            "local_vm": {"host": "192.168.31.199"},
        }
        self.uploads: list[tuple[str, Path, str, int]] = []
        self.sensitive_calls: list[dict[str, object]] = []

    def create_temp_dir(self, node: str, base: str, label: str) -> str:
        return f"{base.rstrip('/')}/{label}-contract"

    def run(self, node, script, allowed, timeout=120):
        ssh_module = importlib.import_module("release.ssh")
        if node != "racknerd" or set(allowed) != {"active_port"}:
            raise AssertionError("unexpected read-only slot lookup")
        return ssh_module.SSHResult({"active_port": "18081"})

    def upload_file(self, node: str, local_path: Path, remote_path: str, mode: int) -> None:
        self.uploads.append((node, local_path, remote_path, mode))

    def run_with_sensitive_input(self, node, script, allowed, data, timeout=120):
        module = load_plugin_api()
        ssh_module = importlib.import_module("release.ssh")
        self.sensitive_calls.append(
            {
                "node": node,
                "script": script,
                "allowed": set(allowed),
                "data_before_clear": bytes(data),
                "timeout": timeout,
            }
        )
        for index in range(len(data)):
            data[index] = 0
        return ssh_module.SSHResult(dict(self.values))


def result_values(**changes: str) -> dict[str, str]:
    value = {
        "operation": "install",
        "installation_id": "7",
        "previous_version": "not_installed",
        "previous_binary_sha256": "not_installed",
        "target_version": "0.1.0",
        "previous_state": "not_installed",
        "final_state": "disabled",
        "binary_sha256": "b" * 64,
        "signature_status": "trusted",
        "config_revision_preserved": "true",
        "managed_scope_digest_preserved": "true",
        "runtime_healthy": "true",
        "instances_expected": "2",
        "instances_verified": "2",
        "restoration_status": "not_required",
        "write_uncertain": "false",
        "remote_error_status": "none",
        "remote_error_class": "none",
        "remote_error_code": "none",
        "remote_error_content_type": "none",
        "remote_error_body_bytes": "0",
        "remote_error_body_kind": "none",
    }
    value.update(changes)
    return value


class PluginAPIContractTest(unittest.TestCase):
    def identity(self, path: Path):
        module = load_plugin_api()
        return module.PackageIdentity(
            path=path,
            arch="amd64",
            version="0.1.0",
            package_sha256="a" * 64,
            binary_sha256="b" * 64,
            key_id="production-key",
            signature_status="trusted",
        )

    def test_first_install_contract_requires_disabled_state(self) -> None:
        module = load_plugin_api()
        self.assertIn('if operation == "install" and after.get("state") != "disabled"', module._REMOTE_HELPER)
        self.assertNotIn("enable_plugin", module._REMOTE_HELPER)

    def test_upgrade_contract_preserves_enabled_config_revision_and_managed_scope(self) -> None:
        module = load_plugin_api()
        helper = module._REMOTE_HELPER
        self.assertIn('if operation == "upgrade" and after.get("state") != before_state', helper)
        self.assertIn("config_revision_preserved=", helper)
        self.assertIn("managed_scope_digest_preserved=", helper)

    def test_apply_uses_one_sensitive_ssh_call_and_clears_credentials(self) -> None:
        module = load_plugin_api()
        runner = FakeRunner(result_values())
        client = module.PluginAPIClient(runner)
        credentials = module.encode_credentials("admin-key")
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "baiyu.codex-state-0.1.0-linux-amd64.s2plugin"
            package.write_bytes(b"signed-package")
            result = client.apply(
                node="racknerd",
                identity=self.identity(package),
                credentials=credentials,
                restore_after=False,
            )
        self.assertEqual(result.operation, "install")
        self.assertEqual(len(runner.sensitive_calls), 1)
        self.assertEqual(credentials, bytearray(len(credentials)))
        self.assertEqual(runner.uploads[0][3], 0o400)
        payload = json.loads(runner.sensitive_calls[0]["data_before_clear"])
        self.assertEqual(payload["admin_api_key"], "admin-key")
        self.assertEqual(
            payload["base_urls"],
            ["http://127.0.0.1:18081/api/v1", "http://127.0.0.1:18080/api/v1"],
        )

    def test_vm_base_url_uses_the_configured_ssh_host(self) -> None:
        module = load_plugin_api()
        client = module.PluginAPIClient(FakeRunner(result_values()))

        self.assertEqual(
            client._base_urls("local_vm"),
            ["http://192.168.31.199:8211/api/v1"],
        )
        self.assertEqual(
            client._base_urls("vm"),
            ["http://192.168.31.199:8211/api/v1"],
        )

    def test_uncertain_write_is_reported_without_a_second_write_attempt(self) -> None:
        module = load_plugin_api()
        runner = FakeRunner(result_values(write_uncertain="true", instances_verified="0"))
        client = module.PluginAPIClient(runner)
        credentials = module.encode_credentials("admin-key")
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "baiyu.codex-state-0.1.0-linux-amd64.s2plugin"
            package.write_bytes(b"signed-package")
            result = client.apply(
                node="racknerd",
                identity=self.identity(package),
                credentials=credentials,
                restore_after=False,
            )
        self.assertTrue(result.write_uncertain)
        self.assertEqual(len(runner.sensitive_calls), 1)
        self.assertEqual(len(runner.uploads), 1)
        self.assertEqual(credentials, bytearray(len(credentials)))

    def test_previous_package_is_bound_to_exact_version_and_binary(self) -> None:
        module = load_plugin_api()
        runner = FakeRunner(result_values())
        client = module.PluginAPIClient(runner)
        credentials = module.encode_credentials("admin-key")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            package = root / "baiyu.codex-state-0.1.0-linux-amd64.s2plugin"
            previous_path = root / "baiyu.codex-state-0.0.9-linux-amd64.s2plugin"
            package.write_bytes(b"signed-package")
            previous_path.write_bytes(b"trusted-previous")
            previous = module.PackageIdentity(
                path=previous_path,
                arch="amd64",
                version="0.0.9",
                package_sha256="c" * 64,
                binary_sha256="d" * 64,
                key_id="production-key",
                signature_status="trusted",
            )
            client.apply(
                node="local_vm",
                identity=self.identity(package),
                credentials=credentials,
                restore_after=True,
                previous_packages=[previous],
            )
        payload = json.loads(runner.sensitive_calls[0]["data_before_clear"])
        self.assertEqual(
            list(payload["restore_packages"]),
            [f"0.0.9:{'d' * 64}"],
        )
        self.assertEqual(len(runner.uploads), 2)

    def test_remote_helper_uses_the_first_port_that_accepts_the_admin_api_key(self) -> None:
        module = load_plugin_api()
        target_binary = "b" * 64
        installation = {
            "id": 7,
            "plugin_key": "baiyu.codex-state",
            "version": "0.1.0",
            "binary_sha256": target_binary,
            "signature_status": "trusted",
            "state": "disabled",
            "config_revision": 0,
            "managed_scope": [],
            "runtime_healthy": True,
        }
        calls: list[str] = []

        def urlopen(request, timeout=0):
            calls.append(request.full_url)
            if request.full_url.startswith("http://first/"):
                raise urllib.error.URLError("inactive slot")
            if request.full_url.endswith("/admin/plugins"):
                self.assertEqual({key.lower(): value for key, value in request.headers.items()}["x-api-key"], "admin-key")
                return FakeHTTPResponse({"data": [installation]})
            raise AssertionError(request.full_url)

        output = execute_remote_helper(
            module,
            remote_config(target_binary=target_binary, base_urls=["http://first/api/v1", "http://second/api/v1"]),
            urlopen,
        )
        self.assertIn("operation=no-op", output)
        self.assertIn("instances_expected=1", output)
        self.assertIn("instances_verified=1", output)
        self.assertGreaterEqual(sum(url.startswith("http://second/") for url in calls), 3)

    def test_remote_helper_refuses_upgrade_before_write_without_exact_rollback_package(self) -> None:
        module = load_plugin_api()
        old_binary = "c" * 64
        installation = {
            "id": 7,
            "plugin_key": "baiyu.codex-state",
            "version": "0.0.9",
            "binary_sha256": old_binary,
            "signature_status": "trusted",
            "state": "enabled",
            "config_revision": 3,
            "managed_scope": [{"account_id": 1, "models": ["gpt-6-astra"]}],
            "runtime_healthy": True,
        }
        writes: list[str] = []

        def urlopen(request, timeout=0):
            if request.full_url.endswith("/admin/plugins"):
                return FakeHTTPResponse({"data": [installation]})
            writes.append(request.full_url)
            raise AssertionError("upgrade write must not start without an exact rollback package")

        config = remote_config(target_binary="b" * 64, base_urls=["http://only/api/v1"])
        config["restore_after"] = True
        config["restore_packages"] = {"0.0.9": "/tmp/wrong-key.s2plugin"}
        with self.assertRaisesRegex(RuntimeError, "trusted_previous_package_required"):
            execute_remote_helper(module, config, urlopen)
        self.assertEqual(writes, [])

    def test_remote_helper_reports_sanitized_http_failure_class(self) -> None:
        module = load_plugin_api()
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "plugin.s2plugin"
            package.write_bytes(b"signed-package")

            def urlopen(request, timeout=0):
                if request.full_url.endswith("/admin/plugins"):
                    return FakeHTTPResponse({"data": []})
                raise urllib.error.HTTPError(request.full_url, 400, "bad request", {}, io.BytesIO(b'{"error":{"code":"sensitive-detail"}}'))

            config = remote_config(target_binary="b" * 64, base_urls=["http://only/api/v1"])
            config["target_package"] = str(package)
            output = execute_remote_helper(module, config, urlopen)
        self.assertIn("remote_error_status=400", output)
        self.assertIn("remote_error_class=client_request", output)
        self.assertNotIn("sensitive-detail", output)

    def test_remote_helper_classifies_validation_error_without_echoing_body(self) -> None:
        module = load_plugin_api()
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "plugin.s2plugin"
            package.write_bytes(b"signed-package")

            def urlopen(request, timeout=0):
                if request.full_url.endswith("/admin/plugins"):
                    return FakeHTTPResponse({"data": []})
                raise urllib.error.HTTPError(request.full_url, 400, "bad request", {}, io.BytesIO(b'{"error":{"message":"manifest rejected: private"}}'))

            config = remote_config(target_binary="b" * 64, base_urls=["http://only/api/v1"])
            config["target_package"] = str(package)
            output = execute_remote_helper(module, config, urlopen)
        self.assertIn("remote_error_class=manifest_validation", output)
        self.assertNotIn("manifest rejected", output)

    def test_remote_helper_reports_only_safe_error_code(self) -> None:
        module = load_plugin_api()
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "plugin.s2plugin"
            package.write_bytes(b"signed-package")

            def urlopen(request, timeout=0):
                if request.full_url.endswith("/admin/plugins"):
                    return FakeHTTPResponse({"data": []})
                raise urllib.error.HTTPError(request.full_url, 400, "bad request", {}, io.BytesIO(b'{"error":{"code":"PLUGIN_PACKAGE_INVALID"}}'))

            config = remote_config(target_binary="b" * 64, base_urls=["http://only/api/v1"])
            config["target_package"] = str(package)
            output = execute_remote_helper(module, config, urlopen)
        self.assertIn("remote_error_code=PLUGIN_PACKAGE_INVALID", output)

    def test_remote_helper_reports_only_http_body_metadata(self) -> None:
        module = load_plugin_api()
        with tempfile.TemporaryDirectory() as temporary:
            package = Path(temporary) / "plugin.s2plugin"
            package.write_bytes(b"signed-package")

            def urlopen(request, timeout=0):
                if request.full_url.endswith("/admin/plugins"):
                    return FakeHTTPResponse({"data": []})
                headers = Message()
                headers["Content-Type"] = "text/html"
                raise urllib.error.HTTPError(request.full_url, 400, "bad request", headers, io.BytesIO(b"<html>private</html>"))

            config = remote_config(target_binary="b" * 64, base_urls=["http://only/api/v1"])
            config["target_package"] = str(package)
            output = execute_remote_helper(module, config, urlopen)
        self.assertIn("remote_error_content_type=text/html", output)
        self.assertIn("remote_error_body_bytes=20", output)
        self.assertIn("remote_error_body_kind=html", output)
        self.assertNotIn("private", output)


class FakeHTTPResponse:
    def __init__(self, payload: dict, status: int = 200) -> None:
        self.payload = json.dumps(payload).encode("utf-8")
        self.status = status

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, traceback):
        return False

    def read(self) -> bytes:
        return self.payload


def remote_config(*, target_binary: str, base_urls: list[str]) -> dict[str, object]:
    return {
        "admin_api_key": "admin-key",
        "plugin_id": "baiyu.codex-state",
        "target_version": "0.1.0",
        "target_binary_sha256": target_binary,
        "target_package": "/tmp/plugin.s2plugin",
        "restore_packages": {},
        "restore_after": False,
        "base_urls": base_urls,
    }


def execute_remote_helper(module, config: dict[str, object], urlopen) -> str:
    output = io.StringIO()
    with (
        mock.patch("sys.stdin", io.StringIO(json.dumps(config))),
        mock.patch("urllib.request.urlopen", side_effect=urlopen),
        contextlib.redirect_stdout(output),
    ):
        try:
            exec(module._REMOTE_HELPER, {})
        except SystemExit as error:
            if error.code not in (0, None):
                raise
    return output.getvalue()


if __name__ == "__main__":
    unittest.main()
