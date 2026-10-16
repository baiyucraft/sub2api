"""Resource and evidence boundaries, not proof that a live drill has run."""
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))
from release import recovery_drill as drill


class RecoveryDrillTests(unittest.TestCase):
    def setUp(self):
        self.config = {
            "schema": 1,
            "drill_id": "drill-0123456789abcdef",
            "backend": drill.BACKEND,
            "fixture_kind": "synthetic-offline-complete-recovery",
            "release_id": "270-aaaaaaaaaaaa-1-deadbeef",
            "recovery_helper_bundle_sha256": "b" * 64,
            "pre_image_id": "sha256:" + "c" * 64,
            "recovery_point_sha256": "d" * 64,
            "redis_health_budget_seconds": 180,
        }
        self.report = {
            "schema": 1,
            "status": "pass",
            **{key: self.config[key] for key in ("drill_id", "backend", "release_id", "pre_image_id", "recovery_point_sha256", "recovery_helper_bundle_sha256")},
            **{phase: True for phase in drill.PHASES},
            "isolation_verified": True,
            "completed_data_not_reapplied": True,
            "stale_ingress_reconciled": True,
            "ttl_natural_expiry": True,
            "redis_lazy_expiry_observed": True,
            "interrupts": ["app_healthy", "backup_units_restored"],
            "redis_health_wait_seconds": 65,
            "checkpoint_sha256": {phase: "e" * 64 for phase in drill.PHASES},
        }

    def test_only_strict_offline_backend_is_supported(self):
        drill.validate_config(self.config)
        for backend in ("host", "docker-privileged", "vmware-unproved", "approved-json"):
            with self.subTest(backend=backend):
                bad = {**self.config, "backend": backend}
                with self.assertRaises(drill.DrillBlocked):
                    drill.validate_config(bad)

    def test_arbitrary_arguments_and_production_fixtures_are_rejected(self):
        for mutation in ({"launch_args": ["--bind=/opt/sub2api"]}, {"fixture_kind": "production"}, {"drill_id": "../production"}, {"release_id": "272-aaaaaaaaaaaa-1-deadbeef"}, {"redis_health_budget_seconds": True}):
            with self.subTest(mutation=mutation):
                with self.assertRaises(drill.DrillBlocked):
                    drill.validate_config({**self.config, **mutation})

    def test_resources_missing_cannot_start_a_guest(self):
        unavailable = {"schema": 1, "status": "blocked", "blockers": ["missing_systemd-nspawn"]}
        with mock.patch.object(drill, "probe", return_value=unavailable), \
             mock.patch.object(drill, "validate_root") as root, \
             mock.patch.object(drill.subprocess, "Popen") as spawn:
            plan = drill.plan(Path("/var/lib/sub2api-recovery-drills/drill-0123456789abcdef/rootfs"), self.config)
            self.assertEqual(plan["status"], "blocked")
            with self.assertRaises(drill.DrillBlocked):
                drill.execute_host(Path("/missing"), self.config, 3600)
            root.assert_not_called()
            spawn.assert_not_called()

    def test_host_production_path_cannot_be_used_as_guest(self):
        with self.assertRaises(drill.DrillBlocked):
            drill.validate_root(Path("/opt/sub2api"), self.config)

    def test_launch_has_private_users_network_and_no_host_binds(self):
        command = drill.nspawn_command(Path("/owned/rootfs"), self.config)
        self.assertIn("--private-network", command)
        self.assertIn("--private-users=pick", command)
        self.assertIn("--settings=no", command)
        self.assertIn("--register=no", command)
        self.assertTrue(all(not option.startswith(("--bind", "--network-veth", "--capability")) for option in command))

    def test_helper_source_is_current_bound_bundle_not_incident_claim(self):
        path = drill.guest_helper_directory(self.config)
        self.assertEqual(path.as_posix(), "/opt/sub2api/releases/270-aaaaaaaaaaaa-1-deadbeef/recovery-helpers/" + "b" * 64)
        self.assertNotIn(".active-release", path.as_posix())

    def test_finish_has_stable_signed_gate_inputs_after_claim_moves(self):
        with mock.patch.dict(os.environ, {"RECOVERY_HELPER_GATE": "/unsafe/old.json", "DOCKER_HOST": "tcp://production", "COMPOSE_FILE": "/unsafe/compose"}):
            env = drill.guest_environment(self.config)
        self.assertEqual(env["RECOVERY_HELPER_GATE"].replace("\\", "/"), "/opt/sub2api/releases/" + self.config["release_id"] + "/gate.json")
        self.assertEqual(env["RECOVERY_HELPER_SIGNATURE"].replace("\\", "/"), "/opt/sub2api/releases/" + self.config["release_id"] + "/gate.sig")
        self.assertNotIn(".active-release", env["RECOVERY_HELPER_GATE"])
        self.assertNotIn("DOCKER_HOST", env)
        self.assertNotIn("COMPOSE_FILE", env)

    def test_bundle_verification_uses_the_shared_production_api(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            document = {"schema": 1, "commit_sha": "a" * 40, "files": {"original/path/restore.sh": "c" * 64}}
            (directory / "recovery-helper-bundle.json").write_text(json.dumps(document), encoding="utf-8")
            api = mock.Mock()
            api.recovery_bundle_sha256.return_value = "b" * 64
            with mock.patch.object(drill, "bundle_api", return_value=api):
                drill.verify_helper_bundle(directory, "b" * 64)
                api.recovery_bundle_sha256.assert_called_once_with(document)
                api.verify_recovery_bundle.assert_called_once_with(directory, document)

    def test_bundle_identity_drift_stops_before_helper_use(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            (directory / "recovery-helper-bundle.json").write_text("{}", encoding="utf-8")
            api = mock.Mock()
            api.recovery_bundle_sha256.return_value = "f" * 64
            with mock.patch.object(drill, "bundle_api", return_value=api):
                with self.assertRaises(drill.DrillBlocked):
                    drill.verify_helper_bundle(directory, "b" * 64)
                api.verify_recovery_bundle.assert_not_called()

    def test_real_shared_bundle_layout_and_extra_files(self):
        from release import recovery_bundle as bundle
        if os.name != "nt" and os.geteuid() != 0:
            self.skipTest("production bundle verifier requires root")
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary).resolve()
            directory.chmod(0o700)
            checksums = {}
            for name in sorted(bundle.REQUIRED):
                group = "release/" if name.endswith(".py") else ("maintenance/181/" if name == "restore-backup-units.sh" else "maintenance/release/")
                original = bundle.PREFIX + group + name
                content = ("fixture byte identity: " + name + "\n").encode()
                checksums[original] = hashlib.sha256(content).hexdigest()
                source = directory / "sources" / original
                source.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                source.write_bytes(content)
                source.chmod(0o400)
                alias = directory / name
                alias.write_bytes(content)
                alias.chmod(0o500 if name.endswith(".sh") else 0o400)
            for path, dirs, files in os.walk(directory):
                Path(path).chmod(0o700)
            document = bundle.recovery_bundle_document("a" * 40, checksums)
            manifest = directory / bundle.BUNDLE_NAME
            manifest.write_bytes(bundle.canonical(document))
            manifest.chmod(0o400)
            checksum = bundle.recovery_bundle_sha256(document)
            self.assertEqual(drill.verify_helper_bundle(directory, checksum), document)
            self.assertTrue(drill.sys.dont_write_bytecode)
            (directory / "unexpected-asset").write_text("not in signed closure", encoding="utf-8")
            with self.assertRaises(RuntimeError):
                drill.verify_helper_bundle(directory, checksum)

    def test_required_actual_cases_cannot_be_missing(self):
        drill.verify_real_evidence(self.report, self.config)
        for key in (*drill.PHASES, "isolation_verified", "completed_data_not_reapplied", "stale_ingress_reconciled", "ttl_natural_expiry", "redis_lazy_expiry_observed", "interrupts", "checkpoint_sha256"):
            with self.subTest(key=key):
                bad = deepcopy(self.report)
                bad.pop(key)
                with self.assertRaises(drill.DrillBlocked):
                    drill.verify_real_evidence(bad, self.config)

    def test_signed_identity_and_measured_delay_are_required(self):
        for mutation in ({"pre_image_id": "sha256:" + "f" * 64}, {"recovery_helper_bundle_sha256": "f" * 64}, {"redis_health_wait_seconds": 60}, {"redis_health_wait_seconds": 181}, {"redis_health_wait_seconds": True}, {"status": "synthetic_pass"}):
            with self.subTest(mutation=mutation):
                with self.assertRaises(drill.DrillBlocked):
                    drill.verify_real_evidence({**self.report, **mutation}, self.config)

    def test_completed_checkpoint_hashes_are_a_full_prefix(self):
        bad = deepcopy(self.report)
        bad["checkpoint_sha256"].pop("postgres_restored")
        with self.assertRaises(drill.DrillBlocked):
            drill.verify_real_evidence(bad, self.config)
        bad = deepcopy(self.report)
        bad["checkpoint_sha256"]["redis_restored"] = "approved"
        with self.assertRaises(drill.DrillBlocked):
            drill.verify_real_evidence(bad, self.config)

    def test_completed_data_identity_comes_from_live_database_and_redis(self):
        outputs = [mock.Mock(stdout="16384\n"), mock.Mock(stdout='null\n["redis-server","--requirepass","fixture-only-secret"]\n'), mock.Mock(stdout="# Server\r\nrun_id:" + "a" * 40 + "\r\n")]
        with mock.patch.object(drill.subprocess, "run", side_effect=outputs) as run:
            self.assertEqual(drill.data_runtime_identity(), ("16384", "a" * 40))
            self.assertIn("SELECT oid FROM pg_database WHERE datname='sub2api'", run.call_args_list[0].args[0])
            self.assertIn("INFO server", run.call_args_list[2].args[0][-1])
            self.assertEqual(run.call_args_list[2].kwargs["input"], "fixture-only-secret\n")
            self.assertNotIn("fixture-only-secret", run.call_args_list[2].args[0])

    def test_no_report_claims_temporary_guest_destruction(self):
        source = Path(drill.__file__).read_text(encoding="utf-8")
        self.assertIn('"temporary_material_destroyed": "not_checked"', source)
        self.assertNotIn("shutil.rmtree", source)


if __name__ == "__main__":
    unittest.main()
