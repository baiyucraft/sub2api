from __future__ import annotations

import os
import shutil
import subprocess
import unittest
from pathlib import Path


SCRIPT_ROOT = Path(__file__).resolve().parents[2]
HELPER = SCRIPT_ROOT / "maintenance" / "release" / "backup-upload.sh"


def bash_binary() -> str | None:
    git_bash = Path("C:/Program Files/Git/bin/bash.exe")
    return str(git_bash) if git_bash.is_file() else shutil.which("bash")


class BackupUploadConfigTest(unittest.TestCase):
    def setUp(self) -> None:
        self.bash = bash_binary()
        if self.bash is None:
            self.skipTest("Bash is required for the backup upload argument fixture")

    def test_upload_uses_configured_host_user_key_and_port(self) -> None:
        script = f'''source "$1"
backup_upload_init
ssh() {{ printf '%s\\n' "$@"; printf 'OK %s %s\\n' "$6" "$7"; }}
backup_upload_send artifact.tar.age {'a' * 64}
'''
        for host in ("upload.example.test", "2001:db8::20"):
            with self.subTest(host_kind="ipv6" if ":" in host else "domain"):
                environment = {
                    **os.environ,
                    "BACKUP_UPLOAD_HOST": host,
                    "BACKUP_UPLOAD_USER": "fixture",
                    "BACKUP_UPLOAD_KEY": "/tmp/fixture-key",
                    "BACKUP_UPLOAD_PORT": "2202",
                }
                result = subprocess.run(
                    [self.bash, "--noprofile", "--norc", "-c", script, "backup-upload-test", str(HELPER)],
                    env=environment, text=True, capture_output=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("/tmp/fixture-key", result.stdout)
                self.assertIn("2202", result.stdout)
                self.assertIn(f"fixture@{host}", result.stdout)
                self.assertIn("upload daily artifact.tar.age", result.stdout)

    def test_missing_upload_configuration_fails_without_values_in_output(self) -> None:
        environment = dict(os.environ)
        for name in ("BACKUP_UPLOAD_HOST", "BACKUP_UPLOAD_USER", "BACKUP_UPLOAD_KEY", "BACKUP_UPLOAD_PORT"):
            environment.pop(name, None)
        environment["BACKUP_UPLOAD_HOST"] = "upload.example.test"
        result = subprocess.run(
            [self.bash, "--noprofile", "--norc", "-c", 'source "$1"; backup_upload_init', "backup-upload-test", str(HELPER)],
            env=environment, text=True, capture_output=True, timeout=10,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("BACKUP_UPLOAD_USER is required", result.stderr)
        self.assertNotIn("upload.example.test", result.stderr + result.stdout)


if __name__ == "__main__":
    unittest.main()
