"""In-memory signatures use a fixed test key, never the production signer."""
import base64
from copy import deepcopy
from dataclasses import replace
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release import recovery_evidence as evidence
from release import recovery_bundle
from release.recovery_gate import changed_paths_sha256


class RecoveryEvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory()
        cls.workspace = Path(cls.temporary.name).resolve()
        # RFC 8032 deterministic test seed, not a generated or installed root.
        cls.key = Ed25519PrivateKey.from_private_bytes(bytes.fromhex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"))
        public = cls.key.public_key().public_bytes(serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo)
        cls.key_path = cls.workspace / evidence.KEY_PATH
        cls.key_path.parent.mkdir(parents=True)
        cls.key_path.write_bytes(public)
        for name in recovery_bundle.REQUIRED:
            group = "release/" if name.endswith(".py") else ("maintenance/181/" if name == "restore-backup-units.sh" else "maintenance/release/")
            path = cls.workspace / (recovery_bundle.PREFIX + group + name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(("fixed test bytes " + name + "\n").encode())
        for path in ("release.py", "maintenance/181/mask-backup-units.sh", "release/supervisor.py", "release/production.py"):
            output = cls.workspace / (recovery_bundle.PREFIX + path)
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_bytes(b"fixed test fixture\n")
        cls.git("init", "-q")
        cls.git("add", ".")
        cls.git("-c", "user.name=Recovery Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "基线测试")
        cls.base = cls.git("rev-parse", "HEAD").decode().strip()
        changed = cls.workspace / (recovery_bundle.PREFIX + "maintenance/release/restore.sh")
        changed.write_bytes(b"changed fixed test restore bytes\n")
        cls.git("add", ".")
        cls.git("-c", "user.name=Recovery Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "目标测试")
        cls.target = cls.git("rev-parse", "HEAD").decode().strip()
        cls.report = {"schema": 1, "mode": "full", "base_commit": cls.base, "target_commit": cls.target, "reason_codes": ["recovery_change_requires_review", "release_state_machine_changed"], "changed_paths_sha256": changed_paths_sha256([changed.relative_to(cls.workspace).as_posix()]), "estimated_extra_seconds": 2400}
        cls.scope = evidence.derive_expected_scope(cls.workspace, cls.report)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    @classmethod
    def git(cls, *args):
        return subprocess.check_output(["git", *args], cwd=cls.workspace, stderr=subprocess.DEVNULL)

    def setUp(self):
        self.drill = "full-dr-11111111-1111-4111-8111-111111111111"
        self.now = 1000121
        fault = {"exit_code": 79, "resume_verified": True, "postgres_oid_before": 12345, "postgres_oid_after": 12345, "redis_run_before_sha256": "5" * 64, "redis_run_after_sha256": "5" * 64}
        source = {"kind": "encrypted-production-baseline", "artifact_sha256": "8" * 64, "observed_artifact_sha256": "8" * 64, "artifact_bytes": 123456, "identity_available": True, "decryption_verified": True, "decrypted_sha256": "9" * 64, "release_id": "270-" + self.base[:12] + "-1000000-11111111", "recovery_point_sha256": "b" * 64, "pre_image_id": "sha256:" + "2" * 64, "recipient_type": "ssh-ed25519", "approved_recipient_sha256": "d" * 64, "derived_recipient_sha256": "d" * 64, "approved_identity_sha256": "c" * 64, "observed_identity_sha256": "c" * 64}
        source["archive_inventory"] = {key: source[key] for key in ("artifact_sha256", "decrypted_sha256", "recovery_point_sha256")}
        source["archive_inventory"].update(postgres_dump_sha256="3" * 64, redis_rdb_sha256="6" * 64)
        source["archive_inventory_sha256"] = evidence.checksum(source["archive_inventory"])
        identity_payload = {"source": source, "recovery_helper_bundle_sha256": self.scope["recovery_helper_bundle_sha256"]}
        binding = hashlib.sha256(evidence.checkpoint_identity_bytes(identity_payload)).hexdigest()
        checkpoints = {}
        for phase in evidence.PHASES:
            raw = evidence.checkpoint_marker_bytes(identity_payload, phase)
            checkpoints[phase] = {"completed": True, "live_reverified": True, "apply_count": 1, "resume_apply_count": 0, "binding_sha256": binding, **{state + "_base64": base64.b64encode(raw).decode() for state in ("started", "done")}, **{state + "_sha256": hashlib.sha256(raw).hexdigest() for state in ("started", "done")}}
        measurements = {
            "compose": {"closure_restored": True, "render_before_sha256": "1" * 64, "render_after_sha256": "1" * 64, "old_image_id": "sha256:" + "2" * 64, "restored_image_id": "sha256:" + "2" * 64},
            "postgres": {"restore_verified": True, "dump_sha256": "3" * 64, "migrations_before_sha256": "4" * 64, "migrations_after_sha256": "4" * 64, "rowcounts_before_sha256": "5" * 64, "rowcounts_after_sha256": "5" * 64},
            "redis": {"restore_verified": True, "rdb_sha256": "6" * 64, "backup_dbsize": 100, "backup_expiring": 90, "restored_dbsize": 20, "restored_expiring": 5, "restored_keyspace_keys": 18, "persistent_before": 10, "persistent_after": 15, "health_wait_ms": 65000, "health_budget_ms": 180000, "ttl_reconciled": True, "lazy_expiry_observed": True},
            "faults": {"app_healthy_interrupt": deepcopy(fault), "cleanup_before_interrupt": deepcopy(fault), "stale_ingress": {"recovery_restored_and_applied_observed": True, "live_health_reverified": True, "applied_removed": True}},
            "checkpoints": checkpoints,
            "rto": {"started_at": 1000000, "completed_at": 1000120, "elapsed_ms": 120000, "budget_ms": 600000},
            "cleanup": {"plaintext_destroyed": True, "owned_guest_destroyed": True, "active_claim_cleared": True, "stale_ingress_removed": True, "foreign_resources_unchanged": True, "foreign_before_sha256": "7" * 64, "foreign_after_sha256": "7" * 64},
            "isolation": {"backend": "nspawn-offline-private-users", "namespaces": {name: {"host": name + ":[10001]", "guest": name + ":[20001]", "guest_pid1": name + ":[20001]"} for name in ("mnt", "pid", "net", "user", "cgroup", "uts", "ipc")}, "host_identity_sha256": "d" * 64, "guest_identity_sha256": "e" * 64, "rootfs_identity_sha256": "f" * 64, "host_docker_engine_id_sha256": "1" * 64, "guest_docker_engine_id_sha256": "2" * 64, "docker_root_dir": "/var/lib/docker", "docker_socket": "/run/docker.sock", "default_routes_absent": True, "host_facing_interfaces_absent": True, "uid0_host": 524288, "uid0_range": 65536, "unified_cgroup2": True},
        }
        self.payload = {**deepcopy(self.scope), "drill_id": self.drill, "created_at": 1000000, "completed_at": 1000120, "source": source, "review": {"reviewer": "independent.test", "audit_id": "audit-" + "a" * 32, "independent": True, "approved_at": 1000120, "findings_sha256": "a" * 64}, "measurements": measurements}
        self.payload["measurements"]["isolation"].update(drill_id=self.drill, release_id=source["release_id"], recovery_helper_bundle_sha256=self.scope["recovery_helper_bundle_sha256"])
        self.patch = mock.patch.object(evidence, "TRUSTED_VM_PUBLIC_KEY", self.key_path)
        self.patch.start()
        self.addCleanup(self.patch.stop)

    def signed(self, payload=None):
        signed = {"schema": 2, "type": "sub2api-full-recovery-review", "payload": deepcopy(self.payload if payload is None else payload)}
        value = base64.b64encode(self.key.sign(evidence.canonical_json(signed))).decode()
        return {**signed, "signature": {"algorithm": "Ed25519", "key_sha256": hashlib.sha256(self.key_path.read_bytes()).hexdigest(), "value": value}}

    def verify(self, envelope, report=None, *, allow_expired=False):
        # Real Git scope is exercised separately. Mutation tests reuse the
        # freshly derived immutable scope to keep verification bounded.
        with mock.patch.object(evidence, "derive_expected_scope", return_value=deepcopy(self.scope)):
            return evidence.verify_full_recovery_evidence(envelope, workspace=self.workspace, report=self.report if report is None else report, now=self.now, allow_expired=allow_expired)

    def test_actual_git_inventory_and_bundle_are_rederived(self):
        scope = evidence.derive_expected_scope(self.workspace, self.report)
        self.assertEqual(scope, self.scope)
        record = scope["inventory"][0]
        self.assertNotEqual(record["before"]["blob_sha1"], record["after"]["blob_sha1"])
        self.assertEqual(record["after"]["mode"], "100644")
        verified = evidence.verify_full_recovery_evidence(self.signed(), workspace=self.workspace, report=self.report, now=self.now)
        self.assertEqual(verified.drill_id, self.drill)

    def test_review_preserves_original_reason_and_audit_identity(self):
        envelope = self.signed()
        with mock.patch.object(evidence, "derive_expected_scope", return_value=deepcopy(self.scope)):
            reviewed = evidence.apply_review(self.report, envelope, workspace=self.workspace, now=self.now)
            first = evidence.verify_reviewed_report(reviewed, workspace=self.workspace, now=self.now)
            second = evidence.verify_reviewed_report(reviewed, workspace=self.workspace, now=self.now)
        self.assertEqual(first, second)
        self.assertEqual(reviewed["schema"], 2)
        for key in evidence.ORIGINAL_FIELDS - {"schema"}:
            self.assertEqual(reviewed[key], self.report[key])
        self.assertIn("recovery_change_requires_review", reviewed["reason_codes"])
        self.assertEqual(reviewed["full_recovery_evidence"]["payload"]["review"]["audit_id"], self.payload["review"]["audit_id"])
        self.assertNotIn("verified", reviewed)

    def test_original_classification_cannot_drift_after_approval(self):
        changed = deepcopy(self.report)
        changed["reason_codes"] = sorted(changed["reason_codes"] + ["manual_full_drill"])
        with self.assertRaisesRegex(RuntimeError, "classification differs"):
            evidence.verify_full_recovery_evidence(self.signed(), workspace=self.workspace, report=changed, now=self.now)

    def test_reviewed_report_digest_must_match_its_full_envelope(self):
        with mock.patch.object(evidence, "derive_expected_scope", return_value=deepcopy(self.scope)):
            report = evidence.apply_review(self.report, self.signed(), workspace=self.workspace, now=self.now)
            report["evidence_sha256"] = "f" * 64
            with self.assertRaisesRegex(RuntimeError, "digest differs"):
                evidence.verify_reviewed_report(report, workspace=self.workspace, now=self.now)

    def test_schema_one_bare_pass_and_synthetic_reports_cannot_authorize(self):
        for value in ({"schema": 1, "postgres_restore": "pass"}, {"schema": 1, "status": "pass", "fixture_kind": "synthetic-offline-complete-recovery"}, {"schema": 2, "verified": True}):
            with self.subTest(value=value):
                with self.assertRaises(RuntimeError):
                    self.verify(value)
        payload = deepcopy(self.payload)
        payload["source"]["kind"] = "synthetic-offline-complete-recovery"
        with self.assertRaises(RuntimeError):
            self.verify(self.signed(payload))

    def test_any_unsigned_payload_mutation_breaks_signature(self):
        envelope = self.signed()
        envelope["payload"]["source"]["artifact_sha256"] = "f" * 64
        with self.assertRaisesRegex(RuntimeError, "signature"):
            self.verify(envelope)

    def test_untrusted_key_fingerprint_and_old_signature_domain_rejected(self):
        envelope = self.signed()
        envelope["signature"]["key_sha256"] = "f" * 64
        with self.assertRaises(RuntimeError):
            self.verify(envelope)
        envelope = self.signed()
        envelope["signature"]["value"] = base64.b64encode(self.key.sign(evidence.canonical_json(envelope["payload"]))).decode()
        with self.assertRaises(RuntimeError):
            self.verify(envelope)

    def test_signed_scope_drift_is_rejected(self):
        for key in ("base_commit", "target_commit", "changed_paths_sha256", "inventory_sha256", "recovery_helper_bundle_sha256", "classification_sha256"):
            with self.subTest(key=key):
                payload = deepcopy(self.payload)
                payload[key] = "f" * (40 if key.endswith("commit") else 64)
                with self.assertRaises(RuntimeError):
                    self.verify(self.signed(payload))
        payload = deepcopy(self.payload)
        payload["inventory"][0]["after"]["mode"] = "100755"
        with self.assertRaises(RuntimeError):
            self.verify(self.signed(payload))

    def test_each_measured_stage_is_required(self):
        for stage in self.payload["measurements"]:
            with self.subTest(stage=stage):
                payload = deepcopy(self.payload)
                payload["measurements"].pop(stage)
                with self.assertRaises(RuntimeError):
                    self.verify(self.signed(payload))

    def test_decrypt_identity_and_artifact_checksums_must_be_measured(self):
        for mutation in ({"identity_available": False}, {"decryption_verified": False}, {"observed_artifact_sha256": "f" * 64}, {"artifact_bytes": 0}, {"decrypted_sha256": "not_checked"}):
            with self.subTest(mutation=mutation):
                payload = deepcopy(self.payload)
                payload["source"].update(mutation)
                with self.assertRaises(RuntimeError):
                    self.verify(self.signed(payload))

    def test_delay_ttl_persistent_floor_and_lazy_counts_all_required(self):
        for mutation in ({"health_wait_ms": 60000}, {"health_wait_ms": 180001}, {"health_wait_ms": True}, {"restored_keyspace_keys": 20}, {"persistent_after": 9}, {"ttl_reconciled": False}, {"lazy_expiry_observed": False}):
            with self.subTest(mutation=mutation):
                payload = deepcopy(self.payload)
                payload["measurements"]["redis"].update(mutation)
                with self.assertRaises(RuntimeError):
                    self.verify(self.signed(payload))

    def test_resume_data_reapply_and_checkpoint_identity_are_rejected(self):
        for fault in ("app_healthy_interrupt", "cleanup_before_interrupt"):
            payload = deepcopy(self.payload)
            payload["measurements"]["faults"][fault]["postgres_oid_after"] += 1
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))
        for mutation in ({"resume_apply_count": 1}, {"live_reverified": False}, {"binding_sha256": "f" * 64}):
            payload = deepcopy(self.payload)
            payload["measurements"]["checkpoints"]["postgres_restored"].update(mutation)
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))

    def test_rto_and_owned_cleanup_cannot_be_missing_or_faked(self):
        for mutation in ({"elapsed_ms": 600001}, {"elapsed_ms": 5}, {"budget_ms": 0}):
            payload = deepcopy(self.payload)
            payload["measurements"]["rto"].update(mutation)
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))
        for key in ("plaintext_destroyed", "owned_guest_destroyed", "foreign_resources_unchanged", "active_claim_cleared"):
            payload = deepcopy(self.payload)
            payload["measurements"]["cleanup"][key] = "pass"
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))

    def test_strict_fields_expiry_and_non_independent_review_rejected(self):
        payload = deepcopy(self.payload)
        payload["approved"] = True
        with self.assertRaises(RuntimeError):
            self.verify(self.signed(payload))
        payload = deepcopy(self.payload)
        payload["review"]["independent"] = False
        with self.assertRaises(RuntimeError):
            self.verify(self.signed(payload))
        self.now += 86401
        with self.assertRaises(RuntimeError):
            self.verify(self.signed())

    def test_historical_review_reverifies_old_bound_proof_without_age_cutoff(self):
        with mock.patch.object(evidence, "derive_expected_scope", return_value=deepcopy(self.scope)):
            reviewed = evidence.apply_review(self.report, self.signed(), workspace=self.workspace, now=self.now)
            self.now += 86401
            with self.assertRaisesRegex(RuntimeError, "expired"):
                evidence.verify_reviewed_report(reviewed, workspace=self.workspace, now=self.now)
            verified = evidence.verify_reviewed_report(reviewed, workspace=self.workspace, now=self.now, allow_expired=True)
            self.assertEqual(verified.drill_id, self.drill)
            with self.assertRaisesRegex(RuntimeError, "expired"):
                evidence.apply_review(self.report, self.signed(), workspace=self.workspace, now=self.now)

    def test_historical_mode_still_rejects_future_bad_clock_signature_and_git_drift(self):
        self.now = self.payload["completed_at"] - 301
        with self.assertRaisesRegex(RuntimeError, "future"):
            self.verify(self.signed(), allow_expired=True)
        self.now = 1000121 + 86401
        for mutation in ({"elapsed_ms": 5000}, {"completed_at": 0}):
            payload = deepcopy(self.payload)
            payload["measurements"]["rto"].update(mutation)
            with self.assertRaisesRegex(RuntimeError, "RTO"):
                self.verify(self.signed(payload), allow_expired=True)
        envelope = self.signed()
        envelope["payload"]["completed_at"] += 1
        with self.assertRaisesRegex(RuntimeError, "signature"):
            self.verify(envelope, allow_expired=True)
        payload = deepcopy(self.payload)
        payload["recovery_helper_bundle_sha256"] = "f" * 64
        with self.assertRaisesRegex(RuntimeError, "differs"):
            self.verify(self.signed(payload), allow_expired=True)
        with self.assertRaises(RuntimeError):
            self.verify(self.signed(), allow_expired="yes")

    def test_recovery_point_image_and_actual_checkpoint_raw_bytes_must_match(self):
        for mutation in ({"recovery_point_sha256": "f" * 64}, {"pre_image_id": "sha256:" + "f" * 64}, {"release_id": "270-" + "f" * 12 + "-1000000-11111111"}):
            payload = deepcopy(self.payload)
            payload["source"].update(mutation)
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))
        for state in ("started", "done"):
            payload = deepcopy(self.payload)
            checkpoint = payload["measurements"]["checkpoints"]["postgres_restored"]
            raw = base64.b64decode(checkpoint[state + "_base64"]).replace(b"phase=postgres_restored", b"phase=redis_restored")
            checkpoint[state + "_base64"] = base64.b64encode(raw).decode()
            checkpoint[state + "_sha256"] = hashlib.sha256(raw).hexdigest()
            with self.assertRaisesRegex(RuntimeError, "checkpoint identity"):
                self.verify(self.signed(payload))

    def test_approved_identity_and_actual_archive_inventory_are_bound(self):
        for mutation in ({"derived_recipient_sha256": "f" * 64}, {"recipient_type": "new-root"}, {"observed_identity_sha256": "f" * 64}, {"archive_inventory_sha256": "f" * 64}):
            payload = deepcopy(self.payload)
            payload["source"].update(mutation)
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))
        payload = deepcopy(self.payload)
        payload["source"]["recipient_type"] = "age1"
        self.assertEqual(self.verify(self.signed(payload)).drill_id, self.drill)
        for key in ("artifact_sha256", "decrypted_sha256", "recovery_point_sha256", "postgres_dump_sha256", "redis_rdb_sha256"):
            payload = deepcopy(self.payload)
            payload["source"]["archive_inventory"][key] = "f" * 64
            payload["source"]["archive_inventory_sha256"] = evidence.checksum(payload["source"]["archive_inventory"])
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))

    def test_isolation_proof_rejects_shared_namespaces_engine_or_network(self):
        for mutation in ({"guest_docker_engine_id_sha256": "1" * 64}, {"guest_identity_sha256": "d" * 64}, {"default_routes_absent": False}, {"host_facing_interfaces_absent": False}, {"uid0_host": 0}, {"uid0_range": 1}, {"unified_cgroup2": False}, {"docker_socket": "/host/run/docker.sock"}, {"drill_id": "full-dr-22222222-2222-4222-8222-222222222222"}, {"release_id": "270-" + "f" * 12 + "-1000000-11111111"}, {"recovery_helper_bundle_sha256": "f" * 64}):
            payload = deepcopy(self.payload)
            payload["measurements"]["isolation"].update(mutation)
            with self.assertRaises(RuntimeError):
                self.verify(self.signed(payload))
        for name in self.payload["measurements"]["isolation"]["namespaces"]:
            payload = deepcopy(self.payload)
            namespace = payload["measurements"]["isolation"]["namespaces"][name]
            namespace["guest"] = namespace["guest_pid1"] = namespace["host"]
            with self.assertRaisesRegex(RuntimeError, "namespace"):
                self.verify(self.signed(payload))

    def test_loader_rejects_duplicate_nonfinite_nonobject_oversized_and_linked_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary).resolve() / "evidence.json"
            path.write_bytes(evidence.canonical_json(self.signed()))
            self.assertEqual(evidence.load_signed_evidence(path), self.signed())
            for raw in (b'{"schema":2,"schema":1}', b'{"payload":{"x":NaN}}', b'{"x":1e999}', b'[]', b'{"x":Infinity}', b'{broken'):
                path.write_bytes(raw)
                with self.assertRaises(RuntimeError):
                    evidence.load_signed_evidence(path)
            path.write_bytes(b" " * (evidence.MAX_EVIDENCE_BYTES + 1))
            with self.assertRaisesRegex(RuntimeError, "size limit"):
                evidence.load_signed_evidence(path)
            path.write_bytes(b"{}")
            linked = path.with_name("linked.json")
            os.link(path, linked)
            with self.assertRaisesRegex(RuntimeError, "single-link"):
                evidence.load_signed_evidence(path)

    def test_ledger_registration_is_unique_and_reverification_does_not_consume(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            directory.chmod(0o700)
            if os.name != "nt" and os.geteuid() != 0:
                self.skipTest("root-only production ledger")
            verified = self.verify(self.signed())
            ledger = evidence.EvidenceLedger(directory)
            ledger.register(verified)
            self.assertEqual(ledger.read_registered(verified), ledger.receipt(verified))
            self.assertEqual(self.verify(self.signed()), verified)
            with self.assertRaisesRegex(RuntimeError, "already registered"):
                ledger.register(verified)
            self.assertEqual(len(list(directory.iterdir())), 1)

    def test_registered_receipt_rejects_missing_drift_extra_and_noncanonical_bytes(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            directory.chmod(0o700)
            if os.name != "nt" and os.geteuid() != 0:
                self.skipTest("root-only production ledger")
            verified = self.verify(self.signed())
            ledger = evidence.EvidenceLedger(directory)
            with self.assertRaises(RuntimeError):
                ledger.read_registered(verified)
            ledger.register(verified)
            for mutation in ({"evidence_sha256": "f" * 64}, {"target_commit": "f" * 40}, {"completed_at": verified.completed_at + 1}):
                with self.assertRaisesRegex(RuntimeError, "receipt differs"):
                    ledger.read_registered(replace(verified, **mutation))
            path = directory / (verified.drill_id + ".json")
            original = path.read_bytes()
            for raw in (original + b"\n", evidence.canonical_json({**ledger.receipt(verified), "verified": True}), original.replace(b'"schema":1', b'"schema":1.0')):
                path.write_bytes(raw)
                with self.assertRaises(RuntimeError):
                    ledger.read_registered(verified)
            path.write_bytes(original)
            linked = directory / "hardlink.json"
            os.link(path, linked)
            with self.assertRaisesRegex(RuntimeError, "single-link"):
                ledger.read_registered(verified)

    @unittest.skipIf(os.name == "nt", "POSIX ownership and mode enforced in production")
    def test_registered_receipt_rejects_permissions_changed_after_registration(self):
        if os.geteuid() != 0:
            self.skipTest("root-only production ledger")
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            directory.chmod(0o700)
            verified = self.verify(self.signed())
            ledger = evidence.EvidenceLedger(directory)
            ledger.register(verified)
            path = directory / (verified.drill_id + ".json")
            path.chmod(0o644)
            with self.assertRaisesRegex(RuntimeError, "root-only"):
                ledger.read_registered(verified)
            path.chmod(0o600)
            directory.chmod(0o755)
            with self.assertRaisesRegex(RuntimeError, "root-only"):
                ledger.read_registered(verified)


if __name__ == "__main__":
    unittest.main()
