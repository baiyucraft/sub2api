from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import atomic_write, canonical_json
from release.gate import verify_gate
from release.manifest import migration_checksums
from release.profiles import get_profile
from release.state import RunLock, RunState


class ReleaseCoreTest(unittest.TestCase):
    @staticmethod
    def release_unit_checksum(path: Path) -> str:
        return {
            "vm-validate.sh": "validator",
            "sign-gate.sh": "gate-signer",
            "sign-dr-evidence.sh": "dr-signer",
        }[path.name]

    def manifest(self, runner: str, expires_at: int) -> dict:
        profile = get_profile("182")
        return {
            "commit_sha": "a" * 40,
            "profile": "182",
            "runner_sha256": runner,
            "vm_validator_sha256": "validator",
            "vm_gate_signer_sha256": "gate-signer",
            "vm_dr_signer_sha256": "dr-signer",
            "release_asset_sha256": {"asset": "digest"},
            "origin": profile["origin"],
            "vm_identity": profile["vm_identity"],
            "expires_at": expires_at,
        }

    def test_atomic_write_replaces_complete_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            atomic_write(path, b"old\n")
            atomic_write(path, b"new\n")
            self.assertEqual(path.read_bytes(), b"new\n")
            self.assertFalse(list(path.parent.glob(f".{path.name}.*")))

    def test_stale_lock_file_does_not_block_new_process(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            path.write_text("pid=999999\n", encoding="utf-8")
            with RunLock(path):
                pass
            self.assertTrue(path.exists())
            self.assertIn(f"pid={os.getpid()}", path.read_text(encoding="utf-8"))

    def test_active_lock_rejects_second_release(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.lock"
            with RunLock(path):
                with self.assertRaisesRegex(RuntimeError, "another release process"):
                    with RunLock(path):
                        pass

    def test_terminal_state_cannot_resume_running(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            state = RunState.create(Path(directory) / "state.json", "release")
            state.transition("vm", "failed")
            with self.assertRaisesRegex(RuntimeError, "terminal"):
                state.transition("vm", "running")

    def test_migration_checksum_matches_runner_trimmed_content(self) -> None:
        profile = {"migrations": ["migration.sql"]}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            migration = root / "backend" / "migrations" / "migration.sql"
            migration.parent.mkdir(parents=True)
            migration.write_text("\nSELECT 1;\n\n", encoding="utf-8")
            with mock.patch("release.manifest.workspace_root", return_value=root):
                checksums = migration_checksums(profile)
        self.assertEqual(checksums["migration.sql"], hashlib.sha256(b"SELECT 1;").hexdigest())

    def test_profile_191_extends_profile_187_with_official_migrations(self) -> None:
        profile_187 = get_profile("187")
        profile_191 = get_profile("191")
        self.assertEqual(profile_191["version"], "0.1.157-baiyu")
        self.assertEqual(
            profile_191["migrations"],
            profile_187["migrations"]
            + [
                "188_add_subscription_plan_currency.sql",
                "189_channel_image_input_price.sql",
                "190_usage_log_image_input_tokens.sql",
                "191_audit_logs.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_191)), profile_191["migrations"])

    def test_current_profiles_are_allowed_by_release_entrypoints(self) -> None:
        expected_release_pattern = "(182|187|191|192|194|195|197|198)"
        expected_profile_check = "$profile == 182 || $profile == 187 || $profile == 191 || $profile == 192 || $profile == 194 || $profile == 195 || $profile == 197 || $profile == 198"
        for relative_path in (
            "release/vm-validate.sh",
            "release/sign-gate.sh",
            "maintenance/release/context.sh",
            "maintenance/release/prepare.sh",
            "maintenance/release/promote-backup.sh",
            "maintenance/181/mask-backup-units.sh",
            "maintenance/181/restore-backup-units.sh",
        ):
            content = (DEPLOY_ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn(expected_release_pattern, content, relative_path)
        for relative_path in (
            "release/vm-validate.sh",
            "maintenance/release/prepare.sh",
        ):
            content = (DEPLOY_ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn(expected_profile_check, content, relative_path)

        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("drverify/[^/]+", validator)

    def test_profile_192_extends_profile_191_with_group_duplicate_migration(self) -> None:
        profile_191 = get_profile("191")
        profile_192 = get_profile("192")
        self.assertEqual(profile_192["version"], "0.1.158-baiyu")
        self.assertEqual(
            profile_192["migrations"],
            profile_191["migrations"] + ["192_group_duplicate_operation_id.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_192)), profile_192["migrations"])

    def test_profile_194_extends_profile_192_with_prompt_audit_migrations(self) -> None:
        profile_192 = get_profile("192")
        profile_194 = get_profile("194")
        self.assertEqual(profile_194["version"], "0.1.160-baiyu")
        self.assertEqual(
            profile_194["migrations"],
            profile_192["migrations"]
            + ["193_prompt_audit.sql", "194_prompt_audit_full_prompt.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_194)), profile_194["migrations"])

    def test_profile_195_extends_profile_194_with_monitor_rate_migration(self) -> None:
        profile_194 = get_profile("194")
        profile_195 = get_profile("195")
        self.assertEqual(profile_195["version"], "0.1.161-baiyu")
        self.assertEqual(
            profile_195["migrations"],
            profile_194["migrations"] + ["195_upstream_scheduling_monitor_rates.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_195)), profile_195["migrations"])

    def test_profile_197_extends_profile_195_with_upstream_migrations(self) -> None:
        profile_195 = get_profile("195")
        profile_197 = get_profile("197")
        self.assertEqual(profile_197["version"], "0.1.162-baiyu")
        self.assertEqual(
            profile_197["migrations"],
            profile_195["migrations"]
            + [
                "196_ops_ingress_reject_aggregates.sql",
                "197_auth_cache_invalidation_outbox.sql",
            ],
        )
        self.assertEqual(list(migration_checksums(profile_197)), profile_197["migrations"])

    def test_profile_198_extends_profile_197_with_managed_monitor_key_name_migration(self) -> None:
        profile_197 = get_profile("197")
        profile_198 = get_profile("198")
        self.assertEqual(profile_198["version"], "0.1.162-baiyu")
        self.assertEqual(
            profile_198["migrations"],
            profile_197["migrations"] + ["198_normalize_managed_monitor_key_names.sql"],
        )
        self.assertEqual(list(migration_checksums(profile_198)), profile_198["migrations"])

    def test_profile_194_requires_prompt_audit_disabled_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        context = (DEPLOY_ROOT / "maintenance" / "release" / "context.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")

        self.assertIn("prompt_audit_state == 't|0|0'", validator)
        self.assertIn("prompt_audit_disabled:$prompt_audit_disabled", validator)
        self.assertIn("assert_prompt_audit_disabled()", context)
        self.assertIn("$profile != 197 && $profile != 198", context)
        self.assertEqual(production.count('"prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events"'), 3)
        self.assertIn('expected_profile in {"194", "195", "197", "198"}', gate)

    def test_profile_195_requires_semantic_migration_evidence(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        production = (DEPLOY_ROOT / "release" / "production.py").read_text(encoding="utf-8")
        gate = (DEPLOY_ROOT / "release" / "gate.py").read_text(encoding="utf-8")
        switch = (DEPLOY_ROOT / "maintenance" / "release" / "switch.sh").read_text(encoding="utf-8")
        assertion = (DEPLOY_ROOT / "maintenance" / "release" / "migration-195-assert.sh").read_text(encoding="utf-8")

        self.assertIn("migration_195_verified:$migration_195_verified", validator)
        self.assertIn("managed_monitor_key_names_verified:$managed_monitor_key_names_verified", validator)
        self.assertIn('bash "$source_dir/deploy/maintenance/release/migration-195-assert.sh" preflight', validator)
        self.assertIn("MIGRATION_STATUS=absent", validator)
        self.assertIn("MIGRATION_STATUS=verified", validator)
        self.assertIn("migration_195_verified_state", validator)
        self.assertIn("verified_replay=true", validator)
        self.assertIn("verified_low_watermark_rejected=true", validator)
        self.assertIn("probe_migration_195_recorded", validator)
        self.assertIn("migration_195_status=verified", validator)
        self.assertIn('MIGRATION_STATUS="$migration_195_status"', validator)
        self.assertIn('redis-cli SET sched:v2:outbox:watermark "$probe_outbox_highwater"', validator)
        self.assertIn('[[ $consumed_event_id == 0 || $sentinel_event_id -gt $consumed_event_id ]]', validator)
        self.assertIn("ASSERT_DB_USER=\"$database_owner\"", validator)
        self.assertNotIn("ASSERT_DB_USER=\"$database_user\"", validator)
        self.assertIn("create_probe_database()", validator)
        self.assertIn("if ASSERT_CONTEXT_FILE=", validator)
        self.assertIn('sh -lc "dropdb -U', validator)
        self.assertEqual(validator.count("create_probe_database\n"), 2)
        self.assertEqual(validator.count('docker exec -i sub2api-postgres sh -lc "pg_restore'), 2)
        self.assertEqual(validator.count('< "$state_dir/probe.dump"'), 2)
        self.assertNotIn("dropdb -U \\\"\\${POSTGRES_USER:-postgres}\\\" $probe_db && createdb", validator)
        self.assertIn("fixture_rejected=true", validator)
        self.assertIn("restore_completed=true", validator)
        self.assertIn("clean_preflight=true", validator)
        self.assertIn("migrate-candidate.log", validator)
        self.assertIn('exec 2>"$state_dir/validator.stderr"', validator)
        self.assertIn('rm -f "$state_dir/validator.stderr"', validator)
        self.assertIn("migration_missing_object", validator)
        self.assertIn("migration_constraint", validator)
        self.assertIn("migration_182_semantic", validator)
        self.assertIn("migration_195_semantic", validator)
        self.assertIn("failed_migration=$(sed", validator)
        self.assertIn('category="migration_file_$failed_migration"', validator)
        self.assertIn("migration_permission", validator)
        self.assertIn("migration_config", validator)
        self.assertIn("migration_runner_init", validator)
        self.assertIn("migration_advisory_lock", validator)
        self.assertIn("migration_go_timezone", validator)
        self.assertIn("migration_database_timezone", validator)
        self.assertIn('test -f \"/usr/share/zoneinfo/$PROBE_TIMEZONE\"', validator)
        self.assertIn("migration_missing_group_rate_snapshots", validator)
        self.assertIn("migration_missing_timezone_lock", validator)
        self.assertIn("migration_missing_advisory_function", validator)
        self.assertIn("migration_sqlstate=$(sed", validator)
        self.assertIn('category="migration_sqlstate_$migration_sqlstate"', validator)
        self.assertIn("$category == migration_timezone", validator)
        self.assertIn('"migration_195_verified", "fixture_rejected", "restore_completed", "clean_preflight", "verified_replay", "verified_low_watermark_rejected"', gate)
        self.assertIn("any(evidence.get(field) is not True", gate)
        self.assertIn('"migration_195_plan_sha256"', production)
        self.assertIn('migration-195-assert.sh preflight', production)
        self.assertIn('migration-195-assert.sh" postflight', switch)
        self.assertIn("unproven == 0 && $conflict == 0 && $unexpected == 0", assertion)
        self.assertIn('expected_profile in {"195", "197", "198"}', gate)
        self.assertIn('self.profile["name"] not in {"195", "197", "198"}', production)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 ]]', switch)
        self.assertIn('[[ $profile == 195 || $profile == 197 || $profile == 198 ]]', assertion)
        self.assertIn('expected_profile == "198"', gate)
        self.assertIn("managed monitor key-name evidence", gate)

    def test_profile_194_gate_rejects_missing_prompt_audit_disabled_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "194"
            document = {
                "manifest": manifest,
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
            ):
                with self.assertRaisesRegex(RuntimeError, "Prompt Audit disabled-state evidence"):
                    verify_gate(root, public_key, "194")

    def test_profile_197_gate_rejects_missing_migration_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            manifest = self.manifest("runner", int(time.time()) + 60)
            manifest["profile"] = "197"
            document = {
                "manifest": manifest,
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(),
                    "integration_verified": True,
                    "vm_restore_verified": True,
                    "prompt_audit_disabled": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True, stdout=subprocess.DEVNULL)
            with (
                mock.patch("release.gate.runner_checksum", return_value="runner"),
                mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}),
                mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum),
                mock.patch("release.gate.get_profile", return_value={"origin": manifest["origin"], "vm_identity": manifest["vm_identity"]}),
            ):
                with self.assertRaisesRegex(RuntimeError, "migration 195 semantic evidence"):
                    verify_gate(root, public_key, "197")

    def test_vm_post_build_space_gate_does_not_double_count_image(self) -> None:
        validator = (DEPLOY_ROOT / "release" / "vm-validate.sh").read_text(encoding="utf-8")
        self.assertIn("required_before=$((database_size * 2 + current_image_size * 2 + 1073741824))", validator)
        self.assertIn("required_free=$((database_size * 2 + candidate_size + 1073741824))", validator)
        self.assertNotIn("required_free=$((database_size + candidate_size", validator)

    def test_gate_rejects_archive_replacement_and_expiry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            document = {
                "manifest": self.manifest("runner", int(time.time()) + 60),
                "evidence": {
                    "candidate_image_id": "sha256:" + "b" * 64,
                    "candidate_archive_sha256": digest,
                    "integration_verified": True,
                    "vm_restore_verified": True,
                },
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="runner"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
                verify_gate(root, public_key, "182")
                archive.write_bytes(b"replaced")
                with self.assertRaisesRegex(RuntimeError, "archive checksum"):
                    verify_gate(root, public_key, "182")
                archive.write_bytes(b"candidate")
                document["manifest"]["expires_at"] = int(time.time()) - 1
                (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
                subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
                with self.assertRaisesRegex(RuntimeError, "expired"):
                    verify_gate(root, public_key, "182")
                self.assertEqual(verify_gate(root, public_key, "182", allow_expired=True)["manifest"]["expires_at"], document["manifest"]["expires_at"])

    def test_gate_rejects_runner_version_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            private_key = root / "private.pem"
            public_key = root / "public.pem"
            subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key)], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(["openssl", "pkey", "-in", str(private_key), "-pubout", "-out", str(public_key)], check=True, stdout=subprocess.DEVNULL)
            archive = root / "candidate.tar.gz"
            archive.write_bytes(b"candidate")
            document = {
                "manifest": self.manifest("old", int(time.time()) + 60),
                "evidence": {"candidate_image_id": "sha256:" + "b" * 64, "candidate_archive_sha256": hashlib.sha256(b"candidate").hexdigest(), "integration_verified": True, "vm_restore_verified": True},
            }
            (root / "gate.json").write_bytes(canonical_json(document) + b"\n")
            subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin", "-in", str(root / "gate.json"), "-out", str(root / "gate.sig")], check=True)
            with mock.patch("release.gate.runner_checksum", return_value="new"), mock.patch("release.gate.release_asset_checksums", return_value={"asset": "digest"}), mock.patch("release.gate.sha256_file", side_effect=self.release_unit_checksum):
                with self.assertRaisesRegex(RuntimeError, "different release runner"):
                    verify_gate(root, public_key, "182")


if __name__ == "__main__":
    unittest.main()
