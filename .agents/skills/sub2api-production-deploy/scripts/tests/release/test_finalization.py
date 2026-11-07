from __future__ import annotations

import argparse
import base64
import hashlib
import json
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release import finalization, supervisor
from release.atomic import canonical_json
from release.doctor import ReleaseDoctor

REMOTE_CONSUMED = finalization._remote_consumed
DOCTOR_CHECKS = finalization._doctor_checks


class FinalizationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "releases"
        self.root.mkdir()
        self.identifier = "265-aaaaaaaaaaaa-1-deadbeef"
        self.run = self.root / self.identifier
        self.run.mkdir()
        self.candidate = "sha256:" + "b" * 64
        self.helper = {"commit_sha": "c" * 40, "runner_sha256": "d" * 64, "assets": {"helper.py": {"mode": "100644", "blob": "e" * 40, "sha256": "f" * 64}}}
        self.manifest = {"schema": 1, "release_id": self.identifier, "profile": "265", "commit_sha": "a" * 40, "runner_sha256": "f" * 64}
        self.runner = {"schema": 1, "release_id": self.identifier, "profile": "265", "commit": "a" * 40, "runner_sha256": "f" * 64, "pid": 123, "process_token": "old", "status": "failed", "exit_code": 1}
        for name in finalization.ORIGINAL_FILES:
            path = self.run / name
            path.parent.mkdir(parents=True, exist_ok=True)
            self.write(name, {"schema": 1})
        self.write("manifest.json", self.manifest)
        self.write("runner.json", self.runner)
        self.write("gate/gate.json", {"manifest": self.manifest, "evidence": {"candidate_image_id": self.candidate}})
        self.original = {"release_id": self.identifier, "status": "verified", "candidate_image_id": self.candidate, "running_image_id": self.candidate, "claim_final_state": "consumed"}
        self.production = {**finalization.REMOTE_REQUIRED, "running_image_id": self.candidate}
        # Keep this producer contract independent of the consumer's constants.
        self.doctor = {
            "local_ready": "true", "host_keys_ready": "true", "vm_ready": "true", "vm_release_unit_status": "verified",
            "racknerd_ready": "true", "backup_protocol_ready": "true", "nginx_ingress_policy": "ready",
            "production_migration_status": "verified", "dmit_ready": "true", "proxy_v2_ready": "true",
            "release_proxy_ready": "true", "legacy_proxy_disabled": "true", "backup_ready": "true",
            "relay_external_health": "pass", "production_current_image_id": self.candidate,
            "production_current_commit_sha": "a" * 40,
        }
        self.lease = mock.MagicMock()
        self.lease.record = {"vm_boot_id": "00000000-0000-0000-0000-000000000001"}
        self.lease.__enter__.return_value = self.lease

        def complete(*, evidence_sha256, before_owner_commit=None):
            if before_owner_commit is not None:
                before_owner_commit()
            preserve = finalization.RetainedVMFinalization.call_args.kwargs.get("preserve_running_vm") is True
            record = {
                "schema": 2 if preserve else 1, "status": "verified", "evidence_sha256": evidence_sha256,
                "vm_boot_id": "00000000-0000-0000-0000-000000000001",
                "vm_power_status": "running" if preserve else "stopped", "vm_cleanup_status": "preserved" if preserve else "verified",
            }
            if preserve:
                record["mode"] = "preserve-running"
            self.write("vm-finalization.json", record)
            self.lease.record = record

        self.lease.complete.side_effect = complete
        self.patches = [
            mock.patch.object(supervisor, "RUN_ROOT", self.root),
            mock.patch.object(finalization, "_process_identity", return_value=None),
            mock.patch.object(finalization, "_helper_identity", return_value=self.helper),
            mock.patch.object(supervisor, "_verified_evidence_view", return_value=self.original),
            mock.patch.object(finalization, "_remote_consumed", return_value=self.production),
            mock.patch.object(finalization, "_doctor_checks", return_value=self.doctor),
            mock.patch.object(finalization, "_cleanup_vm_tasks", return_value={"vm_tasks_cleanup": "verified"}),
            mock.patch.object(finalization, "ReleaseDoctor"),
            mock.patch.object(finalization, "SSHRunner"),
            mock.patch.object(finalization, "RetainedVMFinalization", return_value=self.lease),
            mock.patch.object(finalization, "verify_retained_finalization", side_effect=lambda *a, **k: json.loads((self.run / "vm-finalization.json").read_text())),
            mock.patch("builtins.print"),
        ]
        for patch in self.patches:
            patch.start()
            self.addCleanup(patch.stop)

    def write(self, name, value):
        (self.run / name).write_bytes(canonical_json(value) + b"\n")

    def finalize(self, *, preserve=False):
        finalization.finalize_consumed(argparse.Namespace(release_id=self.identifier, preserve_running_vm=preserve))

    def test_success_preserves_original_worker_and_application_evidence(self):
        before = {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES}
        self.finalize()
        self.assertEqual(before, {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES})
        value = supervisor.verified_result_view(self.identifier)
        self.assertEqual(value["finalization_status"], "verified")
        self.assertEqual(value["original_runner_status"], "failed")
        self.assertEqual(value["original_runner_exit"], 1)
        self.assertEqual(value["vm_power_status"], "stopped")

    def test_idempotent_success_rechecks_live_without_vm_ssh_cleanup_or_shutdown(self):
        self.finalize()
        finalization._cleanup_vm_tasks.reset_mock()
        self.lease.complete.reset_mock()
        finalization._doctor_checks.reset_mock()
        self.finalize()
        self.lease.complete.assert_not_called()
        finalization._cleanup_vm_tasks.assert_not_called()
        finalization._doctor_checks.assert_called_once()
        self.assertFalse(finalization._doctor_checks.call_args.kwargs["include_vm"])

    def test_result_write_failure_resumes_from_verified_stopped_vm_record(self):
        original_write = supervisor._write_json

        def fail_result(path, value):
            if path.name == "finalization-result.json":
                raise OSError("disk write rejected")
            return original_write(path, value)

        with mock.patch.object(supervisor, "_write_json", side_effect=fail_result):
            with self.assertRaises(OSError):
                self.finalize()
        finalization._cleanup_vm_tasks.reset_mock()
        finalization._doctor_checks.reset_mock()
        self.finalize()
        finalization._cleanup_vm_tasks.assert_not_called()
        self.assertTrue(all(call.kwargs.get("include_vm") is False for call in finalization._doctor_checks.call_args_list))
        self.assertTrue((self.run / "finalization-result.json").exists())

    def test_last_owner_commit_failure_can_retry_only_verified_local_completion(self):
        original_complete = self.lease.complete.side_effect

        def fail_owner(**kwargs):
            original_complete(**kwargs)
            raise OSError("owner commit rejected")

        self.lease.complete.side_effect = fail_owner
        with self.assertRaises(OSError):
            self.finalize()
        self.lease.complete.side_effect = original_complete
        finalization._cleanup_vm_tasks.reset_mock()
        self.finalize()
        finalization._cleanup_vm_tasks.assert_not_called()
        self.assertEqual(self.lease.complete.call_count, 2)

    def test_live_failed_pid_and_uncertain_process_identity_are_rejected(self):
        for returned in ("old", RuntimeError("access denied")):
            with self.subTest(returned=returned), mock.patch.object(finalization, "_process_identity", **({"side_effect": returned} if isinstance(returned, Exception) else {"return_value": returned})):
                with self.assertRaises(RuntimeError):
                    self.finalize()
                self.lease.complete.assert_not_called()

    def test_wrong_worker_identity_or_status_is_rejected(self):
        for field, value in (("status", "running"), ("exit_code", 0), ("commit", "b" * 40), ("runner_sha256", "e" * 64), ("pid", None)):
            with self.subTest(field=field):
                self.write("runner.json", {**self.runner, field: value})
                with self.assertRaisesRegex(RuntimeError, "identified failed"):
                    self.finalize()
        self.lease.complete.assert_not_called()

    def test_unconsumed_changed_image_doctor_or_owner_failure_never_shuts_down(self):
        for target in ("_remote_consumed", "_doctor_checks", "RetainedVMFinalization"):
            with self.subTest(target=target), mock.patch.object(finalization, target, side_effect=RuntimeError("unproven")):
                with self.assertRaises(RuntimeError):
                    self.finalize()
                self.lease.complete.assert_not_called()
                self.assertFalse((self.run / "finalization-result.json").exists())

    def test_original_evidence_or_helper_drift_rejects_formal_verification(self):
        self.finalize()
        original_bytes = (self.run / "state.json").read_bytes()
        self.write("state.json", {"schema": 1, "changed": True})
        with self.assertRaisesRegex(RuntimeError, "original evidence differs"):
            supervisor.verified_result_view(self.identifier)
        (self.run / "state.json").write_bytes(original_bytes)
        with mock.patch.object(finalization, "_helper_identity", return_value={**self.helper, "commit_sha": "d" * 40}):
            with self.assertRaisesRegex(RuntimeError, "original evidence differs"):
                supervisor.verified_result_view(self.identifier)

    def test_finalization_record_drift_or_unverified_vm_owner_is_rejected(self):
        self.finalize()
        result = json.loads((self.run / "finalization-result.json").read_text())
        self.write("finalization-result.json", {**result, "checks_sha256": "e" * 64})
        with self.assertRaisesRegex(RuntimeError, "checksum differs"):
            supervisor.verified_result_view(self.identifier)
        self.write("finalization-result.json", result)
        with mock.patch.object(finalization, "verify_retained_finalization", side_effect=RuntimeError("owner unproven")):
            with self.assertRaisesRegex(RuntimeError, "owner unproven"):
                supervisor.verified_result_view(self.identifier)

    def test_failed_runner_without_finalization_is_still_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "not successfully terminal"):
            supervisor.verified_result_view(self.identifier)

    def test_recorded_checks_reject_extra_remote_sensitive_fields(self):
        self.finalize()
        checks = json.loads((self.run / "finalization-checks.json").read_text())
        checks["production"]["raw_log"] = "never expose"
        self.write("finalization-checks.json", checks)
        with self.assertRaisesRegex(RuntimeError, "allowlist"):
            finalization.finalized_result_view(self.identifier)

    def test_status_retains_failed_runner_and_projects_independent_verified_cleanup(self):
        self.finalize()
        self.write("vm-lifecycle.json", {"schema": 1})  # Same original fixture bytes.
        value = supervisor.status_view(self.identifier)
        self.assertEqual(value["runner_status"], "failed")
        self.assertEqual(value["runner_exit"], 1)
        self.assertEqual(value["finalization_status"], "verified")
        self.assertEqual(value["vm_cleanup_status"], "verified")

    def test_remote_probe_requires_consumed_signature_claim_inventory_and_current_identity(self):
        manifest = {**self.manifest, "release_asset_sha256": {".agents/skills/sub2api-production-deploy/scripts/maintenance/release/context.sh": "e" * 64}}
        ssh = mock.Mock()
        ssh.run.return_value.values = self.production
        ssh.deployment_environment = SimpleNamespace(
            public_domain="sub2api.example.test",
            direct_route={"address": "198.51.100.10", "observer": "direct-observer", "target_node": "app-node"},
            relay_route={"address": "198.51.100.20", "observer": "relay-observer", "target_node": "app-node"},
            assert_current=mock.Mock(),
        )
        backup = {"pre_switch_image_id": "sha256:" + "c" * 64, "artifact": "fixture.tar.age", "transport_artifact": "transport.tar.age", "artifact_size": "1", "artifact_sha256": "d" * 64, "release_sha256": "d" * 64, "traffic_preserved": "true", "redis_backup_mode": "rdb", "no_restart_path_proven": "true", "local_restore_point_ready": "true"}
        REMOTE_CONSUMED(ssh, manifest, self.candidate, finalization._original_hashes(self.run), backup_evidence=backup)
        script = ssh.run.call_args_list[0].args[1]
        for required in ("sha256sum --strict -c CLAIM_SHA256SUMS", "openssl pkeyutl -verify", "root:root:400:1", "gate.sig", ".active-release", "candidate_image_id=", "flock -xn", "test \"$container\" = sub2api"):
            self.assertIn(required, script)

    def test_doctor_filters_snapshot_and_checks_live_application_migration_catalog(self):
        snapshot = {"current_image_id": self.candidate, "production_current_commit_sha": "a" * 40, "schema_migrations": []}
        remote = {**self.doctor, "production_snapshot_b64": base64.b64encode(canonical_json(snapshot)).decode(), "backup_public_ip": "do-not-persist"}
        doctor_mock = mock.Mock()
        doctor_mock.run.side_effect = [{"local_ready": "true", "host_keys_ready": "true"}, remote]
        with mock.patch.object(finalization, "ReleaseDoctor", return_value=doctor_mock):
            values = DOCTOR_CHECKS({**self.manifest, "schema": 2, "migration_catalog": []}, self.helper, mock.Mock())
        self.assertNotIn("production_snapshot_b64", values)
        self.assertNotIn("backup_public_ip", values)
        self.assertEqual(values["migration_catalog_verified"], "true")

    def test_doctor_consumes_real_backup_relay_health_producer(self):
        environment = SimpleNamespace(
            public_domain="sub2api.example.test",
            relay_route={"address": "198.51.100.20", "observer": "relay-observer"},
            deployment={"backup_upload": {"node": "backup-uploader"}},
            profile_values=lambda: {}, assert_current=lambda: None,
        )
        producer_runner = SimpleNamespace(
            deployment_environment=environment, assert_configuration_current=lambda: None,
            run=mock.Mock(side_effect=[
                SimpleNamespace(values={"backup_ready": "true", "backup_free_bytes": "9000000000"}),
                SimpleNamespace(values={"relay_external_health": "pass"}),
            ]),
        )
        produced = ReleaseDoctor("265", runner=producer_runner, environment=environment).check_backup()
        self.assertEqual(produced, {"backup_ready": "true", "backup_free_bytes": "9000000000", "relay_external_health": "pass"})
        self.assertEqual(producer_runner.run.call_args.args[0], "relay-observer")
        self.assertEqual(producer_runner.run.call_args.args[2], {"relay_external_health"})
        remote = {key: value for key, value in self.doctor.items() if key not in {"backup_ready", "relay_external_health"}}
        remote.update(produced)
        for include_vm in (True, False):
            with self.subTest(include_vm=include_vm):
                doctor = mock.Mock()
                doctor.run.side_effect = [{"local_ready": "true", "host_keys_ready": "true"}, remote]
                with mock.patch.object(finalization, "ReleaseDoctor", return_value=doctor):
                    checked = DOCTOR_CHECKS(self.manifest, self.helper, mock.Mock(), include_vm=include_vm)
                self.assertEqual(checked["relay_external_health"], "pass")
                self.assertNotIn("dmit_external_health", checked)

    def test_relay_health_or_application_drift_blocks_completion_before_owner_release(self):
        before = {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES}
        for drift in ("missing", "failed", "legacy_only", "image", "application_commit"):
            with self.subTest(drift=drift):
                remote = dict(self.doctor)
                if drift in {"missing", "legacy_only"}:
                    remote.pop("relay_external_health")
                if drift == "legacy_only":
                    remote["dmit_external_health"] = "pass"
                elif drift == "failed":
                    remote["relay_external_health"] = "fail"
                elif drift == "image":
                    remote["production_current_image_id"] = "sha256:" + "e" * 64
                elif drift == "application_commit":
                    remote["production_current_commit_sha"] = "e" * 40
                doctor = mock.Mock()
                doctor.run.side_effect = lambda nodes: {"local_ready": "true", "host_keys_ready": "true"} if nodes == ("local",) else remote
                with mock.patch.object(finalization, "ReleaseDoctor", return_value=doctor), mock.patch.object(finalization, "_doctor_checks", side_effect=DOCTOR_CHECKS):
                    with self.assertRaisesRegex(RuntimeError, "doctor evidence is incomplete|different application identity"):
                        self.finalize(preserve=True)
                self.lease.complete.assert_not_called()
                finalization._cleanup_vm_tasks.assert_not_called()
                for name in ("finalization-checks.json", "vm-finalization.json", "finalization-result.json"):
                    self.assertFalse((self.run / name).exists())
                self.assertEqual(before, {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES})

    def test_recorded_doctor_accepts_relay_and_rejects_legacy_external_health_field(self):
        self.finalize(preserve=True)
        checks = json.loads((self.run / "finalization-checks.json").read_bytes())
        originals = finalization._original_hashes(self.run)
        finalization._validate_checks(self.run, checks, self.manifest, self.helper, originals)
        self.assertEqual(checks["doctor"]["relay_external_health"], "pass")
        for retain_relay in (True, False):
            with self.subTest(retain_relay=retain_relay):
                doctor = {**checks["doctor"], "dmit_external_health": "pass"}
                if not retain_relay:
                    doctor.pop("relay_external_health")
                with self.assertRaisesRegex(RuntimeError, "field allowlist differs"):
                    finalization._validate_checks(self.run, {**checks, "doctor": doctor}, self.manifest, self.helper, originals)

    def test_retained_encrypted_recovery_binds_signed_baseline_and_original_backup(self):
        backup = {"pre_switch_image_id": "sha256:" + "c" * 64, "artifact": "fixture.tar.age", "transport_artifact": "transport.tar.age", "artifact_size": "1", "artifact_sha256": "d" * 64, "release_sha256": "d" * 64, "traffic_preserved": "true", "redis_backup_mode": "rdb", "no_restart_path_proven": "true", "local_restore_point_ready": "true"}
        manifest = {**self.manifest, "schema": 2, "production_current_image_id": backup["pre_switch_image_id"]}
        script = finalization._retained_recovery_script(manifest, backup)
        text = "".join(f"{field}={backup[field]}\n" for field in ("artifact", "transport_artifact", "artifact_size", "artifact_sha256", "traffic_preserved", "redis_backup_mode", "no_restart_path_proven", "local_restore_point_ready"))
        self.assertIn(hashlib.sha256(text.encode()).hexdigest() + "  backup-result", script)
        self.assertIn("sha256sum --strict -c recovery-point.age.sha256", script)
        self.assertIn("root:root:600:1", script)
        self.assertNotIn("rm ", script)
        with self.assertRaisesRegex(RuntimeError, "signed baseline"):
            finalization._retained_recovery_script({**manifest, "production_current_image_id": self.candidate}, backup)
        for field, value in (("release_sha256", "f" * 64), ("artifact_size", "0"), ("artifact", "../fixture.tar.age"), ("traffic_preserved", "false")):
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                finalization._retained_recovery_script(manifest, {**backup, field: value})

    def test_preserve_success_binds_three_schema2_records_and_keeps_originals(self):
        before = {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES}
        self.finalize(preserve=True)
        self.assertEqual(before, {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES})
        for name in ("finalization-checks.json", "finalization-result.json", "vm-finalization.json"):
            value = json.loads((self.run / name).read_bytes())
            self.assertEqual((value["schema"], value["mode"]), (2, "preserve-running"))
        value = supervisor.verified_result_view(self.identifier)
        self.assertEqual((value["vm_power_status"], value["vm_cleanup_status"]), ("running", "preserved"))
        self.assertEqual((value["original_runner_status"], value["original_runner_exit"]), ("failed", 1))
        self.assertTrue(all(call.kwargs["include_vm"] is True for call in finalization._doctor_checks.call_args_list))

    def test_preserve_success_retry_rechecks_vm_and_never_repeats_cleanup(self):
        self.finalize(preserve=True)
        self.lease.complete.reset_mock()
        finalization._cleanup_vm_tasks.reset_mock()
        finalization._doctor_checks.reset_mock()
        self.finalize(preserve=True)
        self.lease.complete.assert_not_called()
        finalization._cleanup_vm_tasks.assert_not_called()
        finalization._doctor_checks.assert_called_once()
        self.assertTrue(finalization._doctor_checks.call_args.kwargs["include_vm"])
        with self.assertRaisesRegex(RuntimeError, "mode mismatch"):
            self.finalize()

    def test_shutdown_result_cannot_be_replayed_as_preserve(self):
        self.finalize()
        self.lease.complete.reset_mock()
        finalization._cleanup_vm_tasks.reset_mock()
        with self.assertRaisesRegex(RuntimeError, "mode mismatch"):
            self.finalize(preserve=True)
        self.lease.complete.assert_not_called()
        finalization._cleanup_vm_tasks.assert_not_called()

    def test_preserve_owner_and_result_write_failure_retry_skips_cleanup_but_keeps_vm_doctor(self):
        for failure in ("owner", "result"):
            with self.subTest(failure=failure):
                for name in ("finalization-checks.json", "finalization-result.json", "vm-finalization.json"):
                    (self.run / name).unlink(missing_ok=True)
                self.lease.record = {"vm_boot_id": "00000000-0000-0000-0000-000000000001"}
                complete = self.lease.complete.side_effect
                write = supervisor._write_json

                def fail_owner(**kwargs):
                    complete(**kwargs)
                    raise OSError("owner commit rejected")

                def fail_result(path, value):
                    if path.name == "finalization-result.json":
                        raise OSError("result commit rejected")
                    return write(path, value)

                patch = mock.patch.object(self.lease, "complete", side_effect=fail_owner) if failure == "owner" else mock.patch.object(supervisor, "_write_json", side_effect=fail_result)
                with patch, self.assertRaises(OSError):
                    self.finalize(preserve=True)
                self.assertEqual(self.lease.record["status"], "verified")
                finalization._cleanup_vm_tasks.reset_mock()
                finalization._doctor_checks.reset_mock()
                self.finalize(preserve=True)
                finalization._cleanup_vm_tasks.assert_not_called()
                self.assertTrue(all(call.kwargs["include_vm"] is True for call in finalization._doctor_checks.call_args_list))

    def test_preserve_failures_before_and_after_cleanup_do_not_create_success(self):
        for target in ("_doctor_checks", "_cleanup_vm_tasks", "complete"):
            with self.subTest(target=target):
                before = {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES}
                patch = mock.patch.object(self.lease, target, side_effect=RuntimeError("completion failed")) if target == "complete" else mock.patch.object(finalization, target, side_effect=RuntimeError("preflight failed"))
                finalization._cleanup_vm_tasks.reset_mock()
                with patch, self.assertRaises(RuntimeError):
                    self.finalize(preserve=True)
                if target == "_doctor_checks":
                    finalization._cleanup_vm_tasks.assert_not_called()
                self.assertFalse((self.run / "finalization-result.json").exists())
                self.assertEqual(before, {name: (self.run / name).read_bytes() for name in finalization.ORIGINAL_FILES})

    def test_preserve_late_original_helper_and_checks_drift_reject_owner_commit(self):
        original_complete = self.lease.complete.side_effect
        for drift in ("original", "helper", "checks"):
            with self.subTest(drift=drift):
                for name in ("finalization-checks.json", "finalization-result.json", "vm-finalization.json"):
                    (self.run / name).unlink(missing_ok=True)
                self.lease.record = {"vm_boot_id": "00000000-0000-0000-0000-000000000001"}
                before = (self.run / "manifest.json").read_bytes()

                def change_before_completion(**kwargs):
                    if drift == "original":
                        self.write("manifest.json", {**self.manifest, "changed": True})
                    elif drift == "checks":
                        checks = json.loads((self.run / "finalization-checks.json").read_bytes())
                        self.write("finalization-checks.json", {**checks, "checked_at": 0})
                    else:
                        finalization._helper_identity.return_value = {**self.helper, "commit_sha": "e" * 40}
                    return original_complete(**kwargs)

                with mock.patch.object(self.lease, "complete", side_effect=change_before_completion), self.assertRaisesRegex(RuntimeError, "before owner commit"):
                    self.finalize(preserve=True)
                self.assertFalse((self.run / "finalization-result.json").exists())
                (self.run / "manifest.json").write_bytes(before)
                finalization._helper_identity.return_value = self.helper

    def test_preserve_unknown_schema_mode_and_cross_record_modes_are_rejected(self):
        self.finalize(preserve=True)
        for name in ("finalization-checks.json", "finalization-result.json", "vm-finalization.json"):
            original = (self.run / name).read_bytes()
            for change in ({"schema": 3}, {"schema": True}, {"mode": "shutdown-owned"}, {"mode": None}):
                with self.subTest(name=name, change=change):
                    self.write(name, {**json.loads(original), **change})
                    with self.assertRaises(RuntimeError):
                        supervisor.verified_result_view(self.identifier)
            (self.run / name).write_bytes(original)
        checks = json.loads((self.run / "finalization-checks.json").read_bytes())
        checks.pop("mode")
        self.write("finalization-checks.json", {**checks, "schema": 1})
        finalization._cleanup_vm_tasks.reset_mock()
        with self.assertRaisesRegex(RuntimeError, "mode mismatch"):
            self.finalize(preserve=True)
        finalization._cleanup_vm_tasks.assert_not_called()

    def test_preserve_checks_mode_is_rechecked_inside_lease(self):
        self.finalize(preserve=True)
        (self.run / "finalization-result.json").unlink()
        read = supervisor._read_json
        checks_path = self.run / "finalization-checks.json"
        reads = 0

        def changed_checks(path, **kwargs):
            nonlocal reads
            value = read(path, **kwargs)
            if path == checks_path:
                reads += 1
                if reads > 1:
                    value = {**value, "schema": 1}
                    value.pop("mode")
            return value

        self.lease.complete.reset_mock()
        with mock.patch.object(supervisor, "_read_json", side_effect=changed_checks), self.assertRaisesRegex(RuntimeError, "mode mismatch"):
            self.finalize(preserve=True)
        self.lease.complete.assert_not_called()
        self.assertFalse((self.run / "finalization-result.json").exists())

    def test_preserve_doctor_requires_complete_current_catalog_and_all_nodes(self):
        from release.migration_planner import discover_migration_catalog

        catalog = discover_migration_catalog(finalization.WORKSPACE)
        self.assertEqual(len(catalog), 359)
        self.assertTrue(any(row["filename"] == "305_scoped_admin_api_keys.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "306_group_image_count_billing.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "303_group_strip_optional_image_tools.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "302_enable_gemini_image_generation_groups.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "301_upstream_cache_guard_admissions.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "298_group_scheduling_protection_policies.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "299_upstream_cache_guard_revisions.sql" for row in catalog))
        self.assertTrue(any(row["filename"] == "296_upstream_cache_guard.sql" for row in catalog))
        rows = [{"filename": row["filename"], "checksum": row["checksum"]} for row in catalog]
        for drift in ("none", "pending", "checksum", "unknown", "backup"):
            with self.subTest(drift=drift):
                observed = [dict(row) for row in rows]
                if drift == "pending":
                    observed = [row for row in observed if row["filename"] != "296_upstream_cache_guard.sql"]
                elif drift == "checksum":
                    next(row for row in observed if row["filename"] == "296_upstream_cache_guard.sql")["checksum"] = "0" * 64
                elif drift == "unknown":
                    observed.append({"filename": "999_unregistered.sql", "checksum": "0" * 64})
                snapshot = {"current_image_id": self.candidate, "production_current_commit_sha": "a" * 40, "schema_migrations": observed}
                remote = {**self.doctor, "production_snapshot_b64": base64.b64encode(canonical_json(snapshot)).decode()}
                if drift == "backup":
                    remote["backup_ready"] = "false"
                doctor = mock.Mock()
                doctor.run.side_effect = [{"local_ready": "true", "host_keys_ready": "true"}, remote]
                manifest = {**self.manifest, "schema": 2, "migration_catalog": catalog}
                with mock.patch.object(finalization, "ReleaseDoctor", return_value=doctor):
                    if drift == "none":
                        self.assertEqual(DOCTOR_CHECKS(manifest, self.helper, mock.Mock(), include_vm=True)["migration_catalog_verified"], "true")
                    else:
                        with self.assertRaises((RuntimeError, ValueError)):
                            DOCTOR_CHECKS(manifest, self.helper, mock.Mock(), include_vm=True)
                self.assertEqual(doctor.run.call_args_list[1].args[0], ("vm", "racknerd", "dmit", "backup"))


if __name__ == "__main__":
    unittest.main()
