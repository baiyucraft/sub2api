from __future__ import annotations

import sys
import unittest
from pathlib import Path


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


class FakeClient:
    def __init__(self, stdout: bytes, stderr: bytes = b""):
        self.stdout = stdout
        self.stderr = stderr

    def exec_command(self, command: str, timeout: int, get_pty: bool):
        return None, FakeStream(self.stdout), FakeStream(self.stderr)

    def close(self) -> None:
        pass


class SSHOutputTest(unittest.TestCase):
    def test_connection_files_are_repo_local(self) -> None:
        self.assertEqual(SSH_CONFIG, ROOT / ".ssh.local")
        self.assertEqual(KNOWN_HOSTS, ROOT / ".tmp" / "known_hosts")

    def runner(self, stdout: bytes, stderr: bytes = b"") -> SSHRunner:
        instance = object.__new__(SSHRunner)
        instance.connect = lambda name: FakeClient(stdout, stderr)
        return instance

    def test_accepts_only_declared_fields(self) -> None:
        result = self.runner(b"health=pass\nimage=sha256\n").run("vm", "true", {"health", "image"})
        self.assertEqual(result.values["health"], "pass")

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


if __name__ == "__main__":
    unittest.main()
