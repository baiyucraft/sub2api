from __future__ import annotations

import importlib.util
import io
import sys
import unittest
from contextlib import redirect_stdout
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("racknerd_readonly_status.py")
SPEC = importlib.util.spec_from_file_location("racknerd_readonly_status", SCRIPT_PATH)
assert SPEC is not None and SPEC.loader is not None
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


def valid_document() -> dict[str, object]:
    return {
        "servers": {
            "racknerd": {
                "purpose": "production",
                "host": "example.invalid",
                "port": 22,
                "user": "operator",
                "password": "secret",
                "connection": "proxy",
                "proxy": "127.0.0.1:7897",
                "proxy_command": "C:/Program Files/Git/mingw64/bin/connect.exe -S 127.0.0.1:7897 %h %p",
            },
            "other": {"ignored": True},
        }
    }


class ConfigTests(unittest.TestCase):
    def test_valid_config(self) -> None:
        config = MODULE.parse_config_document(valid_document())
        self.assertEqual(config.proxy_host, "127.0.0.1")
        self.assertEqual(config.proxy_port, 7897)

    def test_accepts_quoted_connect_executable(self) -> None:
        document = valid_document()
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
        document = valid_document()
        document["servers"]["racknerd"]["proxy_command"] += " --extra"
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)

    def test_rejects_nonlocal_proxy(self) -> None:
        document = valid_document()
        document["servers"]["racknerd"]["proxy"] = "10.0.0.1:7897"
        document["servers"]["racknerd"]["proxy_command"] = (
            "C:/Tools/connect.exe -S 10.0.0.1:7897 %h %p"
        )
        with self.assertRaisesRegex(MODULE.StatusCheckError, "proxy_config_unsupported"):
            MODULE.parse_config_document(document)


class ParserTests(unittest.TestCase):
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
