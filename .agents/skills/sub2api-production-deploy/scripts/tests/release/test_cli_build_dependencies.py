from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import cli
from test_vm_build_proof import DOCKERFILE_BYTES, proof_case, verified_case


REPORT = verified_case(proof_case()[1]).report


class BuildDependencyOrderingTest(unittest.TestCase):
    def _run(self, command, dependency_error=None):
        manifest, proof = proof_case()
        manifest["migration_catalog"] = []
        identifier = manifest["release_id"]
        order = []
        doctor = mock.Mock()
        doctor.run.side_effect = [{}, {}, {
            "production_current_image_id": "sha256:" + "b" * 64,
            "production_snapshot_b64": "encoded",
        }]
        descriptor = mock.Mock(spec=Path)
        def check(*args):
            order.append("dependencies")
            if dependency_error:
                raise dependency_error
            return verified_case(proof)
        def bind_snapshot(manifest, snapshot, *args):
            order.append("snapshot_binding")
            return manifest, snapshot
        def prepare(*args):
            order.append("recovery_package")
            return descriptor, "/rack/pre-gate", "/vm/pre-gate"
        def gate(*args, **kwargs):
            order.append("gate")
            return Path("gate")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            run_dir = root / identifier
            if command == "deploy":
                run_dir.mkdir()
                (run_dir / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            with (
                mock.patch.object(cli, "RUN_ROOT", root),
                mock.patch.object(cli, "get_profile", return_value={"name": "274", "gate_schema": 2}),
                mock.patch.object(cli, "create_manifest", return_value=manifest),
                mock.patch.object(cli, "release_id", return_value=identifier),
                mock.patch.object(cli, "ReleaseDoctor", return_value=doctor),
                mock.patch.object(cli, "install_vm_validator"),
                mock.patch.object(cli, "bootstrap_production"),
                mock.patch.object(cli, "decode_snapshot", return_value={"schema_migrations": []}),
                mock.patch.object(cli, "_recovery_gate_report", return_value={"mode": "specialized"}),
                mock.patch.object(cli, "ensure_vm_build_dependencies", side_effect=check),
                mock.patch.object(cli, "_bind_preflight_snapshot", side_effect=bind_snapshot),
                mock.patch("release.vm_build.git_blob_sha256", side_effect=lambda commit, path:
                           hashlib.sha256(DOCKERFILE_BYTES).hexdigest() if path == "Dockerfile"
                           else proof["helper_sha256"]),
                mock.patch.object(cli, "prepare_pre_gate_inputs", side_effect=prepare) as package,
                mock.patch.object(cli, "create_vm_gate", side_effect=gate) as build,
                mock.patch.object(cli, "release") as production,
                mock.patch("release.vm_lifecycle.load_settings", return_value=None),
                mock.patch("builtins.print"),
            ):
                args = argparse.Namespace(profile="274", commit="a" * 40, deployment_mode="downtime", release_id=identifier)
                error = None
                try:
                    if command == "deploy":
                        cli.deploy(args, acquire_lock=False)
                    else:
                        cli.vm_validate(args)
                except Exception as caught:
                    error = caught
                report = json.loads((run_dir / "build-dependencies.json").read_text(encoding="utf-8"))
                state = json.loads((run_dir / "state.json").read_text(encoding="utf-8"))
                if dependency_error:
                    self.assertIs(error, dependency_error)
                    package.assert_not_called()
                    build.assert_not_called()
                    production.assert_not_called()
                    self.assertEqual(state["status"], "failed")
                    self.assertEqual(state["stage"], "vm_validate")
                    self.assertEqual(report["build_failure_code"], "build_preflight_unproven")
                    self.assertNotIn("secret", json.dumps(report))
                    self.assertEqual(order, ["snapshot_binding", "dependencies"])
                else:
                    self.assertIsNone(error)
                    self.assertEqual(order, ["snapshot_binding", "dependencies", "recovery_package", "gate"])
                    self.assertEqual(report["build_dependency_verified_count"], "5")
                    bound = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
                    self.assertEqual(bound["build_dependencies"], proof)
                    self.assertEqual(bound["build_dependencies_sha256"], report["build_dependency_proof_sha256"])
                    self.assertEqual(state["status"], "running")
                    self.assertEqual(production.call_count, 1 if command == "deploy" else 0)

    def test_deploy_verifies_dependencies_before_recovery_package(self):
        self._run("deploy")

    def test_vm_validate_verifies_dependencies_before_recovery_package(self):
        self._run("vm_validate")

    def test_deploy_does_not_prepare_backup_or_gate_after_dependency_failure(self):
        self._run("deploy", RuntimeError("secret transport details"))

    def test_vm_validate_does_not_prepare_backup_or_gate_after_dependency_failure(self):
        self._run("vm_validate", RuntimeError("secret transport details"))

    def test_malformed_exception_report_is_replaced_with_fixed_failure(self):
        for field in REPORT:
            with self.subTest(field=field):
                error = RuntimeError("secret transport details")
                error.report = {**REPORT, "build_dependency_status": "failed", "build_failure_code": "build_registry_502", field: "secret"}
                self._run("deploy", error)

    def test_non_string_exception_report_is_replaced_with_fixed_failure(self):
        error = RuntimeError("secret transport details")
        error.report = {**REPORT, "build_failure_code": ["secret"]}
        self._run("vm_validate", error)
