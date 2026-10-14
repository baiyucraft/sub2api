"""Deterministic signer tests; fixtures are never usable production evidence."""
from copy import deepcopy
import hashlib
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

from cryptography.hazmat.primitives import serialization

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(Path(__file__).resolve().parent))
from release import full_recovery_authorization as authorization
from release import full_recovery_drill_authorization as offline
from release import recovery_evidence as evidence
from release import recovery_bundle
from release.recovery_gate import changed_paths_sha256
from test_recovery_evidence import RecoveryEvidenceTests as Fixtures


class FullRecoveryAuthorizationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        Fixtures.setUpClass()
        for name in recovery_bundle.FULL_RECOVERY_REQUIRED:
            path = Fixtures.workspace / (recovery_bundle.PREFIX + 'release/' + name)
            path.write_bytes(('synthetic unit-only ' + name).encode())
        Fixtures.git('add', '.')
        Fixtures.git('-c', 'user.name=Signer Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', '独立签发测试')
        Fixtures.target = Fixtures.git('rev-parse', 'HEAD').decode().strip()
        paths = [p.decode() for p in Fixtures.git('diff', '--name-only', '-z', Fixtures.base, Fixtures.target).split(b'\0') if p]
        Fixtures.report['target_commit'] = Fixtures.target
        Fixtures.report['changed_paths_sha256'] = changed_paths_sha256(paths)
        Fixtures.scope = evidence.derive_expected_scope(Fixtures.workspace, Fixtures.report)

    @classmethod
    def tearDownClass(cls):
        Fixtures.tearDownClass()

    def setUp(self):
        self.fixture = Fixtures()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.root.chmod(0o700)
        self.workspace = Fixtures.workspace
        self.report = self.fixture.report
        self.now = self.fixture.now
        payload = self.fixture.payload
        self.receipt = {"schema": 1, "type": "sub2api-full-recovery-measurements", **{k: deepcopy(payload[k]) for k in ("drill_id", "created_at", "completed_at", "source", "measurements", "recovery_helper_bundle_sha256")}}
        raw = evidence.git(self.workspace, "show", self.report["target_commit"] + ":.agents/skills/sub2api-production-deploy/scripts/release/full_recovery_drill.py")
        collector_sha = hashlib.sha256(raw).hexdigest()
        self.approval = {"schema": 1, "type": "sub2api-full-recovery-approval", "drill_id": payload["drill_id"], "collector_id": "linux-root:" + collector_sha, "collector_sha256": collector_sha, **payload["review"], "expires_at": self.now + 600, "user_authorization_sha256": "c" * 64, "classification_sha256": evidence.checksum(self.report), "receipt_sha256": hashlib.sha256(evidence.canonical_json(self.receipt)).hexdigest(), "approved_identity_sha256": payload["source"]["approved_identity_sha256"], "approved_recipient_sha256": payload["source"]["approved_recipient_sha256"]}
        self.private = self.root / "existing-test-key.pem"
        self.private.write_bytes(Fixtures.key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
        self.private.chmod(0o600)
        self.patch = mock.patch.object(authorization, "TRUSTED_VM_PUBLIC_KEY", Fixtures.key_path)
        self.patch.start()
        self.addCleanup(self.patch.stop)
        for name, value in (("classification.json", self.report), ("receipt.json", self.receipt), ("approval.json", self.approval)):
            self.write(name, value)

    def write(self, name, document):
        p = self.root / name
        p.write_bytes(evidence.canonical_json(document))
        p.chmod(0o600)
        return p

    def request(self):
        return authorization.build_request(classification=self.root / "classification.json", receipt=self.root / "receipt.json", approval=self.root / "approval.json", workspace=self.workspace, now=self.now)

    def sign(self):
        self.write("request.json", self.request())
        return authorization.sign_request(request=self.root / "request.json", classification=self.root / "classification.json", receipt=self.root / "receipt.json", approval=self.root / "approval.json", private_key=self.private, workspace=self.workspace, output=self.root / "signed.json", now=self.now)

    def test_independent_receipt_signs_only_existing_full_review_domain(self):
        envelope = self.sign()
        verified = evidence.verify_full_recovery_evidence(envelope, workspace=self.workspace, report=self.report, now=self.now)
        self.assertEqual(verified.drill_id, self.receipt["drill_id"])
        self.assertEqual(envelope["payload"]["measurements"], self.receipt["measurements"])
        self.assertNotIn("manifest", envelope)
        self.assertNotIn("evidence", envelope)

    def test_synthetic_receipt_rejected_even_with_matching_approval_hash(self):
        self.receipt["source"]["kind"] = "synthetic-offline-complete-recovery"
        self.approval["receipt_sha256"] = hashlib.sha256(evidence.canonical_json(self.receipt)).hexdigest()
        self.write("receipt.json", self.receipt)
        self.write("approval.json", self.approval)
        with self.assertRaisesRegex(RuntimeError, "real encrypted"):
            self.sign()
        self.assertFalse((self.root / "signed.json").exists())

    def test_changed_receipt_cannot_use_previous_review(self):
        self.receipt["measurements"]["cleanup"]["owned_guest_destroyed"] = False
        self.write("receipt.json", self.receipt)
        with self.assertRaisesRegex(RuntimeError, "actual receipt"):
            self.request()

    def test_caller_cannot_change_measured_values_after_request(self):
        request = self.request()
        request["payload"]["measurements"]["redis"]["health_wait_ms"] = 1
        self.write("request.json", request)
        with self.assertRaisesRegex(RuntimeError, "independently approved measured"):
            authorization.sign_request(request=self.root / "request.json", classification=self.root / "classification.json", receipt=self.root / "receipt.json", approval=self.root / "approval.json", private_key=self.private, workspace=self.workspace, output=self.root / "signed.json", now=self.now)

    def test_approval_identity_and_collector_code_are_exact(self):
        for field in ("approved_identity_sha256", "collector_sha256"):
            saved = deepcopy(self.approval)
            self.approval[field] = "f" * 64
            self.write("approval.json", self.approval)
            with self.assertRaises(RuntimeError):
                self.request()
            self.approval = saved

    def test_no_key_creation_or_overwrite(self):
        self.sign()
        with self.assertRaises(RuntimeError):
            self.sign()
        self.assertEqual(self.private.read_bytes(), Fixtures.key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))

    def test_approval_before_completion_or_expired_is_rejected(self):
        for approved_at, expires_at in ((self.receipt["completed_at"] - 1, self.now + 600), (self.receipt["completed_at"], self.now - 1)):
            self.approval.update(approved_at=approved_at, expires_at=expires_at)
            self.write("approval.json", self.approval)
            with self.assertRaises(RuntimeError):
                self.request()

    def test_offline_authorization_never_accepts_gate_schema(self):
        for payload in ({"manifest": {}, "evidence": {}}, {"schema": 2, "type": "sub2api-full-recovery-review", "payload": self.fixture.payload}):
            with self.assertRaises(RuntimeError):
                offline.validate(payload, now=self.now)

    def test_root_issuer_rejects_missing_approval_and_synthetic_domain(self):
        bad = self.write("bad.json", {"schema": 1, "status": "pass"})
        with self.assertRaises(RuntimeError):
            authorization.issue_drill_authorization(payload_path=bad, approval_path=bad, output=self.root / "offline-auth.json", workspace=self.workspace, private_key=self.private, now=self.now)
        self.assertFalse((self.root / "offline-auth.json").exists())

    def offline_payload(self):
        prefix = recovery_bundle.PREFIX
        def asset(relative):
            return hashlib.sha256(evidence.git(self.workspace, 'show', self.report['target_commit'] + ':' + prefix + relative)).hexdigest()
        return {"schema": 1, "type": offline.DOMAIN, "intended_use": "offline-full-dr-only", "guest_manifest_sha256": "6" * 64, "drill_id": self.receipt['drill_id'], "release_id": self.receipt['source']['release_id'], "target_commit": self.report['target_commit'], "recovery_helper_bundle_sha256": self.receipt['recovery_helper_bundle_sha256'], "trusted_public_key_sha256": hashlib.sha256(Fixtures.key_path.read_bytes()).hexdigest(), "collector_sha256": asset('release/full_recovery_drill.py'), "collector_id": 'linux-root:' + asset('release/full_recovery_drill.py'), "verifier_sha256": asset('release/full_recovery_drill_authorization.py'), "bundle_verifier_sha256": asset('release/recovery_bundle.py'), "isolation_guard_sha256": asset('maintenance/release/recovery-isolation.sh'), "artifact_sha256": '8' * 64, "artifact_bytes": 123456, "recipient_type": 'age1', "pre_image_id": 'sha256:' + '2' * 64, "approved_identity_sha256": 'c' * 64, "approved_recipient_sha256": 'd' * 64, "created_at": self.now, "expires_at": self.now + 600, "reviewer": 'independent.test', "audit_id": 'audit-' + 'a' * 32, "user_authorization_sha256": 'e' * 64, "drill_context": {"schema": 1, "type": "sub2api-offline-recovery-context", "release_id": self.receipt['source']['release_id'], "source_snapshot_sha256": '9' * 64, "commit_sha": Fixtures.base, "profile": 270, "version": '0.2.16-unit-only', "deployment_mode": 'downtime', "candidate_image_id": 'sha256:' + '2' * 64, "archive_kind": 'encrypted-recovery-source', "archive_sha256": '8' * 64}}

    def test_offline_context_rejects_gate_candidate_and_different_source(self):
        payload = self.offline_payload()
        for field, value in (("schema", True), ("type", "gate"), ("candidate_image_id", 'sha256:' + 'f' * 64), ("archive_kind", "candidate"), ("archive_sha256", 'f' * 64), ("source_snapshot_sha256", ""), ("commit_sha", 'bad'), ("profile", True), ("version", "bad\nversion"), ("deployment_mode", "blue-green")):
            bad = deepcopy(payload)
            bad['drill_context'][field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                offline.validate(bad, now=self.now)

    def test_capture_namespace_does_not_impersonate_running_source_revision(self):
        payload = self.offline_payload()
        payload['drill_context'].update(commit_sha='f' * 40, profile=269, version='0.2.15-running-source')
        offline.validate(payload, now=self.now)
        self.assertNotEqual(payload['release_id'].split('-')[1], payload['drill_context']['commit_sha'][:12])

    def test_claim_context_must_equal_independently_signed_source_metadata(self):
        payload = self.offline_payload()
        path = self.write('drill-metadata.json', payload['drill_context'])
        offline.verify_context(path, payload)
        changed = deepcopy(payload['drill_context'])
        changed['source_snapshot_sha256'] = 'f' * 64
        self.write('drill-metadata.json', changed)
        with self.assertRaisesRegex(RuntimeError, 'signed offline source'):
            offline.verify_context(path, payload)

    def test_claim_context_rejects_symlink(self):
        payload = self.offline_payload()
        path = self.root / 'drill-metadata.json'
        try:
            path.symlink_to(self.write('alternate-metadata.json', payload['drill_context']))
        except OSError as error:
            self.skipTest('symlink capability unavailable: ' + str(error.errno))
        with self.assertRaises(RuntimeError):
            offline.verify_context(path, payload)

    def test_separate_execution_domain_issues_only_exact_independent_request(self):
        payload = self.offline_payload()
        request = self.write('offline-request.json', payload)
        approval = {'schema': 1, 'type': 'sub2api-isolated-recovery-approval', 'request_sha256': hashlib.sha256(request.read_bytes()).hexdigest(), 'reviewer': payload['reviewer'], 'audit_id': payload['audit_id'], 'independent': True, 'approved_at': self.now, 'user_authorization_sha256': payload['user_authorization_sha256']}
        consent = self.write('offline-approval.json', approval)
        envelope = authorization.issue_drill_authorization(payload_path=request, approval_path=consent, output=self.root / 'offline-auth.json', workspace=self.workspace, private_key=self.private, now=self.now)
        self.assertEqual(envelope['payload']['intended_use'], 'offline-full-dr-only')
        self.assertNotIn('manifest', envelope)
        self.assertNotIn('evidence', envelope)
        self.assertNotIn('status', envelope)
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
        import base64
        key = serialization.load_pem_public_key(Fixtures.key_path.read_bytes())
        key.verify(base64.b64decode(envelope['signature']['value']), offline.canonical(payload))

    def test_offline_approval_cannot_authorize_modified_code_or_expired_request(self):
        payload = self.offline_payload()
        for field, value in (('collector_sha256', 'f' * 64), ('expires_at', self.now - 1)):
            bad = deepcopy(payload)
            bad[field] = value
            if field == 'collector_sha256':
                bad['collector_id'] = 'linux-root:' + value
            request = self.write('offline-request.json', bad)
            consent = self.write('offline-approval.json', {'schema': 1, 'type': 'sub2api-isolated-recovery-approval', 'request_sha256': hashlib.sha256(request.read_bytes()).hexdigest(), 'reviewer': bad['reviewer'], 'audit_id': bad['audit_id'], 'independent': True, 'approved_at': self.now, 'user_authorization_sha256': bad['user_authorization_sha256']})
            with self.assertRaises(RuntimeError):
                authorization.issue_drill_authorization(payload_path=request, approval_path=consent, output=self.root / 'offline-auth.json', workspace=self.workspace, private_key=self.private, now=self.now)
            self.assertFalse((self.root / 'offline-auth.json').exists())


if __name__ == "__main__":
    unittest.main()
