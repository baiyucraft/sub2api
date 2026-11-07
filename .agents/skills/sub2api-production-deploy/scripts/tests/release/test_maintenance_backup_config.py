from __future__ import annotations

import os
import shutil
import subprocess
import unittest
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[6]
SCRIPT = REPOSITORY / "deploy/maintenance/181/maintenance-backup.sh"
BASH = shutil.which("bash")
if os.name == "nt" and Path("C:/Program Files/Git/bin/bash.exe").is_file():
    BASH = "C:/Program Files/Git/bin/bash.exe"


@unittest.skipUnless(BASH, "Bash is required for maintenance backup guards")
class MaintenanceBackupConfigTest(unittest.TestCase):
    def test_missing_upload_configuration_fails_before_ssh(self) -> None:
        source = SCRIPT.read_text(encoding="utf-8")
        preflight = source[:source.index("timestamp=$(date -u")]
        names = (
            "BACKUP_UPLOAD_HOST",
            "BACKUP_UPLOAD_USER",
            "BACKUP_UPLOAD_KEY",
            "BACKUP_UPLOAD_PORT",
        )
        for missing in names:
            with self.subTest(missing=missing):
                environment = {
                    key: value
                    for key, value in os.environ.items()
                    if key not in {"BASH_ENV", "ENV", "SHELLOPTS", *names}
                }
                environment.update({
                    "BACKUP_UPLOAD_HOST": "backup.example.test",
                    "BACKUP_UPLOAD_USER": "fixture",
                    "BACKUP_UPLOAD_KEY": "/tmp/fixture-key",
                    "BACKUP_UPLOAD_PORT": "2202",
                })
                environment.pop(missing)
                result = subprocess.run(
                    [str(BASH), "--noprofile", "--norc", "-c", preflight + "\nssh() { printf 'called\\n'; }\nssh\n"],
                    env=environment,
                    capture_output=True, text=True, timeout=10,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("called", result.stdout + result.stderr)

    def test_invalid_upload_values_fail_closed(self) -> None:
        source = SCRIPT.read_text(encoding="utf-8")
        preflight = source[:source.index("timestamp=$(date -u")]
        base = {
            key: value
            for key, value in os.environ.items()
            if key not in {"BASH_ENV", "ENV", "SHELLOPTS"}
        }
        base.update({
            "BACKUP_UPLOAD_HOST": "backup.example.test",
            "BACKUP_UPLOAD_USER": "fixture",
            "BACKUP_UPLOAD_KEY": "/tmp/fixture-key",
            "BACKUP_UPLOAD_PORT": "2202",
        })
        for name, value in (("BACKUP_UPLOAD_USER", "bad user"), ("BACKUP_UPLOAD_PORT", "65536")):
            with self.subTest(name=name):
                environment = {**base, name: value}
                result = subprocess.run(
                    [str(BASH), "--noprofile", "--norc", "-c", preflight + "\nprintf 'accepted\\n'\n"],
                    env=environment, capture_output=True, text=True, timeout=10,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("accepted", result.stdout)


if __name__ == "__main__":
    unittest.main()
