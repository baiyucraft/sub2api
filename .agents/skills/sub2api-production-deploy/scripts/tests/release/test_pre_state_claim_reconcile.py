from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import shlex
import shutil
import sys
import tempfile
import unittest
from contextlib import ExitStack, nullcontext
from pathlib import Path
from types import SimpleNamespace
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SCRIPTS))

from release import production, supervisor, vm_lifecycle
from release.atomic import canonical_json
from release.production_snapshot import snapshot_sha256
from release.process import run_hidden


class PreStateClaimTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.identifier = "277-aaaaaaaaaaaa-123-deadbeef"
        self.run = self.root / "releases" / self.identifier
        self.run.mkdir(parents=True)
        self.shared = self.root / "shared"
        self.shared.mkdir()
        self.trust = self.root / "trust.pub"
        self.trust.write_bytes(b"fixture-trust")
        prefix = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/"
        self.manifest = {"schema": 2, "release_id": self.identifier, "profile": "277", "commit_sha": "a" * 40,
                         "release_asset_sha256": {prefix + name: "d" * 64 for name in ("context.sh", "cleanup-state.sh", "reconcile.sh")}}
        self.snapshot = {"current_image_id": "sha256:" + "b" * 64, "production_current_commit_sha": "b" * 40,
                         "schema_migrations": [{"filename": "001_base.sql", "checksum": "c" * 64}]}
        self.document = {"manifest": self.manifest, "evidence": {
            "candidate_image_id": "sha256:" + "a" * 64,
            "production_current_image_id": self.snapshot["current_image_id"],
            "production_snapshot_sha256": snapshot_sha256(self.snapshot),
            "candidate_archive_sha256": hashlib.sha256(b"archive").hexdigest(),
        }}
        self.runner = {"schema": 1, "release_id": self.identifier, "profile": "277", "commit": "a" * 40,
                       "status": "blocked_reconciliation", "pid": 771, "process_token": "original-token", "exit_code": 1}
        self.failed = {"release_id": self.identifier, "status": "blocked_reconciliation", "stage": "blocked_reconciliation",
                       "history": [{"stage": stage} for stage in ("stage_assets", "stage_assets_verified", "production_preflight_v2",
                           "production_preflight_v2_verified", "pre_switch_public_health_verified", "freeze", "recovery_started", "blocked_reconciliation")]}
        self.owner = {"schema": 1, "vm_identity": "isolated-vm", "release_id": self.identifier,
                      "source_commit": "a" * 40, "pid": 771, "process_token": "original-token",
                      "lease_token": "lease", "lease_status": "retained", "initially_running": True,
                      "vm_started_by_release": False, "vm_power_status": "running",
                      "vm_boot_id": "00000000-1111-2222-3333-444444444444"}
        for name, value in {"manifest.json": self.manifest, "runner.json": self.runner, "state.json": {"status": "failed"},
                            "release-state.json": {"status": "blocked_reconciliation"}, "vm-lifecycle.json": self.owner,
                            "gate/gate.json": self.document, "gate/production-result.json": self.failed}.items():
            self.write(name, value)
        (self.run / "gate/gate.sig").write_bytes(b"fixture-signature")
        (self.shared / "owner.json").write_bytes(canonical_json(self.owner) + b"\n")
        self.originals = {str(path.relative_to(self.run)): path.read_bytes() for path in self.run.rglob("*") if path.is_file()}
        self.proof = {"pre_state_claim": "verified", "production_snapshot_sha256": snapshot_sha256(self.snapshot),
                      "running_image_id": self.snapshot["current_image_id"]}
        self.inspection = {"decision": "claim_only_recover", "failure_code": "freeze_failed_before_state"}
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.stack.enter_context(mock.patch.object(supervisor, "RUN_ROOT", self.run.parent))
        self.stack.enter_context(mock.patch.object(supervisor, "TRUSTED_VM_PUBLIC_KEY", self.trust))
        self.process = self.stack.enter_context(mock.patch.object(supervisor, "_process_token", return_value=None))
        self.stack.enter_context(mock.patch.object(supervisor, "verify_gate", return_value=self.document))
        self.stack.enter_context(mock.patch.object(vm_lifecycle, "load_settings", return_value=SimpleNamespace(identity="isolated-vm")))
        self.stack.enter_context(mock.patch.object(vm_lifecycle, "_shared_dir", return_value=self.shared))
        self.stack.enter_context(mock.patch.object(vm_lifecycle, "shared_lock", return_value=nullcontext()))
        self.vm_probe = self.stack.enter_context(mock.patch.object(vm_lifecycle, "_preserve_running_probe"))
        self.stack.enter_context(mock.patch("release.process.process_identity", return_value=None))
        self.runtime = self.stack.enter_context(mock.patch.object(supervisor, "_prove_pre_state_runtime", return_value={
            "production_doctor": "verified", "direct_route_health": "pass", "dmit_route_health": "pass"}))

    def write(self, name, value):
        path = self.run / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(canonical_json(value) + b"\n")

    def assert_originals(self):
        for name, raw in self.originals.items():
            self.assertEqual((self.run / name).read_bytes(), raw, name)

    def execute(self, *, inspection=None, proof=None, remote=None):
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=inspection or self.inspection), \
             mock.patch.object(supervisor, "_prove_pre_state_claim", **(proof or {"return_value": self.proof})) as prove, \
             mock.patch.object(supervisor, "SSHRunner") as ssh:
            ssh.return_value.run.return_value.values = remote or {"release_claim_reconciled": "true", "plaintext_state_removed": "true"}
            result = supervisor._reconcile_pre_state_claim(self.identifier)
            return result, prove, ssh

    def test_recovery_preserves_failure_and_commits_owner_after_independent_receipt(self):
        result, prove, ssh = self.execute()
        self.assertEqual(result["status"], "claim_recovered")
        self.assertFalse(result["application_deployed"])
        self.assertEqual(result["original_runner_status"], "blocked_reconciliation")
        self.assert_originals()
        owner = json.loads((self.shared / "owner.json").read_bytes())
        self.assertEqual(owner["lease_status"], "released")
        self.assertEqual(owner["vm_cleanup_status"], "preserved")
        self.assertEqual(owner["pre_state_claim_result_sha256"], hashlib.sha256((self.run / "pre-state-claim-result.json").read_bytes()).hexdigest())
        self.vm_probe.assert_called_once()
        checks = (self.run / "pre-state-claim-checks.json").read_bytes()
        self.assertEqual(result["checks_sha256"], hashlib.sha256(checks).hexdigest())
        self.assertIs(ssh.return_value.run.call_args.kwargs["allow_stderr"], True)
        script = ssh.return_value.run.call_args.args[1]
        self.assertLess(script.index("flock -n 9"), script.index("observations="))
        self.assertLess(script.index('test "$observed_sha"'), script.rindex("/cleanup-state.sh"))
        self.assertNotIn("docker rm", script)
        self.assertTrue(prove.call_args.kwargs["recovered"])

    def test_repeated_reconciliation_only_verifies_receipt(self):
        first, _, _ = self.execute()
        second, _, ssh = self.execute(inspection={"decision": "already_recovered", "failure_code": "none"})
        self.assertEqual(first, second)
        ssh.return_value.run.assert_not_called()
        self.assert_originals()

    def test_completed_remote_cleanup_can_resume_after_lost_reply(self):
        with self.assertRaisesRegex(RuntimeError, "cleanup reply"):
            self.execute(remote={"release_claim_reconciled": "false", "plaintext_state_removed": "true"})
        self.assertTrue((self.run / "pre-state-claim-checks.json").exists())
        self.assertFalse((self.run / "pre-state-claim-result.json").exists())
        result, _, ssh = self.execute(inspection={"decision": "already_recovered"})
        self.assertEqual(result["status"], "claim_recovered")
        ssh.return_value.run.assert_not_called()
        self.assert_originals()

    def test_probe_failure_does_not_write_checks_or_run_cleanup(self):
        with mock.patch.object(supervisor, "SSHRunner") as ssh, self.assertRaisesRegex(RuntimeError, "baseline"):
            self.execute(proof={"side_effect": RuntimeError("baseline drift")})
        ssh.assert_not_called()
        self.assertFalse((self.run / "pre-state-claim-checks.json").exists())
        self.assertFalse((self.run / "pre-state-claim-result.json").exists())
        self.assert_originals()

    def test_post_cleanup_failure_never_publishes_success(self):
        with self.assertRaisesRegex(RuntimeError, "post cleanup"):
            self.execute(proof={"side_effect": [self.proof, RuntimeError("post cleanup failed")]})
        self.assertTrue((self.run / "pre-state-claim-checks.json").exists())
        self.assertFalse((self.run / "pre-state-claim-result.json").exists())
        self.assert_originals()

    def test_vm_boot_probe_failure_retains_owner_and_original_files(self):
        self.vm_probe.side_effect = RuntimeError("VM boot changed")
        with self.assertRaisesRegex(RuntimeError, "VM boot"):
            self.execute()
        self.assertEqual(json.loads((self.shared / "owner.json").read_bytes()), self.owner)
        self.assertTrue((self.run / "pre-state-claim-result.json").exists())
        self.assert_originals()

    def test_vm_configuration_drift_rejects_probe_and_retains_owner(self):
        caller_settings = SimpleNamespace(identity="isolated-vm")
        changed_settings = SimpleNamespace(identity="isolated-vm", startup_timeout_seconds=999)
        with mock.patch.object(vm_lifecycle, "load_settings", side_effect=[caller_settings, changed_settings]):
            with self.assertRaisesRegex(vm_lifecycle.VMLifecycleError, "pre_state_claim_config_changed"):
                self.execute()
        self.vm_probe.assert_not_called()
        self.assertEqual(json.loads((self.shared / "owner.json").read_bytes()), self.owner)
        self.assertTrue((self.run / "pre-state-claim-result.json").exists())
        self.assert_originals()

    def test_owner_write_failure_retains_owner_and_retries_only_commit(self):
        with mock.patch.object(vm_lifecycle, "atomic_write", side_effect=OSError("owner write failed")):
            with self.assertRaisesRegex(OSError, "owner write"):
                self.execute()
        self.assertEqual(json.loads((self.shared / "owner.json").read_bytes()), self.owner)
        self.assertTrue((self.run / "pre-state-claim-result.json").exists())
        result, _, ssh = self.execute(inspection={"decision": "already_recovered"})
        self.assertEqual(result["status"], "claim_recovered")
        ssh.return_value.run.assert_not_called()
        self.assertEqual(json.loads((self.shared / "owner.json").read_bytes())["lease_status"], "released")
        self.assert_originals()

    def test_original_or_receipt_drift_is_rejected(self):
        self.execute()
        self.write("runner.json", {**self.runner, "exit_code": 2})
        with self.assertRaisesRegex(RuntimeError, "checks changed"):
            self.execute(inspection={"decision": "already_recovered"})

    def test_wrong_or_released_vm_owner_is_rejected_before_remote_write(self):
        for change in ({"release_id": "other"}, {"lease_status": "released"}, {"lease_token": "changed"}):
            with self.subTest(change=change), mock.patch.object(supervisor, "SSHRunner") as ssh:
                (self.shared / "owner.json").write_bytes(canonical_json({**self.owner, **change}))
                with self.assertRaisesRegex(RuntimeError, "VM owner"):
                    supervisor._reconcile_pre_state_claim(self.identifier)
                ssh.assert_not_called()

    def test_live_worker_and_unknown_process_observation_fail_closed(self):
        for behavior in ({"return_value": "original-token"}, {"side_effect": RuntimeError("unknown PID")}):
            with self.subTest(behavior=behavior), mock.patch.object(supervisor, "_process_token", **behavior):
                with self.assertRaises(RuntimeError):
                    self.execute()
        self.assertFalse((self.run / "pre-state-claim-checks.json").exists())

    def test_only_freeze_intent_may_override_dangerous_history(self):
        self.assertTrue(supervisor._pre_state_failure_eligible(self.identifier, self.manifest, self.runner, self.failed, self.document))
        for stage in ("release_state_captured", "migration_preflight", "migration_and_switch", "nginx_ingress_applied", "public_route_verification", "unknown_stage"):
            with self.subTest(stage=stage):
                changed = {**self.failed, "history": self.failed["history"] + [{"stage": stage}]}
                self.assertFalse(supervisor._pre_state_failure_eligible(self.identifier, self.manifest, self.runner, changed, self.document))
        self.assertIn("freeze", supervisor.DANGEROUS_STAGES)

    def test_wrong_gate_or_unbound_worker_is_rejected(self):
        for runner in ({**self.runner, "release_id": "other"}, {**self.runner, "commit": "c" * 40},
                       {**self.runner, "process_token": ""}, {**self.runner, "exit_code": None}):
            self.assertFalse(supervisor._pre_state_failure_eligible(self.identifier, self.manifest, runner, self.failed, self.document))
        self.assertFalse(supervisor._pre_state_failure_eligible(self.identifier, {**self.manifest, "schema": 1}, self.runner, self.failed, self.document))
        for change in ({"release_id": "other"}, {"profile": "276"}, {"commit": "b" * 40}, {"commit_sha": "b" * 40}):
            self.assertFalse(supervisor._pre_state_failure_eligible(self.identifier, self.manifest, self.runner, {**self.failed, **change}, self.document))

    def test_snapshot_schema_and_image_drift_fail_before_cleanup(self):
        values = {"pre_state_claim": "verified", "snapshot_b64": base64.b64encode(canonical_json(self.snapshot)).decode()}
        with mock.patch.object(supervisor, "SSHRunner") as ssh:
            ssh.return_value.run.return_value.values = values
            self.assertEqual(supervisor._prove_pre_state_claim(self.run, self.document), self.proof)
            for snapshot in ({**self.snapshot, "current_image_id": "sha256:" + "e" * 64},
                             {**self.snapshot, "schema_migrations": []}):
                ssh.return_value.run.return_value.values = {**values, "snapshot_b64": base64.b64encode(canonical_json(snapshot)).decode()}
                with self.assertRaisesRegex(RuntimeError, "baseline"):
                    supervisor._prove_pre_state_claim(self.run, self.document)

    def test_strict_probe_binds_claim_signature_helpers_state_and_routes(self):
        script = production.pre_state_claim_probe_script(self.document, self.run / "gate", self.trust)
        for guard in ("openssl pkeyutl -verify", "CLAIM_SHA256SUMS", "-type l", "-links +1", "test ! -L /opt/sub2api/backups/release-state/",
                      ".277-aaaaaaaaaaaa-123-deadbeef.staging.*",
                      "sub2api-migrate-", 'test "$upstream_port" = "$active_port"', 'test "$timer_active" = active',
                      'case "$service_active" in inactive|failed)', "schema_migrations"):
            self.assertIn(guard, script)
        self.assertNotIn('source "$active', script)
        self.assertNotIn("rm ", script)
        recovered = production.pre_state_claim_probe_script(self.document, self.run / "gate", self.trust, recovered=True)
        self.assertIn("test ! -e /opt/sub2api/releases/.active-release", recovered)
        self.assertIn('test -f "$active/plaintext-cleaned"', recovered)

    def test_native_bash_probe_rejects_observer_failures_and_partial_resources(self):
        bash = shutil.which("bash") or next((str(path) for path in (
            Path("C:/Program Files/Git/bin/bash.exe"), Path("C:/MProgram/Git/bin/bash.exe")) if path.is_file()), None)
        if not bash:
            self.skipTest("native Bash is unavailable")
        sandbox = self.root / "shell"
        claim = sandbox / "opt/sub2api/releases/.active-release"
        assets = claim / "assets"
        assets.mkdir(parents=True)
        state_root = sandbox / "opt/sub2api/backups/release-state"
        state_root.mkdir(parents=True)
        (claim.parent / self.identifier).mkdir()
        document = json.loads(json.dumps(self.document))
        for name in ("context.sh", "cleanup-state.sh", "reconcile.sh"):
            data = b"#!/usr/bin/env bash\ntrue\n"
            (assets / name).write_bytes(data)
            document["manifest"]["release_asset_sha256"][".agents/skills/sub2api-production-deploy/scripts/maintenance/release/" + name] = hashlib.sha256(data).hexdigest()
        self.write("gate/gate.json", document)
        for name, data in {"gate.json": (self.run / "gate/gate.json").read_bytes(), "gate.sig": b"fixture-signature",
                           "release_id": ("release_id=" + self.identifier + "\n").encode(), "candidate.tar.gz": b"archive"}.items():
            (claim / name).write_bytes(data)
        lines = [hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.relative_to(claim).as_posix()
                 for path in sorted(claim.rglob("*")) if path.is_file() and path.name != "release_id"]
        (claim / "CLAIM_SHA256SUMS").write_bytes(("\n".join(lines) + "\n").encode("ascii"))
        trust = sandbox / "opt/sub2api-release-trust/vm-gate-ed25519.pub"
        trust.parent.mkdir(parents=True)
        trust.write_bytes(self.trust.read_bytes())
        slot = sandbox / "opt/sub2api/active-app"
        slot.write_bytes(("container=sub2api\nport=18080\nimage_id=" + self.snapshot["current_image_id"] + "\n").encode("ascii"))
        upstream = sandbox / "etc/nginx/conf.d/sub2api-release-upstream.conf"
        upstream.parent.mkdir(parents=True)
        normal_upstream = "upstream sub2api_release_backend {\n    server 127.0.0.1:18080;\n    keepalive 128;\n}\n"
        upstream.write_bytes(normal_upstream.encode("ascii"))
        snapshot = "printf 'snapshot_b64=%s\\n' " + shlex.quote(base64.b64encode(canonical_json(self.snapshot)).decode())
        with mock.patch.object(production, "snapshot_script", return_value=snapshot):
            script = production.pre_state_claim_probe_script(document, self.run / "gate", self.trust)
        script = script.replace("/opt", sandbox.as_posix() + "/opt").replace("/etc/nginx", sandbox.as_posix() + "/etc/nginx")
        prelude = r'''
stat() {
  case "$3" in */assets/*) printf '0:0:500:1\n';; */assets|*/.active-release) printf '0:0:700\n';; *) printf '0:0:400:1\n';; esac
  test "$FAULT" != stat_query
}
openssl() { test "$FAULT" != signature; }
nginx() { test "$FAULT" != nginx_query; }
sha256sum() { command sha256sum "$@"; test "$FAULT" != hash_query; }
find() { command find "$@"; test "$FAULT" != find_query; }
docker() {
  if test "$FAULT" = docker_query; then printf 'sub2api\n'; return 4; fi
  case "$1:$2" in
    ps:-a) printf 'sub2api\n';
      if test "$FAULT" = candidate; then printf 'sub2api-candidate-IDENTIFIER\n'; fi
      if test "$FAULT" = migration; then printf 'sub2api-migrate-IDENTIFIER\n'; fi ;;
    inspect:-f)
      case "$3" in *Health*) if test "$FAULT" = unhealthy; then printf 'unhealthy\n'; else printf 'healthy\n'; fi ;;
        *State.Status*) printf 'running\n';; *) return 5;; esac ;;
    *) return 5;;
  esac
}
systemctl() {
  if test "$FAULT" = unit_query; then printf 'inactive\n'; return 4; fi
  case "$1:$2" in
    is-active:nginx|is-active:sub2api-backup.timer) printf 'active\n';;
    is-active:sub2api-backup.service) printf 'inactive\n'; return 3;;
    is-enabled:sub2api-backup.timer) printf 'enabled\n';;
    is-enabled:sub2api-backup.service) printf 'static\n';;
    *) return 5;;
  esac
}
'''.replace("IDENTIFIER", self.identifier)
        marker = sandbox / "completed"
        faults = ("none", "stat_query", "signature", "nginx_query", "hash_query", "find_query", "docker_query", "unit_query",
                  "candidate", "migration", "unhealthy", "route", "staging_file", "staging_directory", "state_directory")
        for fault in faults:
            with self.subTest(fault=fault):
                marker.unlink(missing_ok=True)
                staging = state_root / ("." + self.identifier + ".staging.fixture")
                state = state_root / self.identifier
                if fault == "staging_file":
                    staging.write_bytes(b"partial-state")
                elif fault == "staging_directory":
                    staging.mkdir()
                elif fault == "state_directory":
                    state.mkdir()
                elif fault == "route":
                    upstream.write_bytes(normal_upstream.replace("    keepalive", "    server 127.0.0.1:18081;\n    keepalive").encode("ascii"))
                full = prelude + "\n" + script + "\nprintf completed > " + shlex.quote(marker.as_posix()) + "\n"
                result = run_hidden([bash, "--noprofile", "--norc", "-s"], input=full, text=True, capture_output=True,
                                    env={**os.environ, "FAULT": fault}, timeout=20)
                self.assertEqual(result.returncode == 0, fault == "none", result.stderr)
                self.assertEqual(marker.exists(), fault == "none", result.stdout)
                if staging.is_dir():
                    staging.rmdir()
                elif staging.exists():
                    staging.unlink()
                if state.exists():
                    state.rmdir()
                upstream.write_bytes(normal_upstream.encode("ascii"))

    def test_reconcile_cli_does_not_rewrite_failure_state(self):
        with mock.patch.object(supervisor, "_inspect_reconciliation", return_value=self.inspection), \
             mock.patch.object(supervisor, "_reconcile_pre_state_claim", return_value={"status": "claim_recovered", "application_deployed": False}) as close, \
             mock.patch.object(supervisor, "_write_json") as write, mock.patch("builtins.print"):
            supervisor.reconcile(argparse.Namespace(release_id=self.identifier, mode="recover"))
        close.assert_called_once_with(self.identifier)
        write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
