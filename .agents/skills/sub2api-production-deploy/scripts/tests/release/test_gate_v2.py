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
from release.manifest import release_unit_relative_paths, validate_manifest_profile_contract
from release.migration_planner import CHECKSUM_POLICY_VERSION, catalog_sha256, checksum_policy_sha256
from release.paths import LAYOUT_SKILL_V1
from release.profiles import CURRENT_RELEASE_PROFILE, get_profile


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

    @staticmethod
    def _catalog() -> list[dict]:
        return [
            {"filename": "246_example.sql", "checksum": "a" * 64, "non_transactional": False},
            {"filename": "285_upstream_null_rate_lifecycle.sql", "checksum": "b" * 64, "non_transactional": False},
            {"filename": "286_add_payment_order_bonus_amount.sql", "checksum": "c" * 64, "non_transactional": False},
            {"filename": "287_add_typesafe_platform.sql", "checksum": "d" * 64, "non_transactional": False},
        ]

    def _document(self, pending: list[dict] | None = None, *, profile: str = CURRENT_RELEASE_PROFILE) -> dict:
        catalog = self._catalog()
        contract = get_profile(profile)
        image = "sha256:" + "b" * 64
        snapshot = "c" * 64
        archive = b"candidate"
        (self.root / "candidate.tar.gz").write_bytes(archive)
        manifest = {
            "schema": 2,
            "deployment_mode": "blue-green",
            "profile": profile,
            "release_id": f"{profile}-aaaaaaaaaaaa-1-aaaaaaaa",
            "version": contract["version"],
            "origin": contract["origin"],
            "vm_identity": contract["vm_identity"],
            "parent_profile": contract["parent"],
            "new_migrations": list(contract["new_migrations"]),
            "release_policy": dict(contract["release_policy"]),
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
            "recovery_gate": {
                "schema": 1,
                "mode": "specialized",
                "base_commit": "f" * 40,
                "target_commit": "a" * 40,
                "reason_codes": ["migration_changed"],
                "changed_paths_sha256": "1" * 64,
                "estimated_extra_seconds": 600,
            },
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
        evidence["migration_evidence"]["migration_285"] = {
            "checksum": catalog[1]["checksum"], "preflight": True, "postflight": True,
            "vm_semantics": True, "verified_replay": True,
        }
        return {"gate_version": 2, "profile_id": int(profile), "manifest": manifest, "evidence": evidence}

    def _sign(self, document: dict) -> None:
        payload = self.root / "gate.json"
        payload.write_bytes(canonical_json(document) + b"\n")
        subprocess.run(
            ["openssl", "pkeyutl", "-sign", "-inkey", str(self.private_key), "-rawin", "-in", str(payload), "-out", str(self.root / "gate.sig")],
            check=True,
            capture_output=True,
        )

    def _verify(self, *, allow_historical_runner: bool = False, profile: str = CURRENT_RELEASE_PROFILE, asset_checksum=None, validate_profile_contract: bool = False) -> dict:
        with (
            mock.patch("release.gate.validate_manifest_profile_contract", wraps=validate_manifest_profile_contract if validate_profile_contract else None),
            mock.patch("release.manifest.discover_migration_catalog", return_value=self._catalog()),
            mock.patch("release.gate.runner_checksum", return_value="d" * 64),
            mock.patch("release.gate.release_asset_checksums", return_value=self._assets()),
            mock.patch("release.gate.sha256_file", side_effect=asset_checksum or self._asset_checksum),
        ):
            return verify_gate(
                self.root,
                self.public_key,
                profile,
                allow_historical_runner=allow_historical_runner,
                accepted_schemas=frozenset({2}),
            )

    def test_valid_empty_pending_round_trip(self) -> None:
        document = self._document()
        self._sign(document)
        self.assertEqual(self._verify(validate_profile_contract=True), document)

    def test_current_profile_rejects_signed_wrong_version_parent_or_migrations(self) -> None:
        for field, value, message in (
            ("version", "0.2.10-baiyu", "version does not match"),
            ("parent_profile", "258", "parent profile does not match"),
            ("new_migrations", get_profile("262")["new_migrations"], "new migrations do not match"),
            ("new_migrations", ["284_unified_proxy_bindings.sql"], "new migrations do not match"),
        ):
            with self.subTest(field=field):
                document = self._document()
                document["manifest"][field] = value
                self._sign(document)
                with self.assertRaisesRegex(RuntimeError, message):
                    self._verify(validate_profile_contract=True)

    def test_profile_260_historical_signed_contract_rejects_drift(self) -> None:
        document = self._document(profile="260")
        self._sign(document)
        self.assertEqual(self._verify(profile="260", allow_historical_runner=True, validate_profile_contract=True), document)
        for field, value, message in (
            ("version", "0.2.13-baiyu", "version does not match"),
            ("parent_profile", "260", "parent profile does not match"),
            ("new_migrations", [], "new migrations do not match"),
            ("new_migrations", get_profile("261")["new_migrations"], "new migrations do not match"),
        ):
            with self.subTest(field=field):
                document = self._document(profile="260")
                document["manifest"][field] = value
                self._sign(document)
                with self.assertRaisesRegex(RuntimeError, message):
                    self._verify(profile="260", allow_historical_runner=True, validate_profile_contract=True)

    def test_signed_profile_261_ordinary_pending_migrations_round_trip(self) -> None:
        pending = [{"filename": item["filename"], "checksum": item["checksum"]} for item in self._catalog()[2:]]
        document = self._document(pending=pending)
        self._sign(document)
        self.assertEqual(self._verify(validate_profile_contract=True), document)
        for invalid in (
            list(reversed(pending)),
            [{**pending[0], "checksum": "0" * 64}, pending[1]],
            [{**pending[0], "preflight": True, "postflight": True}, pending[1]],
        ):
            with self.subTest(pending=invalid):
                document = self._document(pending=invalid)
                self._sign(document)
                with self.assertRaises(RuntimeError):
                    self._verify(validate_profile_contract=True)

    def test_profile_262_historical_signed_contract_rejects_drift(self) -> None:
        document = self._document(profile="262")
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "only accepted for current profile 263"):
            self._verify(profile="262")
        self.assertEqual(self._verify(profile="262", allow_historical_runner=True, validate_profile_contract=True), document)
        for field, value, message in (
            ("version", "0.2.14-baiyu", "version does not match"),
            ("parent_profile", "262", "parent profile does not match"),
            ("new_migrations", [], "new migrations do not match"),
            ("new_migrations", get_profile("261")["new_migrations"], "new migrations do not match"),
        ):
            with self.subTest(field=field):
                document = self._document(profile="262")
                document["manifest"][field] = value
                self._sign(document)
                with self.assertRaisesRegex(RuntimeError, message):
                    self._verify(profile="262", allow_historical_runner=True, validate_profile_contract=True)

    def test_unregistered_profile_264_is_rejected_even_for_recovery(self) -> None:
        document = self._document()
        document["profile_id"] = 264
        document["manifest"].update(profile="264", release_id="264-aaaaaaaaaaaa-1-aaaaaaaa")
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "only accepted for current profile 263"):
            self._verify(profile="264")
        with self.assertRaisesRegex(ValueError, "unknown release profile: 264"):
            self._verify(profile="264", allow_historical_runner=True)

    def test_fast_gate_requires_non_restore_evidence(self) -> None:
        document = self._document()
        document["manifest"]["recovery_gate"].update(
            mode="fast",
            reason_codes=["ordinary_change"],
            estimated_extra_seconds=0,
        )
        document["evidence"]["vm_restore_verified"] = False
        document["evidence"]["release_policy"]["restore_points_verified"] = False
        self._sign(document)
        self.assertEqual(self._verify(), document)

    def test_fast_gate_cannot_claim_restore_verification(self) -> None:
        document = self._document()
        document["manifest"]["recovery_gate"].update(
            mode="fast",
            reason_codes=["ordinary_change"],
            estimated_extra_seconds=0,
        )
        document["evidence"]["release_policy"]["restore_points_verified"] = False
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "must not claim"):
            self._verify()

    def test_historical_runner_is_allowed_only_for_recovery(self) -> None:
        document = self._document()
        document["profile_id"] = 246
        document["manifest"].update(profile="246", release_id="246-aaaaaaaaaaaa-1-aaaaaaaa", version="0.2.1-baiyu")
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "only accepted for current profile 263"):
            self._verify(profile="246")
        self.assertEqual(self._verify(profile="246", allow_historical_runner=True), document)

    def test_previous_profile_requires_historical_runner_for_recovery(self) -> None:
        document = self._document(profile="260")
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "only accepted for current profile 263"):
            self._verify(profile="260")
        self.assertEqual(self._verify(profile="260", allow_historical_runner=True, validate_profile_contract=True), document)

    def test_signed_profile_261_inherited_pending_lifecycle_migration_round_trip(self) -> None:
        migration = self._catalog()[1]
        pending = {
            "filename": migration["filename"], "checksum": migration["checksum"],
            "preflight": True, "postflight": True, "rollback_policy": "coordinated_restore",
            "hook_results": {"preflight": True, "postflight": True, "vm_semantics": True, "verified_replay": True},
        }
        document = self._document(pending=[pending])
        self._sign(document)
        self.assertEqual(self._verify(validate_profile_contract=True), document)
        pending["checksum"] = "0" * 64
        document = self._document(pending=[pending])
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "checksum"):
            self._verify(validate_profile_contract=True)

    def test_historical_recovery_uses_signed_commit_assets_not_current_validator(self) -> None:
        document = self._document()
        document["profile_id"] = 257
        document["manifest"].update(profile="257", release_id="257-aaaaaaaaaaaa-1-aaaaaaaa", version="0.2.9-baiyu")
        self._sign(document)
        self.assertEqual(
            self._verify(profile="257", allow_historical_runner=True, asset_checksum=lambda _path: "new-validator"),
            document,
        )

    def test_signed_285_replay_requires_semantic_evidence(self) -> None:
        document = self._document()
        del document["evidence"]["migration_evidence"]["migration_285"]
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "migration evidence"):
            self._verify()

    def test_signed_285_requires_every_boolean_and_bound_checksum(self) -> None:
        for field in ("preflight", "postflight", "vm_semantics", "verified_replay"):
            for value in (False, 1, "true", None):
                with self.subTest(field=field, value=value):
                    document = self._document()
                    document["evidence"]["migration_evidence"]["migration_285"][field] = value
                    self._sign(document)
                    with self.assertRaisesRegex(RuntimeError, "285 semantic evidence"):
                        self._verify()
        document = self._document()
        document["evidence"]["migration_evidence"]["migration_285"]["checksum"] = "0" * 64
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "285 semantic evidence"):
            self._verify()

    def test_285_pending_cannot_use_ordinary_or_incomplete_hook_evidence(self) -> None:
        migration = self._catalog()[1]
        document = self._document(pending=[{"filename": migration["filename"], "checksum": migration["checksum"]}])
        with self.assertRaisesRegex(RuntimeError, "hooked pending item"):
            _validate_v2_pending(document["manifest"], document["evidence"])
        pending = {
            "filename": migration["filename"], "checksum": migration["checksum"],
            "preflight": True, "postflight": True, "rollback_policy": "coordinated_restore",
            "hook_results": {"preflight": True, "postflight": True},
        }
        document = self._document(pending=[pending])
        with self.assertRaisesRegex(RuntimeError, "incomplete hook results"):
            _validate_v2_pending(document["manifest"], document["evidence"])

    def test_285_semantic_evidence_rejects_unknown_fields_or_missing_catalog(self) -> None:
        document = self._document()
        document["evidence"]["migration_evidence"]["migration_285"]["raw_body"] = "not_allowed"
        with self.assertRaisesRegex(RuntimeError, "285 semantic evidence"):
            _validate_v2_pending(document["manifest"], document["evidence"])
        document = self._document()
        document["manifest"]["migration_catalog"] = document["manifest"]["migration_catalog"][:1]
        with self.assertRaisesRegex(RuntimeError, "285 catalog entry"):
            _validate_v2_pending(document["manifest"], document["evidence"])

    def test_historical_catalog_without_285_retains_old_evidence_contract(self) -> None:
        document = self._document(profile="259")
        document["manifest"]["migration_catalog"] = document["manifest"]["migration_catalog"][:1]
        del document["evidence"]["migration_evidence"]["migration_285"]
        _validate_v2_pending(document["manifest"], document["evidence"])

    def test_invalid_285_catalog_type_is_rejected_cleanly(self) -> None:
        for catalog in (None, "invalid", {}):
            with self.subTest(catalog=catalog):
                document = self._document()
                document["manifest"]["migration_catalog"] = catalog
                with self.assertRaisesRegex(RuntimeError, "catalog is invalid"):
                    _validate_v2_pending(document["manifest"], document["evidence"])

    def test_current_gate_rejects_different_local_validator(self) -> None:
        document = self._document()
        self._sign(document)
        with self.assertRaisesRegex(RuntimeError, "different vm-validate.sh"):
            self._verify(asset_checksum=lambda _path: "new-validator")

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
            verify_gate(self.root, self.public_key, "257", accepted_schemas=frozenset({1, 2}))
        v1.assert_not_called()
        v2.assert_not_called()

    def test_signed_v1_is_rejected_by_v2_only_entry(self) -> None:
        self._sign({"manifest": {"schema": 1}, "evidence": {}})
        with self.assertRaisesRegex(RuntimeError, "schema is not accepted"):
            verify_gate(self.root, self.public_key, "253", accepted_schemas=frozenset({2}))

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
