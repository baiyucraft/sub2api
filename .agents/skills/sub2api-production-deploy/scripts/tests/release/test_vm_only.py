from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import canonical_json
from release.gate import verify_gate_v2, verify_vm_only_gate
from release.manifest import create_vm_only_manifest


class VMOnlyGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.private_key = self.root / "private.pem"
        self.public_key = self.root / "public.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(self.private_key)], check=True, capture_output=True)
        subprocess.run(["openssl", "pkey", "-in", str(self.private_key), "-pubout", "-out", str(self.public_key)], check=True, capture_output=True)

    def tearDown(self) -> None:
        self.temp.cleanup()

    def _bundle(self) -> Path:
        archive = b"candidate"
        (self.root / "candidate.tar.gz").write_bytes(archive)
        manifest = {
            "schema": 2,
            "vm_only_schema": 1,
            "scope": "vm-only",
            "release_id": "248-aaaaaaaaaaaa-1-aaaaaaaa",
            "created_at": int(time.time()),
            "expires_at": int(time.time()) + 3600,
            "commit_sha": "a" * 40,
            "origin": "https://github.com/baiyucraft/sub2api.git",
            "profile": "248",
            "version": "0.2.3-baiyu",
            "vm_identity": "sub2api-dev",
            "vm_port": 8211,
            "vm_data": "/opt/sub2api-deploy/data-dev",
            "source_archive_sha256": "b" * 64,
            "vm_only_validator_sha256": "c" * 64,
            "vm_only_switch_sha256": "d" * 64,
        }
        evidence = {
            "candidate_image_id": "sha256:" + "e" * 64,
            "candidate_archive_sha256": hashlib.sha256(archive).hexdigest(),
            "candidate_size": len(archive),
            "candidate_identity_verified": True,
            "candidate_health": "pass",
            "existing_app_health": "pass",
            "vm_database_boundary": True,
            "vm_redis_boundary": True,
            "data_dev_boundary": True,
        }
        document = {"gate_version": 2, "profile_id": 248, "manifest": manifest, "evidence": evidence}
        (self.root / "gate.json").write_bytes(canonical_json(document) + b"\n")
        subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(self.private_key), "-rawin", "-in", str(self.root / "gate.json"), "-out", str(self.root / "gate.sig")], check=True, capture_output=True)
        return self.root

    def test_vm_only_gate_is_verified_by_separate_contract(self) -> None:
        bundle = self._bundle()
        with mock.patch("release.gate.get_profile", return_value={"gate_schema": 2, "version": "0.2.3-baiyu", "origin": "https://github.com/baiyucraft/sub2api.git"}):
            self.assertEqual(verify_vm_only_gate(bundle, self.public_key, "248")["manifest"]["scope"], "vm-only")

    def test_vm_only_gate_is_not_accepted_as_production_gate(self) -> None:
        bundle = self._bundle()
        with self.assertRaises(Exception):
            verify_gate_v2(bundle, self.public_key, "248")

    def test_vm_only_manifest_has_no_production_snapshot_fields(self) -> None:
        profile = {
            "name": "248",
            "version": "0.2.3-baiyu",
            "origin": "https://github.com/baiyucraft/sub2api.git",
            "gate_ttl_seconds": 3600,
        }
        with mock.patch("release.manifest.check_output_hidden", return_value="https://github.com/baiyucraft/sub2api.git"):
            manifest = create_vm_only_manifest("a" * 40, profile, "248-aaaaaaaaaaaa-1-aaaaaaaa", "b" * 64, "c" * 64, "d" * 64)
        self.assertNotIn("production_current_image_id", manifest)
        self.assertNotIn("production_snapshot_sha256", manifest)


if __name__ == "__main__":
    unittest.main()
