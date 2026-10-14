"""Exercise CLI/worker/Gate wiring with real Git and RFC 8032 signatures."""
from __future__ import annotations

import argparse
from copy import deepcopy
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from release import cli, gate, paths, supervisor
from release import recovery_evidence as evidence
from release.atomic import canonical_json
from release.recovery_gate import classify, require_full, require_specialized
from release.profiles import CURRENT_RELEASE_PROFILE
import test_recovery_evidence as evidence_fixture
import test_gate_v2 as gate_fixture


class RecoveryReviewCLITests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        evidence_fixture.RecoveryEvidenceTests.setUpClass()

    @classmethod
    def tearDownClass(cls):
        evidence_fixture.RecoveryEvidenceTests.tearDownClass()

    def setUp(self):
        self.fixture = evidence_fixture.RecoveryEvidenceTests("test_actual_git_inventory_and_bundle_are_rederived")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.run_root = self.root / "releases"
        self.run_root.mkdir(mode=0o700)
        for target, name, value in (
            (cli, "WORKSPACE", self.fixture.workspace),
            (paths, "WORKSPACE", self.fixture.workspace),
            (cli, "RUN_ROOT", self.run_root),
            (supervisor, "RUN_ROOT", self.run_root),
            (evidence.time, "time", lambda: self.fixture.now),
        ):
            patch = mock.patch.object(target, name, value)
            patch.start()
            self.addCleanup(patch.stop)
        self.snapshot = {"production_current_commit_sha": self.fixture.base, "schema_migrations": []}

    def proof(self, report=None, *, filename="proof.json", payload=None):
        if payload is None:
            payload = deepcopy(self.fixture.payload)
            payload.update(evidence.derive_expected_scope(self.fixture.workspace, report or self.fixture.report))
        path = self.root / filename
        path.write_bytes(canonical_json(self.fixture.signed(payload)))
        return path

    def accept(self, path=None, catalog=None, requested="auto"):
        return cli._recovery_gate_report(self.fixture.target, self.snapshot, catalog or [], requested, str(path) if path else None)

    def test_cli_freezes_pending_and_manual_reasons_before_verifying(self):
        catalog = [{"filename": "001_example.sql", "checksum": "a" * 64, "non_transactional": False}]
        original = classify(self.fixture.workspace, self.fixture.base, self.fixture.target)
        self.assertEqual(original, self.fixture.report)
        path = self.proof(original)
        for pending, requested in ((catalog, "auto"), ([], "full")):
            with self.subTest(requested=requested, pending=bool(pending)):
                with self.assertRaisesRegex(RuntimeError, "classification differs"):
                    self.accept(path, pending, requested)
        self.assertFalse((self.run_root / ".full-recovery-evidence").exists())
        expected = require_full(require_specialized(original, "pending_migrations"))
        reviewed = self.accept(self.proof(expected, filename="frozen.json"), catalog, "full")
        self.assertEqual(reviewed["reason_codes"], expected["reason_codes"])
        self.assertIn("recovery_change_requires_review", reviewed["reason_codes"])
        self.assertNotIn("verified", reviewed)

    def test_cli_rejects_tampering_without_registering_and_exact_replay_is_read_only(self):
        path = self.proof()
        document = evidence.load_signed_evidence(path)
        document["payload"]["review"]["findings_sha256"] = "b" * 64
        path.write_bytes(canonical_json(document))
        with self.assertRaisesRegex(RuntimeError, "signature"):
            self.accept(path)
        self.assertFalse((self.run_root / ".full-recovery-evidence").exists())
        path = self.proof()
        first = self.accept(path)
        receipt = self.run_root / ".full-recovery-evidence" / (self.fixture.drill + ".json")
        original_bytes, original_mtime = receipt.read_bytes(), receipt.stat().st_mtime_ns
        self.assertEqual(self.accept(path), first)
        self.assertEqual(receipt.read_bytes(), original_bytes)
        self.assertEqual(receipt.stat().st_mtime_ns, original_mtime)
        payload = deepcopy(self.fixture.payload)
        payload["review"]["findings_sha256"] = "b" * 64
        with self.assertRaisesRegex(RuntimeError, "registered receipt differs"):
            self.accept(self.proof(payload=payload, filename="reuse.json"))
        self.assertEqual(receipt.read_bytes(), original_bytes)

    def test_cli_missing_evidence_remains_blocked(self):
        with self.assertRaisesRegex(RuntimeError, "independent full DR drill"):
            self.accept()

    def test_worker_receives_an_immutable_copy_of_the_complete_signed_envelope(self):
        source = self.proof()
        original = evidence.load_signed_evidence(source)
        captured = {}

        def launch(command, **kwargs):
            captured["command"] = command
            run_dir = next(path for path in self.run_root.iterdir() if path.is_dir())
            runner_path = run_dir / "runner.json"
            runner = json.loads(runner_path.read_bytes())
            runner["status"] = "running"
            runner_path.write_bytes(canonical_json(runner))
            return mock.Mock(pid=456)

        def manifest(commit, profile, identifier, mode):
            return {"release_id": identifier, "profile": profile["name"], "commit_sha": commit, "deployment_mode": mode}

        # Mock only spawning and unrelated manifest setup; the copied proof is
        # reverified against actual immutable Git objects and the fixed key.
        with mock.patch.object(supervisor, "create_manifest", side_effect=manifest), mock.patch.object(supervisor, "runner_checksum", return_value="c" * 64), mock.patch.object(supervisor, "popen_detached_worker", side_effect=launch), mock.patch.object(supervisor, "_process_token", return_value="token"):
            identifier = supervisor.start(argparse.Namespace(profile=CURRENT_RELEASE_PROFILE, commit=self.fixture.target, deployment_mode="downtime", recovery_gate_mode="full", full_recovery_evidence=str(source)), announce=False)
        command = captured["command"]
        copied = Path(command[command.index("--full-recovery-evidence") + 1])
        self.assertEqual(copied, self.run_root / identifier / "full-recovery-evidence.json")
        self.assertEqual(copied.read_bytes(), canonical_json(original))
        self.assertEqual(command[command.index("--recovery-gate-mode") + 1], "full")
        source.write_bytes(b"changed after worker dispatch")
        verified = evidence.verify_full_recovery_evidence(evidence.load_signed_evidence(copied), workspace=self.fixture.workspace, report=self.fixture.report)
        self.assertEqual(verified.drill_id, self.fixture.drill)
        self.assertEqual(copied.stat().st_nlink, 1)
        # atomic_write uses the parent directory access boundary on Windows;
        # POSIX mode bits are enforced only on hosts that implement them.
        if os.name != "nt":
            self.assertEqual(copied.stat().st_mode & 0o222, 0)

    def gate_document(self):
        template = gate_fixture.GateV2Test("test_valid_empty_pending_round_trip")
        template.root = self.root
        document = template._document()
        reviewed = evidence.apply_review(self.fixture.report, self.fixture.signed(), workspace=self.fixture.workspace)
        old_image = reviewed["full_recovery_evidence"]["payload"]["source"]["pre_image_id"]
        document["manifest"].update(commit_sha=self.fixture.target, release_id=f"{CURRENT_RELEASE_PROFILE}-{self.fixture.target[:12]}-1-aaaaaaaa", recovery_gate=reviewed, production_current_image_id=old_image)
        document["evidence"]["production_current_image_id"] = old_image
        return document

    def verify_gate(self, document, *, allow_expired=False):
        raw = canonical_json(document) + b"\n"
        (self.root / "gate.json").write_bytes(raw)
        (self.root / "gate.sig").write_bytes(self.fixture.key.sign(raw))
        # Assets/catalog are another contract. Do not mock either signature,
        # classification validation or full evidence Git scope derivation.
        with mock.patch("release.manifest.discover_migration_catalog", return_value=gate_fixture.GateV2Test._catalog()), mock.patch.object(gate, "runner_checksum", return_value="d" * 64), mock.patch.object(gate, "release_asset_checksums", return_value=gate_fixture.GateV2Test._assets()), mock.patch.object(gate, "sha256_file", side_effect=gate_fixture.GateV2Test._asset_checksum):
            return gate.verify_gate(self.root, self.fixture.key_path, CURRENT_RELEASE_PROFILE, allow_expired=allow_expired, accepted_schemas=frozenset({2}))

    def test_gate_reverifies_nested_signature_even_when_outer_gate_is_resigned(self):
        document = self.gate_document()
        self.assertEqual(self.verify_gate(document), document)
        nested = document["manifest"]["recovery_gate"]["full_recovery_evidence"]
        nested["payload"]["review"]["findings_sha256"] = "b" * 64
        # The outer Gate and envelope digest are valid for the tampered bytes.
        document["manifest"]["recovery_gate"]["evidence_sha256"] = evidence.checksum(nested)
        with self.assertRaisesRegex(RuntimeError, "signature"):
            self.verify_gate(document)

    def test_historical_gate_relaxes_only_age_and_still_verifies_signature(self):
        document = self.gate_document()
        self.fixture.now += 86401
        with self.assertRaisesRegex(RuntimeError, "expired"):
            self.verify_gate(document)
        self.assertEqual(self.verify_gate(document, allow_expired=True), document)
        nested = document["manifest"]["recovery_gate"]["full_recovery_evidence"]
        nested["payload"]["source"]["observed_artifact_sha256"] = "f" * 64
        document["manifest"]["recovery_gate"]["evidence_sha256"] = evidence.checksum(nested)
        with self.assertRaisesRegex(RuntimeError, "signature"):
            self.verify_gate(document, allow_expired=True)

    def test_gate_rejects_recovery_proof_for_another_production_image(self):
        document = self.gate_document()
        document["manifest"]["production_current_image_id"] = "sha256:" + "f" * 64
        document["evidence"]["production_current_image_id"] = "sha256:" + "f" * 64
        with self.assertRaisesRegex(RuntimeError, "old image differs"):
            self.verify_gate(document)

    def test_legacy_gate_rejects_new_reviewed_report(self):
        reviewed = evidence.apply_review(self.fixture.report, self.fixture.signed(), workspace=self.fixture.workspace)
        document = {"manifest": {"schema": 1, "recovery_gate": reviewed}, "evidence": {}}
        raw = canonical_json(document)
        (self.root / "gate.json").write_bytes(raw)
        (self.root / "gate.sig").write_bytes(self.fixture.key.sign(raw))
        with self.assertRaisesRegex(RuntimeError, "requires Gate v2"):
            gate.verify_gate(self.root, self.fixture.key_path, "195", accepted_schemas=frozenset({1}))


if __name__ == "__main__":
    unittest.main()
