from __future__ import annotations

import copy
import hashlib
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
from release.gate import _validate_v2_pending, gate_payload, verify_gate
from release.manifest import release_unit_relative_paths
from release.migration_planner import CHECKSUM_POLICY_VERSION, catalog_sha256, checksum_policy_sha256
from release.paths import LAYOUT_SKILL_V1


class GateV2Test(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.private_key = self.root / "private.pem"
        self.public_key = self.root / "public.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(self.private_key)], check=True, capture_output=True)
        subprocess.run(["openssl", "pkey", "-in", str(self.private_key), "-pubout", "-out", str(self.public_key)], check=True, capture_output=True)

    def tearDown(self) -> None:
        self.temp.cleanup()

    @staticmethod
    def _assets() -> dict[str, str]:
        units = release_unit_relative_paths(LAYOUT_SKILL_V1)
        return {
            units["validator"]: "validator",
            units["gate_signer"]: "gate-signer",
            units["dr_signer"]: "dr-signer",
        }

    @staticmethod
    def _asset_checksum(path: Path) -> str:
        return {
            "vm-validate.sh": "validator",
            "sign-gate.sh": "gate-signer",
            "sign-dr-evidence.sh": "dr-signer",
        }[path.name]

    def _document(self, pending: list[dict] | None = None) -> dict:
        catalog = [{"filename": "246_example.sql", "checksum": "a" * 64, "non_transactional": False}]
        image = "sha256:" + "b" * 64
        snapshot = "c" * 64
        archive = b"candidate"
        (self.root / "candidate.tar.gz").write_bytes(archive)
        manifest = {
            "schema": 2,
            "profile": "242",
            "release_id": "242-aaaaaaaaaaaa-1-aaaaaaaa",
            "version": "0.1.182-baiyu",
            "commit_sha": "a" * 40,
            "expires_at": int(time.time()) + 3600,
            "release_asset_layout": LAYOUT_SKILL_V1,
            "runner_sha256": "d" * 64,
            "vm_validator_sha256": "validator",
            "vm_gate_signer_sha256": "gate-signer",
            "vm_dr_signer_sha256": "dr-signer",
            "release_asset_sha256": self._assets(),
            "production_current_image_id": image,
            "production_snapshot_sha256": snapshot,
            "migration_catalog": catalog,
            "catalog_sha256": catalog_sha256(catalog),
            "checksum_policy_sha256": checksum_policy_sha256(),
        }
        evidence = {
            "candidate_image_id": "sha256:" + "e" * 64,
            "candidate_archive_sha256": hashlib.sha256(archive).hexdigest(),
            "candidate_size": len(archive),
            "integration_verified": True,
            "vm_restore_verified": True,
            "vm_database_boundary": True,
            "vm_redis_boundary": True,
            "data_dev_boundary": True,
            "production_current_image_id": image,
            "production_snapshot_sha256": snapshot,
            "catalog_sha256": manifest["catalog_sha256"],
            "checksum_policy_sha256": checksum_policy_sha256(),
            "checksum_policy_version": CHECKSUM_POLICY_VERSION,
            "migration_evidence": {
                "database_high_watermark": None,
                "pending": pending or [],
                "existing_checksums_verified": True,
                "isolated_upgrade_verified": True,
                "final_schema_verified": True,
            },
            "release_policy": {"canary_verified": "not_checked", "restore_points_verified": True},
        }
        return {"gate_version": 2, "profile_id": 242, "manifest": manifest, "evidence": evidence}

    def _sign(self, document: dict) -> None:
        payload = self.root / "gate.json"
        payload.write_bytes(canonical_json(document) + b"\n")
        subprocess.run(
            ["openssl", "pkeyutl", "-sign", "-inkey", str(self.private_key), "-rawin", "-in", str(payload), "-out", str(self.root / "gate.sig")],
            check=True,
            capture_output=True,
        )

    def _verify(self, *, allow_historical_runner: bool = False) -> dict:
        with (
            mock.patch("release.gate.validate_manifest_profile_contract"),
            mock.patch("release.gate.get_profile", return_value={}),
            mock.patch("release.gate.runner_checksum", return_value="d" * 64),
            mock.patch("release.gate.release_asset_checksums", return_value=self._assets()),
            mock.patch("release.gate.sha256_file", side_effect=self._asset_checksum),
        ):
            return verify_gate(
                self.root,
                self.public_key,
                "242",
                allow_historical_runner=allow_historical_runner,
                accepted_schemas=frozenset({2}),
            )

    def test_valid_empty_pending_round_trip(self) -> None:
        document = self._document()
        self._sign(document)
        self.assertEqual(self._verify(), document)

    def test_historical_runner_is_allowed_only_for_recovery(self) -> None:
        document = self._document()
        self._sign(document)
        self.assertEqual(self._verify(allow_historical_runner=True), document)

    def test_schema_tamper_is_rejected_before_dispatch(self) -> None:
        document = self._document()
        self._sign(document)
        document["manifest"]["schema"] = 1
        (self.root / "gate.json").write_bytes(canonical_json(document) + b"\n")
        with (
            mock.patch("release.gate.verify_gate_v1") as v1,
            mock.patch("release.gate.verify_gate_v2") as v2,
            self.assertRaises(subprocess.CalledProcessError),
        ):
            verify_gate(self.root, self.public_key, "242", accepted_schemas=frozenset({1, 2}))
        v1.assert_not_called()
        v2.assert_not_called()

    def test_signed_v1_is_rejected_by_v2_only_entry(self) -> None:
        self._sign({"manifest": {"schema": 1}, "evidence": {}})
        with self.assertRaisesRegex(RuntimeError, "schema is not accepted"):
            verify_gate(self.root, self.public_key, "242", accepted_schemas=frozenset({2}))

    def test_unsigned_pending_mutation_is_rejected(self) -> None:
        document = self._document()
        self._sign(document)
        document["evidence"]["migration_evidence"]["pending"] = [{"filename": "246_example.sql", "checksum": "a" * 64}]
        (self.root / "gate.json").write_bytes(canonical_json(document) + b"\n")
        with self.assertRaises(subprocess.CalledProcessError):
            self._verify()

    def test_candidate_size_boolean_is_rejected(self) -> None:
        document = self._document()
        document["evidence"]["candidate_size"] = True
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "candidate size"):
            self._verify()

    def test_candidate_archive_size_must_match_file(self) -> None:
        document = self._document()
        document["evidence"]["candidate_size"] += 1
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "archive size"):
            self._verify()

    def test_candidate_size_must_be_positive_json_integer(self) -> None:
        for value in (0, -1, 1.0, "1"):
            with self.subTest(value=value):
                document = self._document()
                document["evidence"]["candidate_size"] = value
                self._sign(document)
                with self.assertRaisesRegex(RuntimeError, "candidate size"):
                    self._verify()

    def test_high_watermark_must_be_a_catalog_filename(self) -> None:
        document = self._document()
        document["evidence"]["migration_evidence"]["database_high_watermark"] = []
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "high watermark"):
            self._verify()

    def test_hook_results_must_cover_registered_phases(self) -> None:
        filename = "243_backfill_codex_fingerprint_seed.sql"
        catalog = [{"filename": filename, "checksum": "a" * 64, "non_transactional": False}]
        base = {
            "filename": filename,
            "checksum": "a" * 64,
            "preflight": True,
            "postflight": True,
            "rollback_policy": "coordinated_restore",
        }
        evidence = {
            "migration_evidence": {
                "database_high_watermark": None,
                "pending": [{**base, "hook_results": {}}],
                "existing_checksums_verified": True,
                "isolated_upgrade_verified": True,
                "final_schema_verified": True,
            }
        }
        with self.assertRaisesRegex(RuntimeError, "incomplete hook results"):
            _validate_v2_pending({"migration_catalog": catalog}, evidence)
        valid = copy.deepcopy(evidence)
        valid["migration_evidence"]["pending"][0]["hook_results"] = {"preflight": True, "postflight": True}
        _validate_v2_pending({"migration_catalog": catalog}, valid)

    def test_ordinary_pending_cannot_carry_hook_fields(self) -> None:
        filename = "246_example.sql"
        catalog = [{"filename": filename, "checksum": "a" * 64, "non_transactional": False}]
        evidence = {
            "migration_evidence": {
                "database_high_watermark": None,
                "pending": [{"filename": filename, "checksum": "a" * 64, "preflight": True}],
                "existing_checksums_verified": True,
                "isolated_upgrade_verified": True,
                "final_schema_verified": True,
            }
        }
        with self.assertRaisesRegex(RuntimeError, "ordinary migration"):
            _validate_v2_pending({"migration_catalog": catalog}, evidence)


if __name__ == "__main__":
    unittest.main()
