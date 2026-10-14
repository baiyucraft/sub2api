"""Cross-module rejection tests, not a Linux/nspawn integration pass.

These tests exercise the actual Guest receipt validator and final schema-2
measurement validator with malformed deterministic documents. They neither
mock runtime observations nor assert that a real recovery drill has run.
"""
from copy import deepcopy
from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from release import full_recovery_drill as drill
from release import recovery_evidence as evidence
import test_recovery_evidence as fixtures


class FullRecoveryIntegrationContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        fixtures.RecoveryEvidenceTests.setUpClass()

    @classmethod
    def tearDownClass(cls):
        fixtures.RecoveryEvidenceTests.tearDownClass()

    def setUp(self):
        self.fixture = fixtures.RecoveryEvidenceTests()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        p = self.fixture.payload
        self.payload = deepcopy(p)
        self.receipt = {"schema": 1, "type": "sub2api-full-recovery-measurements", **{k: deepcopy(p[k]) for k in ("drill_id", "created_at", "completed_at", "source", "measurements", "recovery_helper_bundle_sha256")}}
        self.receipt["measurements"].pop("rto")
        clean = self.receipt["measurements"]["cleanup"]
        for key in ("owned_guest_destroyed", "foreign_resources_unchanged", "foreign_before_sha256", "foreign_after_sha256"):
            clean.pop(key)
        self.config = {k: self.receipt["source"][k] for k in ("release_id", "artifact_sha256", "pre_image_id", "approved_identity_sha256", "approved_recipient_sha256")}
        self.config.update(drill_id=p["drill_id"], recovery_helper_bundle_sha256=p["recovery_helper_bundle_sha256"])

    def reject(self, receipt):
        with self.assertRaises((RuntimeError, drill.FullDrillBlocked)):
            drill.validate_guest_receipt(receipt, self.config)

    def test_missing_namespace_is_never_measured_success(self):
        for name in drill.NAMESPACES:
            receipt = deepcopy(self.receipt)
            del receipt["measurements"]["isolation"]["namespaces"][name]
            self.reject(receipt)

    def test_host_namespace_or_engine_alias_is_never_accepted(self):
        for name in drill.NAMESPACES:
            receipt = deepcopy(self.receipt)
            item = receipt["measurements"]["isolation"]["namespaces"][name]
            item["guest"] = item["host"]
            item["guest_pid1"] = item["host"]
            self.reject(receipt)
        receipt = deepcopy(self.receipt)
        isolation = receipt["measurements"]["isolation"]
        isolation["guest_docker_engine_id_sha256"] = isolation["host_docker_engine_id_sha256"]
        self.reject(receipt)

    def test_pause_duration_cannot_replace_actual_formal_health_wait(self):
        for observed in (0, 59999, 60000, 180001, True):
            receipt = deepcopy(self.receipt)
            receipt["measurements"]["redis"]["health_wait_ms"] = observed
            self.reject(receipt)

    def test_natural_expiry_does_not_substitute_for_observed_lazy_expiry(self):
        receipt = deepcopy(self.receipt)
        redis = receipt["measurements"]["redis"]
        redis["restored_keyspace_keys"] = redis["restored_dbsize"]
        self.reject(receipt)

    def test_resume_cannot_reapply_either_data_phase(self):
        for phase in ("postgres_restored", "redis_restored"):
            receipt = deepcopy(self.receipt)
            receipt["measurements"]["checkpoints"][phase].update(apply_count=2, resume_apply_count=1)
            self.reject(receipt)
        for key in ("postgres_oid_after", "redis_run_after_sha256"):
            receipt = deepcopy(self.receipt)
            fault = receipt["measurements"]["faults"]["cleanup_before_interrupt"]
            fault[key] = 54321 if key == "postgres_oid_after" else "f" * 64
            self.reject(receipt)

    def test_wrong_raw_checkpoint_and_false_cleanup_fail_before_host_destruction(self):
        receipt = deepcopy(self.receipt)
        receipt["measurements"]["checkpoints"]["compose_restored"]["done_base64"] = "YmFk"
        self.reject(receipt)
        for key in ("plaintext_destroyed", "active_claim_cleared", "stale_ingress_removed"):
            receipt = deepcopy(self.receipt)
            receipt["measurements"]["cleanup"][key] = False
            self.reject(receipt)

    def test_final_validator_requires_actual_host_cleanup(self):
        for key in ("owned_guest_destroyed", "foreign_resources_unchanged"):
            payload = deepcopy(self.payload)
            payload["measurements"]["cleanup"][key] = False
            with self.assertRaises(RuntimeError):
                evidence.validate_measurements(payload["measurements"], payload)

    def test_source_and_compose_cannot_change_old_image_or_archive(self):
        for section, key in (("compose", "restored_image_id"), ("postgres", "dump_sha256"), ("redis", "rdb_sha256")):
            receipt = deepcopy(self.receipt)
            receipt["measurements"][section][key] = "sha256:" + "f" * 64 if key == "restored_image_id" else "f" * 64
            self.reject(receipt)


if __name__ == "__main__":
    unittest.main()
