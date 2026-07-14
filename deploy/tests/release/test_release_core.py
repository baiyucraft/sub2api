from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import atomic_write, canonical_json
from release.gate import verify_gate
from release.manifest import migration_checksums
from release.profiles import get_profile
from release.state import RunLock, RunState


class ReleaseCoreTest(unittest.TestCase):
    def manifest(self, runner: str, expires_at: int) -> dict:
        profile = get_profile("182")
        return {
            "commit_sha": "a" * 40,
            "profile": "182",
            "runner_sha256": runner,
            "vm_validator_sha256": "validator",
            "release_asset_sha256": {"asset": "digest"},
            "origin": profile["origin"],
            "vm_identity": profile["vm_identity"],
            "expires_at": expires_at,
        }

    def test_atomic_write_replaces_complete_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            atomic_write(path, b"old\n")
            atomic_write(path, b"new\n")
            self.assertEqual(path.read_bytes(), b"new\n")
            self.assertFalse(list(path.parent.glob(f".{path.name}.*")))

    def test_stale_lock_file_does_not_block_new_process(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            path.write_text("pid=999999\n", encoding="utf-8")
            with RunLock(path):
                pass
            self.assertTrue(path.exists())
            self.assertIn(f"pid={os.getpid()}", path.read_text(encoding="utf-8"))

    def test_active_lock_rejects_second_release(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            with RunLock(path):
                with self.assertRaisesRegex(RuntimeError, "another release process"):
                    with RunLock(path):
                        pass

    def test_terminal_state_cannot_resume_running(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            state = RunState.create(Path(directory) / "state.json", "release")
            state.transition("vm", "failed")
            with self.assertRaisesRegex(RuntimeError, "terminal"):
                state.transition("vm", "running")

    def test_migration_checksum_matches_runner_trimmed_content(self) -> None:
        profile = {"migrations": ["migration.sql"]}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            migration = root / "backend" / "migrations" / "migration.sql"
            migration.parent.mkdir(parents=True)
            migration.write_text("\nSELECT 1;\n\n", encoding="utf-8")
            with mock.patch("release.manifest.workspace_root", return_value=root):
                checksums = migration_checksums(profile)
        self.assertEqual(checksums["migration.sql"], hashlib.sha256(b"SELECT 1;").hexdigest())

    def test_vm_post_build_space_gate_does_not_double_count_image(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("required_before=$((database_size + 536870912))", validator)
        self.assertIn("required_free=$((database_size + 536870912))", validator)
        self.assertNotIn("required_free=$((database_size + candidate_size", validator)

    def test_gate_rejects_archive_replacement_and_expiry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            document = {
                "manifest": self.manifest("runner", int(time.time()) + 60),
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": digest,
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="runner"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", return_value="validator"):
                verify_gate(root, public_key, "182")
                archive.write_bytes(b"replaced")
                with self.assertRaisesRegex(RuntimeError, "archive checksum"):
                    verify_gate(root, public_key, "182")
                archive.write_bytes(b"candidate")
                document["manifest"]["expires_at"] = int(time.time()) - 1
                (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
                with self.assertRaisesRegex(RuntimeError, "expired"):
                    verify_gate(root, public_key, "182")

    def test_gate_rejects_runner_version_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            document = {
                "manifest": self.manifest("old", int(time.time()) + 60),
                "evidence": {"candidate_image_id": "sha256:" + "b" * 64, "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(), "integration_verified": True, "vm_restore_verified": True},
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="new"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", return_value="validator"):
                with self.assertRaisesRegex(RuntimeError, "different release runner"):
                    verify_gate(root, public_key, "182")


if __name__ == "__main__":
    unittest.main()
