from __future__ import annotations

import importlib.util
import io
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import yaml


SCRIPT_PATH = Path(__file__).with_name("racknerd_readonly_status.py")
SPEC = importlib.util.spec_from_file_location("racknerd_readonly_status", SCRIPT_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


def valid_document() -> dict[str, object]:
    return {
        "servers": {
            "dmit": {
                "purpose": "relay",
                "host": "relay.invalid",
                "port": 1030,
                "user": "root",
                "private_key": "relay-key",
                "connection": "direct",
            },
            "racknerd": {
                "purpose": "production",
                "host": "example.invalid",
                "port": 22,
                "user": "operator",
                "password": "secret",
                "connection": "http_connect_via_ssh",
                "proxy": "192.0.2.1:1080",
                "proxy_via": "dmit",
            },
            "other": {"ignored": True},
        }
    }


def deployment_document() -> dict:
    return yaml.safe_load((SCRIPT_PATH.parents[4] / ".ssh.local.example.yaml").read_text(encoding="utf-8"))


class ConfigTests(unittest.TestCase):
    def test_valid_config(self) -> None:
        config = MODULE.parse_config_document(valid_document())
        self.assertEqual(config.proxy_host, "192.0.2.1")
        self.assertEqual(config.proxy_port, 1080)
        self.assertEqual(config.relay.host, "relay.invalid")

    @staticmethod
    def legacy_document() -> dict[str, object]:
        document = valid_document()
        racknerd = document["servers"]["racknerd"]
        racknerd["connection"] = "proxy"
        racknerd["proxy"] = "127.0.0.1:7897"
        racknerd.pop("proxy_via")
        racknerd["proxy_command"] = "C:/Program Files/Git/mingw64/bin/connect.exe -S 127.0.0.1:7897 %h %p"
        return document

    def test_accepts_quoted_connect_executable(self) -> None:
        document = self.legacy_document()
        document["servers"]["racknerd"]["proxy_command"] = (
            '"C:/Program Files/Git/mingw64/bin/connect.exe" -S 127.0.0.1:7897 %h %p'
        )
        config = MODULE.parse_config_document(document)
        self.assertEqual(config.proxy_port, 7897)

    def test_rejects_unknown_racknerd_field(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["unexpected"] = "value"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "config_invalid"):
            MODULE.parse_config_document(document)

    def test_rejects_boolean_port(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["port"] = True
        with self.assertRaisesRegex(MODULE.StatusCheckError, "config_invalid"):
            MODULE.parse_config_document(document)

    def test_requires_exactly_one_auth_method(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["private_key"] = "key"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "config_invalid"):
            MODULE.parse_config_document(document)

    def test_rejects_proxy_extra_argument(self) -> None:
        document = self.legacy_document()
        document["servers"]["racknerd"]["proxy_command"] += " --extra"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)

    def test_rejects_nonlocal_proxy(self) -> None:
        document = self.legacy_document()
        document["servers"]["racknerd"]["proxy"] = "10.0.0.1:7897"
        document["servers"]["racknerd"]["proxy_command"] = (
            "C:/Tools/connect.exe -S 10.0.0.1:7897 %h %p"
        )
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)

    def test_rejects_wrong_relay_proxy_endpoint(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["proxy"] = "192.0.2.1:70000"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)

    def test_relay_proxy_port_is_explicit_configuration(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["proxy"] = "192.0.2.1:3128"
        self.assertEqual(MODULE.parse_config_document(document).proxy_port, 3128)

    def test_public_deployment_example_resolves_configured_roles(self) -> None:
        document = deployment_document()
        config = MODULE.parse_config_document(document)
        self.assertEqual(config.host, document["servers"]["app-node"]["host"])
        self.assertEqual(config.relay.host, document["servers"]["relay-node"]["host"])
        self.assertEqual(config.proxy_host, document["deployment"]["relay_probe"]["listen_address"])
        self.assertEqual(config.proxy_port, document["deployment"]["relay_probe"]["listen_port"])

    def test_deployment_config_uses_nondefault_proxy_endpoint(self) -> None:
        document = deployment_document()
        document["servers"]["app-node"]["proxy"] = "198.51.100.9:3128"
        document["deployment"]["relay_probe"]["listen_address"] = "198.51.100.9"
        document["deployment"]["relay_probe"]["listen_port"] = 3128
        config = MODULE.parse_config_document(document)
        self.assertEqual((config.proxy_host, config.proxy_port), ("198.51.100.9", 3128))

    def test_deployment_config_supports_explicit_direct_connection(self) -> None:
        document = deployment_document()
        app = document["servers"]["app-node"]
        app["connection"] = "direct"
        app.pop("proxy")
        app.pop("proxy_via")
        config = MODULE.parse_config_document(document)
        self.assertIsNone(config.relay)
        self.assertIsNone(config.proxy_host)

    def test_deployment_roles_ignore_unreferenced_legacy_entry(self) -> None:
        document = deployment_document()
        document["servers"]["racknerd"] = {"host": "old.example.test", "user": "old", "port": 22}
        self.assertEqual(MODULE.parse_config_document(document).host, document["servers"]["app-node"]["host"])

    def test_deployment_alias_ambiguity_is_rejected_by_shared_validator(self) -> None:
        document = deployment_document()
        document["servers"]["racknerd"] = {"host": "old.example.test", "user": "old", "port": 22}
        document["deployment"]["health_routes"]["relay"]["observer"] = "racknerd"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "config_invalid"):
            MODULE.parse_config_document(document)

    def test_drift_is_blocked_before_creating_a_connection(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "config.yaml"
            path.write_text(yaml.safe_dump(deployment_document()), encoding="utf-8")
            config = MODULE.load_config(path, yaml)
            path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
            paramiko = SimpleNamespace(SSHClient=mock.Mock())
            with self.assertRaisesRegex(MODULE.StatusCheckError, "config_changed"):
                MODULE.connect_and_check(config, paramiko, mock.Mock())
            paramiko.SSHClient.assert_not_called()

    def test_drift_after_relay_connect_blocks_target_connection(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "config.yaml"
            path.write_text(yaml.safe_dump(deployment_document()), encoding="utf-8")
            config = MODULE.load_config(path, yaml)
            client, relay, transport = mock.Mock(), mock.Mock(), mock.Mock()
            relay.get_transport.return_value = transport
            relay.connect.side_effect = lambda **kwargs: path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
            ssh_error = type("SSHError", (Exception,), {})
            paramiko = SimpleNamespace(
                SSHClient=mock.Mock(side_effect=[client, relay]), RejectPolicy=mock.Mock(),
                BadHostKeyException=ssh_error, hostkeys=SimpleNamespace(InvalidHostKey=ssh_error),
                AuthenticationException=ssh_error, SSHException=ssh_error,
            )
            with self.assertRaisesRegex(MODULE.StatusCheckError, "config_changed"):
                MODULE.connect_and_check(config, paramiko, mock.Mock())
            transport.open_channel.assert_not_called()
            client.connect.assert_not_called()
            relay.close.assert_called_once()

    def test_drift_during_final_check_cannot_report_success(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "config.yaml"
            document = deployment_document()
            app = document["servers"]["app-node"]
            app["connection"] = "direct"
            app.pop("proxy")
            app.pop("proxy_via")
            path.write_text(yaml.safe_dump(document), encoding="utf-8")
            config = MODULE.load_config(path, yaml)
            client = mock.Mock()
            ssh_error = type("SSHError", (Exception,), {})
            paramiko = SimpleNamespace(
                SSHClient=mock.Mock(return_value=client), RejectPolicy=mock.Mock(),
                BadHostKeyException=ssh_error, hostkeys=SimpleNamespace(InvalidHostKey=ssh_error),
                AuthenticationException=ssh_error, SSHException=ssh_error,
            )
            def checked(*args):
                path.write_text(path.read_text(encoding="utf-8") + "\n", encoding="utf-8")
                return {"checked_at": "2026-11-07T00:00:00Z"}
            with mock.patch.object(MODULE, "CHECKS", MODULE.CHECKS[:1]), mock.patch.object(MODULE, "run_remote_check", side_effect=checked):
                with self.assertRaisesRegex(MODULE.StatusCheckError, "config_changed"):
                    MODULE.connect_and_check(config, paramiko, mock.Mock())
            self.assertNotIn("sock", client.connect.call_args.kwargs)

    def test_rejects_non_direct_relay(self) -> None:
        document = valid_document()
        document["servers"]["dmit"]["connection"] = "proxy"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)


class ParserTests(unittest.TestCase):
    def test_http_connect_accepts_success_response(self) -> None:
        class Channel:
            def __init__(self):
                self.response = bytearray(b"HTTP/1.0 200 Connection established\r\nHeader: value\r\n\r\n")
                self.request = b""

            def sendall(self, value: bytes) -> None:
                self.request += value

            def recv(self, size: int) -> bytes:
                value = bytes(self.response[:size])
                del self.response[:size]
                return value

        channel = Channel()
        MODULE.establish_http_connect(channel, "rack.invalid", 1030)
        self.assertIn(b"CONNECT rack.invalid:1030 HTTP/1.1", channel.request)

    def test_http_connect_rejects_forbidden_response(self) -> None:
        class Channel:
            response = bytearray(b"HTTP/1.0 403 Access denied\r\n\r\n")

            def sendall(self, _value: bytes) -> None:
                pass

            def recv(self, size: int) -> bytes:
                value = bytes(self.response[:size])
                del self.response[:size]
                return value

        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_or_connection_failed"):
            MODULE.establish_http_connect(Channel(), "rack.invalid", 1030)
    def test_internal_health_uses_the_recorded_active_port(self) -> None:
        check = next(item for item in MODULE.CHECKS if item.name == "internal_health_http")
        self.assertIn("active_slot=/opt/sub2api/active-app", check.command)
        self.assertIn("18080|18081", check.command)
        self.assertIn("${active_port}/health", check.command)
        self.assertNotIn("127.0.0.1:18080/health", check.command)

    def test_parses_expected_app_output(self) -> None:
        image_id = "sha256:" + "a" * 64
        self.assertEqual(
            MODULE.parse_app(f"{image_id}|running|healthy"),
            {"app_image_id": image_id, "app_state": "running", "app_health": "healthy"},
        )

    def test_rejects_extra_app_output(self) -> None:
        image_id = "sha256:" + "a" * 64
        with self.assertRaisesRegex(MODULE.StatusCheckError, "remote_output_invalid"):
            MODULE.parse_app(f"{image_id}|running|healthy|secret")

    def test_rejects_unhealthy_app(self) -> None:
        image_id = "sha256:" + "a" * 64
        with self.assertRaisesRegex(MODULE.StatusCheckError, "remote_status_unhealthy"):
            MODULE.parse_app(f"{image_id}|running|unhealthy")

    def test_rejects_non_200_health(self) -> None:
        with self.assertRaisesRegex(MODULE.StatusCheckError, "remote_status_unhealthy"):
            MODULE.parse_http("503")

    def test_rejects_non_ascii_remote_output(self) -> None:
        class Channel:
            def settimeout(self, _timeout: int) -> None:
                pass

            def recv_exit_status(self) -> int:
                return 0

        class Stream:
            channel = Channel()

            def __init__(self, value: bytes):
                self.value = value

            def read(self, _size: int) -> bytes:
                return self.value

        class Client:
            def exec_command(self, _command: str, timeout: int):
                self.timeout = timeout
                return None, Stream("密钥".encode()), Stream(b"")

        with self.assertRaisesRegex(MODULE.StatusCheckError, "remote_output_invalid"):
            MODULE.run_remote_check(Client(), MODULE.CHECKS[0])


class MainTests(unittest.TestCase):
    def test_unexpected_exception_is_sanitized(self) -> None:
        original_loader = MODULE.load_third_party_modules
        MODULE.load_third_party_modules = lambda: (_ for _ in ()).throw(RuntimeError("secret detail"))
        output = io.StringIO()
        try:
            with redirect_stdout(output):
                result = MODULE.main([])
        finally:
            MODULE.load_third_party_modules = original_loader

        self.assertEqual(result, 1)
        self.assertEqual(output.getvalue(), "status=failed\nerror_category=unexpected_failure\n")
        self.assertNotIn("secret detail", output.getvalue())


if __name__ == "__main__":
    unittest.main()
