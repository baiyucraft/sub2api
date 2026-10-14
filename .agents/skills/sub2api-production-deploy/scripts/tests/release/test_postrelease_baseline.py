"""Never promote using a pre-Gate capture or an unmeasured candidate image."""
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import postrelease_baseline as baseline
from release.atomic import canonical_json


class PostreleaseBaselineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.release_id = "270-" + "1" * 12 + "-1792000000-aabbccdd"
        self.source = {"release_id": self.release_id, "artifact_sha256": "a" * 64, "approved_identity_sha256": "b" * 64, "approved_recipient_sha256": "c" * 64}
        for name in ("gate.json", "gate.sig", "candidate.tar.gz"):
            (self.root / name).write_bytes((name + " fixture").encode())
        self.evidence = {"candidate_archive_sha256": baseline.sha256_file(self.root / "candidate.tar.gz"), "candidate_image_id": "sha256:" + "d" * 64, "migration_evidence": {"pending": [{"filename": "298_fixture.sql"}]}}
        self.source["pre_image_id"] = self.evidence["candidate_image_id"]
        self.manifest = {"commit_sha": "1" * 40, "release_id": self.release_id, "catalog_sha256": "e" * 64, "checksum_policy_sha256": "f" * 64, "recovery_gate": {"full_recovery_evidence": {"payload": {"source": deepcopy(self.source)}}}}
        self.payload = {"drill_id": "full-dr-11111111-1111-4111-8111-111111111111", "created_at": 1792000000, "completed_at": 1792000100, "source": self.source, "measurements": {"redis": {"backup_dbsize": 100, "restored_dbsize": 90, "backup_expiring": 20, "restored_expiring": 10}, "isolation": {"guest_docker_engine_id_sha256": "7" * 64, "rootfs_identity_sha256": "8" * 64}}}
        self.load = {"schema": 1, "type": "sub2api-baseline-candidate-image-load", "drill_id": self.payload["drill_id"], "release_id": self.release_id, "candidate_archive_sha256": self.evidence["candidate_archive_sha256"], "candidate_image_id": self.evidence["candidate_image_id"], "observed_image_id": self.evidence["candidate_image_id"], "guest_docker_engine_id_sha256": "7" * 64, "rootfs_identity_sha256": "8" * 64, "load_exit_code": 0, "created_at": 1792000050}
        self.bundle = self.root / "bundle.sha256"
        hashes = ["a" * 64, self.evidence["candidate_archive_sha256"], baseline.sha256_file(self.root / "gate.json"), baseline.sha256_file(self.root / "gate.sig"), "2" * 64, "3" * 64]
        self.bundle.write_bytes(b"".join((digest + "  " + name + "\n").encode() for digest, name in zip(hashes, ("artifact.tar.age", "candidate.tar.gz", "gate.json", "gate.sig", "manifest", "SHA256SUMS"))))
        os.chmod(self.bundle, 0o600)
        self.classification = self.root / "classification.json"
        self.review = self.root / "full-review.json"
        self.candidate_load = self.root / "candidate-load.json"
        self.findings = self.root / "audit-findings.json"

    def build(self):
        for path, value in ((self.classification, {}), (self.review, {"payload": self.payload}), (self.candidate_load, self.load)):
            path.write_bytes(canonical_json(value)); os.chmod(path, 0o600)
        findings = {"schema": 1, "type": "sub2api-baseline-image-load-review", "drill_id": self.payload["drill_id"], "candidate_load_sha256": baseline.sha256_file(self.candidate_load), "candidate_bundle_sha256": baseline.sha256_file(self.bundle)}
        self.findings.write_bytes(canonical_json(findings)); os.chmod(self.findings, 0o600)
        self.payload["review"] = {"findings_sha256": baseline.sha256_file(self.findings)}
        self.review.write_bytes(canonical_json({"payload": self.payload}))
        with mock.patch.object(baseline, "verify_gate", return_value={"manifest": self.manifest, "evidence": self.evidence}), mock.patch.object(baseline, "verify_full_recovery_evidence", return_value=SimpleNamespace(target_commit="1" * 40)):
            return baseline.build_legacy_evidence(gate_dir=self.root, profile="270", classification=self.classification, full_review=self.review, candidate_bundle_checksum=self.bundle, candidate_load=self.candidate_load, audit_findings=self.findings, now=1792000200)

    def test_maps_only_current_release_actual_restore_and_candidate_load(self):
        result = self.build()
        self.assertEqual(result["release_id"], self.release_id)
        self.assertEqual(result["candidate_bundle_sha256"], hashlib.sha256(self.bundle.read_bytes()).hexdigest())
        migration = {"catalog_sha256": self.manifest["catalog_sha256"], "checksum_policy_sha256": self.manifest["checksum_policy_sha256"], "pending": self.evidence["migration_evidence"]["pending"]}
        self.assertEqual(result["migration_checksum"], hashlib.sha256(canonical_json(migration)).hexdigest())
        self.assertEqual(len(result), 21)

    def test_pre_gate_capture_is_rejected(self):
        self.source["release_id"] = self.release_id.replace("aabbccdd", "aabbccde")
        with self.assertRaisesRegex(RuntimeError, "this release backup"):
            self.build()

    def test_preswitch_old_image_backup_is_not_a_postrelease_baseline(self):
        self.source["pre_image_id"] = "sha256:" + "5" * 64
        with self.assertRaisesRegex(RuntimeError, "running candidate image"):
            self.build()

    def test_backup_for_another_identity_is_rejected(self):
        self.source["approved_identity_sha256"] = "4" * 64
        with self.assertRaisesRegex(RuntimeError, "identity differs"):
            self.build()

    def test_old_image_cannot_replace_candidate_load(self):
        self.load["observed_image_id"] = "sha256:" + "6" * 64
        with self.assertRaisesRegex(RuntimeError, "candidate image load"):
            self.build()

    def test_successful_load_on_the_host_or_another_guest_is_rejected(self):
        self.load["guest_docker_engine_id_sha256"] = "9" * 64
        with self.assertRaisesRegex(RuntimeError, "outside the measured Guest"):
            self.build()

    def test_current_floor_inequality_does_not_override_legacy_ttl_equality(self):
        self.payload["measurements"]["redis"]["restored_dbsize"] = 91
        with self.assertRaisesRegex(RuntimeError, "TTL equality"):
            self.build()

    def test_other_encrypted_artifact_is_rejected(self):
        self.source["artifact_sha256"] = "5" * 64
        with self.assertRaisesRegex(RuntimeError, "another encrypted artifact"):
            self.build()

    def test_sign_promote_reconciles_existing_signature_and_uses_original_promoter(self):
        client = mock.Mock()
        signed = {"evidence_sha256": "1" * 64, "signature_sha256": "2" * 64}
        promoted = {"promotion_status": "verified", "verified_target": "fixture", **signed, "verified_bundle_sha256": "3" * 64}
        client.run.side_effect = [SimpleNamespace(values=signed), SimpleNamespace(values={"staged": "true"}), SimpleNamespace(values=promoted)]
        client.create_temp_dir.side_effect = ["/opt/sub2api-deploy/release-input/baseline-evidence.abcdefgh", "/srv/sub2api-backups/releases/270/promotion-input/fixture.abcdefgh", "/srv/sub2api-backups/baseline-promote.abcdefgh"]
        drill = "dr-270-20261015T000000Z"
        result = baseline.sign_and_promote(release_id=self.release_id, drill_id=drill, evidence_sha256="1" * 64, runner=client)
        self.assertEqual(result, promoted)
        signing = client.run.call_args_list[0].args[1]
        self.assertIn("if test ! -e", signing)
        self.assertIn("/usr/local/libexec/sub2api-sign-dr-evidence", signing)
        self.assertNotIn(".pem", signing)
        self.assertEqual(client.copy_file_between.call_count, 2)
        self.assertTrue(client.upload_file.call_args.args[1].name.endswith("promote-dr-baseline.sh"))

    def test_failed_signer_never_transfers_or_promotes(self):
        client = mock.Mock()
        client.run.side_effect = RuntimeError("bounded signer failure")
        with self.assertRaisesRegex(RuntimeError, "signer failure"):
            baseline.sign_and_promote(release_id=self.release_id, drill_id="dr-270-20261015T000000Z", evidence_sha256="1" * 64, runner=client)
        client.copy_file_between.assert_not_called()
        client.upload_file.assert_not_called()


if __name__ == "__main__":
    unittest.main()
