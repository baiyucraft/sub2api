from __future__ import annotations

import sys
import ast
import json
import stat
import tempfile
import unittest
from types import SimpleNamespace
from pathlib import Path
from unittest import mock

import paramiko


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.ssh import KNOWN_HOSTS, REMOTE_RAW_LOGS, ROOT, SSH_CONFIG, SSHResult, SSHRunner, TRANSFER_MAX_ACTIVE_CONNECTIONS


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
        return SimpleNamespace(st_size=len(self.value), st_mode=stat.S_IFREG | 0o600, st_nlink=1)

    def lstat(self, path: str):
        return self.stat(path)

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
    def test_all_structured_transfer_events_declare_stage(self) -> None:
        source = (DEPLOY_ROOT / "release" / "ssh.py").read_text(encoding="utf-8")
        tree = ast.parse(source)
        missing: list[int] = []
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != "_emit":
                continue
            if not any(keyword.arg == "stage" for keyword in node.keywords):
                missing.append(node.lineno)
        self.assertEqual(missing, [])

    def test_transfer_ranges_are_contiguous_and_balanced(self) -> None:
        self.assertEqual(SSHRunner._transfer_ranges(0, 16), [(0, 0)])
        ranges = SSHRunner._transfer_ranges(100, 16)
        self.assertEqual(len(ranges), 16)
        self.assertEqual(ranges[0][0], 0)
        self.assertEqual(ranges[-1][1], 100)
        self.assertTrue(all(end > start for start, end in ranges))
        self.assertLessEqual(max(end - start for start, end in ranges) - min(end - start for start, end in ranges), 1)

    def test_large_upload_uses_sixteen_parts_and_atomic_assembly(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}
        runner._require_temp_path = lambda *_args: None
        uploaded: list[tuple[int, int]] = []
        runner._upload_range = lambda _name, _local, _remote, start, end: uploaded.append((start, end))
        runner.run = mock.Mock(return_value=SSHResult({"assembled_size": str(64 * 1024 * 1024)}))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar.gz"
            path.write_bytes(b"x" * (64 * 1024 * 1024))
            runner._upload_file_parallel("vm", path, "/tmp/transfer/image.tar.gz", 0o400)
        self.assertEqual(len(uploaded), 16)
        self.assertEqual(sorted(uploaded)[0][0], 0)
        self.assertEqual(sorted(uploaded)[-1][1], 64 * 1024 * 1024)
        script = runner.run.call_args.args[1]
        self.assertIn("cat --", script)
        self.assertIn("mv -f", script)
        self.assertIn("assembled_size", script)

    def test_large_upload_preserves_parts_when_a_part_fails(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}
        runner._require_temp_path = lambda *_args: None
        runner._emit = lambda *_args, **_kwargs: None
        runner.run = mock.Mock()
        runner._upload_range = mock.Mock(side_effect=OSError("disconnect"))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar.gz"
            path.write_bytes(b"x" * (64 * 1024 * 1024))
            with self.assertRaisesRegex(OSError, "disconnect"):
                runner._upload_file_parallel("vm", path, "/tmp/transfer/image.tar.gz", 0o400)
        runner.run.assert_not_called()

    def test_large_download_uses_sixteen_parts_and_atomic_assembly(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}
        runner._require_temp_path = lambda *_args: None
        downloaded: list[tuple[int, int]] = []

        def download_range(_name, _remote, local, start, end, _file_size):
            downloaded.append((start, end))
            local.write_bytes(bytes([len(downloaded)]) * (end - start))

        runner._download_range = download_range
        with tempfile.TemporaryDirectory() as directory:
            local = Path(directory) / "image.tar.gz"
            runner._download_file_parallel("vm", "/tmp/transfer/image.tar.gz", local, 64)
            self.assertEqual(local.stat().st_size, 64)
            self.assertEqual(len(downloaded), 16)
            self.assertEqual(sorted(downloaded)[0][0], 0)
            self.assertEqual(sorted(downloaded)[-1][1], 64)
            self.assertFalse(any(local.parent.glob("image.tar.gz.parallel-*")))

    def test_large_download_recovers_failed_parallel_parts_serially(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}
        runner._require_temp_path = lambda *_args: None
        events: list[str] = []
        runner._emit = lambda _name, **event: events.append(event["event"])
        attempts: dict[int, int] = {}

        def download_range(_name, _remote, local, start, end, _file_size):
            attempts[start] = attempts.get(start, 0) + 1
            if start != 0 and attempts[start] == 1:
                raise paramiko.SSHException("connection reset")
            local.write_bytes(bytes([start % 251]) * (end - start))

        runner._download_range = download_range
        with tempfile.TemporaryDirectory() as directory:
            local = Path(directory) / "image.tar.gz"
            runner._download_file_parallel("vm", "/tmp/transfer/image.tar.gz", local, 64)
            self.assertEqual(local.stat().st_size, 64)
            self.assertFalse(any(local.parent.glob("image.tar.gz.parallel-*")))
        self.assertIn("transfer_parallel_degraded", events)
        self.assertEqual(TRANSFER_MAX_ACTIVE_CONNECTIONS, 4)

    def test_large_download_keeps_checkpoints_after_part_failure(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}
        runner._require_temp_path = lambda *_args: None

        def download_range(_name, _remote, local, start, end, _file_size):
            if start == 0:
                local.write_bytes(b"done" * ((end - start) // 4))
                return
            raise OSError("disconnect")

        runner._download_range = download_range
        with tempfile.TemporaryDirectory() as directory:
            local = Path(directory) / "image.tar.gz"
            with self.assertRaisesRegex(OSError, "disconnect"):
                runner._download_file_parallel("vm", "/tmp/transfer/image.tar.gz", local, 64)
            self.assertTrue((local.parent / "image.tar.gz.parallel-part-00").is_file())
            self.assertFalse(local.exists())

    def test_download_uses_prefetch_and_resumes_local_checkpoint(self) -> None:
        value = b"0123456789"
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("vm", "/tmp/transfer")}

        class Source:
            def __init__(self):
                self.position = 0
                self.prefetch_args = None
                self.channel = FakeChannel()

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def settimeout(self, _timeout):
                pass

            def seek(self, position):
                self.position = position

            def prefetch(self, file_size, max_concurrent_requests):
                self.prefetch_args = (file_size, max_concurrent_requests)

            def read(self, size):
                chunk = value[self.position:self.position + size]
                self.position += len(chunk)
                return chunk

        source = Source()

        class SFTP:
            def lstat(self, _path):
                return SimpleNamespace(st_size=len(value), st_mode=stat.S_IFREG | 0o600, st_nlink=1)

            def file(self, _path, _mode):
                return source

            def close(self):
                pass

        class Client:
            def open_sftp(self):
                return SFTP()

            def close(self):
                pass

        runner.connect = lambda _name: Client()
        with tempfile.TemporaryDirectory() as directory:
            local = Path(directory) / "recovery.tar"
            local.write_bytes(value[:3])
            runner.download_file("vm", "/tmp/transfer/recovery.tar", local)
            self.assertEqual(local.read_bytes(), value)
        self.assertEqual(source.prefetch_args, (len(value), 64))

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

    def test_structured_ssh_events_never_record_command_or_input(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            runner = self.runner(b"health=pass\n")
            runner.event_log_path = str(path)
            runner.release_id = "235-aaaaaaaaaaaa-1-deadbeef"
            runner.deployment_mode = "blue-green"
            runner.run_with_input("vm", "echo api_key=secret", {"health"}, b"password=secret\n")
            content = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in content.splitlines()]
            self.assertNotIn("api_key", content)
            self.assertNotIn("password", content)
            self.assertNotIn("secret", content)
            self.assertEqual(events[0]["event"], "command_started")
            self.assertEqual(events[-1]["event"], "command_finished")
            self.assertTrue(all(event["node"] == "vm" for event in events))

    def test_release_remote_commands_write_root_only_raw_logs_on_every_node(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.release_id = "235-aaaaaaaaaaaa-1-deadbeef"
        runner.deployment_mode = "downtime"
        command_id = "0123456789abcdef"
        for node, template in REMOTE_RAW_LOGS.items():
            command = runner._wrap_remote_raw_logging(node, "printf 'health=pass\\n'", command_id)
            expected = template.format(release_id=runner.release_id)
            self.assertIn(expected, command)
            self.assertIn("install -d -o 0 -g 0 -m 700", command)
            self.assertIn("0:0:600:1", command)
            self.assertIn(command_id, command)
            self.assertIn("stream=stdout", command)
            self.assertIn("stream=stderr", command)

    def test_non_release_remote_commands_do_not_create_raw_logs(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.release_id = None
        runner.deployment_mode = None
        command = runner._wrap_remote_raw_logging("backup", "printf 'health=pass\\n'", "0123456789abcdef")
        self.assertNotIn("remote.raw.log", command)

    def test_reads_only_protected_structured_remote_event_log(self) -> None:
        value = b'{"schema":1}\n'
        runner = object.__new__(SSHRunner)
        runner.connect = lambda _name: FakeSFTPClient(value)
        runner.temp_dirs = set()
        self.assertEqual(runner.read_release_events("racknerd", "235-aaaaaaaaaaaa-1-deadbeef"), value)

    def test_run_with_input_keeps_structured_output_contract(self) -> None:
        result = self.runner(b"health=pass\n").run_with_input("vm", "read secret", {"health"}, b"secret\n")
        self.assertEqual(result.values, {"health": "pass"})

    def test_rejects_non_structured_or_unknown_output(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "non-structured"):
            self.runner(b"hello\n").run("vm", "true", {"health"})
        with self.assertRaisesRegex(RuntimeError, "undeclared"):
            self.runner(b"health=pass\nsecret=value\n").run("vm", "true", {"health"})

    def test_rejects_any_stderr(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "unexpected stderr"):
            self.runner(b"health=pass\n", b"token=secret\n").run("vm", "true", {"health"})

    def test_successful_remote_wrapper_does_not_promote_stderr_to_exit_97(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.release_id = "235-aaaaaaaaaaaa-1-deadbeef"
        runner.deployment_mode = "downtime"
        wrapped = runner._wrap_remote_raw_logging("racknerd", "printf 'health=pass\\n'; printf 'warning\\n' >&2", "0123456789abcdef")
        self.assertNotIn("code=97", wrapped)
        self.assertIn("stream=stderr exit=%s", wrapped)

    def test_temp_dir_rejects_path_outside_base(self) -> None:
        runner = self.runner(b"temp_dir=/tmp/escape\n")
        with self.assertRaisesRegex(RuntimeError, "invalid temporary"):
            runner.create_temp_dir("vm", "/opt/release", "stage")

    def test_sftp_rejects_unregistered_path(self) -> None:
        runner = self.runner(b"")
        with self.assertRaisesRegex(RuntimeError, "outside a registered"):
            runner.upload("vm", b"data", "/tmp/predictable")

    def test_copy_file_between_retries_transient_sftp_disconnect(self) -> None:
        runner = object.__new__(SSHRunner)
        runner.temp_dirs = {("racknerd", "/tmp/source"), ("vm", "/tmp/target")}
        runner._emit = lambda *_args, **_kwargs: None
        attempts = {"source": 0}

        class SourceFile:
            def __init__(self, fail: bool):
                self.fail = fail

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def read(self, _size: int) -> bytes:
                if self.fail:
                    raise paramiko.SSHException("connection dropped")
                return b"payload"

        class TargetFile:
            def __init__(self, owner):
                self.owner = owner

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def write(self, value: bytes) -> None:
                self.owner.value = value

            def flush(self) -> None:
                pass

        class SFTP:
            def __init__(self, source: bool, fail: bool = False):
                self.source = source
                self.fail = fail
                self.value = b""

            def stat(self, _path: str):
                return SimpleNamespace(st_size=7, st_mode=stat.S_IFREG | 0o600, st_nlink=1)

            lstat = stat

            def file(self, _path: str, mode: str):
                if self.source:
                    return SourceFile(self.fail)
                return TargetFile(self)

            def chmod(self, _path: str, _mode: int) -> None:
                pass

            def close(self) -> None:
                pass

        class Client:
            def __init__(self, source: bool, fail: bool = False):
                self.sftp = SFTP(source, fail)

            def open_sftp(self):
                return self.sftp

            def close(self) -> None:
                pass

        def connect(name: str):
            if name == "racknerd":
                attempts["source"] += 1
                return Client(True, attempts["source"] == 1)
            return Client(False)

        runner.connect = connect
        self.assertEqual(runner.copy_file_between("racknerd", "/tmp/source/file", "vm", "/tmp/target/file"), 7)
        self.assertEqual(attempts["source"], 2)


if __name__ == "__main__":
    unittest.main()
