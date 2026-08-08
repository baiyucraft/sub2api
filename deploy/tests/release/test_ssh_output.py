from __future__ import annotations

import sys
import unittest
from types import SimpleNamespace
from pathlib import Path
from unittest import mock

import paramiko


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.ssh import KNOWN_HOSTS, ROOT, SSH_CONFIG, SSHRunner


class FakeChannel:
    def recv_exit_status(self) -> int:
        return 0


class FakeStream:
    def __init__(self, value: bytes):
        self.value = value
        self.channel = FakeChannel()

    def read(self) -> bytes:
        return self.value

    def write(self, value: bytes) -> None:
        self.value += value

    def flush(self) -> None:
        pass


class FakeInputChannel:
    def shutdown_write(self) -> None:
        pass


class FakeInput(FakeStream):
    def __init__(self):
        super().__init__(b"")
        self.channel = FakeInputChannel()


class FakeClient:
    def __init__(self, stdout: bytes, stderr: bytes = b""):
        self.stdout = stdout
        self.stderr = stderr

    def exec_command(self, command: str, timeout: int, get_pty: bool):
        return FakeInput(), FakeStream(self.stdout), FakeStream(self.stderr)

    def close(self) -> None:
        pass


class FakeSFTPFile:
    def __init__(self, value: bytes):
        self.value = value

    def __enter__(self):
        return self

    def __exit__(self, *_args) -> None:
        pass

    def read(self, _size: int) -> bytes:
        return self.value


class FakeSFTP:
    def __init__(self, value: bytes):
        self.value = value

    def stat(self, _path: str):
        return SimpleNamespace(st_size=len(self.value))

    def file(self, _path: str, _mode: str):
        return FakeSFTPFile(self.value)

    def close(self) -> None:
        pass


class FakeSFTPClient(FakeClient):
    def __init__(self, value: bytes):
        super().__init__(b"")
        self.value = value

    def open_sftp(self):
        return FakeSFTP(self.value)


class SSHOutputTest(unittest.TestCase):
    def test_connection_files_are_repo_local(self) -> None:
        self.assertEqual(SSH_CONFIG, ROOT / ".ssh.local")
        self.assertEqual(KNOWN_HOSTS, ROOT / ".tmp" / "known_hosts")

    def test_connection_retries_transport_banner_failures(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.servers = {"vm": {"host": "example.test", "user": "root", "password": "secret"}}
        runner.temp_dirs = set()
        clients = []
        attempts = {"count": 0}

        class Transport:
            def set_keepalive(self, _seconds: int) -> None:
                pass

        class Client:
            def load_host_keys(self, _path: str) -> None:
                pass

            def get_host_keys(self):
                return {}

            def set_missing_host_key_policy(self, _policy) -> None:
                pass

            def connect(self, **_kwargs) -> None:
                attempts["count"] += 1
                if attempts["count"] < 3:
                    raise paramiko.SSHException("banner")

            def get_transport(self):
                return Transport()

            def close(self) -> None:
                pass

        clients.extend([Client(), Client(), Client()])
        with mock.patch("release.ssh.paramiko.SSHClient", side_effect=clients), mock.patch("release.ssh.time.sleep"):
            self.assertIs(clients[2], runner.connect("vm"))

    def runner(self, stdout: bytes, stderr: bytes = b"") -> SSHRunner:
        instance = object.__new__(SSHRunner)
        instance.connect = lambda name: FakeClient(stdout, stderr)
        instance.temp_dirs = set()
        return instance

    def test_accepts_only_declared_fields(self) -> None:
        result = self.runner(b"health=pass\nimage=sha256\n").run("vm", "true", {"health", "image"})
        self.assertEqual(result.values["health"], "pass")

    def test_run_with_input_keeps_structured_output_contract(self) -> None:
        result = self.runner(b"health=pass\n").run_with_input("vm", "read secret", {"health"}, b"secret\n")
        self.assertEqual(result.values, {"health": "pass"})

    def test_canary_key_is_read_without_structured_output(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.connect = lambda _name: FakeSFTPClient(b"sk-1234567890abcdef\n")
        self.assertEqual(runner.read_canary_key(), b"sk-1234567890abcdef")

    def test_canary_key_rejects_invalid_content(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.connect = lambda _name: FakeSFTPClient(b"not-a-key")
        with self.assertRaisesRegex(RuntimeError, "content is invalid"):
            runner.read_canary_key()

    def test_rejects_non_structured_or_unknown_output(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "non-structured"):
            self.runner(b"hello\n").run("vm", "true", {"health"})
        with self.assertRaisesRegex(RuntimeError, "undeclared"):
            self.runner(b"health=pass\nsecret=value\n").run("vm", "true", {"health"})

    def test_rejects_any_stderr(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "unexpected stderr"):
            self.runner(b"health=pass\n", b"token=secret\n").run("vm", "true", {"health"})

    def test_temp_dir_rejects_path_outside_base(self) -> None:
        runner = self.runner(b"temp_dir=/tmp/escape\n")
        with self.assertRaisesRegex(RuntimeError, "invalid temporary"):
            runner.create_temp_dir("vm", "/opt/release", "stage")

    def test_sftp_rejects_unregistered_path(self) -> None:
        runner = self.runner(b"")
        with self.assertRaisesRegex(RuntimeError, "outside a registered"):
            runner.upload("vm", b"data", "/tmp/predictable")


if __name__ == "__main__":
    unittest.main()
