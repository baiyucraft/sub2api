from __future__ import annotations

import sys
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import process


class ReleaseProcessTest(unittest.TestCase):
    def test_windows_worker_uses_no_window_without_detached_process(self) -> None:
        child = mock.Mock()
        with (
            mock.patch.object(process.os, "name", "nt"),
            mock.patch.object(process.subprocess, "CREATE_NO_WINDOW", 0x08000000, create=True),
            mock.patch.object(process.subprocess, "CREATE_NEW_PROCESS_GROUP", 0x00000200, create=True),
            mock.patch.object(process.subprocess, "DETACHED_PROCESS", 0x00000008, create=True),
            mock.patch.object(process.subprocess, "Popen", return_value=child) as popen,
        ):
            result = process.popen_detached_worker(["python", "worker.py"], cwd="work")
        self.assertIs(result, child)
        flags = popen.call_args.kwargs["creationflags"]
        self.assertEqual(flags & 0x08000000, 0x08000000)
        self.assertEqual(flags & 0x00000200, 0x00000200)
        self.assertEqual(flags & 0x00000008, 0)
        self.assertNotIn("start_new_session", popen.call_args.kwargs)

    def test_windows_child_inherits_no_window_flag(self) -> None:
        with (
            mock.patch.object(process.os, "name", "nt"),
            mock.patch.object(process.subprocess, "CREATE_NO_WINDOW", 0x08000000, create=True),
            mock.patch.object(process.subprocess, "run") as run,
        ):
            process.run_hidden(["openssl", "version"], creationflags=0x20, check=True)
        self.assertEqual(run.call_args.kwargs["creationflags"], 0x08000020)
        self.assertTrue(run.call_args.kwargs["check"])

    def test_linux_worker_starts_new_session(self) -> None:
        with mock.patch.object(process.os, "name", "posix"), mock.patch.object(process.subprocess, "Popen") as popen:
            process.popen_detached_worker(["python", "worker.py"])
        self.assertTrue(popen.call_args.kwargs["start_new_session"])
        self.assertNotIn("creationflags", popen.call_args.kwargs)


if __name__ == "__main__":
    unittest.main()
